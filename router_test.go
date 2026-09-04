package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// What a share tunnel must never hand out: the repository, dotfiles with
// secrets, and every wp-config variant that is not the PHP file itself — a
// .bak holds the database password in plain text. Ordinary assets, and the
// ACME directory, stay served.
func TestSensitivePath(t *testing.T) {
	deny := []string{
		"/.git/HEAD", "/.git/config", "/.env", "/.htaccess", "/.user.ini",
		"/wp-content/.git/index", "/wp-config.php.bak", "/wp-config.php~",
		"/wp-config.php.agent-local.bak", "/wp-config.php.save",
		"/wp-content/debug.log", "/backup.sql", "/dump.sql.gz", "/old/site.sql.zip",
		"/wp-content/themes/x/style.css.orig", "/notes.swp", "/WP-CONFIG.PHP.BAK",
		"/a/../.git/config",
	}
	for _, p := range deny {
		if !sensitivePath(p) {
			t.Errorf("%s should be refused", p)
		}
	}
	allow := []string{
		"/", "/index.php", "/wp-content/uploads/2026/09/photo.jpg",
		"/wp-includes/js/jquery/jquery.min.js", "/.well-known/acme-challenge/token",
		"/wp-content/themes/x/style.css", "/robots.txt", "/sitemap.xml",
		"/wp-content/plugins/p/readme.txt", "/logo.svg",
	}
	for _, p := range allow {
		if sensitivePath(p) {
			t.Errorf("%s should be served", p)
		}
	}
}

// The router answers a refused path itself with 404 rather than falling
// through to PHP, and never opens the file.
func TestServeStaticRefusesSensitiveFiles(t *testing.T) {
	docroot := t.TempDir()
	os.MkdirAll(filepath.Join(docroot, ".git"), 0o755)
	os.WriteFile(filepath.Join(docroot, ".git", "config"), []byte("[core]"), 0o644)
	os.WriteFile(filepath.Join(docroot, "wp-config.php.bak"), []byte("DB_PASSWORD"), 0o644)
	os.WriteFile(filepath.Join(docroot, "logo.svg"), []byte("<svg/>"), 0o644)
	r := &Router{}
	for _, p := range []string{"/.git/config", "/wp-config.php.bak"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		if !r.serveStatic(rec, req, docroot) {
			t.Errorf("%s: expected the router to answer, not fall through", p)
		}
		if rec.Code != http.StatusNotFound || rec.Body.String() == "[core]" || rec.Body.String() == "DB_PASSWORD" {
			t.Errorf("%s: code %d body %q", p, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	if !r.serveStatic(rec, httptest.NewRequest(http.MethodGet, "/logo.svg", nil), docroot) || rec.Code != 200 {
		t.Errorf("ordinary asset: handled=%v code=%d", rec.Code == 200, rec.Code)
	}
}

// A log name is a bare file stem; anything that could leave the logs
// directory is refused before it becomes a path.
func TestLogNameGate(t *testing.T) {
	for _, ok := range []string{"apache", "daemon", "fpm-mysite", "wp-my-site", "mysql", "front.log"} {
		if !logName.MatchString(ok) {
			t.Errorf("%q should be a valid log name", ok)
		}
	}
	for _, bad := range []string{"", "../etc/passwd", "a/b", ".hidden", "-x", "a b"} {
		if logName.MatchString(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// The TLS handshake resolves the SNI host before ServeHTTP ever runs, so it
// has to see a rename another process just saved. It used to consult a stale
// copy of the store: the CLI renamed a site, and the daemon's handshake kept
// refusing the new name ("no site for host") until some other request
// happened to reload — the browser showed ERR_SSL_PROTOCOL_ERROR for the new
// domain for seconds after every rename.
func TestHandshakeSeesRenameFromAnotherProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	docroot := filepath.Join(home, "wp")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	// The daemon's store: loaded once, then only reloaded on demand.
	daemonStore, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	daemonStore.PutSite(&Site{Slug: "s", Domain: "old.test", WPDir: docroot, PHPVersion: "8.4",
		DBName: "al_s", DBUser: "al_s", DBPass: "x"})
	if err := daemonStore.Save(); err != nil {
		t.Fatal(err)
	}
	r := NewRouter(NewEngine(daemonStore))

	// The CLI's store: a separate process renaming the site and saving.
	cliStore, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	site := cliStore.Site("s")
	site.Domain = "new.test"
	cliStore.PutSite(site)
	if err := cliStore.Save(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(cliStore.path, future, future)

	// First thing the daemon does for the new name is the handshake. It must
	// resolve without any prior request having reloaded the store.
	if _, err := r.getCertificate(&tls.ClientHelloInfo{ServerName: "new.test"}); err != nil {
		t.Fatalf("handshake for the renamed domain failed: %v", err)
	}
	if _, err := r.getCertificate(&tls.ClientHelloInfo{ServerName: "old.test"}); err == nil {
		t.Error("the old name should no longer get a certificate")
	}
}
