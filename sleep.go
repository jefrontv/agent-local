package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

// sleep.go parks a pool that nothing has touched for a while, and leaves the
// waking to the router: it has always started a pool whose socket is gone
// (router.go, ServeHTTP), so a parked site is never a broken one, only a site
// whose next request pays a ~0.3 s boot.
//
// What lives here is the clock. The request log cannot be it: it is in memory,
// 2048 entries shared by every site, wiped by each daemon handover and emptied
// by the hub's clear button, so a site with no record there means "unknown",
// never "idle". This clock is written on the request path, read by the sweeper,
// and persisted only when a pool parks or when the daemon exits — a store write
// costs an flock and an fsync, which is not a price a request may pay.

// activity is the per-pool clock plus the in-flight count that keeps the sweeper
// off a pool that is mid-request. The zero value is usable: the daemon goes
// through NewEngine, but tests build &Engine{} directly.
type activity struct {
	mu       sync.Mutex
	last     map[string]time.Time
	inflight map[string]int
}

// stamp records a request against a pool.
func (a *activity) stamp(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	if a.last == nil {
		a.last = map[string]time.Time{}
	}
	a.last[id] = time.Now()
	a.mu.Unlock()
}

// remember seeds a pool's clock from the store, so a daemon handover does not
// reset a window that was already half spent. A zero time is ignored: an
// unknown pool is treated as freshly used, which postpones a park and can never
// cause one.
func (a *activity) remember(id string, t time.Time) {
	if id == "" || t.IsZero() {
		return
	}
	a.mu.Lock()
	if a.last == nil {
		a.last = map[string]time.Time{}
	}
	if cur, ok := a.last[id]; !ok || t.After(cur) {
		a.last[id] = t
	}
	a.mu.Unlock()
}

// lastFor reports when a pool was last used, and whether that is known.
func (a *activity) lastFor(id string) (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.last[id]
	return t, ok
}

// enter and leave count requests holding a pool. The router wraps every request
// that reaches a pool, because parking one mid-render cuts a page in half:
// StopFPM signals the whole process group and SIGKILLs it five seconds later,
// while a request is allowed 130.
func (a *activity) enter(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	if a.inflight == nil {
		a.inflight = map[string]int{}
	}
	a.inflight[id]++
	a.mu.Unlock()
}

func (a *activity) leave(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	if a.inflight[id] > 0 {
		a.inflight[id]--
	}
	a.mu.Unlock()
}

// inflightFor is how many requests a pool is serving right now.
func (a *activity) inflightFor(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inflight[id]
}

// forget drops a pool's clock and count, for a pool that no longer exists.
func (a *activity) forget(id string) {
	a.mu.Lock()
	delete(a.last, id)
	delete(a.inflight, id)
	a.mu.Unlock()
}

// sleepInput is everything the park decision looks at, gathered so the decision
// can be read in one place and tested without spawning a pool.
type sleepInput struct {
	now      time.Time
	window   time.Duration
	enabled  bool // idle sleeping is on at all
	router   bool // the front can wake a parked pool
	alive    bool // the pool is up
	inflight int  // requests it is serving
	last     time.Time
	known    bool // the clock is known
}

// due reports whether this pool should be parked now. Every refusal has its own
// reason, and each one is a way a wrong answer would show up as a broken site, a
// killed request or a pool that parks the moment it is used.
func (in sleepInput) due() bool {
	switch {
	case !in.enabled, !in.router, !in.alive, in.inflight > 0:
		return false
	case !in.known:
		// No record means unknown, not idle: a daemon that has just started has
		// no history for a pool that has been up for days.
		return false
	}
	return in.now.Sub(in.last) >= in.window
}

// SleepIdlePools parks every pool that has been quiet for longer than the
// configured window, and returns their pool ids. It is one pass: the caller owns
// the interval.
func (e *Engine) SleepIdlePools(now time.Time) []string {
	if e.Store == nil {
		return nil
	}
	window, enabled := e.Store.IdleSleepWindow()
	if !enabled {
		return nil
	}
	onRouter := FrontKind(e.Store) == "router"
	var slept []string
	park := func(id string, last time.Time) bool {
		if err := e.StopFPM(id); err != nil {
			log.Printf("idle sleep: %s: %v", id, err)
			return false
		}
		e.act.forget(id)
		slept = append(slept, id)
		return true
	}

	for _, site := range e.Store.Sites() {
		if site.State != StateRunning {
			continue
		}
		in := e.sleepInputFor(site.Slug, now, window, enabled, onRouter)
		if !in.due() {
			e.startClockIfUnknown(site.Slug, in, now)
			continue
		}
		if park(site.Slug, in.last) {
			site.LastActive = in.last
			e.Store.SetSiteState(site, StateSleeping)
		}
	}
	for _, w := range e.Store.Worktrees() {
		if w.State != StateRunning {
			continue
		}
		in := e.sleepInputFor(w.ID, now, window, enabled, onRouter)
		if !in.due() {
			e.startClockIfUnknown(w.ID, in, now)
			continue
		}
		if park(w.ID, in.last) {
			w.LastActive = in.last
			e.Store.SetWorktreeState(w, StateSleeping)
		}
	}
	if len(slept) > 0 {
		if err := e.Store.Save(); err != nil {
			log.Printf("idle sleep: saving state: %v", err)
		}
	}
	return slept
}

// sleepInputFor gathers the facts about one pool.
func (e *Engine) sleepInputFor(id string, now time.Time, window time.Duration, enabled, onRouter bool) sleepInput {
	last, known := e.act.lastFor(id)
	return sleepInput{
		now:      now,
		window:   window,
		enabled:  enabled,
		router:   onRouter,
		alive:    e.FPMAlive(id),
		inflight: e.act.inflightFor(id),
		last:     last,
		known:    known,
	}
}

// startClockIfUnknown gives a live pool with no history a clock starting now.
// Without it, a pool that was already up when the daemon (or this feature)
// started would never be a candidate, and idle sleeping would silently do
// nothing on the machine that needs it most.
func (e *Engine) startClockIfUnknown(id string, in sleepInput, now time.Time) {
	if in.known || !in.alive || !in.enabled || !in.router {
		return
	}
	e.act.remember(id, now)
}

// WakeSleeping stops nothing and starts every pool this machine parked. The
// Apache front proxies straight to the pool's socket and cannot start one, so a
// parked site under it is a dark site: switching to that front, or booting with
// it, has to put them all back.
func (e *Engine) WakeSleeping() (int, error) {
	var firstErr error
	woke := 0
	for _, site := range e.Store.Sites() {
		if site.State != StateSleeping {
			continue
		}
		if err := e.StartSite(site.Slug); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		woke++
	}
	for _, w := range e.Store.Worktrees() {
		if w.State != StateSleeping {
			continue
		}
		if err := e.StartWorktree(w.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		woke++
	}
	return woke, firstErr
}

// SeedActivity loads persisted clocks into the live map, so a daemon handover
// does not hand every pool a fresh window.
func (e *Engine) SeedActivity() {
	for _, site := range e.Store.Sites() {
		e.act.remember(site.Slug, site.LastActive)
	}
	for _, w := range e.Store.Worktrees() {
		e.act.remember(w.ID, w.LastActive)
	}
}

// PersistActivity writes the live clocks back to the store, once, on the way
// out. Nothing else persists them: the point of the in-memory clock is that a
// request never touches disk.
func (e *Engine) PersistActivity() error {
	changed := false
	for _, site := range e.Store.Sites() {
		last, known := e.act.lastFor(site.Slug)
		if !known || !last.After(site.LastActive) {
			continue
		}
		site.LastActive = last
		e.Store.PutSite(site)
		changed = true
	}
	for _, w := range e.Store.Worktrees() {
		last, known := e.act.lastFor(w.ID)
		if !known || !last.After(w.LastActive) {
			continue
		}
		w.LastActive = last
		e.Store.PutWorktree(w)
		changed = true
	}
	if !changed {
		return nil
	}
	return e.Store.Save()
}

// idleSweepEvery is how often the daemon looks for pools that have gone quiet.
// A minute keeps a park close to the window rather than up to a sweep late, and
// a pass costs a state walk and a clock comparison per live pool.
const idleSweepEvery = time.Minute

// sleepLoop parks quiet pools on a beat. Same shape as updateLoop: a bare loop
// with a sleep, started by RunDaemon, ending when the process does.
func sleepLoop(store *Store, engine *Engine) {
	if _, enabled := store.IdleSleepWindow(); !enabled {
		log.Printf("idle sleep: off — %s idle on to park quiet sites", AppName)
	} else if FrontKind(store) != "router" {
		log.Printf("idle sleep: off under the apache front, which cannot wake a parked pool")
	}
	// Not at boot: the restore has just decided what should be up, and sweeping
	// in that same second would only re-read it.
	time.Sleep(idleSweepEvery)
	for {
		store.ReloadIfChanged()
		if slept := engine.SleepIdlePools(time.Now()); len(slept) > 0 {
			log.Printf("idle sleep: parked %s", strings.Join(slept, ", "))
		}
		time.Sleep(idleSweepEvery)
	}
}

// IdleSleeps is how long a pool has been quiet, and whether the clock is known.
// Used by the CLI to answer "when does this site park".
func (e *Engine) IdleSleeps(id string, now time.Time) (time.Duration, bool) {
	last, known := e.act.lastFor(id)
	if !known {
		return 0, false
	}
	return now.Sub(last), true
}
