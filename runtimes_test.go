package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Discovery executes every PHP it finds (~90ms each), which made every command
// pay ~700ms to learn what the store already knew. The cache is only sound if it
// notices when the recorded toolchain stops being true, so that judgement is
// pinned here rather than left to a TTL alone.
func TestInventoryFresh(t *testing.T) {
	dir := t.TempDir()
	php := filepath.Join(dir, "php")
	fpm := filepath.Join(dir, "php-fpm")
	brew := filepath.Join(dir, "brew")
	mysqld := filepath.Join(dir, "mysqld")
	for _, p := range []string{php, fpm, brew, mysqld} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	good := func() *Inventory {
		inv := &Inventory{
			PHPs:    []Runtime{{Version: "8.3", Bin: php, FPM: fpm}},
			Brew:    brew,
			MySQL:   MySQLRuntime{Kind: "mariadb", Bin: mysqld},
			Refresh: time.Now(),
		}
		return inv
	}

	if !inventoryFresh(good()) {
		t.Error("a scan from just now with every binary present should be reused")
	}

	stale := good()
	stale.Refresh = time.Now().Add(-inventoryTTL - time.Minute)
	if inventoryFresh(stale) {
		t.Error("a scan older than the TTL must be redone")
	}

	never := good()
	never.Refresh = time.Time{}
	if inventoryFresh(never) {
		t.Error("an inventory that was never stamped must be redone")
	}

	empty := good()
	empty.PHPs = nil
	if inventoryFresh(empty) {
		t.Error("no PHP recorded means nothing was discovered yet")
	}

	// The case a TTL alone would miss: brew upgraded or uninstalled a keg, so the
	// recorded path is gone. Serving a site with a stale php path fails much later,
	// somewhere far less obvious.
	movedPHP := good()
	movedPHP.PHPs[0].Bin = filepath.Join(dir, "gone", "php")
	if inventoryFresh(movedPHP) {
		t.Error("a missing php binary must force a rescan")
	}

	movedFPM := good()
	movedFPM.PHPs[0].FPM = filepath.Join(dir, "gone", "php-fpm")
	if inventoryFresh(movedFPM) {
		t.Error("a missing php-fpm must force a rescan")
	}

	movedBrew := good()
	movedBrew.Brew = filepath.Join(dir, "gone", "brew")
	if inventoryFresh(movedBrew) {
		t.Error("a missing brew must force a rescan")
	}

	movedDB := good()
	movedDB.MySQL.Bin = filepath.Join(dir, "gone", "mysqld")
	if inventoryFresh(movedDB) {
		t.Error("a missing database engine must force a rescan")
	}

	// A runtime with no recorded paths cannot be validated by stat, and must not
	// be treated as proof of freshness either way — it simply is not disqualifying.
	pathless := good()
	pathless.PHPs[0].Bin, pathless.PHPs[0].FPM = "", ""
	if !inventoryFresh(pathless) {
		t.Error("an entry with no paths should not disqualify an otherwise fresh scan")
	}
}

// EnsureInventory must persist what it scans, or the next process pays again —
// which was the whole cost being removed.
func TestEnsureInventoryPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	EnsureInventory(store)
	if store.Inventory().Refresh.IsZero() {
		t.Fatal("EnsureInventory did not stamp the scan")
	}

	// A second store, as the next command would open: it must not need to rescan.
	next, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if next.Inventory().Refresh.IsZero() {
		t.Error("the scan was not written to disk; every command would rescan")
	}
}

// The launchd LaunchAgent hands the daemon PATH=/usr/bin:/bin:/usr/sbin:/sbin,
// so every exec.LookPath in DiscoverInventory misses and the scan comes back
// with no toolchain at all. Persisting that answered "php 8.5 not installed"
// for all 49 sites on this machine until someone happened to rerun a CLI
// command from a shell with a real PATH. A scan that contradicts the disk is
// not an answer.
func TestEnsureInventoryKeepsScanWhenPATHGoesBlind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	bin := t.TempDir()
	php := filepath.Join(bin, "php")
	fpm := filepath.Join(bin, "php-fpm")
	for _, p := range []string{php, fpm} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	*store.Inventory() = Inventory{
		PHPs: []Runtime{{Version: "8.5", Bin: php, FPM: fpm}},
		Brew: "/opt/homebrew/bin/brew",
		// Old enough that the TTL forces the rescan this test is about.
		Refresh: time.Now().Add(-inventoryTTL - time.Minute),
	}

	t.Setenv("PATH", "")
	EnsureInventory(store)

	if got := store.Inventory().Runtimes(); len(got) != 1 || got[0] != "8.5" {
		t.Fatalf("a blind scan wiped the inventory: %v", got)
	}
	if store.Inventory().Brew == "" {
		t.Error("brew path dropped by a scan that could not see it")
	}

	// And it must not reach disk: the next process has to be free to rescan
	// once its PATH is sane again.
	next, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Inventory().PHPs) != 0 {
		t.Error("the rejected scan was persisted anyway")
	}
}

// A genuine uninstall still has to be recorded, or the store keeps pointing at
// binaries that are gone.
func TestScanContradictsDiskOnlyWhenBinariesSurvive(t *testing.T) {
	dir := t.TempDir()
	php := filepath.Join(dir, "php")
	if err := os.WriteFile(php, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	present := &Inventory{PHPs: []Runtime{{Version: "8.5", Bin: php}}}
	gone := &Inventory{PHPs: []Runtime{{Version: "8.5", Bin: filepath.Join(dir, "gone", "php")}}}
	blind := &Inventory{}

	if !scanContradictsDisk(present, blind) {
		t.Error("an empty scan while the recorded php is still on disk is an environment fault")
	}
	if scanContradictsDisk(gone, blind) {
		t.Error("an empty scan after the php really was removed is the truth")
	}
	if scanContradictsDisk(present, present) {
		t.Error("a scan that found something is never a contradiction")
	}
	if scanContradictsDisk(blind, blind) {
		t.Error("with nothing recorded there is nothing to protect")
	}
}
