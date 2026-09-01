package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedSnapshot(t *testing.T, slug, name string) string {
	t.Helper()
	dir := P().SnapshotsDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".sql.gz")
	if err := os.WriteFile(path, []byte("fake dump"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSnapshotIsAuto(t *testing.T) {
	cases := map[string]bool{
		"20260901-120000":                 false,
		"20260901-120000-pre-migration":   false,
		"20260901-120000-auto-import":     true,
		"20260901-120000-auto-delete":     true,
		"20260901-120000-automatic-ish":   false,
		"20260901-120000-auto-restore-2":  true,
		"20260901-120000-not-auto-import": false,
	}
	for name, want := range cases {
		if got := snapshotIsAuto(name); got != want {
			t.Errorf("snapshotIsAuto(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSnapshotTime(t *testing.T) {
	fallback := time.Date(2001, 1, 1, 0, 0, 0, 0, time.Local)
	got := snapshotTime("20260901-153045-auto-reset", fallback)
	want := time.Date(2026, 9, 1, 15, 30, 45, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
	// A hand-renamed file falls back to its mtime instead of a zero time.
	if got := snapshotTime("my-backup", fallback); !got.Equal(fallback) {
		t.Errorf("fallback = %v, want %v", got, fallback)
	}
}

func TestSnapshotsListing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := NewEngine(nil)

	// No directory yet is an empty list, not an error.
	snaps, err := e.Snapshots("fresh")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("empty listing = %v, %v", snaps, err)
	}

	seedSnapshot(t, "s", "20260901-100000-auto-import")
	seedSnapshot(t, "s", "20260901-120000-pre-migration")
	seedSnapshot(t, "s", "20260901-110000")
	// Residue that must not be listed as a snapshot.
	os.WriteFile(filepath.Join(P().SnapshotsDir("s"), "notes.txt"), []byte("x"), 0o644)

	snaps, err = e.Snapshots("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("listed %d snapshots, want 3", len(snaps))
	}
	// Newest first, by the name's own timestamp.
	order := []string{"20260901-120000-pre-migration", "20260901-110000", "20260901-100000-auto-import"}
	for i, want := range order {
		if snaps[i].Name != want {
			t.Errorf("snaps[%d] = %s, want %s", i, snaps[i].Name, want)
		}
	}
	if !snaps[2].Auto || snaps[0].Auto || snaps[1].Auto {
		t.Errorf("auto flags wrong: %+v", snaps)
	}
}

func TestPruneAutoSnapshots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := NewEngine(nil)

	for i := range 8 {
		seedSnapshot(t, "s", time.Date(2026, 9, 1, 10, i, 0, 0, time.Local).Format(snapshotTimeLayout)+"-auto-reset")
	}
	manualOld := seedSnapshot(t, "s", "20260801-090000-keep-me")
	manualNew := seedSnapshot(t, "s", "20260901-110000")

	e.pruneAutoSnapshots("s", 5)

	snaps, err := e.Snapshots("s")
	if err != nil {
		t.Fatal(err)
	}
	autos := 0
	for _, s := range snaps {
		if s.Auto {
			autos++
		}
	}
	if autos != 5 {
		t.Errorf("kept %d auto snapshots, want 5", autos)
	}
	// The oldest autos went; manual snapshots are never pruned.
	if !fileExists(manualOld) || !fileExists(manualNew) {
		t.Error("pruning removed a manual snapshot")
	}
	for _, s := range snaps {
		if s.Auto && s.CreatedAt.Before(time.Date(2026, 9, 1, 10, 3, 0, 0, time.Local)) {
			t.Errorf("kept an auto snapshot older than the newest five: %s", s.Name)
		}
	}
}

func TestRestoreSnapshotResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "s", Domain: "s.test", DBName: "al_s"})
	e := NewEngine(store)

	// Nothing to restore: the error says how to take one.
	if _, err := e.RestoreSnapshot("s", "", false); err == nil || !strings.Contains(err.Error(), "no snapshots") {
		t.Errorf("empty restore error = %v", err)
	}

	seedSnapshot(t, "s", "20260901-100000-good")
	// An unknown name names what does exist rather than guessing.
	_, err = e.RestoreSnapshot("s", "20260901-999999", false)
	if err == nil || !strings.Contains(err.Error(), "20260901-100000-good") {
		t.Errorf("unknown-name error should list snapshots, got %v", err)
	}

	// An unknown site is a fact about the request.
	if _, err := e.RestoreSnapshot("ghost", "", false); err == nil || !strings.Contains(err.Error(), "no such site") {
		t.Errorf("ghost site error = %v", err)
	}
}
