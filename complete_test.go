package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The completion prompt is the first thing a new site goes through, and it is
// pure logic over a directory tree — so it gets pinned here rather than being
// re-checked by hand in a terminal.
func TestCompleteDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, d := range []string{"Documents/Sites/sulo", "Documents/Sites/sulo-old", "Documents/Notes",
		"Downloads", ".config/private"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A file must never be offered: a site lives in a directory.
	if err := os.WriteFile(filepath.Join(home, "Documents", "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		in    string
		want  string
		note  string // substring, "" = not checked
		notes string
	}{
		{name: "absolute path stays absolute",
			in: filepath.Join(home, "Doc"), want: filepath.Join(home, "Documents") + "/"},
		{name: "absolute home prefix is not collapsed to tilde",
			in: home[:len(home)-1], want: home + "/"},
		{name: "tilde notation is kept",
			in: "~/Doc", want: "~/Documents/"},
		{name: "unique match opens the directory",
			in: "~/Down", want: "~/Downloads/"},
		{name: "several matches complete the shared prefix only",
			in: "~/Documents/Sites/s", want: "~/Documents/Sites/sulo", notes: "sulo"},
		{name: "exact name with longer siblings steps in on the next tab",
			in: "~/Documents/Sites/sulo", want: "~/Documents/Sites/sulo/", notes: "sulo-old"},
		{name: "case insensitive fallback",
			in: "~/doc", want: "~/Documents/"},
		{name: "trailing separator lists instead of completing the name",
			in: "~/Documents/Sites/", want: "~/Documents/Sites/sulo", notes: "sulo-old"},
		{name: "hidden directories stay hidden until named",
			// Documents + Downloads share "Do" and .config is not offered.
			in: "~/", want: "~/Do", notes: "Downloads"},
		{name: "hidden directory completes when asked for by name",
			in: "~/.con", want: "~/.config/"},
		{name: "no match leaves the value alone",
			in: "~/zzz", want: "~/zzz", note: "nothing matches zzz"},
		{name: "files are not candidates",
			in: "~/Documents/read", want: "~/Documents/read", note: "nothing matches"},
		{name: "empty input starts at home",
			in: "", want: "~/Do", notes: "Downloads"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note := completeDir(c.in)
			if got != c.want {
				t.Errorf("completeDir(%q) value = %q, want %q", c.in, got, c.want)
			}
			if c.note != "" && !strings.Contains(note, c.note) {
				t.Errorf("completeDir(%q) note = %q, want it to contain %q", c.in, note, c.note)
			}
			if c.notes != "" && !strings.Contains(note, c.notes) {
				t.Errorf("completeDir(%q) note = %q, want it to list %q", c.in, note, c.notes)
			}
		})
	}
}

// A completed path must survive the round trip into the engine, or completion is
// cosmetic: ResolveDir is what actually decides where the site goes.
func TestResolveDirRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Sites", "thing"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"~/Sites/thing", "~/Sites/thing/", filepath.Join(home, "Sites", "thing")} {
		got, err := ResolveDir(in)
		if err != nil {
			t.Fatalf("ResolveDir(%q): %v", in, err)
		}
		if want := filepath.Join(home, "Sites", "thing"); got != want {
			t.Errorf("ResolveDir(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ResolveDir("  "); err == nil {
		t.Error("ResolveDir(blank) should refuse rather than resolve to the process cwd")
	}
}

// DirUsable decides whether a fresh install may write into a directory. A stray
// dotfile makes a directory not empty — a git checkout is not empty just because
// everything in it is hidden.
func TestDirUsable(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	os.MkdirAll(empty, 0o755)
	withDot := filepath.Join(root, "dot")
	os.MkdirAll(filepath.Join(withDot, ".git"), 0o755)
	withFile := filepath.Join(root, "file")
	os.MkdirAll(withFile, 0o755)
	os.WriteFile(filepath.Join(withFile, "index.php"), []byte("x"), 0o644)

	for _, c := range []struct {
		dir  string
		want bool
	}{
		{empty, true},
		{filepath.Join(root, "does-not-exist"), true},
		{withDot, false},
		{withFile, false},
	} {
		if got := DirUsable(c.dir); got != c.want {
			t.Errorf("DirUsable(%s) = %v, want %v", filepath.Base(c.dir), got, c.want)
		}
	}
}

// DocrootFor picks what actually gets served. Getting this wrong points the site
// at a repo root and every request 404s.
func TestDocrootFor(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(p, 0o755)
		return p
	}
	wpAt := func(dir string) {
		os.WriteFile(filepath.Join(dir, "wp-load.php"), []byte("<?php"), 0o644)
	}

	flat := mk("flat")
	wpAt(flat)
	nested := mk("nested")
	wpAt(mk("nested/wp"))
	localwp := mk("localwp")
	wpAt(mk("localwp/app/public"))
	bare := mk("bare")

	for _, c := range []struct{ dir, want string }{
		{flat, flat},
		{nested, filepath.Join(nested, "wp")},
		{localwp, filepath.Join(localwp, "app", "public")},
		{bare, bare}, // nothing to find: serve what was chosen
	} {
		if got := DocrootFor(c.dir); got != c.want {
			t.Errorf("DocrootFor(%s) = %s, want %s", filepath.Base(c.dir), got, c.want)
		}
	}
}

// The sites directory decides where every create without an explicit path goes,
// so its resolution and its effect on delete are pinned here.
func TestSitesDirSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.SitesDir(), P().Sites(); got != want {
		t.Errorf("unset SitesDir = %q, want the app's own tree %q", got, want)
	}
	if err := store.SetSitesDir("~/Sites"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Sites")
	if got := store.SitesDir(); got != want {
		t.Errorf("SitesDir = %q, want %q (tilde expanded)", got, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Errorf("SetSitesDir should create the directory it accepts: %v", err)
	}
	if got, want := store.SiteDirFor("blog"), filepath.Join(home, "Sites", "blog"); got != want {
		t.Errorf("SiteDirFor = %q, want %q", got, want)
	}
	// A file cannot be a sites directory.
	f := filepath.Join(home, "afile")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := store.SetSitesDir(f); err == nil {
		t.Error("SetSitesDir accepted a file")
	}
	// Empty restores the default rather than leaving an unusable value behind -
	// on disk too. The merge used to skip a setting absent from the saved
	// document (omitempty), so a reset never outlived the process.
	if err := store.SetSitesDir(""); err != nil {
		t.Fatal(err)
	}
	if got, want := store.SitesDir(), P().Sites(); got != want {
		t.Errorf("after reset SitesDir = %q, want %q", got, want)
	}
	if again, err := OpenStore(); err != nil {
		t.Fatal(err)
	} else if again.Data.SitesDir != "" {
		t.Errorf("reset did not reach disk: reopened store has sites_dir %q", again.Data.SitesDir)
	}

	// Delete may wipe a whole directory only inside a tree we manage: our own,
	// or the configured sites directory. Everything else is the user's.
	store.SetSitesDir(filepath.Join(home, "Sites"))
	e := NewEngine(store)
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(P().Sites(), "x"), true},
		{filepath.Join(home, "Sites", "x"), true},
		{filepath.Join(home, "Documents", "client-work"), false},
		{filepath.Join(home, "Sites"), false}, // the parent itself is never ours to remove
	} {
		if got := e.managedDir(c.path); got != c.want {
			t.Errorf("managedDir(%s) = %v, want %v", c.path, got, c.want)
		}
	}
}
