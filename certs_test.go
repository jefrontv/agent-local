package main

import (
	"errors"
	"strings"
	"testing"
)

// The setup paths are served whether or not a cert ends up trusted, so a lost
// trust error shows up as a browser warning the user reads as a broken site.
// Three shapes matter: a silent success, a plain failure that has to gain the
// remedy, and TrustCert's own non-interactive message, which already names it
// and must not have it bolted on twice.
func TestTrustCertOrReport(t *testing.T) {
	plain := errors.New("the password prompt was cancelled or the keychain refused; x.test.crt is still untrusted")
	named := errors.New("needs root to trust x.test.crt (run: agent-local sudo, or: agent-local cert x.test --trust)")

	cases := []struct {
		name  string
		trust error
		calls int
		wants []string
	}{
		{name: "trusted", trust: nil, calls: 0},
		{name: "plain failure", trust: plain, calls: 1, wants: []string{"cert not trusted: ", "agent-local sudo"}},
		{name: "already names the remedy", trust: named, calls: 1, wants: []string{"agent-local sudo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stages, details []string
			trustCertOrReport("x.test.crt", false,
				func(string, bool) error { return tc.trust },
				func(stage, detail string) {
					stages = append(stages, stage)
					details = append(details, detail)
				})

			if len(details) != tc.calls {
				t.Fatalf("reported %d times, want %d: %v", len(details), tc.calls, details)
			}
			for _, detail := range details {
				for _, want := range tc.wants {
					if !strings.Contains(detail, want) {
						t.Errorf("report %q does not mention %q", detail, want)
					}
				}
			}
			if tc.calls == 1 && stages[0] != "warn" {
				t.Errorf("stage = %q, want warn", stages[0])
			}
			if tc.calls == 1 && strings.Count(details[0], "agent-local sudo") != 1 {
				t.Errorf("remedy duplicated in %q", details[0])
			}
		})
	}
}
