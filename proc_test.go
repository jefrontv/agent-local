package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The shared cmdline scan has to see this very test binary: its own pid must
// resolve under a substring of its own command line, and an empty query must
// match nothing rather than everything.
func TestProcCmdlinesSeesSelf(t *testing.T) {
	self := os.Args[0]
	cmds := procCmdlines(self)
	got, ok := cmds[os.Getpid()]
	if !ok || got == "" {
		t.Fatalf("own pid missing from scan of %q", self)
	}
	if len(procCmdlines("no-such-process-al-test-xyz")) != 0 {
		t.Error("impossible substring should match nothing")
	}
	if len(procCmdlines("")) != 0 {
		t.Error("empty query should match nothing")
	}
}

// Identity verification against the shared scan: true for our own command
// line, false for a marker it never contained, false for dead input.
func TestVerifyPidAgainstScan(t *testing.T) {
	if !verifyPid(os.Getpid(), os.Args[0]) {
		t.Error("own pid with own argv substring should verify")
	}
	if verifyPid(os.Getpid(), "no-such-marker-al-test-xyz") {
		t.Error("foreign marker should not verify")
	}
	if verifyPid(-1, "x") || verifyPid(os.Getpid(), "") {
		t.Error("bad input should not verify")
	}
}

// The batch check fails closed: a pool with no pid file and no socket is
// absent from the map, so readers see stopped rather than a guess.
func TestBatchFailsClosedWithoutPool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.PutSite(&Site{Slug: "ghost", Domain: "ghost.test", WPDir: filepath.Join(home, "wp"),
		PHPVersion: "8.4", DBName: "al_ghost", DBUser: "al_ghost", DBPass: "x"})
	e := NewEngine(store)
	if got := e.fpmAliveBatch([]string{"ghost"}); got["ghost"] {
		t.Error("pool with no pid file or socket should not read alive")
	}
	if got := e.fpmAliveBatch(nil); len(got) != 0 {
		t.Error("empty input should give an empty map")
	}
}
