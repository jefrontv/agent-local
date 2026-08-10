package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Finding is one doctor check result.
type Finding struct {
	Check   string `json:"check"`
	Status  string `json:"status"` // ok | warn | fail
	Detail  string `json:"detail"`
	FixHint string `json:"fix_hint,omitempty"`
	FixCmd  string `json:"fix_cmd,omitempty"`
	FixRoot bool   `json:"fix_root,omitempty"` // fix needs root
	AutoFix bool   `json:"auto_fix,omitempty"` // doctor --fix can apply
}

// DoctorReport is the full health report.
type DoctorReport struct {
	Findings []Finding `json:"findings"`
}

// Doctor runs all checks against current state.
func Doctor(store *Store) *DoctorReport {
	e := NewEngine(store)
	rep := &DoctorReport{}
	add := func(f Finding) { rep.Findings = append(rep.Findings, f) }

	// OS
	add(Finding{Check: "platform", Status: "ok", Detail: runtime.GOOS + "/" + runtime.GOARCH})

	// brew
	if store.Inventory().Brew == "" {
		add(Finding{Check: "homebrew", Status: "fail", Detail: "not installed",
			FixHint: "install.sh installs it", FixCmd: "agent-local install brew", AutoFix: true})
	} else {
		add(Finding{Check: "homebrew", Status: "ok", Detail: store.Inventory().Brew})
	}

	// PHP runtimes
	rts := store.Inventory().Runtimes()
	if len(rts) == 0 {
		add(Finding{Check: "php", Status: "fail", Detail: "no PHP toolchain found",
			FixCmd: "agent-local install php 8.3", AutoFix: true})
	} else {
		detail := strings.Join(rts, ", ")
		for _, rt := range store.Inventory().PHPs {
			if rt.FPM == "" {
				detail += fmt.Sprintf(" (%s lacks fpm)", rt.Version)
			}
		}
		add(Finding{Check: "php", Status: "ok", Detail: detail})
	}

	// database engine
	if store.Inventory().MySQL.Bin == "" {
		add(Finding{Check: "database", Status: "fail", Detail: "no MySQL/MariaDB engine",
			FixCmd: "agent-local install mariadb", AutoFix: true})
	} else {
		st := "ok"
		detail := store.Inventory().MySQL.Kind + " " + store.Inventory().MySQL.Version
		if !e.DBRunning() && len(store.Sites()) > 0 {
			st = "warn"
			detail += " (not running)"
		}
		add(Finding{Check: "database", Status: st, Detail: detail})
	}

	// http front
	front := FrontKind(store)
	if portOpen(DefaultHTTPPort) {
		add(Finding{Check: "http", Status: "ok", Detail: front + " listening on :" + fmt.Sprint(DefaultHTTPPort)})
	} else if len(store.Sites()) > 0 {
		add(Finding{Check: "http", Status: "warn", Detail: front + " not running",
			FixCmd: "agent-local daemon --background", AutoFix: true})
	} else {
		add(Finding{Check: "http", Status: "ok", Detail: front + " (no sites yet)"})
	}

	// bare-URL loopback alias (LocalWP binds wildcard :80)
	if AliasActive() {
		add(Finding{Check: "bare-urls", Status: "ok", Detail: LoopbackAlias + " alias up — http://<domain> hits agent-local"})
	} else if len(store.Sites()) > 0 {
		add(Finding{Check: "bare-urls", Status: "warn", Detail: "no " + LoopbackAlias + " alias; bare URLs hit other apps (Local)",
			FixCmd: "agent-local alias", AutoFix: true, FixRoot: true})
	}

	// per-site + per-worktree hosts entries
	var missingHosts []string
	for _, site := range store.Sites() {
		for _, d := range append([]string{site.Domain}, site.Aliases...) {
			if !hostsHas(d) {
				missingHosts = append(missingHosts, d)
			}
		}
	}
	for _, w := range store.Data.Worktrees {
		if !hostsHas(w.Domain) {
			missingHosts = append(missingHosts, w.Domain)
		}
	}
	if len(missingHosts) > 0 {
		add(Finding{Check: "dns", Status: "warn", Detail: "missing /etc/hosts: " + strings.Join(missingHosts, ", "),
			FixCmd: "agent-local doctor --fix", AutoFix: true, FixRoot: true})
	} else {
		add(Finding{Check: "dns", Status: "ok", Detail: "all domains resolve via /etc/hosts"})
	}

	// certs
	var missingCerts []string
	for _, d := range store.AllDomains() {
		cert, _ := CertPaths(d)
		if !fileExists(cert) {
			missingCerts = append(missingCerts, d)
		}
	}
	if len(missingCerts) > 0 {
		add(Finding{Check: "tls", Status: "warn", Detail: "no certs: " + strings.Join(missingCerts, ", "),
			FixCmd: "agent-local doctor --fix", AutoFix: true})
	} else if len(store.AllDomains()) > 0 {
		add(Finding{Check: "tls", Status: "ok", Detail: "certs present for all domains"})
	}

	// per-site liveness
	for _, site := range store.Sites() {
		if e.FPMRunning(site.Slug) {
			code, err := httpProbeHost("http://127.0.0.1:"+fmt.Sprint(DefaultHTTPPort)+"/", site.Domain)
			if err == nil && code < 500 {
				add(Finding{Check: "site:" + site.Slug, Status: "ok", Detail: fmt.Sprintf("http %d", code)})
			} else {
				add(Finding{Check: "site:" + site.Slug, Status: "warn", Detail: "fpm up but probe failed"})
			}
		}
	}

	// An .htaccess uploads rewrite is invisible to the built-in router: it is an
	// Apache directive. Sites carrying one and no media fallback would silently
	// 404 every image the local database references but the disk does not have.
	for _, site := range store.Sites() {
		if site.MediaFallback != "" {
			continue
		}
		if origin := htaccessUploadsRule(site.WPDir); origin != "" {
			add(Finding{Check: "media:" + site.Slug, Status: "warn",
				Detail:  ".htaccess redirects missing uploads to " + origin + ", which the router cannot read",
				FixHint: "agent-local media " + site.Slug + " --auto",
				FixCmd:  "agent-local media " + site.Slug + " --auto", AutoFix: true})
		}
	}

	// Same for a site: a docroot removed behind our back leaves a row that looks
	// healthy and answers 404. Say so rather than let it be discovered by hand.
	for _, site := range store.Sites() {
		if !fileExists(site.WPDir) {
			add(Finding{Check: "site:" + site.Slug, Status: "warn",
				Detail:  "docroot gone from " + shortHome(site.WPDir),
				FixHint: "delete the stale row: agent-local delete " + site.Slug + " --yes"})
		}
	}

	// A preview whose checkout was removed behind our back sits in the catalogue
	// looking merely stopped, then refuses every connection. Name it instead.
	for _, wt := range store.Data.Worktrees {
		if !fileExists(wt.Path) {
			add(Finding{Check: "preview:" + wt.ID, Status: "warn",
				Detail:  "checkout gone from " + shortHome(wt.Path),
				FixHint: "remove the stale row with D on the Worktrees tab"})
		}
	}

	// stale pidfiles
	for _, f := range []string{"mysql.pid", "apache.pid"} {
		pidf := filepath.Join(P().Run(), f)
		if b, err := os.ReadFile(pidf); err == nil && strings.TrimSpace(string(b)) != "" {
			pid := 0
			fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid)
			if pid > 0 && !Alive(pid) {
				os.Remove(pidf)
			}
		}
	}

	return rep
}

func hostsHas(domain string) bool {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
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

// DoctorFix applies auto-fixable findings. Returns what it did.
func DoctorFix(store *Store, interactive bool) []string {
	var done []string
	rep := Doctor(store)
	for _, f := range rep.Findings {
		if !f.AutoFix || f.Status == "ok" {
			continue
		}
		switch {
		case strings.HasPrefix(f.Check, "dns"):
			var missing []string
			for _, d := range store.AllDomains() {
				if !hostsHas(d) {
					missing = append(missing, d)
				}
			}
			if n, err := EnsureHosts(interactive, missing); err == nil && n > 0 {
				done = append(done, fmt.Sprintf("added %d /etc/hosts entries", n))
			}
		case f.Check == "tls":
			for _, d := range store.AllDomains() {
				cert, _, created, err := EnsureCert(d)
				if err == nil && created {
					_ = TrustCert(cert, interactive)
					done = append(done, "issued cert for "+d)
				}
			}
		case f.Check == "http":
			if err := EnsureHTTPFront(store); err == nil {
				done = append(done, "started http front")
			}
		case f.Check == "bare-urls":
			if err := EnsureLoopAlias(interactive); err == nil {
				if n, err := EnsureHosts(interactive, store.AllDomains()); err == nil {
					// daemon restart binds the alias :80/:443 listeners
					StopDaemons()
					_ = EnsureHTTPFront(store)
					done = append(done, fmt.Sprintf("bare URLs enabled (%d hosts lines on %s)", n, LoopbackAlias))
				}
			}
		}
	}
	return done
}

// RenderReport prints the report human-readable.
func (r *DoctorReport) RenderReport() string {
	var b strings.Builder
	icon := map[string]string{"ok": "✓", "warn": "!", "fail": "✗"}
	for _, f := range r.Findings {
		b.WriteString(fmt.Sprintf(" %s %-12s %s\n", icon[f.Status], f.Check, f.Detail))
		if f.Status != "ok" && f.FixCmd != "" {
			b.WriteString(fmt.Sprintf("   fix: %s\n", f.FixCmd))
		}
	}
	return b.String()
}
