package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// LoopbackAlias is the address agent-local uses for bare port-80/443
// serving. LocalWP binds wildcard :80/:443; our root front daemon binds
// this specific alias, which wins those connections (specific beats
// wildcard). Same architecture as LocalWP's own router.
const LoopbackAlias = "127.0.0.2"

// LoopbackAlias6 is the IPv6 half. It exists for one reason: macOS resolves
// ".local" through mDNS, so a name with only an A record in /etc/hosts still sends
// the AAAA question to Bonjour, where nobody answers and the resolver waits five
// seconds — per lookup, uncached. Answering both families from the file removes
// that entirely. A ULA rather than ::1, because ::1 is where another local-dev
// router answers when one is running, and our own sites would land on it.
const LoopbackAlias6 = "fd00:a10c::2"

const pfConfMarker = "# agent-local bare-URL anchor"

var aliasCache int // 0=unknown 1=up 2=down

// AliasActive reports whether lo0 carries 127.0.0.2 (cached per process).
func AliasActive() bool {
	if aliasCache == 0 {
		out, err := runCmdOut("ifconfig", "lo0")
		if err == nil && strings.Contains(out, "inet "+LoopbackAlias+" ") {
			aliasCache = 1
		} else {
			aliasCache = 2
		}
	}
	return aliasCache == 1
}

// hostsIP is the address /etc/hosts entries should point at: the alias when
// available (bare URLs work), else 127.0.0.1 (port-suffixed URLs).
func hostsIP() string {
	if AliasActive() {
		return LoopbackAlias
	}
	return "127.0.0.1"
}

// frontPlistPath is where the root LaunchDaemon lives.
func frontPlistPath() string { return "/Library/LaunchDaemons/local.agent-local.front.plist" }

// frontDaemonAlive reports whether the bare-URL front is serving. It is a
// root process, and macOS hides other users' process arguments, so pgrep
// cannot see it; the port can. The one legitimate reason for the port to be
// free is a yield in progress, when it is standing aside on request.
func frontDaemonAlive() bool {
	return dialable(LoopbackAlias, 80) || yieldActive()
}

// frontDaemonInstalled reports whether launchd has the front daemon at all.
// An alias without this is an orphan: something started the front by hand
// once, and the first thing that kills it takes every bare URL down with it.
func frontDaemonInstalled() bool { return fileExists(frontPlistPath()) }

// watchFront runs inside the router daemon: an alias with nothing serving
// behind it is a machine where every bare URL refuses connections while the
// alias check still says fine. The front also lets go of the port for up to
// twenty seconds while another local router boots, so one missed check is
// not a verdict; two in a row, thirty seconds apart, is. Then reinstall
// through the allowlist, or say so in the log with the command.
func watchFront() {
	var lastTry time.Time
	misses := 0
	for {
		time.Sleep(30 * time.Second)
		if !interfaceHasAddr(LoopbackAlias) || frontDaemonAlive() {
			misses = 0
			continue
		}
		misses++
		if misses < 2 || time.Since(lastTry) < 10*time.Minute {
			continue
		}
		lastTry = time.Now()
		aliasCache = 0
		if err := EnsureLoopAlias(false); err != nil {
			log.Printf("front: %s is up but nothing serves :80/:443 on it, and the front daemon could not be reinstalled silently (%v) — run: %s alias", LoopbackAlias, err, AppName)
		} else {
			log.Printf("front: reinstalled the bare-URL front daemon under launchd")
		}
	}
}

// EnsureLoopAlias adds 127.0.0.2 to lo0 and installs the root front daemon
// that binds 127.0.0.2:80/:443 and pipes to our router ports. One-time root
// setup (via the allowlist: silent); the LaunchDaemon restores it at boot.
// pf was tried first but a globally-enabled pf stalls plain loopback
// connections, so it is removed here and never used.
func EnsureLoopAlias(interactive bool) error {
	_ = RemovePFWiring(interactive)
	if err := RunPrivileged(interactive, "/sbin/ifconfig", "lo0", "alias", LoopbackAlias); err != nil {
		return err
	}
	// Best-effort: without the IPv6 half everything still works, ".local" domains
	// are just slow. Never fail the setup over it.
	if err := RunPrivileged(interactive, "/sbin/ifconfig", "lo0", "inet6", LoopbackAlias6, "prefixlen", "128", "alias"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not add the %s alias (.local domains will be slow): %v\n", LoopbackAlias6, err)
	}
	aliasCache = 0
	return installFrontDaemon(interactive)
}

// Alias6Active reports whether lo0 carries the IPv6 alias.
func Alias6Active() bool { return interfaceHasAddr(LoopbackAlias6) }

// interfaceHasAddr reports whether any interface carries an address.
func interfaceHasAddr(addr string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	want := net.ParseIP(addr)
	for _, a := range addrs {
		if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.Equal(want) {
			return true
		}
	}
	return false
}

// RemovePFWiring strips any pf.conf block from earlier builds and reloads.
func RemovePFWiring(interactive bool) error {
	b, err := os.ReadFile("/etc/pf.conf")
	if err != nil || !strings.Contains(string(b), pfConfMarker) {
		return nil
	}
	var kept []string
	dropping := false
	// pf must be OFF: an enabled pf stalls loopback connects on this system.
	_ = RunPrivileged(interactive, "/sbin/pfctl", "-d")
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == pfConfMarker {
			dropping = true
			continue
		}
		if dropping && (strings.HasPrefix(t, "rdr-anchor ") || strings.HasPrefix(t, "anchor \"agent-local\"") || strings.HasPrefix(t, "load anchor ")) {
			continue
		}
		dropping = false
		kept = append(kept, line)
	}
	_ = runPrivilegedWrite(interactive, "/etc/pf.conf", []byte(strings.Join(kept, "\n")))
	// pf stays DISABLED: enabling it stalls loopback for unknown reasons on
	// this system; nothing needs it.
	return nil
}

// RemoveLoopAlias tears down the front daemon + alias.
func RemoveLoopAlias(interactive bool) error {
	_ = RunPrivileged(interactive, "/sbin/ifconfig", "lo0", "inet6", LoopbackAlias6, "-alias")
	dst := frontPlistPath()
	_ = RunPrivileged(interactive, "/bin/launchctl", "unload", dst)
	_ = RunPrivileged(interactive, "/bin/rm", dst)
	_ = RunPrivileged(interactive, "/sbin/ifconfig", "lo0", "-alias", LoopbackAlias)
	aliasCache = 0
	return nil
}

// installFrontDaemon writes + loads the root LaunchDaemon running
// `agent-local front-daemon` (binds 127.0.0.2:80/:443, pipes to the daemon).
func installFrontDaemon(interactive bool) error {
	// The installed binary, not whichever build ran this command: a plist
	// naming a working-tree build (or a versioned Caskroom file brew upgrade
	// removes) is a root daemon that stops existing.
	self, err := installedBinaryPath()
	if err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>local.agent-local.front</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string><string>front-daemon</string><string>--run-dir</string><string>%s</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, self, P().Run(), P().Log("front"), P().Log("front"))
	dst := frontPlistPath()
	_ = RunPrivileged(interactive, "/bin/launchctl", "unload", dst)
	if err := runPrivilegedWrite(interactive, dst, []byte(plist)); err != nil {
		return err
	}
	_ = RunPrivileged(interactive, "/usr/sbin/chown", "root:wheel", dst)
	_ = RunPrivileged(interactive, "/bin/chmod", "644", dst)
	return RunPrivileged(interactive, "/bin/launchctl", "load", dst)
}

// BareURL is the URL a browser should use for a site: https, because every domain
// we serve gets a certificate we generate and trust, so offering http would be
// showing people the worse of two URLs we already support. Bare when the
// 127.0.0.2 alias carries it, else port-suffixed.
func BareURL(s *Site) string { return BareDomainURL(s.Domain) }

// BareDomainURL is BareURL for any domain we serve (sites and worktrees).
func BareDomainURL(domain string) string {
	if !fileExists(certPathFor(domain)) {
		// No certificate yet: https would fail outright, http merely redirects.
		return httpDomainURL(domain)
	}
	if AliasActive() && hostsTarget(domain) == LoopbackAlias {
		return "https://" + domain
	}
	return fmt.Sprintf("https://%s:%d", domain, DefaultHTTPSPort)
}

// httpDomainURL is the plain-http form, for the cases that genuinely need it.
func httpDomainURL(domain string) string {
	if AliasActive() && hostsTarget(domain) == LoopbackAlias {
		return "http://" + domain
	}
	return fmt.Sprintf("http://%s:%d", domain, DefaultHTTPPort)
}

// certPathFor is the certificate we would have issued for a domain.
func certPathFor(domain string) string {
	cert, _ := CertPaths(domain)
	return cert
}
