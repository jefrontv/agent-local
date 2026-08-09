package main

import (
	"fmt"
	"os"
	"strings"
)

// LoopbackAlias is the address agent-local uses for bare port-80/443
// serving. LocalWP binds wildcard :80/:443; our root front daemon binds
// this specific alias, which wins those connections (specific beats
// wildcard). Same architecture as LocalWP's own router.
const LoopbackAlias = "127.0.0.2"

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
	aliasCache = 0
	return installFrontDaemon(interactive)
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
	tmp := P().Run() + "/pf.conf.new"
	if err := os.WriteFile(tmp, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return nil
	}
	defer os.Remove(tmp)
	_ = RunPrivileged(interactive, "/bin/cp", tmp, "/etc/pf.conf")
	// pf stays DISABLED: enabling it stalls loopback for unknown reasons on
	// this system; nothing needs it.
	return nil
}

// RemoveLoopAlias tears down the front daemon + alias.
func RemoveLoopAlias(interactive bool) error {
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
	self, err := os.Executable()
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
		<string>%s</string><string>front-daemon</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, self, P().Log("front"), P().Log("front"))
	tmp := P().Run() + "/front.plist"
	if err := os.WriteFile(tmp, []byte(plist), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	dst := frontPlistPath()
	_ = RunPrivileged(interactive, "/bin/launchctl", "unload", dst)
	if err := RunPrivileged(interactive, "/bin/cp", tmp, dst); err != nil {
		return err
	}
	_ = RunPrivileged(interactive, "/usr/sbin/chown", "root:wheel", dst)
	_ = RunPrivileged(interactive, "/bin/chmod", "644", dst)
	return RunPrivileged(interactive, "/bin/launchctl", "load", dst)
}

// BareURL is the URL a browser should use for a site: bare domain when the
// 127.0.0.2 alias carries it, else the port-suffixed loopback URL.
func BareURL(s *Site) string { return BareDomainURL(s.Domain) }

// BareDomainURL is BareURL for any domain we serve (sites and worktrees).
func BareDomainURL(domain string) string {
	if AliasActive() && hostsTarget(domain) == LoopbackAlias {
		return "http://" + domain
	}
	return fmt.Sprintf("http://%s:%d", domain, DefaultHTTPPort)
}
