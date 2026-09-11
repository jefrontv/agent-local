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

// uploadsPrefix appends /uploads/ to the detected content path, and falls
// back to the kind default when nothing is configured.
func TestUploadsPrefix(t *testing.T) {
	e := testEngine(t)

	wp := t.TempDir()
	os.WriteFile(filepath.Join(wp, "wp-config.php"), []byte(
		"<?php\ndefine('CONTENT_DIR', '/app');\n"), 0o644)
	if got := e.uploadsPrefix(&Site{WPDir: wp, Kind: KindWordPress}); got != "/app/uploads/" {
		t.Errorf("bedrock uploads path = %q, want /app/uploads/", got)
	}

	if got := e.uploadsPrefix(&Site{WPDir: t.TempDir(), Kind: KindWordPress}); got != "/wp-content/uploads/" {
		t.Errorf("stock uploads path = %q, want /wp-content/uploads/", got)
	}

	if got := e.uploadsPrefix(&Site{WPDir: t.TempDir(), Kind: KindJoomla}); got != "/images/" {
		t.Errorf("joomla uploads path = %q, want /images/", got)
	}
}

// No wp-config at all (an attached, not-yet-WordPress docroot) still yields the
// kind default, not a crash.
func TestUploadsPrefixNoConfig(t *testing.T) {
	e := testEngine(t)
	if got := e.uploadsPrefix(&Site{WPDir: t.TempDir(), Kind: KindWordPress}); got != "/wp-content/uploads/" {
		t.Errorf("no-config uploads path = %q, want kind default", got)
	}
}

// Attaching a directory before its files arrive records KindEmpty. Nothing on
// the serving path used to re-detect once WordPress was installed into it, so
// the kind's empty prefix silently disabled the media fallback — every missing
// upload fell through to WordPress and 404'd while the .htaccess origin sat
// there unread. The prefix must be resolved from what is in the docroot now,
// and the record corrected so list/doctor stop disagreeing with it.
func TestUploadsPrefixRedetectsStaleEmptyKind(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wp-load.php"), []byte("<?php"), 0o644)
	site := &Site{Slug: "late", Domain: "late.test", WPDir: dir, Kind: KindEmpty}
	e.Store.PutSite(site)

	if got := e.uploadsPrefix(site); got != "/wp-content/uploads/" {
		t.Errorf("stale empty kind = %q, want the WordPress prefix", got)
	}
	if site.Kind != KindWordPress {
		t.Errorf("record not corrected: kind = %q, want wordpress", site.Kind)
	}
	if got := e.Store.Site("late").Kind; got != KindWordPress {
		t.Errorf("correction not persisted: kind = %q", got)
	}
}

// A docroot that is still empty stays empty: there are no uploads to miss, and
// inventing the WordPress prefix for it would be a guess.
func TestUploadsPrefixKeepsEmptyKindWhenNothingInstalled(t *testing.T) {
	e := testEngine(t)
	if got := e.uploadsPrefix(&Site{WPDir: t.TempDir(), Kind: KindEmpty}); got != "" {
		t.Errorf("empty docroot = %q, want no fallback", got)
	}
}

// A plain PHP site is not WordPress and must not be given its uploads prefix:
// only wp-load.php makes the re-detection branch apply.
func TestUploadsPrefixLeavesPlainPHPAlone(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.php"), []byte("<?php"), 0o644)
	site := &Site{WPDir: dir, Kind: KindPHP}
	if got := e.uploadsPrefix(site); got != "" {
		t.Errorf("plain php = %q, want no fallback", got)
	}
	if site.Kind != KindPHP {
		t.Errorf("plain php kind changed to %q", site.Kind)
	}
}

// The content-dir override still wins, so a Bedrock site that was attached
// empty and then installed resolves to its own uploads path, not the default.
func TestUploadsPrefixContentOverrideBeatsRedetect(t *testing.T) {
	e := testEngine(t)
	root := t.TempDir()
	web := filepath.Join(root, "web")
	cfgDir := filepath.Join(root, "config")
	os.MkdirAll(web, 0o755)
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(filepath.Join(web, "wp-load.php"), []byte("<?php"), 0o644)
	os.WriteFile(filepath.Join(web, "wp-config.php"), []byte(
		"<?php\nrequire_once dirname(__DIR__) . '/config/application.php';\n"), 0o644)
	os.WriteFile(filepath.Join(cfgDir, "application.php"), []byte(
		"<?php\nConfig::define('CONTENT_DIR', '/app');\n"), 0o644)

	if got := e.uploadsPrefix(&Site{WPDir: web, Kind: KindEmpty}); got != "/app/uploads/" {
		t.Errorf("bedrock after empty attach = %q, want /app/uploads/", got)
	}
}

// testEngine is a store and engine over a throwaway HOME, for the uploads
// prefix tests that need to persist a re-detected kind.
func testEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(store)
}

func timeNow() time.Time { return time.Now() }
