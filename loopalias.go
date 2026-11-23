package main

import (
	"fmt"
	"html"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// aliasTTL bounds how often a *negative* answer is re-probed. The alias is
// durable once up, so a positive sticks for the life of the process; a negative
// is only ever true for a moment — the root front daemon adds the address at
// startup — and caching it forever is how a daemon that first asked before
// `agent-local alias` ever ran went on reporting port-suffixed URLs and writing
// /etc/hosts entries onto 127.0.0.1 long after the alias was up. Restarting the
// daemon was the only thing that cleared it.
const aliasTTL = 5 * time.Second

var (
	aliasMu sync.Mutex
	aliasUp bool
	aliasAt time.Time
)

// forgetAlias drops the cached answer so the next AliasActive re-probes. Called
// wherever the alias is deliberately brought up or taken down, so the change is
// believed immediately rather than after aliasTTL.
func forgetAlias() {
	aliasMu.Lock()
	defer aliasMu.Unlock()
	aliasUp, aliasAt = false, time.Time{}
}

// AliasActive reports whether lo0 carries 127.0.0.2. A positive is cached for
// the process; a negative is re-probed every aliasTTL so a daemon that started
// too early corrects itself instead of serving the wrong URLs until restarted.
func AliasActive() bool {
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if aliasUp {
		return true
	}
	if !aliasAt.IsZero() && time.Since(aliasAt) < aliasTTL {
		return false
	}
	aliasAt = time.Now()
	aliasUp = interfaceHasAddr(LoopbackAlias)
	return aliasUp
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

// frontHelper is the root-owned copy of agent-local that the front
// LaunchDaemon runs. Root running a file only root can replace means no user
// process can swap the code launchd starts as root.
const frontHelper = "/Library/PrivilegedHelperTools/agent-local-front"

// frontLogPath is root-owned too: a root process appending to a file in the
// user's home would follow whatever that path is swapped for.
const frontLogPath = "/Library/Logs/agent-local-front.log"

// frontHelperInstalled reports whether the front job runs the root-owned
// helper. Any other program (the legacy ~/.local/bin binary run as root, or a
// UserName job that macOS refuses the ports) needs `agent-local alias`.
func frontHelperInstalled() bool {
	b, err := os.ReadFile(frontPlistPath())
	return err == nil && frontPlistUsesHelper(b) && rootOwned(frontHelper)
}

// frontPlistUsesHelper is frontHelperInstalled's reading of the plist.
func frontPlistUsesHelper(plist []byte) bool {
	s := string(plist)
	return strings.Contains(s, "<string>"+frontHelper+"</string>") && !strings.Contains(s, "<key>UserName</key>")
}

// rootOwned reports whether path is a regular file that root owns in a
// root-owned directory, neither writable by group or others, so only root can
// change what runs from it.
func rootOwned(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	for _, p := range []string{path, filepath.Dir(path)} {
		if p != path {
			if fi, err = os.Stat(p); err != nil {
				return false
			}
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return false
		}
	}
	return true
}

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
		forgetAlias()
		log.Printf("front: nothing serves :80/:443 on %s; run: agent-local alias", LoopbackAlias)
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
	forgetAlias()
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
	// One root script, so one password dialog at most (see installFrontDaemon).
	const script = `/sbin/ifconfig lo0 inet6 "$2" -alias 2>/dev/null
for p in /Library/LaunchDaemons/local.agent-local.alias.ipv4.plist /Library/LaunchDaemons/local.agent-local.alias.ipv6.plist "$1"; do
	[ -e "$p" ] || continue
	/bin/launchctl unload "$p" 2>/dev/null
	/bin/rm -f "$p" || exit 1
done
/bin/rm -f "$4"
/sbin/ifconfig lo0 -alias "$3" 2>/dev/null
exit 0`
	err := RunPrivileged(interactive, "/bin/sh", "-c", script, "_", frontPlistPath(), LoopbackAlias6, LoopbackAlias, frontHelper)
	forgetAlias()
	return err
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
	if !interactive {
		return fmt.Errorf("run agent-local alias to install the front daemon")
	}
	account, err := user.Current()
	if err != nil {
		return err
	}
	if account.Uid == "0" {
		return fmt.Errorf("run agent-local alias as the installing user")
	}
	plist := frontPlist(P().Run())
	v4, v6 := aliasBootPlists()
	// One root script for every file and launchctl call, so a machine without
	// the allowlist sees one password dialog: each RunPrivileged call that
	// sudo -n refuses opens its own, and this install used to be seven.
	// The helper and each plist are staged in a root-only directory and moved
	// into place, so nothing is ever half written where launchd reads it.
	const script = `set -eu
dir=$(/usr/bin/mktemp -d)
trap '/bin/rm -rf "$dir"' EXIT
put() {
	printf '%s' "$2" > "$dir/stage"
	/usr/sbin/chown root:wheel "$dir/stage"
	/bin/chmod 644 "$dir/stage"
	/bin/mv -f "$dir/stage" "$1"
}
/usr/bin/install -d -o root -g wheel -m 0755 "$(/usr/bin/dirname "$5")"
/usr/bin/install -o root -g wheel -m 0755 "$6" "$dir/helper"
/bin/mv -f "$dir/helper" "$5"
put /Library/LaunchDaemons/local.agent-local.alias.ipv4.plist "$1"
put /Library/LaunchDaemons/local.agent-local.alias.ipv6.plist "$2"
/bin/launchctl unload "$4" 2>/dev/null || true
put "$4" "$3"
/bin/launchctl load "$4"`
	return RunPrivileged(interactive, "/bin/sh", "-c", script, "_", v4, v6, plist, frontPlistPath(), frontHelper, self)
}

// frontPlist is the LaunchDaemon for the front. It runs as root, because macOS
// lets an ordinary user bind ports below 1024 only on the wildcard address and
// the front binds 127.0.0.2. What keeps that safe is the program: frontHelper,
// a copy only root can replace, never the user-writable installed binary.
func frontPlist(runDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
`, html.EscapeString(frontHelper), html.EscapeString(runDir), html.EscapeString(frontLogPath), html.EscapeString(frontLogPath))
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

// aliasBootPlists are the boot jobs that restore the loopback aliases, which
// lo0 forgets on every restart. installFrontDaemon writes them as root.
func aliasBootPlists() (ipv4, ipv6 string) {
	var out []string
	for _, family := range []string{"ipv4", "ipv6"} {
		label := "local.agent-local.alias." + family
		args := "<string>alias</string><string>127.0.0.2</string>"
		if family == "ipv6" {
			args = "<string>inet6</string><string>fd00:a10c::2</string><string>prefixlen</string><string>128</string><string>alias</string>"
		}
		plist := "<plist version=\"1.0\"><dict><key>Label</key><string>" + label + "</string><key>ProgramArguments</key><array><string>/sbin/ifconfig</string><string>lo0</string>" + args + "</array><key>RunAtLoad</key><true/></dict></plist>"
		out = append(out, plist)
	}
	return out[0], out[1]
}
