package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSetDomainRenamesAndReportsWhatNeedsRoot(t *testing.T) {
	e, _, _ := dbFixture(t)
	var warns []string
	rep := func(_, m string) { warns = append(warns, m) }
	if err := e.SetDomain("demo", "renamed.test", rep); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.Site("demo").Domain; got != "renamed.test" {
		t.Fatalf("domain = %q", got)
	}
	if !e.Store.DomainFree("demo.test") {
		t.Fatal("the old name is still held")
	}
	if joined := strings.Join(warns, "\n"); !strings.Contains(joined, "hosts entry failed") || !strings.Contains(joined, "cert not trusted") {
		t.Fatalf("root-only steps not reported:\n%s", joined)
	}
	e.Store.PutSite(&Site{Slug: "other", Domain: "taken.test", WorkDir: t.TempDir()})
	for _, c := range []struct{ slug, domain, want string }{
		{"demo", "-x", "invalid domain"},
		{"nope", "a.test", "no such site"},
		{"demo", "taken.test", "already in use"},
	} {
		if err := e.SetDomain(c.slug, c.domain, rep); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s -> %s: %v", c.slug, c.domain, err)
		}
	}
}

func TestMoveSiteRelocatesAndRefusesBadTargets(t *testing.T) {
	e, site, _ := dbFixture(t)
	src := site.WorkDir
	for _, c := range []struct{ slug, dest, want string }{
		{"demo", src, "already at"},
		{"demo", filepath.Join(src, "sub"), "moved into itself"},
		{"nope", t.TempDir(), "no such site"},
	} {
		if err := e.MoveSite(c.slug, c.dest); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s -> %s: %v", c.slug, c.dest, err)
		}
	}
	dest := filepath.Join(t.TempDir(), "moved")
	if err := e.MoveSite("demo", dest); err != nil {
		t.Fatal(err)
	}
	moved := e.Store.Site("demo")
	if normalizePath(moved.WorkDir) != normalizePath(dest) || !fileExists(filepath.Join(moved.WPDir, "wp-config.php")) {
		t.Fatalf("site now at %s / %s", moved.WorkDir, moved.WPDir)
	}
	if fileExists(src) {
		t.Fatal("source directory left behind")
	}
}

func TestCreateImportAttachValidateBeforeTouchingAnything(t *testing.T) {
	e, _, state := dbFixture(t)
	steps := []struct {
		name string
		fn   func() error
		want string
	}{
		{"create unnamed", func() error { _, err := e.CreateSite(CreateOpts{}); return err }, "no usable characters"},
		{"create duplicate", func() error { _, err := e.CreateSite(CreateOpts{Name: "demo"}); return err }, "already exists"},
		{"create without php", func() error { _, err := e.CreateSite(CreateOpts{Name: "fresh"}); return err }, "no PHP installed"},
		{"create missing php", func() error { _, err := e.CreateSite(CreateOpts{Name: "fresh", PHPVersion: "5.2"}); return err }, "not installed"},
		{"import nothing", func() error { _, err := e.ImportSite(ImportOpts{}); return err }, "not found"},
		{"import missing dir", func() error {
			_, err := e.ImportSite(ImportOpts{Source: filepath.Join(t.TempDir(), "gone")})
			return err
		}, "not found"},
		{"import non-WordPress", func() error { _, err := e.ImportSite(ImportOpts{Source: t.TempDir()}); return err }, "missing wp-load.php"},
		{"attach duplicate", func() error { _, err := e.AttachSite(AttachOpts{Dir: t.TempDir(), Name: "demo"}); return err }, "already exists"},
		{"attach missing php", func() error {
			_, err := e.AttachSite(AttachOpts{Dir: t.TempDir(), Name: "x", PHPVer: "8.3"})
			return err
		}, "not installed"},
	}
	for _, s := range steps {
		if err := s.fn(); err == nil || !strings.Contains(err.Error(), s.want) {
			t.Errorf("%s: %v", s.name, err)
		}
	}
	if len(e.Store.Sites()) != 1 {
		t.Fatalf("a refused call registered a site: %d sites", len(e.Store.Sites()))
	}
	if fileExists(filepath.Join(state, "al_fresh.sql")) || fileExists(filepath.Join(state, "al_x.sql")) {
		t.Fatal("a refused call created a database")
	}
}
