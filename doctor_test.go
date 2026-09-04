package main

import "testing"

// The sudo finding has to tell four states apart from `sudo -n -l` output:
// nothing passwordless, the pre-0.22 allowlist (staged cp + cert wildcard),
// the 0.22.x allowlist that dropped cert trust entirely, and the current
// template with cert trust scoped to one fixed path. Both stale shapes must
// fail: the first is a security hole, the second is what makes the daemon's
// trust endpoint dead-end with no prompt.
func TestAllowlistCurrent(t *testing.T) {
	current := `User jake may run the following commands on mac:
    (root) NOPASSWD: /usr/bin/tee /etc/hosts
    (root) NOPASSWD: /usr/bin/tee /var/db/agent-local-trust.crt
    (root) NOPASSWD: /usr/bin/security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain /var/db/agent-local-trust.crt`
	preTee := `User jake may run the following commands on mac:
    (root) NOPASSWD: /bin/cp /Users/jake/.agent-local/run/hosts.tmp /etc/hosts
    (root) NOPASSWD: /usr/bin/security add-trusted-cert *`
	noTrust := `User jake may run the following commands on mac:
    (root) NOPASSWD: /usr/bin/tee /etc/hosts
    (root) NOPASSWD: /usr/bin/tee /etc/pf.conf`
	all := `User jake may run the following commands on mac:
    (ALL) NOPASSWD: ALL`
	if !allowlistCurrent(current) {
		t.Error("current template listing should count as current")
	}
	if allowlistCurrent(preTee) {
		t.Error("staged-cp + wildcard allowlist should count as stale")
	}
	if allowlistCurrent(noTrust) {
		t.Error("0.22.x allowlist without cert trust should count as stale")
	}
	if !allowlistCurrent(all) {
		t.Error("general NOPASSWD grant should count as current")
	}
	if allowlistCurrent("") {
		t.Error("empty listing should not count as current")
	}
}

// The staging path the allowlist names must be fixed and outside any
// user-writable tree: a path under $HOME or /tmp would let the allowlist entry
// be aimed at an arbitrary certificate, which is the hole the wildcard had.
func TestTrustStagePathIsRootOnly(t *testing.T) {
	for _, bad := range []string{"/tmp/", "/Users/", "/private/tmp/", "/var/folders/"} {
		if len(trustStagePath) >= len(bad) && trustStagePath[:len(bad)] == bad {
			t.Fatalf("trustStagePath %q lives under user-writable %s", trustStagePath, bad)
		}
	}
	if trustStagePath[0] != '/' {
		t.Fatalf("trustStagePath must be absolute: %q", trustStagePath)
	}
}
