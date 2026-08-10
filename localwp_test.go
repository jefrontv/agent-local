package main

import "testing"

// One pair of privileged ports, two local-dev tools. Our listener is scoped to a
// loopback alias, which can coexist with a wildcard listener — but only when the
// wildcard bound first, since BSD refuses a wildcard bind while a specific
// address holds the port. Starting first therefore stops the other router from
// starting at all, which is invisible unless something says so.
func TestLocalwpFinding(t *testing.T) {
	cases := []struct {
		name        string
		ours, rival bool
		wantStatus  string
		wantFixCmd  bool
	}{
		{"we block the other router", true, false, "warn", true},
		{"sharing the port", true, true, "ok", false},
		{"we stood aside", false, true, "ok", false},
		{"neither is serving", false, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := localwpFinding(3, c.ours, c.rival)
			if c.wantStatus == "" {
				if f != nil {
					t.Fatalf("expected no finding, got %+v", *f)
				}
				return
			}
			if f == nil {
				t.Fatal("expected a finding, got none")
			}
			if f.Status != c.wantStatus {
				t.Errorf("status = %q, want %q", f.Status, c.wantStatus)
			}
			if (f.FixCmd != "") != c.wantFixCmd {
				t.Errorf("FixCmd = %q, want set: %v", f.FixCmd, c.wantFixCmd)
			}
		})
	}
}
