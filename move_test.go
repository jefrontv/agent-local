package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moveEnv builds a stopped site with a docroot nested under its work dir, the
// layout `create` produces. Stopped so MoveSite never reaches for php-fpm.
func moveEnv(t *testing.T) (*Store, *Engine, *Site, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "Sites", "demo")
	wp := filepath.Join(work, "wp")
	mkTree(t, wp, "wp-load.php", "index.php")
	site := &Site{Slug: "demo", Domain: "demo.test", WorkDir: work, WPDir: wp,
		PHPVersion: "8.4", State: StateStopped, DBName: "al_demo", DBUser: "al_demo", DBPass: "x"}
	store.PutSite(site)
	return store, NewEngine(store), site, home
}

// The docroot keeps its position under the work dir, so a created site
// (work/wp) and an attached checkout (work == docroot) both land right. Getting
// this wrong points php-fpm at a directory that does not exist and the site
// 502s with nothing in the log to explain it.
func TestMoveSiteKeepsDocrootPosition(t *testing.T) {
	store, e, _, home := moveEnv(t)
	dest := filepath.Join(home, "Code", "demo")

	if err := e.MoveSite("demo", dest); err != nil {
		t.Fatal(err)
	}
	got := store.Site("demo")
	if got.WorkDir != dest {
		t.Errorf("work dir = %q, want %q", got.WorkDir, dest)
	}
	if want := filepath.Join(dest, "wp"); got.WPDir != want {
		t.Errorf("docroot = %q, want %q", got.WPDir, want)
	}
	if !fileExists(filepath.Join(dest, "wp", "wp-load.php")) {
		t.Error("the files did not arrive at the destination")
	}
	if dirExists(filepath.Join(home, "Sites", "demo")) {
		t.Error("the old directory is still there")
	}
}

// An attached checkout is its own docroot; the two paths must stay equal.
func TestMoveSiteWithDocrootAtRoot(t *testing.T) {
	store, e, site, home := moveEnv(t)
	site.WPDir = site.WorkDir
	store.PutSite(site)

	dest := filepath.Join(home, "elsewhere")
	if err := e.MoveSite("demo", dest); err != nil {
		t.Fatal(err)
	}
	got := store.Site("demo")
	if got.WorkDir != dest || got.WPDir != dest {
		t.Errorf("work=%q docroot=%q, both should be %q", got.WorkDir, got.WPDir, dest)
	}
}

// The move must reach disk. A store that still names the old path sends every
// later command — start, probe, delete — to a directory that is gone.
func TestMoveSitePersists(t *testing.T) {
	_, e, _, home := moveEnv(t)
	dest := filepath.Join(home, "moved")
	if err := e.MoveSite("demo", dest); err != nil {
		t.Fatal(err)
	}
	next, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Site("demo").WorkDir; got != dest {
		t.Errorf("reopened store has work dir %q, want %q", got, dest)
	}
}

// An empty directory at the destination is fine — that is what `mkdir` before
// the move leaves — but anything in it must stop the move, because merging two
// trees cannot be undone.
func TestMoveSiteDestinationRules(t *testing.T) {
	_, e, site, home := moveEnv(t)
	src := site.WorkDir

	empty := filepath.Join(home, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(home, "occupied")
	mkTree(t, occupied, "something.txt")

	if err := e.MoveSite("demo", occupied); err == nil {
		t.Error("moved into a non-empty directory")
	} else if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("unhelpful error for a non-empty destination: %v", err)
	}
	if !fileExists(filepath.Join(src, "wp", "wp-load.php")) {
		t.Fatal("a refused move still disturbed the files")
	}
	if err := e.MoveSite("demo", empty); err != nil {
		t.Errorf("an empty directory should be an acceptable destination: %v", err)
	}
}

// Moving a tree inside itself destroys it, and asking to move somewhere the
// site already is should not stop and restart it for nothing.
func TestMoveSiteRejectsSelfAndDescendant(t *testing.T) {
	_, e, site, _ := moveEnv(t)
	src := site.WorkDir

	if err := e.MoveSite("demo", src); err == nil || !strings.Contains(err.Error(), "already at") {
		t.Errorf("move to its own path: %v", err)
	}
	if err := e.MoveSite("demo", filepath.Join(src, "inner")); err == nil ||
		!strings.Contains(err.Error(), "into itself") {
		t.Errorf("move into a subdirectory of itself: %v", err)
	}
	if !fileExists(filepath.Join(src, "wp", "wp-load.php")) {
		t.Error("a refused move disturbed the files")
	}
}

// Another site's directory is not a free destination.
func TestMoveSiteRejectsAnotherSitesDirectory(t *testing.T) {
	store, e, _, home := moveEnv(t)
	other := filepath.Join(home, "Sites", "other")
	mkTree(t, other, "index.php")
	store.PutSite(&Site{Slug: "other", Domain: "other.test", WorkDir: other, WPDir: other,
		PHPVersion: "8.4", State: StateStopped})

	err := e.MoveSite("demo", other)
	if err == nil || !strings.Contains(err.Error(), "belongs to site other") {
		t.Errorf("move onto another site: %v", err)
	}
}

// A branch preview symlinks absolute paths into the base docroot and its git
// worktree records an absolute gitdir, so moving the base site leaves both
// dangling. Refuse and name the command that clears them.
func TestMoveSiteRefusesWithWorktrees(t *testing.T) {
	store, e, site, home := moveEnv(t)
	store.PutWorktree(&Worktree{ID: "demo--feature", Site: "demo", Branch: "feature",
		Path: filepath.Join(site.WorkDir, ".previews", "feature"), Domain: "feature.demo.test"})

	err := e.MoveSite("demo", filepath.Join(home, "moved"))
	if err == nil {
		t.Fatal("moved a site with a branch preview")
	}
	for _, want := range []string{"branch preview", "worktree demo feature", "--remove"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestMoveSiteUnknownSlug(t *testing.T) {
	_, e, _, home := moveEnv(t)
	if err := e.MoveSite("nope", filepath.Join(home, "x")); err == nil ||
		!strings.Contains(err.Error(), "no such site") {
		t.Errorf("MoveSite on an unknown slug: %v", err)
	}
}
