package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// hostsMu serializes every /etc/hosts read-modify-write in this process. The
// daemon runs jobs concurrently, and two of them editing the file at once
// each read the same original, so whichever copy landed last erased the
// other's lines — with both callers told success. The privileged copy goes
// through one fixed temp path (the sudoers allowlist names it), which is one
// more reason only one cycle can be in flight.
var hostsMu sync.Mutex

// EnsureHosts adds missing domain lines to /etc/hosts (root required) and
// migrates existing agent-local lines to the current target IP (the
// 127.0.0.2 alias when it's up, else 127.0.0.1). Returns lines changed.
func EnsureHosts(interactive bool, domains []string) (int, error) {
	hostsMu.Lock()
	defer hostsMu.Unlock()
	want := hostsIP()
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return 0, err
	}
	changed := 0
	lines := strings.Split(string(b), "\n")
	// LocalWP (and friends) leave ::1 / 127.0.0.1 lines at the top of the file.
	// macOS uses the first match, so those win over our 127.0.0.2 lines and
	// every "bare" URL either hits the wrong stack or needs :10443. Comment
	// them out; our marked lines stay authoritative.
	var shadowed int
	lines, shadowed = commentShadowedHosts(lines, domains)
	changed += shadowed
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

// commentShadowedHosts comments out unmanaged /etc/hosts lines for domains we
// serve. Those leftovers (LocalWP's "::1 name" / "127.0.0.1 name #Local Site")
// sit above our marked lines and win resolution.
func commentShadowedHosts(lines []string, domains []string) ([]string, int) {
	want := map[string]bool{}
	for _, d := range domains {
		if d != "" {
			want[d] = true
		}
	}
	if len(want) == 0 {
		return lines, 0
	}
	ours := map[string]bool{LoopbackAlias: true, LoopbackAlias6: true}
	if !AliasActive() {
		ours["127.0.0.1"] = true
	}
	n := 0
	out := append([]string(nil), lines...)
	for i, line := range out {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.Contains(line, HostsMarker) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hit := false
		for _, f := range fields[1:] {
			if want[f] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		// An unmanaged line that already points at the address we serve can stay.
		if ours[fields[0]] {
			continue
		}
		out[i] = "# " + line + "  # shadowed by agent-local"
		n++
	}
	return out, n
}

// hostsShadowedIPs lists unmanaged addresses still published for a domain.
func hostsShadowedIPs(content, domain string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.Contains(line, HostsMarker) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if f == domain && !seen[fields[0]] {
				seen[fields[0]] = true
				out = append(out, fields[0])
			}
		}
	}
	return out
}

// hostsTarget returns the IP we treat as authoritative for a domain: our
// marked alias if one exists, otherwise the first live hosts line. First-match
// used to return leftover LocalWP ::1/127.0.0.1 lines and every printed URL
// grew a :10443 suffix even though the alias was up.
func hostsTarget(domain string) string {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return ""
	}
	return hostsTargetIn(string(b), domain)
}

func hostsTargetIn(content, domain string) string {
	var first, alias string
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hit := false
		for _, f := range fields[1:] {
			if f == domain {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if first == "" {
			first = fields[0]
		}
		if fields[0] == LoopbackAlias {
			alias = LoopbackAlias
			if strings.Contains(line, HostsMarker) {
				return LoopbackAlias
			}
		}
	}
	if alias != "" {
		return alias
	}
	return first
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
	hostsMu.Lock()
	defer hostsMu.Unlock()
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

// runPrivilegedWrite writes content to path as root by piping it straight
// into `tee`, instead of staging it in a user-writable temp file and then
// `cp`-ing that as root. Staging left a TOCTOU window: anything with write
// access to the staging path (Root().Run()) could swap the file's content
// between our write and the privileged copy running as root. Piping the
// bytes over stdin means root only ever sees what we hand it directly.
func runPrivilegedWrite(interactive bool, path string, content []byte) error {
	if sudoNStdin(content, "/usr/bin/tee", path) == nil {
		return nil
	}
	if !interactive {
		return fmt.Errorf("needs root: tee %s (run: agent-local sudo)", path)
	}
	// osascript's `do shell script` has no stdin channel to pipe into, so the
	// GUI-prompt fallback carries the payload as base64 inside the script
	// text itself — still no file touches disk before root writes it.
	b64 := base64.StdEncoding.EncodeToString(content)
	cmd := fmt.Sprintf("echo %s | base64 -D | /usr/bin/tee %s", shQuote(b64), shQuote(path))
	script := fmt.Sprintf(`do shell script "%s" with administrator privileges`,
		strings.ReplaceAll(strings.ReplaceAll(cmd, `\`, `\\`), `"`, `\"`))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("authorization failed: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sudoNStdin runs sudo -n with args, feeding stdin bytes to the child.
func sudoNStdin(stdin []byte, args ...string) error {
	cmd := exec.Command("sudo", append([]string{"-n"}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// shQuote wraps s in single quotes for embedding in a generated sh command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeRootFile writes content to a root-owned file via tee, piped directly
// (no staged copy on disk — see runPrivilegedWrite).
func writeRootFile(path, content string, interactive bool) error {
	if err := runPrivilegedWrite(interactive, path, []byte(content)); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
