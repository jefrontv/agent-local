package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the mux and the store together, including the direct result and job
// envelopes that Muster consumes. The site lives entirely under a temp home.
func TestLocalAPIContract(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	site := &Site{Slug: "sample", Name: "Sample", Domain: "sample.test", Kind: KindWordPress, WPDir: dir, WorkDir: dir, PHPVersion: "8.3", State: StateStopped, DBName: "external", DBUser: "editor", DBPass: "secret-db", AdminPass: "secret-wp", Attached: true}
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\ndefine('WP_DEBUG', false);\n/* That's all, stop editing! */"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &APIServer{store: e.Store, engine: e, jobs: NewJobHub()}
	routes := a.routes()
	call := func(method, path, body string, code int) apiResp {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		var reply apiResp
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != code || reply.OK != (code < 300) {
			t.Fatalf("%s %s: HTTP %d %s", method, path, w.Code, w.Body.String())
		}
		return reply
	}
	resolve := call("GET", "/resolve?path="+url.QueryEscape(dir), "", 200)
	obj := resolve.Data.(map[string]any)
	db := obj["db"].(map[string]any)
	if db["socket"] != "" || db["pass"] != "secret-db" || db["name"] != "external" {
		t.Fatal("resolve database contract")
	}
	list := call("GET", "/sites", "", 200)
	if _, ok := list.Data.([]any); !ok {
		t.Fatal("expected site array")
	}
	b, _ := json.Marshal(list.Data)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("list returned a secret")
	}
	for _, path := range []string{"/healthz", "/sites/sample", "/sites/sample/worktrees", "/sites/sample/db/snapshots", "/sites/sample/checkpoints", "/sites/sample/wp-debug", "/sites/sample/wp-config/constants", "/sites/sample/errors", "/sites/sample/requests", "/sites/sample/mail", "/sites/sample/share", "/sites/sample/media", "/suffix", "/front", "/sites-dir", "/jobs", "/certs/sample.test"} {
		call("GET", path, "", 200)
	}
	for _, path := range []string{"/sites", "/attach", "/import", "/sites/sample/db/import", "/sites/sample/db/query", "/sites/sample/db/search", "/sites/sample/db/search-replace", "/sites/sample/domain", "/sites/sample/move", "/sites/sample/worktrees", "/resolve"} {
		if path == "/resolve" {
			call("GET", path, "", 400)
			continue
		}
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("POST %s with {}: HTTP %d %.200s", path, w.Code, w.Body.String())
		}
	}
	dup := call("POST", "/sites", `{"name":"sample"}`, 409)
	if dup.Code != "site_exists" || !strings.Contains(dup.Error, "already exists") {
		t.Fatal(dup)
	}
	job := call("POST", "/sites?async=1", `{"name":"sample"}`, 202)
	payload := job.Data.(map[string]any)
	id, _ := payload["job_id"].(string)
	if id == "" {
		id, _ = payload["id"].(string)
	}
	if id == "" {
		t.Fatal(payload)
	}
	a.jobs.Get(id).Wait()
	detail := call("GET", "/jobs/"+id, "", 200).Data.(map[string]any)
	if detail["status"] != "error" || detail["error"] == "" {
		t.Fatal(detail)
	}
	call("POST", "/sites/sample/wp-debug", `{"on":true}`, 200)
	call("POST", "/sites/sample/wp-debug", `{"on":false}`, 200)
	call("DELETE", "/sites/sample/mail", "", 200)
	call("DELETE", "/sites/sample/requests", "", 200)
	for _, suffix := range []string{"", "/start", "/stop", "/restart", "/domain", "/move", "/wp-cli", "/db", "/db/import", "/db/export", "/db/snapshot", "/db/reset", "/wp-debug", "/media", "/login", "/requests", "/share", "/checkpoints", "/worktrees", "/request", "/probe"} {
		method := "POST"
		if suffix == "" || suffix == "/requests" {
			method = "GET"
		}
		call(method, "/sites/missing"+suffix, "{}", 404)
	}
	call("DELETE", "/sites/sample?db=keep&files=keep&snapshot=off", "", 200)
	if e.Store.Site("sample") != nil {
		t.Fatal("deleted row remains")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("checkout removed")
	}
}

func TestSleepActivityReadersKeepStableSnapshots(t *testing.T) {
	e := testEngine(t)
	original := &Site{Slug: "quiet", State: StateRunning}
	e.Store.PutSite(original)
	t0 := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			e.Store.recordSiteActivity(original.Slug, t0.Add(time.Duration(i)), i == 999)
		}
	}()
	for i := 0; i < 1000; i++ {
		snap := e.Store.Site("quiet")
		_ = snap.State
		_ = snap.LastActive
	}
	<-done
	if original.State != StateRunning || e.Store.Site("quiet").State != StateSleeping {
		t.Fatal("activity writes mutated a reader's snapshot")
	}
}
