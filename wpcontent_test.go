package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A stock WordPress install has no content-dir override, so the fallback keeps
// the conventional /wp-content/uploads/ — no non-default value is invented.
func TestContentURLPathDefault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(
		"<?php define('DB_NAME','x');\ndefine('WP_DEBUG', false);\n"), 0o644)
	if got := contentURLPath(dir); got != "" {
		t.Errorf("stock wp-config = %q, want \"\"", got)
	}
}

// Bedrock declares the content dir in config/application.php, required from
// wp-config.php via dirname(__DIR__) . '/config/application.php'.
func TestContentURLPathBedrock(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	cfgDir := filepath.Join(root, "config")
	os.MkdirAll(web, 0o755)
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(web, "wp-config.php"), []byte(
		"<?php\nrequire_once dirname(__DIR__) . '/config/application.php';\n"), 0o644)
	os.WriteFile(filepath.Join(cfgDir, "application.php"), []byte(
		"<?php\nConfig::define('CONTENT_DIR', '/app');\n"), 0o644)
	if got := contentURLPath(web); got != "/app" {
		t.Errorf("bedrock content url path = %q, want /app", got)
	}
}

// A standard define('UPLOADS', ...) overrides the uploads subdirectory.
func TestContentURLPathUploads(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(
		"<?php\ndefine('UPLOADS', '/media');\n"), 0o644)
	if got := contentURLPath(dir); got != "/media" {
		t.Errorf("UPLOADS path = %q, want /media", got)
	}
}

// A literal WP_CONTENT_URL (https://host/custom) yields its path.
func TestContentURLPathFromURL(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(
		"<?php\ndefine('WP_CONTENT_URL', 'https://example.test/app');\n"), 0o644)
	if got := contentURLPath(dir); got != "/app" {
		t.Errorf("WP_CONTENT_URL path = %q, want /app", got)
	}
}

// CONTENT_DIR still pointing at the WP default is not an override.
func TestContentURLPathDefaultExplicit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(
		"<?php\ndefine('CONTENT_DIR', '/wp-content');\n"), 0o644)
	if got := contentURLPath(dir); got != "" {
		t.Errorf("explicit default = %q, want \"\"", got)
	}
}

// A config edit is picked up because the cache keys on the file's mtime.
func TestContentURLPathCachedInvalidates(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "wp-config.php")
	os.WriteFile(cfg, []byte("<?php\n"), 0o644)
	future := timeNow().Add(2 * time.Second)
	os.Chtimes(cfg, future, future)

	// Seed with a Bedrock config.
	os.WriteFile(cfg, []byte("<?php\ndefine('CONTENT_DIR', '/app');\n"), 0o644)
	if got := contentURLPathCached(dir); got != "/app" {
		t.Fatalf("first read = %q, want /app", got)
	}
	// Move the file to a new mtime and remove the override.
	nx := future.Add(2 * time.Second)
	os.Chtimes(cfg, nx, nx)
	os.WriteFile(cfg, []byte("<?php\n"), 0o644)
	os.Chtimes(cfg, nx, nx)
	if got := contentURLPathCached(dir); got != "" {
		t.Errorf("after edit = %q, want \"\"", got)
	}
}

// siteUploadsURLPath appends /uploads/ to the detected content path, and falls
// back to the kind default when nothing is configured.
func TestSiteUploadsURLPath(t *testing.T) {
	wp := t.TempDir()
	os.WriteFile(filepath.Join(wp, "wp-config.php"), []byte(
		"<?php\ndefine('CONTENT_DIR', '/app');\n"), 0o644)
	site := &Site{WPDir: wp, Kind: KindWordPress}
	if got := siteUploadsURLPath(site); got != "/app/uploads/" {
		t.Errorf("bedrock uploads path = %q, want /app/uploads/", got)
	}

	stock := &Site{WPDir: t.TempDir(), Kind: KindWordPress}
	if got := siteUploadsURLPath(stock); got != "/wp-content/uploads/" {
		t.Errorf("stock uploads path = %q, want /wp-content/uploads/", got)
	}

	if got := siteUploadsURLPath(&Site{WPDir: t.TempDir(), Kind: KindJoomla}); got != "/images/" {
		t.Errorf("joomla uploads path = %q, want /images/", got)
	}
}

// No wp-config at all (an attached, not-yet-WordPress docroot) still yields the
// kind default, not a crash.
func TestSiteUploadsURLPathNoConfig(t *testing.T) {
	site := &Site{WPDir: t.TempDir(), Kind: KindWordPress}
	if got := siteUploadsURLPath(site); got != "/wp-content/uploads/" {
		t.Errorf("no-config uploads path = %q, want kind default", got)
	}
}

func timeNow() time.Time { return time.Now() }
