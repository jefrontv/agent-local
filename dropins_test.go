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
