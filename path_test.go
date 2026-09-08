package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon runs under launchd with PATH=/usr/bin:/bin:/usr/sbin:/sbin. Every
// runtime lookup is exec.LookPath, so without this the daemon finds no brew, no
// httpd and no php while all three sit in /opt/homebrew/bin.
func TestNormalizePATHAddsMissingToolchainDirs(t *testing.T) {
	present := ""
	for _, d := range toolchainDirs {
		if dirExists(d) {
			present = d
			break
		}
	}
	if present == "" {
		t.Skip("no standard toolchain dir on this machine")
	}

	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	normalizePATH()

	got := strings.Split(os.Getenv("PATH"), ":")
	if !contains(got, present) {
		t.Fatalf("PATH still missing %s: %v", present, got)
	}
	// Appended, never prepended: whatever the user chose keeps winning.
	if got[0] != "/usr/bin" {
		t.Errorf("existing PATH was reordered: %v", got)
	}
}

// Called from main() on every invocation, including ones already run from a
// full shell — it must not grow PATH each time or duplicate what is there.
func TestNormalizePATHIsIdempotent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	normalizePATH()
	once := os.Getenv("PATH")
	normalizePATH()
	if os.Getenv("PATH") != once {
		t.Errorf("second call changed PATH:\n %q\n %q", once, os.Getenv("PATH"))
	}
}

// A dir already on PATH with a trailing slash is the same dir.
func TestNormalizePATHIgnoresTrailingSlash(t *testing.T) {
	present := ""
	for _, d := range toolchainDirs {
		if dirExists(d) {
			present = d
			break
		}
	}
	if present == "" {
		t.Skip("no standard toolchain dir on this machine")
	}
	t.Setenv("PATH", present+"/")
	normalizePATH()
	if n := strings.Count(os.Getenv("PATH"), present); n != 1 {
		t.Errorf("%s added again despite already being on PATH: %q", present, os.Getenv("PATH"))
	}
}

// Nothing invented: a directory that does not exist is not worth a stat on
// every lookup miss.
func TestNormalizePATHSkipsAbsentDirs(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	normalizePATH()
	for _, d := range toolchainDirs {
		if !dirExists(d) && contains(strings.Split(os.Getenv("PATH"), ":"), d) {
			t.Errorf("added %s which does not exist", d)
		}
	}
}

func TestDirExistsRejectsFiles(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if dirExists(f) {
		t.Error("a regular file is not a PATH entry")
	}
}
