package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The index answers on the exact path only: anything deeper belongs to
// Adminer, the inbox, or WordPress itself.
func TestIsHubPath(t *testing.T) {
	for _, ok := range []string{"/.agent-local", "/.agent-local/"} {
		if !isHubPath(ok) {
			t.Errorf("%s should be the hub page", ok)
		}
	}
	for _, no := range []string{"/", "/.agent-local/adminer", "/.agent-local/mail", "/wp-admin"} {
		if isHubPath(no) {
			t.Errorf("%s should not be the hub page", no)
		}
	}
}

// The hub page used to fall through to WordPress and render blank. It must
// answer itself, styled like the inbox, with links to both tools.
func TestRouterServesHubPage(t *testing.T) {
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
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.2",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	r := NewRouter(NewEngine(store))

	for _, p := range []string{HubPath, HubPath + "/"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{`href="/.agent-local/adminer"`, `href="/.agent-local/mail"`, "s.test", "lamp"} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s body missing %q", p, want)
			}
		}
	}
}

// The daemon route behind the apache ProxyPass validates the pool id like
// the inbox does, and titles the page with the site domain.
func TestHubUIRouteValidatesID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: filepath.Join(home, "wp"),
		PHPVersion: "8.2", DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	mux := (&APIServer{store: store}).routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hub-ui/s", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "s.test") {
		t.Errorf("hub-ui/s = %d, want 200 titled with the domain", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hub-ui/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("hub-ui/nope = %d, want 404", rec.Code)
	}
}

// The apache vhost must forward the exact hub path to the daemon without
// disturbing the adminer Alias or the inbox ProxyPass beside it.
func TestApacheConfProxiesHubPage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: filepath.Join(home, "wp"),
		PHPVersion: "8.2", DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	if err := renderApacheConf(store); err != nil {
		t.Skipf("cannot render conf in test env: %v", err)
	}
	b, err := os.ReadFile(P().ApacheConf())
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	// The Alias only renders once an adminer release is downloaded; the
	// inbox ProxyPass and the hub match render unconditionally.
	for _, want := range []string{`ProxyPassMatch ^/\.agent-local/?$`, "/hub-ui/s", "ProxyPass /.agent-local/mail"} {
		if !strings.Contains(conf, want) {
			t.Errorf("apache conf missing %q", want)
		}
	}
}
