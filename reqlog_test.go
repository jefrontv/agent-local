package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ring that lost the newest record, or kept a stale one past capacity,
// would quietly lie about what a site served.
func TestReqRingKeepsNewestAndWraps(t *testing.T) {
	var r reqRing
	const extra = 50
	for i := range reqLogSize + extra {
		r.add(requestRecord{At: time.Now(), Host: "s.test", Path: fmt.Sprintf("/%d", i), Status: 200})
	}
	got := r.list(reqFilter{Limit: 3})
	if len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	last := reqLogSize + extra - 1
	for i, want := range []string{
		fmt.Sprintf("/%d", last), fmt.Sprintf("/%d", last-1), fmt.Sprintf("/%d", last-2),
	} {
		if got[i].Path != want {
			t.Errorf("newest-first[%d] = %s, want %s", i, got[i].Path, want)
		}
	}
	if all := r.list(reqFilter{}); len(all) != reqLogSize {
		t.Errorf("kept %d records, want the ring size %d", len(all), reqLogSize)
	}
	// IDs keep counting past the wrap, so "newer than X" stays meaningful.
	if got[0].ID != reqLogSize+extra {
		t.Errorf("newest id = %d, want %d", got[0].ID, reqLogSize+extra)
	}
}

func TestReqRingFilters(t *testing.T) {
	var r reqRing
	r.add(requestRecord{At: time.Now(), Host: "a.test", Path: "/old", Status: 200})
	r.add(requestRecord{At: time.Now().Add(-2 * time.Hour), Host: "a.test", Path: "/stale", Status: 200})
	r.add(requestRecord{At: time.Now(), Host: "a.test", Path: "/wp-admin/", Status: 500,
		PHPErrors: []string{"PHP Fatal error: boom"}})
	r.add(requestRecord{At: time.Now(), Host: "b.test", Path: "/other", Status: 404})

	paths := func(f reqFilter) []string {
		var out []string
		for _, rec := range r.list(f) {
			out = append(out, rec.Path)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		f    reqFilter
		want string
	}{
		{"host", reqFilter{Hosts: []string{"b.test"}}, "/other"},
		{"since", reqFilter{Hosts: []string{"a.test"}, Since: time.Hour}, "/wp-admin/,/old"},
		{"errors", reqFilter{ErrorsOnly: true}, "/wp-admin/"},
		{"status", reqFilter{MinStatus: 400}, "/other,/wp-admin/"},
		{"path", reqFilter{PathHas: "admin"}, "/wp-admin/"},
	} {
		if got := strings.Join(paths(tc.f), ","); got != tc.want {
			t.Errorf("%s filter = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Clearing is per host: reproducing a bug on one site must not wipe
	// what another site recorded.
	if n := r.clear([]string{"a.test"}); n != 3 {
		t.Errorf("cleared %d, want 3", n)
	}
	if got := strings.Join(paths(reqFilter{}), ","); got != "/other" {
		t.Errorf("after clear = %q, want /other", got)
	}
}

// The point of the recorder: a real request through the router shows up with
// what the client saw, and the tool pages that refresh themselves do not.
func TestRouterRecordsRequests(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("body{}\n")
	if err := os.WriteFile(filepath.Join(docroot, "style.css"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	r := NewRouter(NewEngine(store))
	reqlog.clear(nil)

	for _, p := range []string{"/style.css", HubPath, MailPath} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://s.test"+p, nil))
	}
	recs := reqlog.list(reqFilter{Hosts: []string{"s.test"}})
	if len(recs) != 1 {
		var paths []string
		for _, rec := range recs {
			paths = append(paths, rec.Path)
		}
		t.Fatalf("recorded %d requests (%v), want only the site request", len(recs), paths)
	}
	rec := recs[0]
	if rec.Path != "/style.css" || rec.Status != 200 || rec.Served != "static" {
		t.Errorf("record = %s %d %s, want /style.css 200 static", rec.Path, rec.Status, rec.Served)
	}
	if rec.Bytes != int64(len(body)) {
		t.Errorf("recorded %d bytes, want %d", rec.Bytes, len(body))
	}
	if rec.Site != "s" {
		t.Errorf("attributed to %q, want the site slug", rec.Site)
	}
}

// The request log is only useful to an agent if the tools are registered
// and wired into dispatch — a descriptor with no case answers "unknown
// tool" while still appearing in tools/list.
func TestRequestToolsRegisteredAndWired(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range mcpTools() {
		names[tl.Name] = true
	}
	for _, want := range []string{"get_requests", "clear_requests"} {
		if !names[want] {
			t.Errorf("tool %s not registered", want)
		}
		// No daemon runs in a test, so the call fails at the socket — but
		// only after dispatch recognised the name and built a request.
		out, isErr := dispatchTool(want, map[string]interface{}{"slug": "s"})
		if !isErr {
			t.Errorf("%s unexpectedly succeeded with no daemon: %v", want, out)
			continue
		}
		if m, okm := out.(map[string]string); okm && strings.HasPrefix(m["error"], "unknown tool") {
			t.Errorf("%s is described but not dispatched", want)
		}
	}
}

// The detail panel shows headers, so the two that carry a live session must
// never reach a record — and a site is free to send a header big enough to
// matter to a 2048-record ring.
func TestSnapHeadersRedactsAndCaps(t *testing.T) {
	h := http.Header{}
	h.Set("Cookie", "wordpress_logged_in=secret")
	h.Set("Authorization", "Bearer secret")
	h.Set("Accept", "text/html")
	h.Set("X-Big", strings.Repeat("a", reqHeaderValue*3))
	for i := range reqHeaderMax * 2 {
		h.Set(fmt.Sprintf("X-Pad-%03d", i), "x")
	}

	got, redacted := snapHeaders(h)
	if len(got) > reqHeaderMax {
		t.Errorf("kept %d headers, want at most %d", len(got), reqHeaderMax)
	}
	flat := fmt.Sprint(got)
	if strings.Contains(flat, "secret") {
		t.Errorf("a credential header reached the record: %s", flat)
	}
	if len(redacted) != 2 {
		t.Errorf("redacted = %v, want Authorization and Cookie named", redacted)
	}
	for _, p := range got {
		if len(p[1]) > reqHeaderValue+3 {
			t.Errorf("header %s kept %d bytes, want capped at %d", p[0], len(p[1]), reqHeaderValue)
		}
	}
}

// The live feed asks for records newer than the newest it holds. Returning
// anything it already has would duplicate rows on screen.
func TestReqRingAfterReturnsOnlyNewer(t *testing.T) {
	var r reqRing
	for i := range 5 {
		r.add(requestRecord{At: time.Now(), Host: "s.test", Path: fmt.Sprintf("/%d", i)})
	}
	mid := r.list(reqFilter{Limit: 3})[2].ID // the third-newest
	got := r.list(reqFilter{After: mid})
	if len(got) != 2 {
		t.Fatalf("after=%d returned %d records, want 2", mid, len(got))
	}
	for _, rec := range got {
		if rec.ID <= mid {
			t.Errorf("after=%d returned id %d", mid, rec.ID)
		}
	}
	if n := len(r.list(reqFilter{After: 1 << 40})); n != 0 {
		t.Errorf("after a future id returned %d records", n)
	}
}

// The page's live half depends on this endpoint: same origin, no token, and
// records the browser does not have yet.
func TestRequestsPageServesJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	r := NewRouter(NewEngine(store))
	reqlog.clear(nil)
	reqlog.add(requestRecord{At: time.Now(), Host: "s.test", Path: "/one", Status: 200, Served: "php"})
	reqlog.add(requestRecord{At: time.Now(), Host: "s.test", Path: "/two", Status: 200, Served: "php"})

	get := func(q string) map[string]interface{} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath+"/requests"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", q, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("GET %s content-type = %q", q, ct)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s body: %v", q, err)
		}
		return out
	}

	all := get("?format=json")["requests"].([]interface{})
	if len(all) != 2 {
		t.Fatalf("format=json returned %d records, want 2", len(all))
	}
	newest := all[0].(map[string]interface{})["id"].(float64)
	if n := len(get(fmt.Sprintf("?format=json&after=%d", int64(newest)))["requests"].([]interface{})); n != 0 {
		t.Errorf("after the newest id returned %d records, want none", n)
	}
}

// Two rendering contracts the screenshots caught: the login tile is a
// <button>, so the bar's small-button type must not reach it, and the lamp
// dot belongs to status, not to headings.
func TestHubPagesTypeAndLamps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	r := NewRouter(NewEngine(store))

	// The uppercase mono rule must be scoped to the bar; unscoped, it
	// reaches the card button and its title renders in the wrong type.
	if strings.Contains(mailCSS, "\n  button {") {
		t.Error("the small-button rule is unscoped and will restyle card buttons")
	}
	for _, page := range []string{"", "/errors", "/requests"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath+page, nil))
		body := rec.Body.String()
		for _, bad := range []string{`class=label><span class=lamp>`, `class=count><span class="lamp`} {
			if strings.Contains(body, bad) {
				t.Errorf("page %q still puts a lamp before a heading (%s)", page, bad)
			}
		}
	}
}
