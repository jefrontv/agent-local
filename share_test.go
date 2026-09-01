package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTunnelURLRe(t *testing.T) {
	// Shapes cloudflared actually logs, banner box included.
	line := `2026-09-01T01:02:03Z INF |  https://poem-brave-lamb-decade.trycloudflare.com                                |`
	if got := tunnelURLRe.FindString(line); got != "https://poem-brave-lamb-decade.trycloudflare.com" {
		t.Errorf("url = %q", got)
	}
	if tunnelURLRe.FindString("INF Requesting new quick Tunnel on trycloudflare.com...") != "" {
		t.Error("matched a line with no URL")
	}
}

func TestShareRegistry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(P().Run(), 0o755)
	mu := filepath.Join(t.TempDir(), shareMUName)
	os.WriteFile(mu, []byte("x"), 0o644)
	pid := sharePidFile("s")
	os.WriteFile(pid, []byte("99999999"), 0o644)

	sh := &Share{Slug: "s", Host: "x.trycloudflare.com", URL: "https://x.trycloudflare.com", muPath: mu}
	shares.add(sh)
	defer shares.remove(sh)

	if shares.ForHost("x.trycloudflare.com") != sh || shares.ForSlug("s") != sh {
		t.Fatal("registry lookups failed")
	}
	if shares.ForHost("y.trycloudflare.com") != nil {
		t.Fatal("phantom host matched")
	}

	// Shutdown is idempotent and removes the artifacts either exit path owns.
	sh.shutdown()
	sh.shutdown()
	if shares.ForSlug("s") != nil {
		t.Error("share still registered after shutdown")
	}
	if fileExists(mu) || fileExists(pid) {
		t.Error("shutdown left mu-plugin or pid file behind")
	}
}

func TestWriteShareMU(t *testing.T) {
	dir := t.TempDir()
	site := &Site{Slug: "s", Domain: "s.test", Aliases: []string{"alias.test"}, WPDir: dir}
	if err := writeShareMU(site, "x.trycloudflare.com"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "wp-content", "mu-plugins", shareMUName))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// Host-conditional: local requests must keep local URLs.
	if !strings.Contains(src, "'x.trycloudflare.com' === $_SERVER['HTTP_HOST']") {
		t.Errorf("mu-plugin is not host-conditional:\n%s", src)
	}
	if !strings.Contains(src, "$al_share_host = 'x.trycloudflare.com'") {
		t.Error("mu-plugin missing tunnel host")
	}
	if !strings.Contains(src, "redirect_canonical") || !strings.Contains(src, "PHP_INT_MAX") {
		t.Error("mu-plugin missing canonical-redirect pinning")
	}
	// The output rewrite must cover the primary domain and every alias —
	// imported sites carry those baked into content and WP_CONTENT_URL.
	if !strings.Contains(src, "ob_start") || !strings.Contains(src, "'s.test', 'alias.test'") {
		t.Errorf("mu-plugin missing output rewrite of local domains:\n%s", src)
	}
}

func TestStartShareNeedsRouterFront(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: t.TempDir()})
	store.Data.Front = "apache"
	e := NewEngine(store)
	_, err = e.StartShare("s", 0, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "router front") {
		t.Errorf("apache-front share error = %v", err)
	}
	if _, err := e.StartShare("ghost", 0, func(string, string) {}); err == nil || !strings.Contains(err.Error(), "no such site") {
		t.Errorf("ghost share error = %v", err)
	}
	if e.StopShare("s") {
		t.Error("stopping an unshared site reported true")
	}
}

func TestShareExpiryFieldRoundtrip(t *testing.T) {
	// The expiry pointer is what tells a caller "this stops itself": absent
	// means until stopped, present is honoured by the timer.
	exp := time.Now().Add(time.Hour)
	sh := &Share{Slug: "s", ExpiresAt: &exp}
	if sh.ExpiresAt == nil || !sh.ExpiresAt.Equal(exp) {
		t.Fatal("expiry lost")
	}
	if (&Share{}).ExpiresAt != nil {
		t.Fatal("zero share carries an expiry")
	}
}
