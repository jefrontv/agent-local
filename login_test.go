package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLoginTestSite(t *testing.T) *Site {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	wpdir := filepath.Join(t.TempDir(), "wp")
	if err := os.MkdirAll(wpdir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Site{
		Slug:   "loginsite",
		Domain: "loginsite.test",
		WPDir:  wpdir,
	}
}

func TestMagicLoginWritesTokenAndPlugin(t *testing.T) {
	site := newLoginTestSite(t)
	e := &Engine{}

	link, err := e.MagicLogin(site, "admin")
	if err != nil {
		t.Fatalf("MagicLogin: %v", err)
	}
	if link.User != "admin" {
		t.Errorf("link.User = %q, want admin", link.User)
	}
	if !strings.Contains(link.URL, "agent_local_login=") {
		t.Errorf("URL missing token param: %s", link.URL)
	}

	tokPath := loginTokenPath(site.Slug)
	info, err := os.Stat(tokPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	var tok loginToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if !strings.Contains(link.URL, tok.Token) {
		t.Errorf("URL does not carry the written token")
	}
	untilExpiry := time.Until(tok.Expires)
	if untilExpiry <= 4*time.Minute || untilExpiry > 5*time.Minute+time.Second {
		t.Errorf("expiry ~5m from now, got %v", untilExpiry)
	}

	muPath := loginMUPath(site)
	muRaw, err := os.ReadFile(muPath)
	if err != nil {
		t.Fatalf("stat mu-plugin: %v", err)
	}
	mu := string(muRaw)
	for _, want := range []string{tokPath, "hash_equals", "wp_set_auth_cookie", "unlink(__FILE__)"} {
		if !strings.Contains(mu, want) {
			t.Errorf("mu-plugin missing %q", want)
		}
	}

	if bin, err := exec.LookPath("php"); err == nil {
		out, err := exec.Command(bin, "-l", muPath).CombinedOutput()
		if err != nil {
			t.Errorf("php -l failed: %v\n%s", err, out)
		}
	}
}

func TestMagicLoginReplacesPreviousToken(t *testing.T) {
	site := newLoginTestSite(t)
	e := &Engine{}

	first, err := e.MagicLogin(site, "admin")
	if err != nil {
		t.Fatalf("first MagicLogin: %v", err)
	}
	second, err := e.MagicLogin(site, "admin")
	if err != nil {
		t.Fatalf("second MagicLogin: %v", err)
	}
	if first.URL == second.URL {
		t.Fatal("second call reused the same token")
	}

	raw, err := os.ReadFile(loginTokenPath(site.Slug))
	if err != nil {
		t.Fatal(err)
	}
	var tok loginToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.URL, tok.Token) {
		t.Error("old token still present in the token file after a second MagicLogin call")
	}
	if !strings.Contains(second.URL, tok.Token) {
		t.Error("token file does not hold the newest token")
	}
}

// A login from a branch domain must land in that branch's wp-admin: the
// mu-plugin goes into the worktree's checkout and the link points at the
// worktree's domain, leaving the site's own docroot untouched.
func TestMagicLoginWorktreeTargetsTheWorktree(t *testing.T) {
	site := newLoginTestSite(t)
	wtPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wtPath, "wp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "wp", "wp-load.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := &Worktree{ID: "loginsite--feature", Site: site.Slug, Branch: "feature", Path: wtPath, Domain: "feature.loginsite.test"}
	e := &Engine{}

	link, err := e.MagicLoginWorktree(site, wt, "admin")
	if err != nil {
		t.Fatalf("MagicLoginWorktree: %v", err)
	}
	if !strings.Contains(link.URL, "://feature.loginsite.test") {
		t.Errorf("URL = %s, want the worktree domain", link.URL)
	}
	if !fileExists(loginMUPathAt(filepath.Join(wtPath, "wp"))) {
		t.Error("mu-plugin not written into the worktree docroot")
	}
	if fileExists(loginMUPath(site)) {
		t.Error("mu-plugin written into the site's docroot")
	}
	if !fileExists(loginTokenPath(wt.ID)) {
		t.Error("token not keyed by the worktree ID")
	}
	if fileExists(loginTokenPath(site.Slug)) {
		t.Error("token written under the site slug")
	}
}
