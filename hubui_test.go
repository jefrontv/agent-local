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
	mux := (&APIServer{store: store, engine: NewEngine(store)}).routes()

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

// The apache vhost must forward the hub and its pages to the daemon without
// disturbing the adminer Alias or the inbox ProxyPass beside it.
func TestApacheConfProxiesHubPages(t *testing.T) {
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
	// inbox ProxyPass and the hub match render unconditionally. The hub
	// match must carry the page through ($1) and must not swallow the
	// longer adminer and mail paths.
	for _, want := range []string{
		`ProxyPassMatch ^/\.agent-local(/(?:errors|requests|login))?/?$`,
		"/hub-ui/s$1", "ProxyPass /.agent-local/mail",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("apache conf missing %q", want)
		}
	}
}

// The hub is a set of pages, not one: each must answer on the router and
// through the apache proxy, an unknown page under the prefix must 404
// rather than fall through to WordPress, and none of it may leak to a
// share tunnel.
func TestHubPagesRouteAndGate(t *testing.T) {
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
	mux := (&APIServer{store: store, engine: NewEngine(store)}).routes()

	for _, page := range []string{"", "/errors", "/requests"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath+page, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("router GET %s = %d, want 200", HubPath+page, rec.Code)
		}
		// Same page through the apache front's proxy route.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hub-ui/s"+page, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("daemon GET /hub-ui/s%s = %d, want 200", page, rec.Code)
		}
		// Links on a proxied page must point at the browser-facing path.
		if page == "" && !strings.Contains(rec.Body.String(), `href="`+HubPath+`/errors"`) {
			t.Error("proxied hub page does not link its own pages")
		}
	}

	// The hub owns the prefix that Adminer and the inbox live under, so a
	// prefix match must not swallow their longer paths.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+MailPath, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "inbox") {
		t.Errorf("GET %s = %d, want the inbox", MailPath, rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath+"/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown hub page = %d, want 404", rec.Code)
	}

	// A share exposes the site, never its tooling.
	sh := &Share{Slug: "s", Host: "abc.trycloudflare.com"}
	shares.add(sh)
	defer shares.remove(sh)
	for _, page := range []string{"", "/errors", "/requests", "/login"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://abc.trycloudflare.com"+HubPath+page, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("shared GET %s = %d, want 404", HubPath+page, rec.Code)
		}
	}
}

// Minting a wp-admin session is a write: a GET must not do it, and neither
// must a form on somebody else's page.
func TestHubLoginIsGuarded(t *testing.T) {
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

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath+"/login", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET login = %d, want 405", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "http://s.test"+HubPath+"/login", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST login = %d, want 403", rec.Code)
	}
}

// The card is the reason to open the hub at all; the two facts a person
// most often goes to a terminal for must be on it, and a non-WordPress site
// must be told so rather than offered a login it cannot use.
func TestHubCardReportsSiteAndKind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(filepath.Join(docroot, "wp-includes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "wp-includes", "version.php"),
		[]byte("<?php\n$wp_version = '6.7.1';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", WPDir: docroot, PHPVersion: "8.4",
		DBName: "al_s", DBUser: "al_s", DBPass: "x", Kind: KindWordPress})
	joomla := filepath.Join(home, "joomla")
	if err := os.MkdirAll(joomla, 0o755); err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "j", Domain: "j.test", WPDir: joomla, PHPVersion: "8.3",
		DBName: "al_j", DBUser: "al_j", DBPass: "x", Kind: KindJoomla})
	r := NewRouter(NewEngine(store))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://s.test"+HubPath, nil))
	body := rec.Body.String()
	for _, want := range []string{"6.7.1", "8.4", "al_s", docroot, `action="` + HubPath + `/login"`} {
		if !strings.Contains(body, want) {
			t.Errorf("WordPress hub card missing %q", want)
		}
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://j.test"+HubPath, nil))
	body = rec.Body.String()
	if !strings.Contains(body, "Joomla") {
		t.Error("Joomla hub card does not name the kind")
	}
	if strings.Contains(body, `action="`+HubPath+`/login"`) {
		t.Error("Joomla hub card offers a wp-admin login")
	}
}
