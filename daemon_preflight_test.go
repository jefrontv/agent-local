package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The boot preflight has to tell a docroot this process may not read from one
// that is simply missing: a denial means the process launched without macOS
// folder access and must stand aside; a missing folder is the user's business.
func TestUnreadableDocrootsTellsDeniedFromMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(home, "denied")
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(denied, 0o755) })
	if os.Getuid() == 0 {
		t.Skip("root reads mode-000 directories; the denial cannot be simulated")
	}
	store.PutSite(&Site{Slug: "locked", Domain: "locked.test", WPDir: denied, PHPVersion: "8.4",
		DBName: "al_locked", DBUser: "al_locked", DBPass: "x"})
	store.PutSite(&Site{Slug: "gone", Domain: "gone.test", WPDir: filepath.Join(home, "nowhere"), PHPVersion: "8.4",
		DBName: "al_gone", DBUser: "al_gone", DBPass: "x"})
	store.PutSite(&Site{Slug: "fine", Domain: "fine.test", WPDir: home, PHPVersion: "8.4",
		DBName: "al_fine", DBUser: "al_fine", DBPass: "x"})

	got := unreadableDocroots(store)
	if len(got) != 1 || got[0] != "locked" {
		t.Fatalf("unreadableDocroots = %v, want [locked]: missing and readable docroots must not count", got)
	}
}
