package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Five processes share sites.json — CLI, daemon, TUI, MCP server, agents — and a
// save used to write a whole in-memory snapshot, erasing anything another one had
// changed since load. These pin the merge that replaced it.
func TestSaveDoesNotClobberOtherWriters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Two independent handles on the same file, as two processes would have.
	daemon, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	daemon.Data.Sites["existing"] = &Site{Slug: "existing", Domain: "existing.test", State: StateStopped}
	if err := daemon.Save(); err != nil {
		t.Fatal(err)
	}

	cli, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// The reported bug: the CLI writes a setting, then the daemon saves a site
	// state change from its older snapshot.
	if err := cli.SetSitesDir("~/Documents/Sites"); err != nil {
		t.Fatal(err)
	}
	daemon.Data.Sites["existing"].State = StateRunning
	if err := daemon.Save(); err != nil {
		t.Fatal(err)
	}

	fresh, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fresh.SitesDir(), filepath.Join(home, "Documents", "Sites"); got != want {
		t.Errorf("sites dir = %q, want %q — the daemon's save clobbered the CLI's setting", got, want)
	}
	if got := fresh.Data.Sites["existing"].State; got != StateRunning {
		t.Errorf("site state = %q, want %q — the daemon's own change was lost", got, StateRunning)
	}
}

func TestSaveMergesSiteAddAndDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	seed, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"alpha", "beta"} {
		seed.Data.Sites[slug] = &Site{Slug: slug, Domain: slug + ".test"}
	}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	a, _ := OpenStore()
	b, _ := OpenStore()

	// A deletes one site; B, which never saw that, adds another.
	a.DelSite("alpha")
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	b.Data.Sites["gamma"] = &Site{Slug: "gamma", Domain: "gamma.test"}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	fresh, _ := OpenStore()
	if _, resurrected := fresh.Data.Sites["alpha"]; resurrected {
		t.Error("alpha came back: a stale writer resurrected a deleted site")
	}
	for _, want := range []string{"beta", "gamma"} {
		if _, ok := fresh.Data.Sites[want]; !ok {
			t.Errorf("%s missing after merge", want)
		}
	}
}

// Settings written by different processes must not fight: each keeps the field it
// actually changed.
func TestSaveMergesDistinctSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	seed, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	one, _ := OpenStore()
	two, _ := OpenStore()

	if err := one.SetSuffix(".localhost"); err != nil {
		t.Fatal(err)
	}
	if err := two.SetSitesDir("~/Sites"); err != nil {
		t.Fatal(err)
	}

	fresh, _ := OpenStore()
	if got := fresh.Suffix(); got != ".localhost" {
		t.Errorf("suffix = %q, want .localhost", got)
	}
	if got, want := fresh.SitesDir(), filepath.Join(home, "Sites"); got != want {
		t.Errorf("sites dir = %q, want %q", got, want)
	}
}

// A field this build does not know about must survive a round trip, or upgrading
// one binary while another still runs would drop the other's settings.
func TestSavePreservesUnknownFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	seed, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	path := P().Store()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Splice in a field from an imagined newer version.
	injected := string(raw[:len(raw)-2]) + ",\n  \"future_setting\": \"keep me\"\n}"
	if err := os.WriteFile(path, []byte(injected), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the mtime forward so the reload is not skipped on a fast filesystem.
	os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))

	old, _ := OpenStore()
	if err := old.SetSuffix(".dev.local"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "future_setting") {
		t.Error("unknown field dropped on save")
	}
	if !strings.Contains(string(after), ".dev.local") {
		t.Error("our own change was not written")
	}
}
