package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDropin(t *testing.T, wpdir, name, body string) {
	t.Helper()
	dir := filepath.Join(wpdir, "wp-content")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A drop-in that embeds another machine's docroot is stale; one pointing into
// this docroot, at PHP/OS paths, or carrying a path fragment concatenated onto
// a constant (`WP_PLUGIN_DIR . '/wp-optimize/cache'`) is not. The origin
// docroot is what gets reported, since that is what the user recognises.
func TestStaleDropinsTellsForeignFromLocal(t *testing.T) {
	wpdir := filepath.Join(t.TempDir(), "public")
	writeDropin(t, wpdir, "advanced-cache.php",
		`<?php $rocket_path = '/home/newark/public_html/wp-content/plugins/wp-rocket/';`)
	writeDropin(t, wpdir, "object-cache.php",
		`<?php require '`+wpdir+`/wp-content/plugins/redis-cache/includes/object-cache.php';
		$predis = '/dependencies/predis/predis/autoload.php';`)
	writeDropin(t, wpdir, "db.php",
		`<?php ini_set('include_path', '/usr/local/lib/php'); $x = "/opt/homebrew/etc/php.ini";
		$c = defined('WP_PLUGIN_DIR') ? WP_PLUGIN_DIR.'/wp-optimize/cache' : false;`)

	got := staleDropins(wpdir)
	if len(got) != 1 {
		t.Fatalf("stale = %+v, want exactly the advanced-cache drop-in", got)
	}
	if got[0].File != "wp-content/advanced-cache.php" || got[0].Paths != "/home/newark/public_html" {
		t.Errorf("stale[0] = %+v, want advanced-cache pointing at /home/newark/public_html", got[0])
	}
}

// No wp-content, or no drop-ins at all, is simply clean — never an error.
func TestStaleDropinsQuietWhenAbsent(t *testing.T) {
	if got := staleDropins(filepath.Join(t.TempDir(), "nowhere")); len(got) != 0 {
		t.Errorf("missing docroot reported %+v", got)
	}
	wpdir := t.TempDir()
	os.MkdirAll(filepath.Join(wpdir, "wp-content"), 0o755)
	if got := staleDropins(wpdir); len(got) != 0 {
		t.Errorf("empty wp-content reported %+v", got)
	}
}

// Fatal detection reads the tail only, ignores fatals older than the window
// (pool logs are never rotated, so last week's crash must not warn forever),
// and returns the last live message trimmed of the log prefix.
func TestRecentFatalsCountsAndTrims(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fpm.log")
	stamp := func(at time.Time) string { return at.UTC().Format("[02-Jan-2006 15:04:05 MST]") }
	now := time.Now()
	var b strings.Builder
	for range 500 {
		b.WriteString(stamp(now.Add(-time.Minute)) + " PHP Fatal error:  old one that should scroll out of the tail\n")
	}
	b.WriteString(stamp(now.Add(-48*time.Hour)) + " PHP Fatal error:  two days ago, outside the window\n")
	b.WriteString(stamp(now.Add(-time.Minute)) + " NOTICE: fpm is running\n")
	b.WriteString(stamp(now.Add(-time.Minute)) + " PHP Warning:  not a fatal\n")
	b.WriteString(stamp(now.Add(-time.Minute)) + " PHP Fatal error:  Uncaught TypeError: implode(): Argument #1 must be string in /x/CSS.php:528\n")
	b.WriteString(stamp(now) + " PHP Fatal error:  Uncaught TypeError: implode(): Argument #1 must be string in /x/CSS.php:528\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	n, last := recentFatals(p, 5, 24*time.Hour)
	if n != 2 {
		t.Errorf("count = %d, want 2 (tail only, and not the two-day-old one)", n)
	}
	if !strings.HasPrefix(last, "Uncaught TypeError: implode()") {
		t.Errorf("last = %q, want the message without the log prefix", last)
	}
	if n, _ := recentFatals(filepath.Join(t.TempDir(), "missing.log"), 10, time.Hour); n != 0 {
		t.Errorf("missing log counted %d fatals", n)
	}
}

// Regeneration only touches WP Rocket's drop-in, and only when the plugin is
// present to do it; everything else is handed back untouched.
func TestRegenerateDropinsGatesOnPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wpdir := filepath.Join(home, "public")
	writeDropin(t, wpdir, "advanced-cache.php", `<?php $p = '/home/x/public_html/wp-content/plugins/wp-rocket/';`)
	writeDropin(t, wpdir, "object-cache.php", `<?php $p = '/home/x/public_html/wp-content/plugins/redis/';`)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "s", Domain: "s.test", WPDir: wpdir, PHPVersion: "8.4", DBName: "al_s", DBUser: "al_s", DBPass: "x"}
	store.PutSite(site)
	e := NewEngine(store)

	// No wp-rocket plugin on disk: nothing is run, both are left.
	fixed, left, err := e.RegenerateDropins(site)
	if len(fixed) != 0 || len(left) != 2 || err != nil {
		t.Fatalf("without plugin: fixed=%v left=%v err=%v; want nothing fixed, both left, no error", fixed, left, err)
	}
	// advanced-cache.php must be untouched — we never hand-edit drop-ins.
	b, _ := os.ReadFile(filepath.Join(wpdir, "wp-content", "advanced-cache.php"))
	if !strings.Contains(string(b), "/home/x/public_html") {
		t.Error("advanced-cache.php was modified without the plugin present")
	}
}

// A rename clears the old hostname's WP Rocket page cache (a warm cache there
// hides faults the new name will hit) and names any stale drop-in, without
// touching the drop-in itself: a rename is an operation on a site someone is
// already working in.
func TestAfterDomainChangeClearsOldCacheAndWarnsOnly(t *testing.T) {
	wpdir := t.TempDir()
	oldCache := filepath.Join(wpdir, "wp-content", "cache", "wp-rocket", "old.test")
	newCache := filepath.Join(wpdir, "wp-content", "cache", "wp-rocket", "new.test")
	for _, d := range []string{oldCache, newCache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "index-https.html"), []byte("<html>"), 0o644)
	}
	stale := `<?php $p = '/home/x/public_html/wp-content/plugins/wp-rocket/';`
	writeDropin(t, wpdir, "advanced-cache.php", stale)

	warns := afterDomainChange(wpdir, "old.test")

	if fileExists(oldCache) {
		t.Error("old hostname's cache should be cleared")
	}
	if !fileExists(newCache) {
		t.Error("another hostname's cache must not be touched")
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/home/x/public_html") || !strings.Contains(warns[0], "doctor --fix") {
		t.Errorf("warns = %v, want one naming the origin path and the fix", warns)
	}
	if b, _ := os.ReadFile(filepath.Join(wpdir, "wp-content", "advanced-cache.php")); string(b) != stale {
		t.Error("rename must not modify the drop-in")
	}
	// No old domain (first-time set): nothing to clear, still reports.
	if got := afterDomainChange(wpdir, ""); len(got) != 1 {
		t.Errorf("empty old domain: warns = %v", got)
	}
}

// A rename must release the old name in the store's domain index at once: the
// old /etc/hosts line is only removed when DomainFree(old) says so, and a
// rename back is refused as "in use" otherwise. A bare field write left the
// index stale, so every rename leaked a hosts line and could not be undone.
func TestSetDomainReleasesOldName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "old.test", WPDir: filepath.Join(home, "wp"), PHPVersion: "8.4",
		DBName: "al_s", DBUser: "al_s", DBPass: "x", State: StateStopped})
	if store.DomainFree("old.test") {
		t.Fatal("precondition: old.test should be taken")
	}
	// Change the domain the way SetDomain does, minus the side effects that
	// need root or a running stack.
	site := store.Site("s")
	site.Domain = "new.test"
	store.PutSite(site)

	if !store.DomainFree("old.test") {
		t.Error("old name still indexed after rename")
	}
	if store.DomainFree("new.test") {
		t.Error("new name not indexed after rename")
	}
	if got, _ := store.LookupDomain("new.test"); got == nil || got.Slug != "s" {
		t.Error("router lookup of the new name misses the site")
	}
	if got, _ := store.LookupDomain("old.test"); got != nil {
		t.Error("router lookup of the old name still resolves")
	}
}
