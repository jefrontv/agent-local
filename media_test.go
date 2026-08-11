package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The .htaccess reader recognises one shape: "missing upload -> that origin".
// It must find the real thing (the rule people generate) and stay quiet on
// anything it cannot be sure about, since the value it returns gets served.
func TestHtaccessUploadsRule(t *testing.T) {
	real := `# BEGIN WP Upload Rewrite - Auto Generated
<IfModule mod_rewrite.c>
	RewriteEngine On
	RewriteCond %{HTTP_HOST} ^ta\.local$
	RewriteCond %{REQUEST_URI} ^/wp-content/uploads/[^\/]*/.*$
	RewriteCond %{REQUEST_FILENAME} !-f
	RewriteCond %{REQUEST_FILENAME} !-d
	RewriteRule ^(.*)$ https://transportaustralia.org.au/$1 [QSA,L]
</IfModule>
# END WP Upload Rewrite

# BEGIN WordPress
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
# END WordPress
`
	cases := []struct {
		name string
		body string
		want string
	}{
		{"the generated uploads rule", real, "https://transportaustralia.org.au"},
		{"trailing path kept", "RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ https://cdn.example.org/media/$1 [L]\n",
			"https://cdn.example.org/media"},
		{"plain wordpress rules are not a fallback",
			"RewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule . /index.php [L]\n", ""},
		{"uploads rule with a local target is not an origin",
			"RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ /fallback.php [L]\n", ""},
		{"commented out rule is ignored",
			"# RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\n# RewriteCond %{REQUEST_FILENAME} !-f\n# RewriteRule ^(.*)$ https://nope.example/$1\n", ""},
		{"uploads condition without the missing-file test",
			"RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteRule ^(.*)$ https://nope.example/$1 [L]\n", ""},
		{"no htaccess at all", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.body != "" {
				if err := os.WriteFile(filepath.Join(dir, ".htaccess"), []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := htaccessUploadsRule(dir); got != c.want {
				t.Errorf("htaccessUploadsRule = %q, want %q", got, c.want)
			}
		})
	}
}

// serveMediaFallback stands in for an Apache rewrite, so the rules it follows
// have to match: uploads only, missing only, query preserved, redirect not proxy.
func TestServeMediaFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "site")
	if err := os.MkdirAll(filepath.Join(docroot, "wp-content", "uploads", "2026", "02"), 0o755); err != nil {
		t.Fatal(err)
	}
	present := filepath.Join("wp-content", "uploads", "2026", "02", "here.png")
	if err := os.WriteFile(filepath.Join(docroot, present), []byte("local bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "s", Domain: "s.test", WPDir: docroot, MediaFallback: "https://origin.example"}
	store.PutSite(site)
	r := NewRouter(NewEngine(store))

	cases := []struct {
		name     string
		path     string
		query    string
		fallback string
		want     bool
		location string
	}{
		{name: "missing upload redirects", path: "/wp-content/uploads/2026/02/gone.png",
			fallback: "https://origin.example", want: true,
			location: "https://origin.example/wp-content/uploads/2026/02/gone.png"},
		{name: "query string is carried over", path: "/wp-content/uploads/2026/02/gone.png", query: "w=300&h=200",
			fallback: "https://origin.example", want: true,
			location: "https://origin.example/wp-content/uploads/2026/02/gone.png?w=300&h=200"},
		{name: "present upload is not redirected", path: "/" + present,
			fallback: "https://origin.example", want: false},
		{name: "outside uploads is not redirected", path: "/wp-content/themes/x/style.css",
			fallback: "https://origin.example", want: false},
		{name: "traversal is not redirected", path: "/wp-content/uploads/../../../../etc/passwd",
			fallback: "https://origin.example", want: false},
		{name: "no fallback configured", path: "/wp-content/uploads/2026/02/gone.png",
			fallback: "", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			site.MediaFallback = c.fallback
			store.PutSite(site)
			target := c.path
			if c.query != "" {
				target += "?" + c.query
			}
			req := httptest.NewRequest(http.MethodGet, "http://s.test"+target, nil)
			rec := httptest.NewRecorder()
			got := r.serveMediaFallback(rec, req, "s.test", docroot)
			if got != c.want {
				t.Fatalf("handled = %v, want %v", got, c.want)
			}
			if !c.want {
				return
			}
			if rec.Code != http.StatusFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
			}
			if loc := rec.Header().Get("Location"); loc != c.location {
				t.Errorf("Location = %q, want %q", loc, c.location)
			}
		})
	}
}

// The setter is the guard on what the router will hand to a browser.
func TestSetMediaFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "site")
	os.MkdirAll(docroot, 0o755)
	os.WriteFile(filepath.Join(docroot, ".htaccess"), []byte(
		"RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ https://from-htaccess.example/$1 [L]\n"), 0o644)

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot})
	e := NewEngine(store)

	if got, err := e.SetMediaFallback("s", "auto"); err != nil || got != "https://from-htaccess.example" {
		t.Errorf("auto = %q, %v; want the .htaccess origin", got, err)
	}
	if got, err := e.SetMediaFallback("s", "https://explicit.example/"); err != nil || got != "https://explicit.example" {
		t.Errorf("explicit = %q, %v; want a normalised origin", got, err)
	}
	if got, err := e.SetMediaFallback("s", ""); err != nil || got != "" {
		t.Errorf("empty = %q, %v; want it turned off", got, err)
	}
	for _, bad := range []string{"origin.example", "ftp://origin.example", "not a url"} {
		if _, err := e.SetMediaFallback("s", bad); err == nil {
			t.Errorf("SetMediaFallback accepted %q", bad)
		}
	}
	if _, err := e.SetMediaFallback("nope", "https://x.example"); err == nil {
		t.Error("SetMediaFallback accepted an unknown slug")
	}
	// auto with nothing to adopt must say so rather than set an empty value.
	store.PutSite(&Site{Slug: "bare", Domain: "bare.test", WPDir: t.TempDir()})
	if _, err := e.SetMediaFallback("bare", "auto"); err == nil {
		t.Error("auto succeeded with no .htaccess rule to adopt")
	}
}

// A rule sitting in the site's own .htaccess must work without a second command —
// needing one is how this looked broken twice. Precedence: pinned value, then an
// explicit off, then the file.
func TestEffectiveMediaFallback(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, ".htaccess"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rule := func(origin string) string {
		return "RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ " + origin + "/$1 [L]\n"
	}

	site := &Site{Slug: "s", WPDir: dir}
	if got := EffectiveMediaFallback(site); got != "" {
		t.Errorf("no .htaccess should mean no fallback, got %q", got)
	}

	write(rule("https://from-file.example"))
	if got := EffectiveMediaFallback(site); got != "https://from-file.example" {
		t.Errorf("the file's rule should be honoured, got %q", got)
	}

	// Editing the file takes effect: the cache is keyed by modification time, not
	// by "we looked once".
	time.Sleep(10 * time.Millisecond)
	write(rule("https://edited.example"))
	os.Chtimes(filepath.Join(dir, ".htaccess"), time.Now().Add(time.Second), time.Now().Add(time.Second))
	if got := EffectiveMediaFallback(site); got != "https://edited.example" {
		t.Errorf("an edited .htaccess should be picked up, got %q", got)
	}

	// A pinned value wins over the file.
	site.MediaFallback = "https://pinned.example"
	if got := EffectiveMediaFallback(site); got != "https://pinned.example" {
		t.Errorf("a pinned origin should win, got %q", got)
	}

	// Explicit off wins over the file, but not over a pinned value being set later.
	site.MediaFallback, site.MediaOff = "", true
	if got := EffectiveMediaFallback(site); got != "" {
		t.Errorf("an explicit off must ignore the file, got %q", got)
	}
}

// The setter has to leave those three states unambiguous.
func TestSetMediaFallbackStates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "site")
	os.MkdirAll(docroot, 0o755)
	os.WriteFile(filepath.Join(docroot, ".htaccess"), []byte(
		"RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ https://file.example/$1 [L]\n"), 0o644)

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot})
	e := NewEngine(store)

	// Untouched: the file is honoured.
	if got := EffectiveMediaFallback(store.Site("s")); got != "https://file.example" {
		t.Errorf("untouched site should honour the file, got %q", got)
	}
	// Off: nothing, and it survives a round trip through the store.
	if _, err := e.SetMediaFallback("s", ""); err != nil {
		t.Fatal(err)
	}
	if !store.Site("s").MediaOff {
		t.Error("empty value should record an explicit off")
	}
	if got := EffectiveMediaFallback(store.Site("s")); got != "" {
		t.Errorf("after --off the fallback must be nothing, got %q", got)
	}
	// auto: pins the file's origin and clears the off flag.
	if got, err := e.SetMediaFallback("s", "auto"); err != nil || got != "https://file.example" {
		t.Fatalf("auto = %q, %v", got, err)
	}
	if store.Site("s").MediaOff {
		t.Error("auto should clear the off flag")
	}
}
