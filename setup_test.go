package main

import (
	"errors"
	"strings"
	"testing"
)

// A rename or a branch preview used to discard this failure entirely, so the
// domain simply never resolved and nothing anywhere said why. The reporter is
// the whole point of the helper, so what it says is what gets pinned here.
func TestEnsureHostsOrReport(t *testing.T) {
	cases := []struct {
		name     string
		n        int
		err      error
		wantKind string // "" = nothing reported
		wantHas  []string
	}{
		{name: "success with lines added reports them", n: 2, wantKind: "dns",
			wantHas: []string{"added /etc/hosts entry"}},
		{name: "success with nothing to do stays quiet", n: 0},
		{name: "failure is reported, not dropped", n: 0, err: errors.New("write /etc/hosts: needs root"),
			wantKind: "warn", wantHas: []string{"hosts entry failed", "needs root"}},
		{name: "a failure naming the remedy does not repeat it", n: 0,
			err:      errors.New("needs root: tee /etc/hosts (run: agent-local sudo)"),
			wantKind: "warn",
			// Exactly one mention: appending a second would read as noise.
			wantHas: []string{"agent-local sudo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotKind, gotDetail string
			report := func(stage, detail string) { gotKind, gotDetail = stage, detail }
			write := func(bool, []string) (int, error) { return c.n, c.err }
			ensureHostsOrReport(false, []string{"s.test"}, write, report)

			if c.wantKind == "" {
				if gotKind != "" || gotDetail != "" {
					t.Fatalf("reported %q %q, want nothing", gotKind, gotDetail)
				}
				return
			}
			if gotKind != c.wantKind {
				t.Errorf("stage = %q, want %q", gotKind, c.wantKind)
			}
			for _, want := range c.wantHas {
				if !strings.Contains(gotDetail, want) {
					t.Errorf("detail %q does not mention %q", gotDetail, want)
				}
			}
			if strings.Count(gotDetail, "agent-local sudo") > 1 {
				t.Errorf("remedy repeated in %q", gotDetail)
			}
		})
	}
}

// The flag decides whether a root step may show the password dialog. It has to
// reach the writer, because that is what turns "needs root" into a prompt.
func TestEnsureHostsOrReportPassesInteractive(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		var got bool
		write := func(i bool, _ []string) (int, error) { got = i; return 0, nil }
		ensureHostsOrReport(interactive, []string{"s.test"}, write, func(string, string) {})
		if got != interactive {
			t.Errorf("write saw interactive=%v, want %v", got, interactive)
		}
	}
}

// What a client reads before it creates anything. Missing root is the state that
// produces an unreachable site; bare URLs are degraded-but-working, so they must
// not fail `ok` or a caller would refuse to create anything on a fine machine.
func TestSetupStateFrom(t *testing.T) {
	current := "NOPASSWD: /usr/bin/security add-trusted-cert -d -r trustRoot -p ssl -k " + trustStagePath

	missing := setupStateFrom("", true)
	if missing["root"] != "missing" || missing["ok"] != false {
		t.Errorf("no allowlist = %+v, want root=missing ok=false", missing)
	}
	if missing["fix"] == nil || missing["detail"] == nil {
		t.Error("a client cannot act on a failing setup state without detail and fix")
	}

	stale := setupStateFrom("NOPASSWD: /usr/bin/tee /etc/hosts", true)
	if stale["root"] != "stale" || stale["ok"] != false {
		t.Errorf("allowlist naming retired commands = %+v, want root=stale", stale)
	}

	ok := setupStateFrom(current, true)
	if ok["root"] != "ok" || ok["ok"] != true {
		t.Errorf("current allowlist = %+v, want ok", ok)
	}
	if ok["detail"] != nil {
		t.Errorf("a healthy machine should carry no detail, got %v", ok["detail"])
	}

	// Bare URLs are optional: the site still works on its port, so this is a
	// note, not a failure.
	degraded := setupStateFrom(current, false)
	if degraded["ok"] != true {
		t.Error("missing bare URLs must not fail setup: sites are served on their port")
	}
	if degraded["bare_urls"] != false || degraded["detail"] == nil {
		t.Errorf("degraded state should say so: %+v", degraded)
	}
}
