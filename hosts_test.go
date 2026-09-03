package main

import (
	"strings"
	"testing"
)

// A ".local" name is only slow while its AAAA question has to go to mDNS, so the
// hosts writer has to think per address family. Recognising "the domain is in the
// file" was enough when every name had one line; it would now leave an IPv4-only
// entry — and a five-second lookup — in place forever.
func TestHostLineHasIP(t *testing.T) {
	content := strings.Join([]string{
		"127.0.0.1 localhost",
		"127.0.0.2 mysite.local # agent-local managed",
		"fd00:a10c::2 other.local # agent-local managed",
		"::1 dual.local #Local Site",
		"127.0.0.1 dual.local #Local Site",
	}, "\n")

	cases := []struct {
		domain, ip string
		want       bool
	}{
		{"mysite.local", "127.0.0.2", true},
		{"mysite.local", "fd00:a10c::2", false}, // the case that must be detected as incomplete
		{"other.local", "fd00:a10c::2", true},
		{"other.local", "127.0.0.2", false},
		{"dual.local", "::1", true},
		{"dual.local", "127.0.0.1", true},
		{"absent.local", "127.0.0.2", false},
		// A domain must not match a line where it is the address, or a substring.
		{"localhost", "127.0.0.1", true},
		{"mysite", "127.0.0.2", false},
	}
	for _, c := range cases {
		if got := hostLineHasIP(content, c.domain, c.ip); got != c.want {
			t.Errorf("hostLineHasIP(%q, %q) = %v, want %v", c.domain, c.ip, got, c.want)
		}
	}
}

// The entries written for a domain must cover every family that is up: one line
// when there is no IPv6 alias, two when there is.
func TestHostsEntriesCoverActiveFamilies(t *testing.T) {
	got := HostsEntries([]string{"a.local", "b.test"})
	if len(got) == 0 {
		t.Fatal("no entries produced")
	}
	families := map[bool]int{}
	for _, line := range got {
		if !strings.Contains(line, HostsMarker) {
			t.Errorf("entry is unmarked, so cleanup would never find it: %q", line)
		}
		families[strings.Contains(strings.Fields(line)[0], ":")]++
	}
	// Two domains, so each family present must appear exactly twice.
	if families[false] != 2 {
		t.Errorf("expected one IPv4 line per domain, got %d", families[false])
	}
	if Alias6Active() && families[true] != 2 {
		t.Errorf("IPv6 alias is up but only %d AAAA lines were produced", families[true])
	}
	if !Alias6Active() && families[true] != 0 {
		t.Errorf("no IPv6 alias, yet %d AAAA lines were produced — they would resolve to nothing", families[true])
	}
}

// The addresses we claim to serve have to be the ones the front daemon binds, or a
// hosts entry points at silence. Which lines count as shadowed depends on whether
// the loopback alias is up — 127.0.0.1 leftovers only shadow when something else
// answers there — so the test pins the alias state instead of inheriting the
// machine's: on a bare CI runner the alias is down and the unpinned test fails.
func TestCommentShadowedHostsLeavesOursAndCommentsLocalWP(t *testing.T) {
	in := []string{
		"::1\tdev-ohm2023.local",
		"127.0.0.1\tdev-ohm2023.local",
		"127.0.0.2 sulo.pact # agent-local managed",
		"127.0.0.2 dev-ohm2023.local # agent-local managed",
		"fd00:a10c::2 dev-ohm2023.local # agent-local managed",
		"::1 dev-ohm2023.local #Local Site",
		"127.0.0.1 dev-ohm2023.local #Local Site",
		"127.0.0.1 other.test #Local Site",
	}
	oldCache := aliasCache
	defer func() { aliasCache = oldCache }()
	for _, tc := range []struct {
		name    string
		aliasUp bool
		wantN   int
		comment []int
		live    []int
	}{
		// Alias up: 127.0.0.1 leftovers shadow our 127.0.0.2 lines, so they go.
		{"alias up", true, 4, []int{0, 1, 5, 6}, []int{2, 3, 4, 7}},
		// Alias down: we serve from 127.0.0.1 itself, so those lines stay.
		{"alias down", false, 2, []int{0, 5}, []int{1, 2, 3, 4, 6, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.aliasUp {
				aliasCache = 1
			} else {
				aliasCache = 2
			}
			out, n := commentShadowedHosts(in, []string{"dev-ohm2023.local"})
			if n != tc.wantN {
				t.Fatalf("commented %d lines, want %d", n, tc.wantN)
			}
			for _, i := range tc.comment {
				if !strings.HasPrefix(strings.TrimSpace(out[i]), "#") {
					t.Errorf("line %d should be commented: %q", i, out[i])
				}
			}
			for _, i := range tc.live {
				if strings.HasPrefix(strings.TrimSpace(out[i]), "#") {
					t.Errorf("line %d should stay live: %q", i, out[i])
				}
			}
		})
	}
}

func TestHostsTargetPrefersAliasOverLocalWP(t *testing.T) {
	content := strings.Join([]string{
		"::1 dev-ohm2023.local",
		"127.0.0.1 dev-ohm2023.local #Local Site",
		"127.0.0.2 dev-ohm2023.local # agent-local managed",
	}, "\n")
	if got := hostsTargetIn(content, "dev-ohm2023.local"); got != LoopbackAlias {
		t.Errorf("hostsTarget = %q, want %s (not the leftover ::1)", got, LoopbackAlias)
	}
}

func TestHostsShadowedIPs(t *testing.T) {
	content := strings.Join([]string{
		"::1 dev-ohm2023.local",
		"127.0.0.1 dev-ohm2023.local #Local Site",
		"127.0.0.2 dev-ohm2023.local # agent-local managed",
		"# ::1 already.commented.local",
	}, "\n")
	got := hostsShadowedIPs(content, "dev-ohm2023.local")
	if strings.Join(got, ",") != "::1,127.0.0.1" {
		t.Errorf("shadowed = %v", got)
	}
	if got := hostsShadowedIPs(content, "already.commented.local"); len(got) != 0 {
		t.Errorf("commented line should not count: %v", got)
	}
}

func TestLoopbackAliasesAreDistinctLoopbacks(t *testing.T) {
	if LoopbackAlias == "127.0.0.1" {
		t.Error("the IPv4 alias must not be 127.0.0.1: a wildcard binder would shadow it")
	}
	if LoopbackAlias6 == "::1" {
		t.Error("the IPv6 alias must not be ::1: another local router answers there")
	}
	if !strings.HasPrefix(LoopbackAlias6, "fd") {
		t.Errorf("IPv6 alias %q should be a ULA, not a routable address", LoopbackAlias6)
	}
}
