package main

import (
	"testing"
	"time"
)

// aliasState saves and restores AliasActive's cache around a test.
func aliasState(t *testing.T) {
	t.Helper()
	up, at := aliasUp, aliasAt
	t.Cleanup(func() {
		aliasMu.Lock()
		aliasUp, aliasAt = up, at
		aliasMu.Unlock()
	})
}

// The alias is only ever down for a moment — the root front daemon adds it at
// startup. Caching that negative for the life of the process is what made a
// daemon started before `agent-local alias` ever ran keep reporting
// port-suffixed URLs and writing /etc/hosts onto 127.0.0.1 long after the alias
// was up, with nothing but a daemon restart to clear it.
func TestAliasActiveNegativeIsNotPermanent(t *testing.T) {
	aliasState(t)

	// A negative inside the TTL answers from cache: no ifconfig per caller.
	aliasMu.Lock()
	aliasUp, aliasAt = false, time.Now()
	aliasMu.Unlock()
	if AliasActive() {
		t.Fatal("a fresh negative should read as down")
	}

	// A negative older than the TTL must be re-probed. What it finds depends on
	// the machine, so assert the probe happened by watching the timestamp move.
	aliasMu.Lock()
	aliasAt = time.Now().Add(-2 * aliasTTL)
	aliasMu.Unlock()
	AliasActive()
	aliasMu.Lock()
	reprobed := time.Since(aliasAt) < aliasTTL
	aliasMu.Unlock()
	if !reprobed {
		t.Error("a stale negative was answered from cache instead of re-probed")
	}
}

// A positive is durable — the alias does not come and go on its own — so it must
// not cost a probe per caller however old the answer is.
func TestAliasActivePositiveSticks(t *testing.T) {
	aliasState(t)

	aliasMu.Lock()
	aliasUp, aliasAt = true, time.Now().Add(-24*time.Hour)
	aliasMu.Unlock()
	if !AliasActive() {
		t.Error("a positive answer should stick regardless of age")
	}
	aliasMu.Lock()
	probed := time.Since(aliasAt) > time.Hour
	aliasMu.Unlock()
	if !probed {
		t.Error("a cached positive was re-probed")
	}
}

// Bringing the alias up or down must be believed immediately, not after the TTL.
func TestForgetAliasForcesReprobe(t *testing.T) {
	aliasState(t)

	aliasMu.Lock()
	aliasUp, aliasAt = false, time.Now()
	aliasMu.Unlock()
	forgetAlias()

	aliasMu.Lock()
	cleared := !aliasUp && aliasAt.IsZero()
	aliasMu.Unlock()
	if !cleared {
		t.Error("forgetAlias left the cached answer in place")
	}
}
