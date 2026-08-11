package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Priv handles the few operations that need root (hosts file, cert trust).
// Strategy: try passwordless sudo first; on failure fall back to an
// osascript GUI password prompt. Interactive installs can prompt in the TUI.

// RunPrivileged runs argv with root privileges. Order: our scoped NOPASSWD
// allowlist (silent), then any passwordless sudo, then — only when
// interactive — the macOS GUI dialog. Install the allowlist once with
// `agent-local sudo` to never see a dialog again.
func RunPrivileged(interactive bool, argv ...string) error {
	// 1) scoped NOPASSWD allowlist or any passwordless sudo (silent)
	if sudoN(append([]string{"-n"}, argv...)...) == nil {
		return nil
	}
	if !interactive {
		return fmt.Errorf("needs root: %s (run: agent-local sudo)", strings.Join(argv, " "))
	}
	script := fmt.Sprintf(`do shell script %s with administrator privileges`, quoteForOsascript(argv))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("authorization failed: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sudoN runs sudo with args (pure variadic so a slice can be spread).
func sudoN(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin = nil
	return cmd.Run()
}

// CanRootNonInteractive reports if sudo -n works now.
func CanRootNonInteractive() bool {
	return sudoN("-n", "true") == nil
}

// quoteForOsascript renders argv for `do shell script`: each arg becomes a
// single-quoted sh word (so $ and spaces survive), then the whole command is
// escaped for the enclosing AppleScript double-quoted string.
func quoteForOsascript(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		q := "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		q = strings.ReplaceAll(q, `\`, `\\`)
		q = strings.ReplaceAll(q, `"`, `\"`)
		parts[i] = q
	}
	return `"` + strings.Join(parts, " ") + `"`
}

func runCmdQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	return cmd.Run()
}

// runCmdOut runs a command and returns its stdout.
func runCmdOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// HostsMarker tags lines we own inside /etc/hosts.
const HostsMarker = "# agent-local managed"

// HostsEntries returns desired /etc/hosts lines for the given domains.
func HostsEntries(domains []string) []string {
	out := []string{}
	for _, d := range domains {
		for _, ip := range hostsIPs() {
			out = append(out, fmt.Sprintf("%s %s %s", ip, d, HostsMarker))
		}
	}
	return out
}

// hostsIPs are the addresses a domain should resolve to: IPv4 always, and IPv6
// when the alias is up. Both families must be answered from the file or a ".local"
// name spends five seconds per lookup waiting for mDNS to answer the AAAA.
func hostsIPs() []string {
	out := []string{hostsIP()}
	if Alias6Active() {
		out = append(out, LoopbackAlias6)
	}
	return out
}

// EnsureHosts adds missing domain lines to /etc/hosts (root required) and
// migrates existing agent-local lines to the current target IP (the
// 127.0.0.2 alias when it's up, else 127.0.0.1). Returns lines changed.
func EnsureHosts(interactive bool, domains []string) (int, error) {
	want := hostsIP()
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return 0, err
	}
	changed := 0
	lines := strings.Split(string(b), "\n")
	// migrate our marker lines to the wanted IP
	for i, line := range lines {
		if !strings.Contains(line, HostsMarker) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.Contains(fields[0], ":") {
			continue // leave our IPv6 lines alone
		}
		if fields[0] != want {
			lines[i] = want + " " + strings.Join(fields[1:], " ")
			changed++
		}
	}
	joined := strings.Join(lines, "\n")
	var add []string
	for _, d := range domains {
		// Per family: an existing IPv4-only entry still needs its AAAA line, which
		// is what makes a .local domain fast.
		for _, ip := range hostsIPs() {
			if !hostLineHasIP(joined+"\n"+strings.Join(add, "\n"), d, ip) {
				add = append(add, fmt.Sprintf("%s %s %s", ip, d, HostsMarker))
				changed++
			}
		}
	}
	if changed == 0 && len(add) == 0 {
		return 0, nil
	}
	newContent := joined
	if len(add) > 0 {
		if !strings.HasSuffix(newContent, "\n") {
			newContent += "\n"
		}
		newContent += strings.Join(add, "\n") + "\n"
	}
	if err := writeRootFile("/etc/hosts", newContent, interactive); err != nil {
		return 0, err
	}
	return changed, nil
}

// hostLineHasIP reports whether content maps a domain at a specific address.
// Checking the domain alone was enough while every name had one line; with two
// families an IPv4-only entry has to be recognised as incomplete.
func hostLineHasIP(content, domain, ip string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != ip {
			continue
		}
		for _, f := range fields[1:] {
			if f == domain {
				return true
			}
		}
	}
	return false
}

// hostLineHas reports whether /etc/hosts content already maps a domain.
func hostLineHas(content, domain string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if f == domain {
				return true
			}
		}
	}
	return false
}

// hostsTarget returns the IP /etc/hosts currently maps a domain to, or "".
func hostsTarget(domain string) string {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if f == domain {
				return fields[0]
			}
		}
	}
	return ""
}

// RemoveHosts strips our marker lines for the named domains. An empty list is
// a no-op on purpose: treating it as "remove everything" turned any malformed
// request into a wipe of every managed hosts entry.
func RemoveHosts(interactive bool, domains []string) error {
	drop := map[string]bool{}
	for _, d := range domains {
		if d != "" {
			drop[d] = true
		}
	}
	if len(drop) == 0 {
		return nil
	}
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return err
	}
	var keep []string
	removed := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, HostsMarker) {
			fields := strings.Fields(line)
			if len(fields) >= 2 && drop[fields[1]] {
				removed++
				continue
			}
		}
		keep = append(keep, line)
	}
	if removed == 0 {
		return nil
	}
	return writeRootFile("/etc/hosts", strings.Join(keep, "\n"), interactive)
}

// writeRootFile writes content to a root-owned file via sudo tee.
func writeRootFile(path, content string, interactive bool) error {
	tmp := P().Run() + "/hosts.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	err := RunPrivileged(interactive, "cp", tmp, path)
	if err != nil {
		err = fmt.Errorf("write %s: %w", path, err)
	}
	return err
}
