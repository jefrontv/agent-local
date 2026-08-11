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
