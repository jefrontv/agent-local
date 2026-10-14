package main

import (
	"testing"
	"time"
)

// A store that has never been told otherwise is on, at twelve hours. The
// sentinel matters: zero is also a real value, and it has to mean "unset" so a
// sites.json written before this feature behaves the way its author expected.
func TestIdleSleepWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		set  int
		want time.Duration
		on   bool
	}{
		{0, IdleSleepDefault, true},
		{IdleSleepOff, 0, false},
		{30, 30 * time.Minute, true},
		{720, 12 * time.Hour, true},
	}
	for _, c := range cases {
		if err := store.SetIdleSleepMinutes(c.set); err != nil {
			t.Fatal(err)
		}
		got, on := store.IdleSleepWindow()
		if got != c.want || on != c.on {
			t.Errorf("set %d: got %v on=%v, want %v on=%v", c.set, got, on, c.want, c.on)
		}
	}
}

// The park decision. Each refusal is a way a wrong answer shows up as a broken
// site, a killed request, or a pool that parks the moment it is used.
func TestSleepDue(t *testing.T) {
	now := time.Now()
	base := sleepInput{
		now: now, window: time.Hour, enabled: true, router: true,
		alive: true, inflight: 0, last: now.Add(-2 * time.Hour), known: true,
	}
	if !base.due() {
		t.Fatal("a pool quiet for two hours past a one-hour window is due")
	}
	refusals := []struct {
		name  string
		alter func(*sleepInput)
	}{
		{"idle sleeping off", func(in *sleepInput) { in.enabled = false }},
		{"apache front, which cannot wake it", func(in *sleepInput) { in.router = false }},
		{"pool is down", func(in *sleepInput) { in.alive = false }},
		{"a request is in flight", func(in *sleepInput) { in.inflight = 1 }},
		{"quiet for less than the window", func(in *sleepInput) { in.last = now.Add(-30 * time.Minute) }},
		{"clock unknown", func(in *sleepInput) { in.known = false }},
	}
	for _, c := range refusals {
		in := base
		c.alter(&in)
		if in.due() {
			t.Errorf("%s: must not park", c.name)
		}
	}
	// Exactly at the window is due: the window is a deadline, not a strict
	// inequality, and rounding either way would only be argued about.
	at := base
	at.last = now.Add(-base.window)
	if !at.due() {
		t.Error("a pool quiet for exactly the window is due")
	}
}

// The boot decision, which is where the memory actually comes back: a machine
// that was off overnight must not rebuild seventy pools nobody has asked for.
func TestRestoreDue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// A store from before this feature has no clock at all, and must behave
	// exactly as it always did.
	if !restoreDue(store, time.Time{}, now) {
		t.Error("a pool with no clock is started, not parked")
	}
	if !restoreDue(store, now.Add(-2*time.Hour), now) {
		t.Error("a pool used two hours ago is inside the twelve-hour window")
	}
	if restoreDue(store, now.Add(-30*time.Hour), now) {
		t.Error("a pool untouched for a day comes back parked")
	}
	if err := store.SetIdleSleepMinutes(IdleSleepOff); err != nil {
		t.Fatal(err)
	}
	if !restoreDue(store, now.Add(-30*24*time.Hour), now) {
		t.Error("with sleeping off, everything that was running is restored")
	}
}

// The clock: unknown is not idle, an unknown pool accepts a seed, a known one
// never moves backwards, and in-flight counts never drift negative.
func TestActivityClock(t *testing.T) {
	var a activity
	if _, known := a.lastFor("site"); known {
		t.Error("nothing is known about a pool nobody has used")
	}
	if n := a.inflightFor("site"); n != 0 {
		t.Errorf("inflight = %d on a pool with no requests", n)
	}

	before := time.Now()
	a.stamp("site")
	used, known := a.lastFor("site")
	if !known || used.Before(before) {
		t.Fatalf("stamp: got %v known=%v", used, known)
	}

	// A stale seed must not rewind a pool that has been used since.
	older := used.Add(-time.Hour)
	a.remember("site", older)
	if got, _ := a.lastFor("site"); !got.Equal(used) {
		t.Errorf("remember moved a known clock backwards: %v", got)
	}
	// Filling an unknown one is how a daemon handover resumes windows.
	a.remember("other", older)
	if got, known := a.lastFor("other"); !known || !got.Equal(older) {
		t.Errorf("remember did not seed an unknown pool: %v known=%v", got, known)
	}
	// A zero time is not a clock, it is the absence of one.
	a.remember("zero", time.Time{})
	if _, known := a.lastFor("zero"); known {
		t.Error("a zero time must not seed a clock")
	}

	a.stamp("")
	if _, known := a.lastFor(""); known {
		t.Error("an empty pool id is not a pool")
	}

	a.enter("site")
	a.enter("site")
	a.leave("site")
	if n := a.inflightFor("site"); n != 1 {
		t.Errorf("inflight = %d, want 1", n)
	}
	a.leave("site")
	a.leave("site")
	if n := a.inflightFor("site"); n != 0 {
		t.Errorf("inflight = %d after every request finished, want 0", n)
	}

	a.forget("site")
	if _, known := a.lastFor("site"); known {
		t.Error("forget must drop the clock")
	}
}

// The clock is written on the way out and read back on the way in. Without it a
// daemon handover postpones every park by a whole window, which on a machine
// that restarts often means the feature never fires.
func TestActivitySurvivesARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "quiet", Domain: "quiet.test", State: StateRunning})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-3 * time.Hour)
	first := &Engine{Store: store}
	first.act.remember("quiet", when)
	if err := first.PersistActivity(); err != nil {
		t.Fatal(err)
	}
	if got := store.Site("quiet").LastActive; !got.Equal(when) {
		t.Fatalf("LastActive = %v, want %v", got, when)
	}

	second, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Site("quiet").LastActive; !got.Equal(when) {
		t.Fatalf("the clock did not reach the file: %v", got)
	}
	next := &Engine{Store: second}
	next.SeedActivity()
	if got, known := next.act.lastFor("quiet"); !known || !got.Equal(when) {
		t.Errorf("the clock did not come back: %v known=%v", got, known)
	}
}

// Nothing about a site that is already down is the sweeper's business: parking a
// pool that is not up would only relabel it "sleeping" and lose the fact that a
// person stopped it.
func TestSleepIdlePoolsLeavesDeadPoolsAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "down", Domain: "down.test", State: StateRunning})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: store}
	e.act.remember("down", time.Now().Add(-48*time.Hour))

	if slept := e.SleepIdlePools(time.Now()); len(slept) != 0 {
		t.Fatalf("slept %v, want none: there is no pool to park", slept)
	}
	if got := store.Site("down").State; got != StateRunning {
		t.Errorf("state = %s, want running", got)
	}
}
