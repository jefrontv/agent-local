package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<?php"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Each kind is named by the file its framework cannot run without; a docroot
// with only an index.php is plain PHP; nothing at all is empty. WordPress
// wins when its marker is present regardless of what else is there.
func TestDetectKind(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  AppKind
	}{
		{"wordpress", []string{"wp-load.php", "index.php"}, KindWordPress},
		{"joomla installed", []string{"index.php", "configuration.php", "administrator/index.php"}, KindJoomla},
		{"joomla fresh", []string{"index.php", "installation/index.php", "administrator/index.php"}, KindJoomla},
		{"laravel public", []string{"index.php", "../artisan"}, KindLaravel},
		{"drupal", []string{"index.php", "core/lib/Drupal.php"}, KindDrupal},
		{"plain php", []string{"index.php", "about.php"}, KindPHP},
		{"empty", nil, KindEmpty},
		// A wp-load.php beside Joomla markers is still WordPress: the marker is
		// specific, and every existing site row relies on this precedence.
		{"wp beats joomla markers", []string{"wp-load.php", "administrator/index.php", "configuration.php"}, KindWordPress},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "public")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			mkTree(t, root, c.files...)
			if got := DetectKind(root); got != c.want {
				t.Errorf("DetectKind = %q, want %q", got, c.want)
			}
		})
	}
}

// DocrootFor must keep every WordPress resolution it had, and additionally
// find a non-WordPress public/ — but never let an index.php beat wp-load.php.
func TestDocrootForNonWordPress(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(p, 0o755)
		mkTree(t, p, files...)
		return p
	}
	laravel := mk("laravel", "artisan")
	mk("laravel/public", "index.php")
	joomlaFlat := mk("joomla", "index.php", "configuration.php", "administrator/index.php")
	// Both a wp/ WordPress and a public/ plain app: WordPress wins.
	mixed := mk("mixed")
	mk("mixed/public", "index.php")
	mk("mixed/wp", "wp-load.php")
	bare := mk("bare")

	for _, c := range []struct{ dir, want string }{
		{laravel, filepath.Join(laravel, "public")},
		{joomlaFlat, joomlaFlat},
		{mixed, filepath.Join(mixed, "wp")},
		{bare, bare},
	} {
		if got := DocrootFor(c.dir); got != c.want {
			t.Errorf("DocrootFor(%s) = %s, want %s", filepath.Base(c.dir), got, c.want)
		}
	}
}

// Sites from before the Kind field carry an empty kind and are WordPress; the
// uploads prefix and tool gating both have to read them that way.
func TestEmptyKindReadsAsWordPress(t *testing.T) {
	s := &Site{Slug: "old"}
	if !s.IsWordPress() {
		t.Fatal("empty Kind must be WordPress for every pre-existing site row")
	}
	if got := s.Kind.UploadsPrefix(); got != "/wp-content/uploads/" {
		t.Errorf("empty kind uploads prefix = %q", got)
	}
	if got := (&Site{Slug: "j", Kind: KindJoomla}).Kind.UploadsPrefix(); got != "/images/" {
		t.Errorf("joomla uploads prefix = %q", got)
	}
	if got := KindPHP.UploadsPrefix(); got != "" {
		t.Errorf("plain php should have no fallback prefix, got %q", got)
	}
}

// The media fallback follows the app's uploads path: a Joomla site redirects
// misses under /images/, and never under /wp-content/uploads/.
func TestMediaFallbackFollowsKind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "public")
	os.MkdirAll(docroot, 0o755)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "j", Domain: "j.test", WPDir: docroot, Kind: KindJoomla, MediaFallback: "https://origin.example"})
	r := NewRouter(NewEngine(store))

	rec := httptest.NewRecorder()
	if !r.serveMediaFallback(rec, httptest.NewRequest(http.MethodGet, "http://j.test/images/hero.jpg", nil), "j.test", docroot) {
		t.Fatal("joomla /images/ miss should redirect to the origin")
	}
	if loc := rec.Header().Get("Location"); loc != "https://origin.example/images/hero.jpg" {
		t.Errorf("Location = %q", loc)
	}
	rec = httptest.NewRecorder()
	if r.serveMediaFallback(rec, httptest.NewRequest(http.MethodGet, "http://j.test/wp-content/uploads/x.jpg", nil), "j.test", docroot) {
		t.Error("a Joomla site must not treat /wp-content/uploads/ as media")
	}
}

// WordPress-only routes answer 409 naming the kind for another app, and keep
// working for WordPress. An "empty" attach that later received WordPress is
// re-detected on the spot.
func TestWordPressOnlyRoutesGateByKind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	jroot := filepath.Join(home, "joomla")
	mkTree(t, jroot, "index.php", "configuration.php", "administrator/index.php")
	store.PutSite(&Site{Slug: "j", Domain: "j.test", WPDir: jroot, Kind: KindJoomla, PHPVersion: "8.4",
		DBName: "al_j", DBUser: "al_j", DBPass: "x"})
	wroot := filepath.Join(home, "wp")
	mkTree(t, wroot, "wp-load.php", "index.php")
	os.WriteFile(filepath.Join(wroot, "wp-config.php"), []byte("<?php\ndefine('WP_DEBUG', false);\n"), 0o644)
	store.PutSite(&Site{Slug: "w", Domain: "w.test", WPDir: wroot, PHPVersion: "8.4",
		DBName: "al_w", DBUser: "al_w", DBPass: "x"})
	// Attached before any files arrived, then WordPress was installed.
	eroot := filepath.Join(home, "late")
	mkTree(t, eroot, "wp-load.php", "index.php")
	os.WriteFile(filepath.Join(eroot, "wp-config.php"), []byte("<?php\n"), 0o644)
	store.PutSite(&Site{Slug: "late", Domain: "late.test", WPDir: eroot, Kind: KindEmpty, PHPVersion: "8.4",
		DBName: "al_late", DBUser: "al_late", DBPass: "x"})
	api := &APIServer{store: store, engine: NewEngine(store)}
	mux := api.routes()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/sites/j/wp-config/constants"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "Joomla") {
		t.Errorf("joomla wp-config constants = %d %s, want 409 naming Joomla", rec.Code, rec.Body.String())
	}
	if rec := get("/sites/j/wp-debug"); rec.Code != 409 {
		t.Errorf("joomla wp-debug = %d, want 409", rec.Code)
	}
	if rec := get("/sites/w/wp-config/constants"); rec.Code != 200 {
		t.Errorf("wordpress wp-config constants = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := get("/sites/late/wp-config/constants"); rec.Code != 200 {
		t.Errorf("late-installed wordpress = %d %s, want 200 after re-detect", rec.Code, rec.Body.String())
	}
	if store.Site("late").Kind != KindWordPress {
		t.Errorf("re-detect should have persisted kind, got %q", store.Site("late").Kind)
	}
}

// Checkpoint scope defaults to the app's mutable tree: wp-content for
// WordPress, the whole docroot otherwise; asking for wp-content on Joomla is
// refused rather than snapshotting a directory that is not there.
func TestCheckpointScopeByKind(t *testing.T) {
	wp := &Site{Slug: "w", WPDir: "/x/wp"}
	if d, err := checkpointScopeDir(wp, ""); err != nil || d != "/x/wp/wp-content" {
		t.Errorf("wordpress default scope = %q, %v", d, err)
	}
	j := &Site{Slug: "j", WPDir: "/x/j", Kind: KindJoomla}
	if d, err := checkpointScopeDir(j, ""); err != nil || d != "/x/j" {
		t.Errorf("joomla default scope = %q, %v", d, err)
	}
	if _, err := checkpointScopeDir(j, "wp-content"); err == nil {
		t.Error("wp-content scope on joomla should be refused")
	}
	if d, err := checkpointScopeDir(j, "all"); err != nil || d != "/x/j" {
		t.Errorf("joomla all scope = %q, %v", d, err)
	}
}
