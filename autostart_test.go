package main

import (
	"os"
	"strings"
	"testing"
)

// The plist is the whole autostart guarantee, and a misordered format argument
// produced a job whose program was an environment variable name — launchd reported
// EX_CONFIG and nothing ran. Assert the shape, not just that it renders.
func TestDaemonPlist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plist := daemonPlist("/usr/local/bin/agent-local")

	if !strings.Contains(plist, "<string>/usr/local/bin/agent-local</string><string>daemon</string>") {
		t.Errorf("ProgramArguments must run the binary with 'daemon':\n%s", plist)
	}
	if !strings.Contains(plist, "<key>Label</key><string>"+daemonAgentLabel+"</string>") {
		t.Error("label missing or misplaced")
	}
	if !strings.Contains(plist, "<key>"+launchdMarker+"</key><string>1</string>") {
		t.Error("the launchd marker must be an environment variable, not the program")
	}
	// The marker must never appear where the program goes.
	prog := plist[strings.Index(plist, "<key>ProgramArguments</key>"):strings.Index(plist, "</array>")]
	if strings.Contains(prog, launchdMarker) {
		t.Errorf("the marker leaked into ProgramArguments:\n%s", prog)
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key><true/>") {
		t.Error("RunAtLoad missing: nothing would start at login")
	}
	// Restart only on failure: a clean exit is how a duplicate steps aside.
	if !strings.Contains(plist, "<key>SuccessfulExit</key><false/>") {
		t.Error("KeepAlive must be failure-only")
	}
	if strings.Contains(plist, "--background") {
		t.Error("launchd must supervise a foreground process, not a self-daemonising one")
	}
}

// A process launchd started must never reload its own agent: booting out the label
// kills the very process doing it, which is how the daemon died seconds after
// taking over.
func TestAutostartLeavesItsOwnJobAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(launchdMarker, "1")
	if err := EnsureDaemonAutostart(); err != nil {
		t.Fatalf("should be a no-op, got %v", err)
	}
	if _, err := os.Stat(daemonAgentPath()); err == nil {
		t.Error("the job wrote its own plist while running as that job")
	}
}

// Autostart prefers the installed binary, so a working tree that moves cannot
// break login.
func TestInstalledBinaryPreferred(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := home + "/.local/bin"
	os.MkdirAll(binDir, 0o755)
	installed := binDir + "/" + AppName
	os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755)

	got, err := installedBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != installed {
		t.Errorf("binary = %q, want the installed copy %q", got, installed)
	}
}
