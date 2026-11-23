package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countRootCalls points sudo at a script that records each call and succeeds
// without running anything, and returns the program each call asked for.
func countRootCalls(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$2\" >> '"+calls+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sudoPath
	sudoPath = fake
	t.Cleanup(func() { sudoPath = old })
	return func() []string {
		b, _ := os.ReadFile(calls)
		return strings.Fields(string(b))
	}
}

// Without the allowlist, every root call that sudo -n refuses opens its own
// password dialog. Each install that can run before the allowlist exists must
// therefore reach root once, as a single script.
func TestRootInstallsAskOnce(t *testing.T) {
	testEngine(t)
	cases := map[string]func() error{
		"allowlist":    installSudoAllowlist,
		"front":        func() error { return installFrontDaemon(true) },
		"remove alias": func() error { return RemoveLoopAlias(true) },
	}
	for name, run := range cases {
		calls := countRootCalls(t)
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := calls(); len(got) != 1 || got[0] != "/bin/sh" {
			t.Errorf("%s reached root %d times (%v), want one /bin/sh script", name, len(got), got)
		}
	}
}

// The front must bind 127.0.0.2:80/443, which macOS allows only root, so its
// job runs as root. It must run the root-owned helper and log outside the
// user's home; a job naming anything else is the state doctor sends to alias.
func TestFrontJobRunsRootOwnedHelper(t *testing.T) {
	plist := frontPlist("/Users/someone/.agent-local/run")
	for _, want := range []string{"<string>" + frontHelper + "</string>", "<string>" + frontLogPath + "</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("front plist lacks %s", want)
		}
	}
	if strings.Contains(plist, "UserName") || strings.Contains(plist, "/Users/someone/.agent-local/logs") {
		t.Errorf("front plist runs as a user or logs into a home directory:\n%s", plist)
	}
	if !frontPlistUsesHelper([]byte(plist)) {
		t.Error("doctor would not accept the plist installFrontDaemon writes")
	}
	legacy := strings.Replace(plist, frontHelper, "/Users/someone/.local/bin/agent-local", 1)
	if frontPlistUsesHelper([]byte(legacy)) {
		t.Error("doctor accepts a root job running a user-writable binary")
	}
	user := strings.Replace(plist, "<dict>", "<dict><key>UserName</key><string>someone</string>", 1)
	if frontPlistUsesHelper([]byte(user)) {
		t.Error("doctor accepts a UserName job, which macOS refuses the ports")
	}
}

func TestRootOwnedRefusesUserFiles(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "agent-local")
	if err := os.WriteFile(mine, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rootOwned(mine) {
		t.Error("a file the test user owns counts as root-owned")
	}
	if !rootOwned("/bin/sh") {
		t.Error("/bin/sh does not count as root-owned")
	}
	if rootOwned("/tmp") {
		t.Error("a directory counts as a root-owned executable")
	}
}

// A machine upgraded from 0.36.1 has no hosts helper yet, only the old
// allowlist's passwordless tee /etc/hosts. Background callers (the daemon, an
// agent creating a site) must keep writing hosts entries through it.
func TestHostsWriteWithoutHelperUsesOldAllowlist(t *testing.T) {
	if fileExists(hostsHelper) {
		t.Skip("this machine has the hosts helper, which takes precedence")
	}
	calls := countRootCalls(t)
	if err := updateManagedHosts("add", []string{"demo.test"}, "127.0.0.1 demo.test\n", false); err != nil {
		t.Fatalf("non-interactive hosts write without the helper: %v", err)
	}
	if got := calls(); len(got) != 1 || got[0] != "/usr/bin/tee" {
		t.Errorf("hosts write reached root as %v, want one tee", got)
	}
}
