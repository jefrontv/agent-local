package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Agents pass a PHP version in whatever notation is at hand. Every one of these
// used to miss the inventory and come back as "not installed".
func TestNormalizePHPVersion(t *testing.T) {
	for in, want := range map[string]string{
		"8.2":      "8.2",
		"  8.2 ":   "8.2",
		"php@8.2":  "8.2",
		"php8.2":   "8.2",
		"8.2.28":   "8.2",
		"PHP 7.4":  "7.4",
		"7.4.33_9": "7.4",
		"":         "",
	} {
		if got := NormalizePHPVersion(in); got != want {
			t.Errorf("NormalizePHPVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// A keg whose dependency brew removed dies with a dyld line. That line is the
// only thing that says what is wrong, so it has to survive into the error, pid
// prefix stripped.
func TestBrokenReasonKeepsTheDyldLine(t *testing.T) {
	out := []byte("dyld[99444]: Library not loaded: @loader_path/../../../../opt/libffi/lib/libffi.8.dylib\n" +
		"  Referenced from: <X> /opt/homebrew/Cellar/php@7.4/7.4.33_9/bin/php\n")
	got := brokenReason(out, os.ErrPermission)
	if want := "Library not loaded: @loader_path/../../../../opt/libffi/lib/libffi.8.dylib"; got != want {
		t.Errorf("brokenReason = %q, want %q", got, want)
	}
	if got := brokenReason(nil, os.ErrNotExist); got != os.ErrNotExist.Error() {
		t.Errorf("brokenReason with no output = %q", got)
	}
}

func TestKegVersionFromPath(t *testing.T) {
	if got := kegVersion("/opt/homebrew/opt/php@7.4"); got != "7.4" {
		t.Errorf("kegVersion(php@7.4) = %q", got)
	}
	if got := kegVersion("/opt/homebrew/opt/postgres"); got != "" {
		t.Errorf("kegVersion(postgres) = %q, want empty", got)
	}
}

// kegMissingDeps reads the keg's own receipt, because asking brew to load a
// third-party formula is exactly what brew may refuse to do.
func TestKegMissingDepsReadsTheReceipt(t *testing.T) {
	prefix := t.TempDir()
	keg := filepath.Join(prefix, "Cellar", "php@7.4", "7.4.33_9")
	if err := os.MkdirAll(filepath.Join(keg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// One dependency present, one gone.
	if err := os.MkdirAll(filepath.Join(prefix, "opt", "gmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := `{"runtime_dependencies":[{"full_name":"gmp"},{"full_name":"libffi"}],
	             "source":{"tap":"shivammathur/php"}}`
	if err := os.WriteFile(filepath.Join(keg, "INSTALL_RECEIPT.json"), []byte(receipt), 0o644); err != nil {
		t.Fatal(err)
	}
	optKeg := filepath.Join(prefix, "opt", "php@7.4")
	if err := os.Symlink(keg, optKeg); err != nil {
		t.Fatal(err)
	}

	missing, tap := kegMissingDeps(filepath.Join(optKeg, "bin", "php"))
	if len(missing) != 1 || missing[0] != "libffi" {
		t.Errorf("missing = %v, want [libffi]", missing)
	}
	if tap != "shivammathur/php" {
		t.Errorf("tap = %q", tap)
	}
}

func phpTestEngine(t *testing.T) (*Store, *Engine) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.Data.Sites["s"] = &Site{Slug: "s", Domain: "s.test", PHPVersion: "8.3", State: StateStopped}
	store.Data.Inv.PHPs = []Runtime{{Version: "8.3", Bin: "/x/php", FPM: "/x/php-fpm"}}
	// A fresh scan stamp, so a missed lookup trusts this inventory instead of
	// re-reading the machine the test is running on.
	store.Data.Inv.Refresh = time.Now()
	return store, NewEngine(store)
}

// "php 7.4 not installed" was the whole message: it named no version that was
// installed, said nothing about the broken keg on disk, and offered no next
// call. An agent that gets that has nowhere to go.
func TestSwitchPHPErrorSaysWhatToDoNext(t *testing.T) {
	store, e := phpTestEngine(t)

	err := e.SwitchPHP("s", "7.4")
	if err == nil {
		t.Fatal("switching to a missing version should fail")
	}
	for _, want := range []string{"7.4", "installed: 8.3", "install php 7.4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Same version, but the keg is on disk and broken: a different problem with a
	// different fix, and the error has to say so.
	store.Data.Inv.BrokenPHPs = []Runtime{{Version: "7.4", Bin: "/opt/homebrew/opt/php@7.4/bin/php",
		Broken: "Library not loaded: libffi.8.dylib"}}
	err = e.SwitchPHP("s", "7.4")
	if err == nil {
		t.Fatal("switching to a broken keg should fail")
	}
	for _, want := range []string{"will not run", "libffi", "/opt/homebrew/opt/php@7.4/bin/php", "install php 7.4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if store.Site("s").PHPVersion != "8.3" {
		t.Error("a failed switch changed the site's version")
	}
}

// The version notation reaches the site record normalized, and a switch to an
// installed version still works through the ensure path.
func TestSwitchPHPAcceptsLooseVersions(t *testing.T) {
	store, e := phpTestEngine(t)
	if err := e.SwitchPHP("s", "php@8.3"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := store.Site("s").PHPVersion; got != "8.3" {
		t.Errorf("php_version = %q, want 8.3", got)
	}
}

// install=false is the caller saying "do not run brew": it must fail with the
// actionable message rather than starting a job.
func TestSwitchPHPAPIRefusesWithoutInstall(t *testing.T) {
	store, _ := phpTestEngine(t)
	api := &APIServer{store: store, engine: NewEngine(store)}
	body, _ := json.Marshal(map[string]any{"version": "7.4", "install": false})
	req := httptest.NewRequest(http.MethodPost, "/sites/s/php", strings.NewReader(string(body)))
	req.SetPathValue("slug", "s")
	rec := httptest.NewRecorder()
	api.handleSwitchPHP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "install php 7.4") {
		t.Errorf("body does not say how to install: %s", rec.Body.String())
	}
}

// An already-installed version answers on the request itself: no job, no wait.
func TestSwitchPHPAPISwitchesInline(t *testing.T) {
	store, _ := phpTestEngine(t)
	api := &APIServer{store: store, engine: NewEngine(store)}
	body, _ := json.Marshal(map[string]any{"version": "8.3"})
	req := httptest.NewRequest(http.MethodPost, "/sites/s/php", strings.NewReader(string(body)))
	req.SetPathValue("slug", "s")
	rec := httptest.NewRecorder()
	api.handleSwitchPHP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Job-Id") != "" {
		t.Error("a no-op switch started a job")
	}
}

// Doctor has to report a broken keg as a broken keg, with a fix. It used to be
// dropped from the scan entirely, so the report said nothing at all.
func TestDoctorReportsBrokenKeg(t *testing.T) {
	inv := &Inventory{BrokenPHPs: []Runtime{{Version: "7.4",
		Bin: "/opt/homebrew/opt/php@7.4/bin/php", Broken: "Library not loaded: libffi.8.dylib"}}}
	found := brokenPHPFindings(inv)
	if len(found) != 1 {
		t.Fatalf("findings = %+v, want one", found)
	}
	f := found[0]
	if f.Check != "php:7.4" || f.Status != "fail" || !f.AutoFix {
		t.Errorf("finding = %+v", f)
	}
	for _, want := range []string{"will not run", "libffi", "/opt/homebrew/opt/php@7.4/bin/php"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail %q does not mention %q", f.Detail, want)
		}
	}
	if !strings.Contains(f.FixCmd, "install php 7.4") {
		t.Errorf("fix cmd = %q", f.FixCmd)
	}
}
