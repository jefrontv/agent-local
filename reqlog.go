package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// reqlog.go records what the built-in router served. Debugging a local site
// is mostly a question the logs cannot answer: which request produced that
// redirect, why was that page slow, what did PHP complain about while it
// rendered. The pool log holds the errors but not the requests; the browser
// holds the requests but not the errors. This keeps both, in one line per
// request, for the last few thousand.
//
// It is in memory on purpose: a request log that survives a restart is an
// archive nobody reads, and writing one to disk on the hot path is a cost
// every request pays for. A ring, no rotation, no I/O.
//
// The Apache front bypasses the router entirely, so under Apache this
// records nothing — the request page says so rather than showing an empty
// table as if the site had no traffic.

// reqLogSize is how many requests are kept. A page load of a WordPress site
// with its assets is 20-60 requests, so this is the last few dozen loads —
// past what anyone reconstructs by hand, and about 1.5 MB.
const reqLogSize = 4096

// reqErrLines caps the PHP error lines attached to one request, so a single
// request in a fatal loop cannot pin a large slice in the ring.
const reqErrLines = 20

// requestRecord is one request the router served.
type requestRecord struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Host     string    `json:"host"`
	Site     string    `json:"site"`               // slug, or "" if the host resolved to nothing
	Worktree string    `json:"worktree,omitempty"` // preview pool id when the host was a branch domain
	Method   string    `json:"method"`
	Path     string    `json:"path"` // path plus query, as requested
	Status   int       `json:"status"`
	Ms       float64   `json:"ms"`
	Bytes    int64     `json:"bytes"`
	// Served is which branch of the router answered: static (a file off
	// disk), php (through the pool), redirect (a directory slash), media (a
	// missing upload sent to the fallback origin), or error (the router
	// itself refused).
	Served string `json:"served"`
	// Shared marks a request that arrived through a public tunnel rather
	// than from this machine.
	Shared bool `json:"shared,omitempty"`
	// PHPErrors are the error lines the site's pool log gained while this
	// request was being served. Under concurrent requests two records can
	// carry the same line: the log is per pool, not per request.
	PHPErrors []string `json:"php_errors,omitempty"`
}

// reqRing is the process-wide request log. A fixed array written under a
// mutex: the hot path is one append, no allocation for the ring itself, and
// nothing to reclaim.
type reqRing struct {
	mu     sync.RWMutex
	buf    [reqLogSize]requestRecord
	head   int   // next slot to write
	filled bool  // head has wrapped at least once
	nextID int64 // monotonic, so a caller can tell "new since" without timestamps
}

var reqlog reqRing

// add records one served request.
func (r *reqRing) add(rec requestRecord) {
	if len(rec.PHPErrors) > reqErrLines {
		rec.PHPErrors = rec.PHPErrors[:reqErrLines]
	}
	r.mu.Lock()
	r.nextID++
	rec.ID = r.nextID
	r.buf[r.head] = rec
	r.head = (r.head + 1) % reqLogSize
	if r.head == 0 {
		r.filled = true
	}
	r.mu.Unlock()
}

// reqFilter narrows a listing. A zero filter means "everything, newest
// first, up to limit".
type reqFilter struct {
	Hosts      []string      // only these Host headers (a site and its aliases and previews)
	Since      time.Duration // only requests this recent; 0 = any age
	ErrorsOnly bool          // only requests that logged a PHP error
	MinStatus  int           // only status >= this (400 for "what failed")
	PathHas    string        // substring match on path
	Limit      int           // 0 = reqLogSize
}

// list returns matching records, newest first.
func (r *reqRing) list(f reqFilter) []requestRecord {
	limit := f.Limit
	if limit <= 0 || limit > reqLogSize {
		limit = reqLogSize
	}
	var cutoff time.Time
	if f.Since > 0 {
		cutoff = time.Now().Add(-f.Since)
	}
	hosts := make(map[string]bool, len(f.Hosts))
	for _, h := range f.Hosts {
		hosts[strings.ToLower(h)] = true
	}

	out := make([]requestRecord, 0, min(limit, 64))
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Walk backwards from the newest write, so "newest first" costs nothing
	// and a small limit stops early.
	n := reqLogSize
	if !r.filled {
		n = r.head
	}
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (r.head - 1 - i + reqLogSize*2) % reqLogSize
		rec := r.buf[idx]
		if rec.At.IsZero() {
			continue
		}
		if len(hosts) > 0 && !hosts[strings.ToLower(rec.Host)] {
			continue
		}
		if !cutoff.IsZero() && rec.At.Before(cutoff) {
			continue
		}
		if f.ErrorsOnly && len(rec.PHPErrors) == 0 {
			continue
		}
		if f.MinStatus > 0 && rec.Status < f.MinStatus {
			continue
		}
		if f.PathHas != "" && !strings.Contains(rec.Path, f.PathHas) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// clear drops records for the given hosts, or every record when hosts is
// empty. Clearing before reproducing a bug is what makes the log readable,
// so it is per site rather than global by default.
func (r *reqRing) clear(hosts []string) int {
	match := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		match[strings.ToLower(h)] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cleared := 0
	for i := range r.buf {
		if r.buf[i].At.IsZero() {
			continue
		}
		if len(match) > 0 && !match[strings.ToLower(r.buf[i].Host)] {
			continue
		}
		r.buf[i] = requestRecord{}
		cleared++
	}
	return cleared
}

// siteHosts is every Host header that belongs to a site: its domain, its
// aliases, its branch previews, and any live share tunnel pointing at it.
// The request log is keyed by Host because that is what the router sees.
func siteHosts(store *Store, site *Site) []string {
	hosts := append([]string{site.Domain}, site.Aliases...)
	for _, wt := range store.WorktreesFor(site.Slug) {
		if wt.Domain != "" {
			hosts = append(hosts, wt.Domain)
		}
	}
	for _, sh := range shares.All() {
		if sh.Slug == site.Slug && sh.Host != "" {
			hosts = append(hosts, sh.Host)
		}
	}
	return hosts
}

// statusWriter records what was actually sent: the status a handler chose
// (200 when it never said) and how many bytes went out. It forwards Flush
// so the FastCGI streaming path keeps streaming.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush keeps the streaming response streaming: proxyFCGIScript flushes each
// chunk, and a wrapper that swallowed Flush would buffer whole pages.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// code is the status to record: a handler that wrote nothing at all still
// produced a 200 as far as the client is concerned.
func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// handleRequests serves GET /sites/{slug}/requests: what the router served
// for this site, newest first. Filters mirror the hub page's, so an agent
// and a person are reading the same thing.
//
//	?since=15m&limit=100&errors=1&status=500&path=/wp-admin
func (a *APIServer) handleRequests(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	q := r.URL.Query()
	f := reqFilter{
		Hosts:      siteHosts(a.store, site),
		ErrorsOnly: q.Get("errors") == "1" || q.Get("errors") == "true",
		PathHas:    q.Get("path"),
		Limit:      atoi0(q.Get("limit")),
	}
	if s := q.Get("since"); s != "" {
		d, err := parseSince(s)
		if err != nil {
			fail(w, 400, "bad since: "+err.Error())
			return
		}
		f.Since = d
	}
	if s := q.Get("status"); s != "" {
		f.MinStatus = atoi0(s)
	}
	recs := reqlog.list(f)
	ok(w, map[string]interface{}{
		"requests": recs,
		"count":    len(recs),
		// Under the apache front the router never sees a request, so an
		// empty list means "not recording", not "no traffic".
		"recording": FrontKind(a.store) != "apache",
		"kept":      reqLogSize,
	})
}

// handleClearRequests empties this site's request log. Clearing, then
// reproducing, is what makes the log answer a question.
func (a *APIServer) handleClearRequests(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	ok(w, map[string]int{"cleared": reqlog.clear(siteHosts(a.store, site))})
}
