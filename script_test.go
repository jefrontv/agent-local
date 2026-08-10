package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Which PHP file runs a request decides whether /wp-admin/ works at all: sending
// a directory to the site's front controller made WordPress canonically redirect
// to the URL it was already serving, which browsers report as
// ERR_TOO_MANY_REDIRECTS. Real servers try that directory's own index.php first.
func TestResolveScript(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(rel string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("<?php"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("index.php")
	mk("wp-admin")
	touch("wp-admin/index.php")
	touch("wp-admin/edit.php")
	mk("wp-content/uploads/2026")
	mk("downloads") // a real directory with no index.php: a permalink may own it
	touch("wp-login.php")

	front := filepath.Join(root, "index.php")
	cases := []struct {
		name       string
		path       string
		wantScript string
		wantName   string
	}{
		{"admin directory answers for itself", "/wp-admin/",
			filepath.Join(root, "wp-admin", "index.php"), "/wp-admin/index.php"},
		{"admin directory without the slash", "/wp-admin",
			filepath.Join(root, "wp-admin", "index.php"), "/wp-admin/index.php"},
		{"a real php file runs directly", "/wp-admin/edit.php",
			filepath.Join(root, "wp-admin", "edit.php"), "/wp-admin/edit.php"},
		{"login script", "/wp-login.php", filepath.Join(root, "wp-login.php"), "/wp-login.php"},
		{"permalink goes to the front controller", "/about-us/", front, "/index.php"},
		{"directory without an index stays with the front controller", "/downloads/", front, "/index.php"},
		{"asset directory stays with the front controller", "/wp-content/uploads/2026/", front, "/index.php"},
		{"missing php falls back rather than 404ing in fpm", "/nope.php", front, "/index.php"},
		{"root", "/", front, "/index.php"},
		{"traversal cannot escape the docroot", "/../../../../etc/passwd", front, "/index.php"},
		{"traversal dressed as an upload", "/wp-content/uploads/../../../../etc/passwd", front, "/index.php"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			script, name := resolveScript(root, c.path)
			if script != c.wantScript {
				t.Errorf("script = %q, want %q", script, c.wantScript)
			}
			if name != c.wantName {
				t.Errorf("scriptName = %q, want %q", name, c.wantName)
			}
		})
	}
}

// The trailing slash is added only for directories that really do answer, so a
// permalink that happens to share a name with a folder is left to WordPress.
func TestDirWithIndex(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "wp-admin"), 0o755)
	os.WriteFile(filepath.Join(root, "wp-admin", "index.php"), []byte("<?php"), 0o644)
	os.MkdirAll(filepath.Join(root, "downloads"), 0o755)
	os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php"), 0o644)

	for _, c := range []struct {
		path string
		want bool
	}{
		{"/wp-admin", true},
		{"/downloads", false}, // no index.php: could be a page
		{"/about-us", false},  // not a directory at all
		{"/", false},
		{"", false},
		{"/../..", false},
	} {
		if got := dirWithIndex(root, c.path); got != c.want {
			t.Errorf("dirWithIndex(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
