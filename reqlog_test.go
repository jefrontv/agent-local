package main

import (
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
	for i := range reqLogSize + 50 {
		r.add(requestRecord{At: time.Now(), Host: "s.test", Path: fmt.Sprintf("/%d", i), Status: 200})
	}
	got := r.list(reqFilter{Limit: 3})
	if len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	for i, want := range []string{"/4145", "/4144", "/4143"} {
		if got[i].Path != want {
			t.Errorf("newest-first[%d] = %s, want %s", i, got[i].Path, want)
		}
	}
	if all := r.list(reqFilter{}); len(all) != reqLogSize {
		t.Errorf("kept %d records, want the ring size %d", len(all), reqLogSize)
	}
	// IDs keep counting past the wrap, so "newer than X" stays meaningful.
	if got[0].ID != reqLogSize+50 {
		t.Errorf("newest id = %d, want %d", got[0].ID, reqLogSize+50)
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
