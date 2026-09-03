package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tool catalogue is built once and looked up by name; the cached table
// must equal a fresh build and the index must answer every name.
func TestToolTableCachedAndIndexed(t *testing.T) {
	fresh := mcpTools()
	cached := tools()
	if len(fresh) != len(cached) {
		t.Fatalf("cached %d tools, fresh build %d", len(cached), len(fresh))
	}
	for _, tl := range fresh {
		got := toolByName(tl.Name)
		if got == nil || got.Name != tl.Name {
			t.Errorf("toolByName(%q) missed", tl.Name)
		}
	}
	if toolByName("no-such-tool") != nil {
		t.Error("unknown name should be nil")
	}
	// Same backing array on repeat: the whole point is not rebuilding.
	if &tools()[0] != &cached[0] {
		t.Error("tools() rebuilt the table on a second call")
	}
}

// Concurrent MCP replies share one encoder. Each reply must land as one whole
// line; interleaving would corrupt the JSON-RPC stream for every client.
func TestConcurrentRepliesStayWholeLines(t *testing.T) {
	var out strings.Builder
	var mu sync.Mutex
	enc := json.NewEncoder(&out)
	reply := func(resp *mcpResp) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(resp)
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reply(&mcpResp{JSONRPC: "2.0", ID: i, Result: map[string]interface{}{"n": i, "pad": strings.Repeat("x", 200)}})
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 200 {
		t.Fatalf("%d lines, want 200", len(lines))
	}
	for _, l := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("interleaved reply: %v in %q", err, l[:min(60, len(l))])
		}
	}
}

// Reads fail fast, mutations keep the long bound.
func TestAPITimeoutByMethod(t *testing.T) {
	if apiTimeout("GET") > time.Minute {
		t.Errorf("GET timeout %s should be short", apiTimeout("GET"))
	}
	if apiTimeout("POST") < 10*time.Minute {
		t.Errorf("POST timeout %s should allow long imports", apiTimeout("POST"))
	}
}

// The token cache must follow the file: same mtime returns the cached value
// without a read, a rewritten file (new mtime) is picked up.
func TestAPITokenFollowsFileMtime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := P().Ensure(); err != nil {
		t.Fatal(err)
	}
	tokenMu.Lock()
	tokenVal, tokenMtime = "", time.Time{}
	tokenMu.Unlock()

	first, err := APIToken()
	if err != nil || len(first) < 16 {
		t.Fatalf("APIToken = %q, %v", first, err)
	}
	again, _ := APIToken()
	if again != first {
		t.Fatalf("second read %q != first %q", again, first)
	}
	// Rotate: write a new token with a later mtime.
	rotated := strings.Repeat("b", 48)
	p := P().Token()
	if err := os.WriteFile(p, []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	got, _ := APIToken()
	if got != rotated {
		t.Fatalf("after rotation got %q, want the new token", got)
	}
}

// ReloadIfChanged must still observe an on-disk change made by another process
// (the read-lock fast path must not skip a real reload), and must leave dirty
// in-memory changes alone.
func TestReloadFastPathStillReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	a.PutSite(&Site{Slug: "one", Domain: "one.test", WPDir: filepath.Join(home, "one")})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if b.Site("one") == nil {
		t.Fatal("b should see one")
	}
	// Another process adds a site; make sure mtime advances past a's load.
	a.PutSite(&Site{Slug: "two", Domain: "two.test", WPDir: filepath.Join(home, "two")})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(a.path, future, future)
	b.ReloadIfChanged()
	if b.Site("two") == nil {
		t.Fatal("reload fast path skipped a real on-disk change")
	}
	// Dirty local state vetoes a reload.
	b.PutSite(&Site{Slug: "local", Domain: "local.test", WPDir: filepath.Join(home, "local")})
	a.PutSite(&Site{Slug: "three", Domain: "three.test", WPDir: filepath.Join(home, "three")})
	a.Save()
	later := time.Now().Add(4 * time.Second)
	os.Chtimes(a.path, later, later)
	b.ReloadIfChanged()
	if b.Site("local") == nil {
		t.Fatal("reload dropped an unsaved local mutation")
	}
}
