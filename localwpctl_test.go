package main

import "testing"

// The port in LocalWP's connection file goes stale on most launches, so the order
// candidates are tried in decides whether importing works at all.
func TestLocalWPPortCandidates(t *testing.T) {
	got := localWPPortCandidates(50123)
	if len(got) == 0 || got[0] != 50123 {
		t.Errorf("advertised port must be tried first, got %v", got)
	}
	last := got[len(got)-1]
	if last != 4000 && !contains(intsToStrings(got), "4000") {
		t.Errorf("the historical default 4000 should be a fallback, got %v", got)
	}
	// No duplicates, whatever the inputs collide on.
	seen := map[int]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("duplicate candidate %d in %v", p, got)
		}
		seen[p] = true
	}
	// A missing advertised port must not produce a zero candidate.
	for _, p := range localWPPortCandidates(0) {
		if p == 0 {
			t.Error("zero is not a port")
		}
	}
}

func intsToStrings(in []int) []string {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, itoa(i))
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// An unreachable source must be reported as such, or the import proceeds into a
// dump that fails with a bare MySQL error.
func TestSourceDBReachable(t *testing.T) {
	if sourceDBReachable("/nonexistent/mysqld.sock", "", 0) {
		t.Error("a missing socket is not reachable")
	}
	if sourceDBReachable("", "", 0) {
		t.Error("no socket and no port is not reachable")
	}
	if sourceDBReachable("", "127.0.0.1", 1) {
		t.Error("port 1 should not be reachable")
	}
}

// The socket only exists while a site runs, and it is keyed by site id: asking
// for it with no id must not return a path that happens to exist.
func TestLocalWPSocketForRequiresID(t *testing.T) {
	if got := localWPSocketFor(""); got != "" {
		t.Errorf("empty id returned %q", got)
	}
	if got := localWPSocketFor("definitely-not-a-site-id"); got != "" {
		t.Errorf("unknown id returned %q", got)
	}
}
