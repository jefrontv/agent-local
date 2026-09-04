package main

import (
	"os"
	"path/filepath"
	"testing"
)

// checkpointTestSite builds a minimal site with a wp-content tree under a
// fresh HOME, and returns the engine plus the site slug.
func checkpointTestSite(t *testing.T) (*Engine, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	docroot := filepath.Join(home, "wp")
	themeDir := filepath.Join(docroot, "wp-content", "themes", "x")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "style.css"), []byte("/* v1 */"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{
		Name:   "x",
		Slug:   "x",
		WPDir:  docroot,
		DBName: "x",
	}
	store.mu.Lock()
	store.Data.Sites["x"] = site
	store.mu.Unlock()
	return &Engine{Store: store}, "x"
}

func TestCheckpointCreatesFilesAndMeta(t *testing.T) {
	e, slug := checkpointTestSite(t)
	info, err := e.Checkpoint(slug, "before-upgrade", "wp-content")
	if err != nil {
		t.Fatal(err)
	}
	if info.Scope != "wp-content" {
		t.Errorf("scope = %q, want wp-content", info.Scope)
	}
	if info.SizeHint != "clone" && info.SizeHint != "copy" {
		t.Errorf("size_hint = %q, want clone or copy", info.SizeHint)
	}
	// DB is unreachable in this test environment, so SnapshotDB must fail
	// and be recorded as a warning, not abort the checkpoint.
	if info.DBSnapshot != "" {
		t.Errorf("db_snapshot = %q, want empty (no db in test)", info.DBSnapshot)
	}
	if info.Warning == "" {
		t.Errorf("warning should explain the missing db snapshot")
	}
	style := filepath.Join(info.FilesPath, "themes", "x", "style.css")
	if _, err := os.Stat(style); err != nil {
		t.Errorf("cloned style.css missing: %v", err)
	}
	meta := filepath.Join(info.Path, "meta.json")
	if _, err := os.Stat(meta); err != nil {
		t.Errorf("meta.json missing: %v", err)
	}
}

func repeat40Plus() string {
	s := ""
	for range 50 {
		s += "x"
	}
	return s
}

func TestSanitizeCheckpointLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Before Upgrade!", "before-upgrade"},
		{"  --spaced--  ", "spaced"},
		{"UPPER_CASE", "upper-case"},
		{"", ""},
		{repeat40Plus(), "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}, // capped at 40
	}
	for _, c := range cases {
		if got := sanitizeCheckpointLabel(c.in); got != c.want {
			t.Errorf("sanitizeCheckpointLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestListCheckpointsIgnoresIncompleteDir(t *testing.T) {
	e, slug := checkpointTestSite(t)
	if _, err := e.Checkpoint(slug, "good", "wp-content"); err != nil {
		t.Fatal(err)
	}
	// Drop an incomplete checkpoint dir (no meta.json) alongside it.
	incomplete := filepath.Join(P().CheckpointsDir(slug), "20200101-000000-incomplete")
	if err := os.MkdirAll(filepath.Join(incomplete, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := e.ListCheckpoints(slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1 (incomplete dir must be ignored)", len(list))
	}
	if list[0].Label != "good" {
		t.Errorf("label = %q, want good", list[0].Label)
	}
}

func TestRollbackFilesRestoresAndKeepsPreviousAside(t *testing.T) {
	e, slug := checkpointTestSite(t)
	site := e.Store.Site(slug)

	info, err := e.Checkpoint(slug, "clean", "wp-content")
	if err != nil {
		t.Fatal(err)
	}

	styleFile := filepath.Join(site.WPDir, "wp-content", "themes", "x", "style.css")
	newFile := filepath.Join(site.WPDir, "wp-content", "themes", "x", "new.php")
	if err := os.WriteFile(styleFile, []byte("/* v2 modified */"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("<?php // added after checkpoint"), 0o644); err != nil {
		t.Fatal(err)
	}

	previous, err := e.rollbackFiles(site, *info)
	if err != nil {
		t.Fatal(err)
	}
	if previous == "" {
		t.Fatal("previous files path is empty, want the pre-rollback dir")
	}

	got, err := os.ReadFile(styleFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "/* v1 */" {
		t.Errorf("style.css after rollback = %q, want restored v1 content", got)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("new.php should be gone from wp-content after rollback, stat err = %v", err)
	}
	preNew := filepath.Join(previous, "themes", "x", "new.php")
	if _, err := os.Stat(preNew); err != nil {
		t.Errorf("new.php should survive under pre-rollback dir: %v", err)
	}
}

func TestRollbackComposesFilesAndSkipsDBWhenNoSnapshot(t *testing.T) {
	e, slug := checkpointTestSite(t)
	site := e.Store.Site(slug)

	info, err := e.Checkpoint(slug, "", "wp-content")
	if err != nil {
		t.Fatal(err)
	}
	if info.DBSnapshot != "" {
		t.Fatalf("expected no db snapshot in test env, got %q", info.DBSnapshot)
	}

	styleFile := filepath.Join(site.WPDir, "wp-content", "themes", "x", "style.css")
	if err := os.WriteFile(styleFile, []byte("/* changed */"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := e.Rollback(slug, info.Name)
	if err != nil {
		t.Fatal(err)
	}
	if report.DBRestored {
		t.Errorf("db_restored = true, want false (no db_snapshot to restore)")
	}
	if !report.FilesRestored {
		t.Errorf("files_restored = false, want true")
	}
	got, err := os.ReadFile(styleFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "/* v1 */" {
		t.Errorf("style.css after Rollback = %q, want restored v1 content", got)
	}
}

func TestDeleteCheckpointRejectsTraversal(t *testing.T) {
	e, slug := checkpointTestSite(t)
	if _, err := e.Checkpoint(slug, "keep", "wp-content"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "a/b", "..", ""} {
		if err := e.DeleteCheckpoint(slug, bad); err == nil {
			t.Errorf("DeleteCheckpoint(%q) = nil error, want rejection", bad)
		}
	}
}

func TestDeleteCheckpointRemovesDir(t *testing.T) {
	e, slug := checkpointTestSite(t)
	info, err := e.Checkpoint(slug, "keep", "wp-content")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteCheckpoint(slug, info.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Errorf("checkpoint dir should be gone, stat err = %v", err)
	}
}
