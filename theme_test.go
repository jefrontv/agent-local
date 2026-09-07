package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every tool page a site serves carries the same scheme switch: the hub, the
// inbox list, a message, and the database GUI wrapper. Drop it from one and
// that page alone stops honouring a pin made on the others.
func TestToolPagesShipThemeSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"}
	store.PutSite(site)
	r := NewRouter(NewEngine(store))
	mid, err := StoreMail("s", []byte(plainMail))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{HubPath, MailPath, MailPath + "/msg/" + mid} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, themeKey) || !strings.Contains(body, `data-mount=".bar .actions"`) {
			t.Errorf("GET %s lacks the theme switch", p)
		}
		if strings.Contains(body, "color-scheme: dark;") && !strings.Contains(body, "color-scheme: light dark") {
			t.Errorf("GET %s is pinned dark", p)
		}
	}

	// The GUI wrapper is PHP carrying the script as a quoted string: the
	// switch has to reach the page, under Adminer's nonce or its CSP drops
	// it, and the quoting has to parse.
	if err := os.MkdirAll(P().AdminerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(P().AdminerPHP(), []byte("<?php /* stub */"), 0o644); err != nil {
		t.Fatal(err)
	}
	boot, err := writeAdminerBoot(site)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(boot)
	for _, want := range []string{themeKey, "=> ''", `'<script' . Adminer\nonce() . ' data-mount=".logout">'`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("adminer wrapper missing %q:\n%s", want, b)
		}
	}
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not on PATH; wrapper not lint-checked")
	}
	if out, err := exec.Command(php, "-l", boot).CombinedOutput(); err != nil {
		t.Errorf("wrapper does not parse: %s", out)
	}
}
