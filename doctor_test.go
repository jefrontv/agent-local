package main

import "testing"

// The sudo finding has to tell three states apart from `sudo -n -l` output:
// nothing passwordless, a stale allowlist from a previous release, and the
// current template. Getting the stale case wrong is exactly how a setup
// fails mid-flight on a machine that looks configured.
func TestAllowlistCurrent(t *testing.T) {
	current := `User jake may run the following commands on mac:
    (root) NOPASSWD: /usr/bin/tee /etc/hosts
    (root) NOPASSWD: /usr/bin/tee /etc/pf.conf`
	stale := `User jake may run the following commands on mac:
    (root) NOPASSWD: /bin/cp /Users/jake/.agent-local/run/hosts.tmp /etc/hosts
    (root) NOPASSWD: /usr/bin/security add-trusted-cert *`
	all := `User jake may run the following commands on mac:
    (ALL) NOPASSWD: ALL`
	if !allowlistCurrent(current) {
		t.Error("current template listing should count as current")
	}
	if allowlistCurrent(stale) {
		t.Error("staged-cp allowlist should count as stale")
	}
	if !allowlistCurrent(all) {
		t.Error("general NOPASSWD grant should count as current")
	}
	if allowlistCurrent("") {
		t.Error("empty listing should not count as current")
	}
}
