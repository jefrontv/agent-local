package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImprovedDomainValidation(t *testing.T) {
	for _, domain := range []string{"a.b", "christopher_boots.local", "one-two.test", "sub.example.test"} {
		if !ValidDomain(domain) {
			t.Errorf("rejected %q", domain)
		}
	}
	for _, domain := range []string{"localhost", "a..b", "-one.test", "one-.test", "one.test.", "ABC.test", "a\nb.test", strings.Repeat("a", 64) + ".test", strings.Repeat("a.", 128) + "test"} {
		if ValidDomain(domain) {
			t.Errorf("accepted %q", domain)
		}
	}
}

func TestFrontHalfCloseKeepsResponse(t *testing.T) {
	listen := func() *net.TCPListener {
		l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}
	backend, front := listen(), listen()
	go func() {
		c, err := backend.AcceptTCP()
		if err != nil {
			return
		}
		defer c.Close()
		b, err := io.ReadAll(c)
		if err == nil {
			fmt.Fprintf(c, "received %s", b)
		}
	}()
	go func() {
		c, err := front.AcceptTCP()
		if err == nil {
			pipeConn(c, backend.Addr().(*net.TCPAddr).Port)
		}
	}()
	c, err := net.DialTCP("tcp4", nil, front.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(c, "hello")
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "received hello" {
		t.Fatalf("response %q, %v", b, err)
	}
}

func TestLogTailAfterOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fpm.log")
	line := "[" + time.Now().UTC().Format("02-Jan-2006 15:04:05 MST") + "] PHP Fatal error:  after large line in /x.php on line 1\n"
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3<<20)+"\n"+line), 0600); err != nil {
		t.Fatal(err)
	}
	entries, _ := collectErrors([]string{path}, []string{"fpm"}, time.Hour, 50)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	n, _ := recentFatals(path, 50, time.Hour)
	if n != 1 {
		t.Fatalf("fatal count %d", n)
	}
	if lines := logDelta(path, 0); len(lines) != 1 {
		t.Fatalf("delta %q", lines)
	}
}

func TestReleaseRejectsMissingSignature(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "main", "refs/heads/main"} {
		if err := verifyReleaseFiles("missing", "asset", "missing", "missing", tag); err == nil {
			t.Fatal(tag)
		}
	}
}

func TestRoutesAndHostValidation(t *testing.T) {
	e := testEngine(t)
	e.Store.PutSite(&Site{Slug: "shop", Domain: "shop.test", Kind: KindWordPress})
	e.Store.PutWorktree(&Worktree{ID: "preview", Site: "another"})
	a := &APIServer{store: e.Store, engine: e}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/missing", 404}, {"PUT", "/jobs", 405}, {"DELETE", "/sites/shop/worktrees/preview", 404}, {"POST", "/sites/shop/worktrees/preview/start", 404}, {"POST", "/sites/shop/worktrees/preview/stop", 404}, {"POST", "/sites/shop/worktrees/preview/wp-cli", 404}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, req)
		var resp apiResp
		if w.Code != tc.status || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.OK || resp.Error == "" {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "http://example.test:10809/healthz", nil)
	w := httptest.NewRecorder()
	a.auth(a.routes()).ServeHTTP(w, req)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
}

func TestImportedSchemaBoundaries(t *testing.T) {
	insert := "INSERT INTO t VALUES ('" + strings.Repeat("a", 150000) + "');\n"
	input := "CREATE DATABASE IF NOT EXISTS old;\nUSE old;\n" + insert + "SELECT 1;"
	filter := &dumpSchemaFilter{reader: bufio.NewReaderSize(strings.NewReader(input), 4096)}
	b, err := io.ReadAll(filter)
	if err != nil || string(b) != insert+"SELECT 1;" {
		t.Fatal("unexpected SQL filtering", err)
	}
	e := testEngine(t)
	e.Store.PutSite(&Site{Slug: "shop", DBName: "external", DBUser: "shop"})
	if err := e.ResetDB("shop"); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatal(err)
	}
	if _, err := e.SearchReplace(e.Store.Site("shop"), "--version", "new", false); err == nil {
		t.Fatal("accepted option")
	}
	if _, err := e.ImportSQL("shop", filepath.Join(t.TempDir(), "missing"), true, true); err == nil {
		t.Fatal("accepted missing dump")
	}
	if _, err := e.CreateSite(CreateOpts{Name: "shop"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	if err := e.SetDomain("shop", "bad..host"); err == nil {
		t.Fatal("invalid domain accepted")
	}
	if _, err := e.ImportSite(ImportOpts{Source: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestCertRenewsWrongSAN(t *testing.T) {
	caTestHome(t)
	crt, key, _, err := EnsureCert("first.test")
	if err != nil {
		t.Fatal(err)
	}
	wrongCert, wrongKey := CertPaths("second.test")
	for from, to := range map[string]string{crt: wrongCert, key: wrongKey} {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(to, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, created, err := EnsureCert("second.test"); err != nil || !created {
		t.Fatalf("renewed %t: %v", created, err)
	}
	if _, err := tls.LoadX509KeyPair(wrongCert, wrongKey); err != nil {
		t.Fatal(err)
	}
	if err := readLeaf(t, wrongCert).VerifyHostname("second.test"); err != nil {
		t.Fatal(err)
	}
}

func TestTLSFilterPreservesDiagnostics(t *testing.T) {
	var out bytes.Buffer
	w := tlsErrorWriter{&out}
	for _, addr := range []string{"127.0.0.1:123", "[::1]:456", "192.0.2.1:777"} {
		fmt.Fprintf(w, "http: TLS handshake error from %s: EOF\n", addr)
	}
	fmt.Fprintln(w, "http: TLS handshake error from 127.0.0.1:888: bad certificate")
	if strings.Contains(out.String(), ":123") || strings.Contains(out.String(), ":456") || !strings.Contains(out.String(), "192.0.2.1") || !strings.Contains(out.String(), "bad certificate") {
		t.Fatal(out.String())
	}
}

func TestInternalProbeHeader(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Local-Probe") != "1" {
			t.Error("missing probe marker")
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	code, _, _, err := httpProbeHostTimed(s.URL, "shop.local", time.Second)
	if err != nil || code != 204 {
		t.Fatalf("%d %v", code, err)
	}
}

type contractTransport func(*http.Request) (*http.Response, error)

func (f contractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestEveryMCPToolDispatches(t *testing.T) {
	e := testEngine(t)
	mux := (&APIServer{engine: e, store: e.Store}).registeredRoutes()
	orig := apiHTTP
	t.Cleanup(func() { apiHTTP = orig })
	for _, tool := range mcpTools() {
		t.Run(tool.Name, func(t *testing.T) {
			called := false
			apiHTTP = &http.Client{Transport: contractTransport(func(r *http.Request) (*http.Response, error) {
				called = true
				_, pattern := mux.Handler(r)
				if pattern == "" {
					t.Errorf("missing handler for %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"data":{}}`)), Header: http.Header{}}, nil
			})}
			args := map[string]any{"slug": "site", "domain": "site.local", "id": "site--preview", "name": "item", "args": []any{"version"}, "domains": []any{"site.local"}}
			local := tool.Name == "localwp_sites" || tool.Name == "ddev_projects"
			if result, bad := dispatchTool(tool.Name, args); bad && !local {
				t.Fatalf("dispatch: %v", result)
			}
			if called == local {
				t.Errorf("unexpected transport selection: called=%t", called)
			}
		})
	}
}

func TestUpdaterRespectsPackageManagerAndVersion(t *testing.T) {
	testEngine(t)
	cell := filepath.Join(os.Getenv("HOME"), "Caskroom", "agent-local", "v1", "agent-local")
	if err := os.MkdirAll(filepath.Dir(cell), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cell, []byte("brew owned"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(os.Getenv("HOME"), ".local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cell, filepath.Join(bin, "agent-local")); err != nil {
		t.Fatal(err)
	}
	if _, err := SelfUpdate(nil); err == nil || !strings.Contains(err.Error(), "Homebrew") {
		t.Fatalf("update a brew install: %v", err)
	}
	for _, tc := range []struct {
		current, tag string
		want         bool
	}{{"0.38.1", "v0.37.0", false}, {"0.38.1", "v0.38.1", false}, {"v0.38.0", "v0.38.1", true}, {"0.38.1-rc.1", "v0.38.1", true}, {"dev", "latest", false}} {
		if got := UpdateAvailable(tc.current, &ghRelease{TagName: tc.tag}); got != tc.want {
			t.Errorf("%s to %s: %t", tc.current, tc.tag, got)
		}
	}
}

func TestGreedyWorkDirMigrationPersists(t *testing.T) {
	e := testEngine(t)
	parent := t.TempDir()
	wp := filepath.Join(parent, "project")
	e.Store.PutSite(&Site{Slug: "project", WorkDir: parent, WPDir: wp})
	e.Store.PutSite(&Site{Slug: "normal", WorkDir: wp, WPDir: filepath.Join(wp, "public")})
	if err := e.Store.Save(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		if s.Site("project").WorkDir != wp || s.Site("normal").WorkDir != wp {
			t.Fatal("incorrect migrated work dirs")
		}
	}
}

func TestParkSerializesRequestEntry(t *testing.T) {
	var a activity
	unlock := poolActivityLocks.lock("park-test")
	done := make(chan struct{})
	go func() { a.enter("park-test"); close(done) }()
	select {
	case <-done:
		unlock()
		t.Fatal("request entered while teardown held lock")
	case <-time.After(10 * time.Millisecond):
	}
	a.forget("park-test")
	unlock()
	<-done
	if a.inflightFor("park-test") != 1 {
		t.Fatal("wake request lost")
	}
	a.leave("park-test")
}

func TestJobListKeepsDetailSecretsPrivate(t *testing.T) {
	h := NewJobHub()
	site := &Site{Slug: "private", DBPass: "db-secret", AdminPass: "wp-secret"}
	j := h.Start("create", func(func(string, string)) (any, error) { return site, nil })
	j.Wait()
	b, err := json.Marshal(h.List())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("secret")) {
		t.Fatalf("list leaked credentials")
	}
	got := j.Snapshot().Result.(*Site)
	if got.DBPass != site.DBPass || got.AdminPass != site.AdminPass || got.DBPass == "" {
		t.Fatal("detail was redacted")
	}
}
