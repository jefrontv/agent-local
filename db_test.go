package main

import (
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoTableDump = "CREATE TABLE `wp_options` (id int);\nCREATE TABLE `wp_posts` (id int);\nINSERT INTO wp_options VALUES (1);\n"

// dbFixture is the CLI fixture site owning al_demo, over the fake server.
func dbFixture(t *testing.T) (*Engine, *Site, string) {
	t.Helper()
	// System dirs only: wp-cli and php live in Homebrew, so nothing here can run them.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	e, site := cliFixture(t)
	site.DBName, site.DBUser = "al_demo", "al_demo"
	e.Store.PutSite(site)
	state := withFakeDB(t, e)
	return e, site, state
}

func writeDump(t *testing.T, name, body string, gz bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if gz {
		w := gzip.NewWriter(f)
		w.Write([]byte(body))
		w.Close()
		return path
	}
	f.WriteString(body)
	return path
}

func TestImportSQLSnapshotsAndRollsBack(t *testing.T) {
	e, _, state := dbFixture(t)
	dump := writeDump(t, "in.sql", twoTableDump, false)
	msg, err := e.ImportSQL("demo", dump, false, true)
	if err != nil || !strings.Contains(msg, "(2 tables)") || strings.Contains(msg, "saved") {
		t.Fatalf("first import: %q %v", msg, err)
	}
	// A second import has something to lose, so it snapshots first.
	msg, err = e.ImportSQL("demo", writeDump(t, "in.sql.gz", twoTableDump, true), true, true)
	if err != nil || !strings.HasPrefix(msg, "saved ") {
		t.Fatalf("second import: %q %v", msg, err)
	}
	// An empty dump fails the table count and the snapshot is put back.
	_, err = e.ImportSQL("demo", writeDump(t, "empty.sql", "-- nothing\n", false), false, true)
	if err == nil || !strings.Contains(err.Error(), "automatically rolled back") {
		t.Fatalf("empty import: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(state, "al_demo.sql"))
	if strings.Count(string(b), "CREATE TABLE") != 2 {
		t.Fatalf("rollback did not restore the tables:\n%s", b)
	}
	for _, bad := range []struct{ slug, path, want string }{
		{"demo", filepath.Join(t.TempDir(), "gone.sql"), "no such file"},
		{"nope", dump, "no such site"},
	} {
		if _, err := e.ImportSQL(bad.slug, bad.path, false, false); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s %s: %v", bad.slug, bad.path, err)
		}
	}
}

func TestExportSnapshotRestoreAndReset(t *testing.T) {
	e, site, state := dbFixture(t)
	if _, err := e.ImportSQL("demo", writeDump(t, "in.sql", twoTableDump, false), false, false); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.sql")
	if _, err := e.ExportSQL("demo", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "wp_posts") {
		t.Fatalf("export missing tables:\n%s", b)
	}
	snap, err := e.SnapshotDB("demo", "manual")
	if err != nil || !strings.HasSuffix(snap.Name, "-manual") || snap.Size == 0 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	snaps, err := e.Snapshots("demo")
	if err != nil || len(snaps) == 0 {
		t.Fatalf("snapshots: %v %v", snaps, err)
	}
	if err := e.ResetDB("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "al_demo.sql")); !os.IsNotExist(err) {
		t.Fatal("reset left the tables")
	}
	msg, err := e.RestoreSnapshot("demo", snap.Name, false)
	if err != nil || !strings.Contains(msg, "(2 tables)") {
		t.Fatalf("restore: %q %v", msg, err)
	}
	site.DBName = "external"
	e.Store.PutSite(site)
	if err := e.ResetDB("demo"); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("reset of a foreign database: %v", err)
	}
	if _, err := e.RestoreSnapshot("demo", "", true); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("restore into a foreign database: %v", err)
	}
}

func TestCLIDatabaseCommands(t *testing.T) {
	_, _, state := dbFixture(t)
	dump := writeDump(t, "in.sql", twoTableDump, false)
	out := filepath.Join(t.TempDir(), "out.sql")
	steps := []struct {
		args       []string
		wantErr    string
		wantOutput string
	}{
		{[]string{"demo"}, "", "al_demo"},
		{[]string{"demo", "import"}, "usage", ""},
		{[]string{"demo", "import", dump, "--keep-urls", "--no-snapshot"}, "", "2 tables"},
		{[]string{"demo", "export", out}, "", "out.sql"},
		{[]string{"demo", "snapshot", "before"}, "", "before"},
		{[]string{"demo", "snapshots"}, "", "before"},
		{[]string{"demo", "restore", "--no-snapshot"}, "", "restored"},
		{[]string{"demo", "reset", "--no-snapshot"}, "", ""},
		{[]string{"demo", "tables"}, "", ""},
		{[]string{"demo", "search"}, "usage", ""},
		{[]string{"demo", "search-replace", "a"}, "usage", ""},
		{[]string{"demo", "SELECT", "1"}, "", ""},
	}
	for _, s := range steps {
		got, err := captureStdout(t, func() error { return cmdDB(s.args) })
		switch {
		case s.wantErr == "" && err != nil:
			t.Fatalf("db %v: %v\n%s", s.args, err, got)
		case s.wantErr != "" && (err == nil || !strings.Contains(err.Error(), s.wantErr)):
			t.Fatalf("db %v: want %q, got %v", s.args, s.wantErr, err)
		case !strings.Contains(got, s.wantOutput):
			t.Fatalf("db %v: want %q in\n%s", s.args, s.wantOutput, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(state, "queries.log")); !strings.Contains(string(b), "SELECT 1") {
		t.Fatal("the CLI did not use the fake server")
	}
}

func TestAPIDatabaseRoutes(t *testing.T) {
	e, _, _ := dbFixture(t)
	routes := (&APIServer{store: e.Store, engine: e, jobs: NewJobHub()}).routes()
	dump := writeDump(t, "in.sql", twoTableDump, false)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/sites/demo/db/import", `{"path":"` + dump + `","keep_urls":true}`},
		{"POST", "/sites/demo/db/export", `{"path":"` + filepath.Join(t.TempDir(), "x.sql") + `"}`},
		{"POST", "/sites/demo/db/snapshot", `{"name":"api"}`},
		{"GET", "/sites/demo/db/snapshots", ""},
		{"POST", "/sites/demo/db/reset", `{}`},
	} {
		code, body := serve(routes, c.method, c.path, c.body)
		if code != 200 {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	code, body := serve(routes, "DELETE", "/sites/demo?files=keep", "")
	if code != 200 || e.Store.Site("demo") != nil {
		t.Fatalf("delete: %d %s", code, body)
	}
}

// serve runs one request through a mux and returns its status and body.
func serve(h http.Handler, method, path, body string) (int, string) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}
