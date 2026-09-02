package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAdminerPath(t *testing.T) {
	if !isAdminerPath("/.agent-local/adminer") {
		t.Error("canonical path missed")
	}
	if !isAdminerPath("/.agent-local/adminer/") {
		t.Error("trailing slash missed")
	}
	if isAdminerPath("/wp-admin") || isAdminerPath("/.agent-local/other") {
		t.Error("false positive")
	}
}

func TestPhpQuoteEscapes(t *testing.T) {
	if got := phpQuote(`o'reilly\x`); got != `'o\'reilly\\x'` {
		t.Errorf("phpQuote = %s", got)
	}
}

func TestWriteAdminerBoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Skip the network: drop a stub adminer.php so EnsureAdminer is a no-op.
	dir := P().AdminerDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(P().AdminerPHP(), []byte("<?php /* stub */"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "demo", DBName: "al_demo", DBUser: "al_demo", DBPass: "p'ass", Domain: "demo.test"}
	boot, err := writeAdminerBoot(site)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(boot)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"al_demo", `p\'ass`, "127.0.0.1:", "function adminer_object", "include __DIR__ . '/adminer-" + adminerVersion + ".php'", "$_SESSION['pwds']", "session_name('adminer_sid')", "?theme=", "readfile(__DIR__ . '/agent-local.css')"} {
		if !strings.Contains(body, want) {
			t.Errorf("boot missing %q:\n%s", want, body)
		}
	}
	info, _ := os.Stat(boot)
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("boot perms %o should be 0600", info.Mode().Perm())
	}
}

func TestAdminerURLUsesSiteHost(t *testing.T) {
	got := AdminerURL("demo.test")
	if !strings.HasSuffix(got, AdminerPath) || !strings.Contains(got, "demo.test") {
		t.Errorf("AdminerURL = %q", got)
	}
}

func TestRouterInterceptsAdminerBeforeWordPress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stub adminer so serveAdminer fails at FPM (no pool), not at download.
	os.MkdirAll(P().AdminerDir(), 0o755)
	os.WriteFile(P().AdminerPHP(), []byte("<?php"), 0o644)

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	r := NewRouter(NewEngine(store))

	req := httptest.NewRequest(http.MethodGet, "http://s.test"+AdminerPath, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	// No FPM socket: 503 from serveAdminer, never a 502 "no site".
	if rec.Code == http.StatusBadGateway && strings.Contains(rec.Body.String(), "no site") {
		t.Fatalf("adminer path fell through to host routing: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusBadGateway {
		// Either "could not start" (503) or php-fpm connect (502) — both mean we
		// intercepted the path and tried to run Adminer.
		t.Errorf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestWorktreeHostGetsMediaFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	os.MkdirAll(filepath.Join(docroot, "wp-content", "uploads"), 0o755)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, MediaFallback: "https://origin.example"})
	store.PutWorktree(&Worktree{ID: "s--feat", Site: "s", Domain: "feat.s.test", Path: filepath.Join(home, "@", "feat")})
	if got := store.FindSiteByDomain("feat.s.test"); got == nil || got.Slug != "s" {
		t.Fatalf("preview host did not resolve to parent site: %+v", got)
	}
	r := NewRouter(NewEngine(store))
	req := httptest.NewRequest(http.MethodGet, "http://feat.s.test/wp-content/uploads/gone.png", nil)
	rec := httptest.NewRecorder()
	if !r.serveMediaFallback(rec, req, "feat.s.test", docroot) {
		t.Fatal("preview host skipped media fallback")
	}
	if loc := rec.Header().Get("Location"); loc != "https://origin.example/wp-content/uploads/gone.png" {
		t.Errorf("Location = %q", loc)
	}
}
