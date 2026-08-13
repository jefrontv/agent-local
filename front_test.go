package main

import (
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

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
