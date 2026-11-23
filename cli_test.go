package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliFixture is a temp HOME holding one stopped WordPress site, with every
// daemon call answered in-process by an APIServer over the same store. Nothing
// here can reach the real daemon, launchd, /etc/hosts or a root prompt.
func cliFixture(t *testing.T) (*Engine, *Site) {
	t.Helper()
	e := testEngine(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\ndefine('WP_DEBUG', false);\n/* That's all, stop editing! */\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "demo", Name: "Demo", Domain: "demo.test", Kind: KindWordPress, WPDir: dir, WorkDir: dir, PHPVersion: "8.3", State: StateStopped, DBName: "al_demo", DBUser: "demo", DBPass: "pw", AdminUser: "admin", AdminPass: "pw"}
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		t.Fatal(err)
	}
	routes := (&APIServer{store: e.Store, engine: e, jobs: NewJobHub()}).routes()
	orig := apiHTTP
	apiHTTP = &http.Client{Transport: contractTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	t.Cleanup(func() { apiHTTP = orig })
	return e, site
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	w.Close()
	os.Stdout = orig
	return <-done, runErr
}

func TestCLIRejectsMissingArguments(t *testing.T) {
	cliFixture(t)
	cases := map[string]func() error{
		"create":     func() error { return cmdCreate(nil) },
		"attach":     func() error { return cmdAttach(nil) },
		"start":      func() error { return cmdStart(nil) },
		"stop":       func() error { return cmdStop(nil) },
		"restart":    func() error { return cmdRestart(nil) },
		"delete":     func() error { return cmdDelete(nil) },
		"open":       func() error { return cmdOpen(nil) },
		"db":         func() error { return cmdDB(nil) },
		"domain":     func() error { return cmdDomain(nil) },
		"move":       func() error { return cmdMove(nil) },
		"worktree":   func() error { return cmdWorktree(nil) },
		"worktrees":  func() error { return cmdWorktrees(nil) },
		"branches":   func() error { return cmdBranches(nil) },
		"wp":         func() error { return cmdWP(nil) },
		"import":     func() error { return cmdImport(nil) },
		"media":      func() error { return cmdMedia(nil) },
		"probe":      func() error { return cmdProbe(nil) },
		"errors":     func() error { return cmdErrors(nil) },
		"checkpoint": func() error { return cmdCheckpoint(nil) },
		"rollback":   func() error { return cmdRollback(nil) },
		"login":      func() error { return cmdLogin(nil) },
		"wpinfo":     func() error { return cmdWPInfo(nil) },
		"wpconst":    func() error { return cmdWPConst(nil) },
		"wpdebug":    func() error { return cmdWPDebug(nil) },
		"mail":       func() error { return cmdMail(nil) },
		"job":        func() error { return cmdJob(nil) },
		"logs":       func() error { return cmdLogs(nil) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := captureStdout(t, fn); err == nil {
				t.Fatal("expected a usage error")
			}
		})
	}
}

func TestCLIReportsUnknownSite(t *testing.T) {
	cliFixture(t)
	cases := map[string]func() error{
		"start":      func() error { return cmdStart([]string{"nope"}) },
		"stop":       func() error { return cmdStop([]string{"nope"}) },
		"restart":    func() error { return cmdRestart([]string{"nope"}) },
		"open":       func() error { return cmdOpen([]string{"nope"}) },
		"db":         func() error { return cmdDB([]string{"nope"}) },
		"media":      func() error { return cmdMedia([]string{"nope"}) },
		"probe":      func() error { return cmdProbe([]string{"nope"}) },
		"errors":     func() error { return cmdErrors([]string{"nope"}) },
		"checkpoint": func() error { return cmdCheckpoint([]string{"nope"}) },
		"wpinfo":     func() error { return cmdWPInfo([]string{"nope"}) },
		"wpconst":    func() error { return cmdWPConst([]string{"nope"}) },
		"wpdebug":    func() error { return cmdWPDebug([]string{"nope"}) },
		"mail":       func() error { return cmdMail([]string{"nope"}) },
		"worktrees":  func() error { return cmdWorktrees([]string{"nope"}) },
		"branches":   func() error { return cmdBranches([]string{"nope"}) },
		"domain":     func() error { return cmdDomain([]string{"nope", "other.test"}) },
		"move":       func() error { return cmdMove([]string{"nope", t.TempDir()}) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := captureStdout(t, fn)
			if err == nil {
				t.Fatal("expected an error for a missing site")
			}
		})
	}
}

func TestCLIListShowsSites(t *testing.T) {
	cliFixture(t)
	out, err := captureStdout(t, cmdList)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "demo") || !strings.Contains(out, "demo.test") {
		t.Fatalf("list output missing the site:\n%s", out)
	}
}

func TestCLIListEmpty(t *testing.T) {
	testEngine(t)
	out, err := captureStdout(t, cmdList)
	if err != nil || !strings.Contains(out, "no sites yet") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestCLISettingsRoundTrip(t *testing.T) {
	e, _ := cliFixture(t)
	htaccess := "RewriteCond %{REQUEST_URI} ^/wp-content/uploads/\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ https://prod.example/$1 [L]\n"
	if err := os.WriteFile(filepath.Join(e.Store.Site("demo").WPDir, ".htaccess"), []byte(htaccess), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	steps := []struct {
		name string
		fn   func() error
		want string
	}{
		{"sites-dir read", func() error { return cmdSitesDir(nil) }, "sites"},
		{"sites-dir set", func() error { return cmdSitesDir([]string{dir}) }, dir},
		{"sites-dir default", func() error { return cmdSitesDir([]string{"--default"}) }, "sites"},
		{"suffix read", func() error { return cmdSuffix(nil) }, "suffix"},
		{"suffix set", func() error { return cmdSuffix([]string{".localhost"}) }, ".localhost"},
		{"idle read", func() error { return cmdIdle(nil) }, "on"},
		{"idle hours", func() error { return cmdIdle([]string{"6"}) }, "6h"},
		{"idle off", func() error { return cmdIdle([]string{"off"}) }, "off"},
		{"idle on", func() error { return cmdIdle([]string{"on"}) }, "default"},
		{"autoupdate read", func() error { return cmdAutoupdate(nil) }, "off"},
		{"autoupdate on", func() error { return cmdAutoupdate([]string{"on"}) }, "on"},
		{"autoupdate off", func() error { return cmdAutoupdate([]string{"off"}) }, "off"},
		{"media read", func() error { return cmdMedia([]string{"demo"}) }, "fallback"},
		{"media set", func() error { return cmdMedia([]string{"demo", "https://example.com"}) }, "example.com"},
		{"media pinned", func() error { return cmdMedia([]string{"demo"}) }, "set here"},
		{"media off", func() error { return cmdMedia([]string{"demo", "--off"}) }, "off"},
		{"media auto", func() error { return cmdMedia([]string{"demo", "--auto"}) }, "prod.example"},
	}
	for _, s := range steps {
		out, err := captureStdout(t, s.fn)
		if err != nil {
			t.Fatalf("%s: %v\n%s", s.name, err, out)
		}
		if !strings.Contains(out, s.want) {
			t.Fatalf("%s: want %q in:\n%s", s.name, s.want, out)
		}
	}
	fresh, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Suffix(); got != ".localhost" {
		t.Fatalf("suffix not saved: %q", got)
	}
	for _, bad := range [][]string{{"soon"}, {"-1"}} {
		if _, err := captureStdout(t, func() error { return cmdIdle(bad) }); err == nil {
			t.Fatalf("idle %v accepted", bad)
		}
	}
	if _, err := captureStdout(t, func() error { return cmdAutoupdate([]string{"maybe"}) }); err == nil {
		t.Fatal("autoupdate accepted a bad value")
	}
}
