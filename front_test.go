package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whether to stand aside at startup. The cost of getting this wrong in one
// direction is a bare URL arriving twenty seconds late; in the other it is
// another tool's sites being unable to start at all, which is how this was found.
func TestRivalDecision(t *testing.T) {
	cases := []struct {
		name       string
		app, taken bool
		want       bool
	}{
		{"rival app open, port free — it may still be booting", true, false, true},
		{"rival app open and already serving — nothing to wait for", true, true, false},
		{"no rival app — bind immediately", false, false, false},
		{"port taken by something else entirely", false, true, false},
	}
	for _, c := range cases {
		if got := rivalDecision(c.app, c.taken); got != c.want {
			t.Errorf("%s: rivalDecision(%v, %v) = %v, want %v", c.name, c.app, c.taken, got, c.want)
		}
	}
}

// The grace must never be unbounded: a rival that never binds cannot be allowed
// to keep the ports, which is the same rule autoYieldMax enforces at runtime.
func TestStartupGraceIsBounded(t *testing.T) {
	if startupGrace <= 0 {
		t.Fatal("startupGrace must be positive")
	}
	if startupGrace > autoYieldMax {
		t.Errorf("startupGrace %s exceeds autoYieldMax %s: a boot wait should not outlast the runtime yield bound",
			startupGrace, autoYieldMax)
	}
}

// localAppRunning has to match a real installation, not a pattern that only looks
// plausible: the whole grace hinges on it. Skips when LocalWP is not running, so
// CI stays green on a machine without it.
func TestLocalAppRunningMatchesReality(t *testing.T) {
	out, err := runCmdOut("pgrep", "-fl", "Local.app")
	if err != nil || out == "" {
		t.Skip("LocalWP is not running on this machine")
	}
	if !localAppRunning() {
		t.Errorf("LocalWP is running (%s) but localAppRunning() said no", firstLine(out))
	}
}

func TestApacheStartArgsAreNotSingleProcess(t *testing.T) {
	args := apacheStartArgs("/opt/homebrew/bin/httpd", "/tmp/httpd.conf")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " -X") || strings.HasSuffix(joined, " -X") {
		t.Fatalf("apache still starts with -X: %v", args)
	}
	found := false
	for _, a := range args {
		if a == "-DFOREGROUND" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing -DFOREGROUND: %v", args)
	}
}

// Which httpd runs the apache front decided itself by PATH order, so a login
// shell picked Homebrew's and the launchd daemon picked Apple's SIP-protected
// /usr/sbin/httpd — a config built for one prefix handed to the other.
func TestDiscoverHTTPPrefersBrewOverSystem(t *testing.T) {
	prefix := t.TempDir()
	brewBin := filepath.Join(prefix, "bin", "brew")
	httpdBin := filepath.Join(prefix, "bin", "httpd")
	if err := os.MkdirAll(filepath.Dir(brewBin), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(brewBin, "#!/bin/sh\necho "+prefix+"\n")
	write(httpdBin, "#!/bin/sh\necho 'Server version: Apache/2.4.62 (Unix)'\n")

	// A different httpd first on PATH — the one that must lose.
	other := t.TempDir()
	write(filepath.Join(other, "httpd"), "#!/bin/sh\necho 'Server version: Apache/2.4.1 (Unix)'\n")
	t.Setenv("PATH", other+":"+filepath.Dir(brewBin))

	got := discoverHTTP(brewBin)
	if got.Bin != httpdBin {
		t.Errorf("discoverHTTP = %q, want the brew copy %q", got.Bin, httpdBin)
	}
	if got.Version != "2.4.62" {
		t.Errorf("version = %q, want the brew copy's 2.4.62", got.Version)
	}
}

// With no brew at all the PATH copy is still better than no apache front.
func TestDiscoverHTTPFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "httpd"),
		[]byte("#!/bin/sh\necho 'Server version: Apache/2.4.1 (Unix)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got := discoverHTTP("")
	if got.Kind != "apache" || got.Version != "2.4.1" {
		t.Errorf("discoverHTTP = %+v, want the PATH copy", got)
	}
}

// No httpd anywhere means the built-in router, not a half-built apache entry.
func TestDiscoverHTTPWithoutApache(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := discoverHTTP(""); got.Kind != "router" || got.Bin != "" {
		t.Errorf("discoverHTTP = %+v, want the router", got)
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
