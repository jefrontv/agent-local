package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Routes that stay inside the temp HOME and the fake database, each with the
// status the contract promises. Search and search-replace are left out: they
// run the machine's wp-cli.
func TestAPIRoutesAnswerWithContractStatuses(t *testing.T) {
	e, site, _ := dbFixture(t)
	routes := (&APIServer{store: e.Store, engine: e, jobs: NewJobHub()}).routes()
	if err := os.MkdirAll(filepath.Join(site.WPDir, "wp-content", "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(P().Log("daemon")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(P().Log("daemon"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dump := writeDump(t, "in.sql", twoTableDump, false)
	cases := []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"POST", "/sites/demo/db/restore", `{"name":"nope"}`, 404, "no snapshots"},
		{"POST", "/sites/demo/db/import", fmt.Sprintf(`{"path":%q,"keep_urls":true,"no_snapshot":true}`, dump), 200, "2 tables"},
		{"POST", "/sites/demo/db/query", `{"sql":"SELECT 1"}`, 200, ""},
		{"POST", "/sites/demo/db/query", `{"sql":""}`, 400, "sql required"},
		{"POST", "/db/query", `{"sql":"SELECT 1"}`, 200, ""},
		{"GET", "/sites/demo/db/tables", "", 200, ""},
		{"POST", "/sites/demo/db/search", `{}`, 400, "at least 3"},
		{"POST", "/sites/demo/db/search-replace", `{"old":"","new":"b"}`, 400, "at least 3"},
		{"POST", "/sites/demo/db/snapshot", `{"name":"keep"}`, 200, "keep"},
		{"POST", "/sites/demo/db/restore", `{"name":"nope"}`, 404, "not_found"},
		{"POST", "/sites/demo/checkpoints", `{"label":"cp"}`, 200, "cp"},
		{"GET", "/sites/demo/checkpoints", "", 200, "cp"},
		{"POST", "/sites/demo/checkpoints/nope/rollback", "", 404, "not_found"},
		{"DELETE", "/sites/demo/checkpoints/nope", "", 404, "not_found"},
		{"POST", "/sites/demo/wp-config/constant", `{"name":"AL_X","value":"1"}`, 200, "AL_X"},
		{"POST", "/sites/demo/wp-config/constant", `{"name":"AL_X","remove":true}`, 200, "removed"},
		{"POST", "/sites/demo/wp-config/constant", `{"name":"bad name","value":"1"}`, 400, "not a valid PHP constant"},
		{"GET", "/sites/demo/wp-config/constants", "", 200, "WP_DEBUG"},
		{"GET", "/logs/daemon?lines=1", "", 200, "two"},
		{"GET", "/logs/nope", "", 404, "no log"},
		{"GET", "/logs/..%2Fetc", "", 400, "bad log name"},
		{"GET", "/status", "", 200, "runtimes"},
		{"GET", "/runtimes", "", 200, "mariadb"},
		{"GET", "/sites/demo/errors?since=2h", "", 200, "2h0m0s"},
		{"GET", "/sites/demo/errors?since=soon", "", 400, "bad since"},
		{"GET", "/sites/demo/mail/nope", "", 404, "no message"},
		{"GET", "/resolve?path=" + filepath.Join(site.WPDir, "wp-content", "themes"), "", 200, "demo"},
		{"GET", "/sites/demo/requests", "", 200, "recording"},
		{"POST", "/sites/demo/php", `{}`, 400, "version required"},
		{"POST", "/sites/demo/domain", `{"domain":"-bad"}`, 400, "invalid domain"},
		{"GET", "/sites/demo/adminer", "", 200, "/.agent-local/adminer"},
		{"GET", "/sites/demo/branches", "", 400, "not a git repo"},
	}
	for _, c := range cases {
		code, body := serve(routes, c.method, c.path, c.body)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s %s: %d %.200s (want %d %q)", c.method, c.path, code, body, c.code, c.want)
		}
	}
}

// The auth wrapper is the only thing between a browser tab and the API: it
// pins the Host header and demands the bearer token on everything but the
// health check and the two UI pages the front proxies.
func TestAPIAuthPinsHostAndToken(t *testing.T) {
	e := testEngine(t)
	a := &APIServer{store: e.Store, engine: e, jobs: NewJobHub()}
	h := a.auth(a.routes())
	tok, err := APIToken()
	if err != nil {
		t.Fatal(err)
	}
	host := fmt.Sprintf("127.0.0.1:%d", DefaultAPIPort)
	for _, c := range []struct {
		host, path, token string
		code              int
	}{
		{"evil.test", "/sites", tok, 421},
		{host, "/sites", "", 401},
		{host, "/sites", "wrong", 401},
		{host, "/sites", tok, 200},
		{host, "/healthz", "", 200},
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+c.path, nil)
		r.Host = c.host
		if c.token != "" {
			r.Header.Set("Authorization", "Bearer "+c.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%s%s token=%q: %d want %d", c.host, c.path, c.token, w.Code, c.code)
		}
	}
}
