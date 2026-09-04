package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPHPLiteral(t *testing.T) {
	cases := []struct {
		value, typ, want string
		wantErr          bool
	}{
		{"true", "", "true", false},
		{"TRUE", "", "true", false},
		{"512M", "", "'512M'", false},
		{"42", "", "42", false},
		{"1.5", "", "1.5", false},
		{"it's", "", `'it\'s'`, false},
		{"42", "string", "'42'", false},
		{"WP_HOME . '/x'", "raw", "WP_HOME . '/x'", false},
		{"x", "bogus", "", true},
	}
	for _, c := range cases {
		got, err := phpLiteral(c.value, c.typ)
		if c.wantErr {
			if err == nil {
				t.Errorf("phpLiteral(%q, %q): expected error, got %q", c.value, c.typ, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("phpLiteral(%q, %q): unexpected error: %v", c.value, c.typ, err)
			continue
		}
		if got != c.want {
			t.Errorf("phpLiteral(%q, %q) = %q, want %q", c.value, c.typ, got, c.want)
		}
	}
}

func TestCheckWPConstName(t *testing.T) {
	bad := []string{"DB_NAME", "AUTH_KEY", "wp_memory_limit", "ABSPATH", "not-valid"}
	for _, n := range bad {
		if err := checkWPConstName(n); err == nil {
			t.Errorf("checkWPConstName(%q): expected error, got nil", n)
		}
	}
	if err := checkWPConstName("WP_MEMORY_LIMIT"); err != nil {
		t.Errorf("checkWPConstName(WP_MEMORY_LIMIT): unexpected error: %v", err)
	}
}

func TestListWPConstants(t *testing.T) {
	src := `<?php
define( 'DB_NAME', 'mydb' );
// define( 'WP_DEBUG', true );
define("WP_MEMORY_LIMIT", '256M');
define( 'WP_HOME', 'https://example.test' );
`
	list := listWPConstants(src)
	if len(list) != 3 {
		t.Fatalf("listWPConstants: got %d constants, want 3: %+v", len(list), list)
	}
	names := map[string]string{}
	for _, c := range list {
		names[c.Name] = c.Value
	}
	if names["DB_NAME"] != "'mydb'" {
		t.Errorf("DB_NAME value = %q", names["DB_NAME"])
	}
	if _, ok := names["WP_DEBUG"]; ok {
		t.Errorf("commented-out WP_DEBUG should be skipped, got %+v", list)
	}
	if names["WP_MEMORY_LIMIT"] != "'256M'" {
		t.Errorf("WP_MEMORY_LIMIT value = %q", names["WP_MEMORY_LIMIT"])
	}
}

func newTestWPConfigSite(t *testing.T) *Site {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	wpDir := filepath.Join(dir, "site")
	if err := os.MkdirAll(wpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `<?php
define( 'DB_NAME', 'mydb' );
define( 'WP_MEMORY_LIMIT', '256M' );

/* That's all, stop editing! Happy publishing. */
require_once ABSPATH . 'wp-settings.php';
`
	if err := os.WriteFile(filepath.Join(wpDir, "wp-config.php"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Site{Slug: "testsite", WPDir: wpDir}
}

func TestSetWPConstantRoundTrip(t *testing.T) {
	site := newTestWPConfigSite(t)
	e := &Engine{}

	// Replaces an existing constant.
	c, err := e.SetWPConstant(site, "WP_MEMORY_LIMIT", "512M", "auto")
	if err != nil {
		t.Fatalf("SetWPConstant (replace): %v", err)
	}
	if c.Value != "'512M'" {
		t.Errorf("replaced value = %q, want '512M'", c.Value)
	}

	cfgPath, _ := wpConfigPath(site)
	if _, err := os.Stat(cfgPath + ".agent-local.bak"); err != nil {
		t.Errorf("expected backup file: %v", err)
	}

	// Adds a constant that was absent.
	c2, err := e.SetWPConstant(site, "WP_ENVIRONMENT_TYPE", "local", "auto")
	if err != nil {
		t.Fatalf("SetWPConstant (add): %v", err)
	}
	if c2.Value != "'local'" {
		t.Errorf("added value = %q, want 'local'", c2.Value)
	}

	list, err := e.WPConstants(site)
	if err != nil {
		t.Fatalf("WPConstants: %v", err)
	}
	found := map[string]string{}
	for _, c := range list {
		found[c.Name] = c.Value
	}
	if found["WP_MEMORY_LIMIT"] != "'512M'" {
		t.Errorf("WP_MEMORY_LIMIT after round trip = %q", found["WP_MEMORY_LIMIT"])
	}
	if found["WP_ENVIRONMENT_TYPE"] != "'local'" {
		t.Errorf("WP_ENVIRONMENT_TYPE after round trip = %q", found["WP_ENVIRONMENT_TYPE"])
	}

	// Rejects a protected name without touching the file.
	if _, err := e.SetWPConstant(site, "DB_NAME", "other", "auto"); err == nil {
		t.Error("SetWPConstant(DB_NAME): expected error")
	}

	// Removes a constant.
	removed, err := e.RemoveWPConstant(site, "WP_ENVIRONMENT_TYPE")
	if err != nil {
		t.Fatalf("RemoveWPConstant: %v", err)
	}
	if !removed {
		t.Error("RemoveWPConstant: expected removed=true")
	}
	list2, _ := e.WPConstants(site)
	for _, c := range list2 {
		if c.Name == "WP_ENVIRONMENT_TYPE" {
			t.Error("WP_ENVIRONMENT_TYPE should be gone after removal")
		}
	}

	removedAgain, err := e.RemoveWPConstant(site, "WP_ENVIRONMENT_TYPE")
	if err != nil {
		t.Fatalf("RemoveWPConstant (already gone): %v", err)
	}
	if removedAgain {
		t.Error("RemoveWPConstant: expected removed=false on second call")
	}
}
