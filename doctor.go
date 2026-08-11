package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
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

	// siteProbeTimeout bounds each liveness probe. Long enough that a warm site
	// always answers, short enough that doctor stays a command you reach for.
	const siteProbeTimeout = 3 * time.Second

	// per-site liveness. Each probe is a round trip to a different pool, so they
	// run together and are reported in site order: eight sites took the wall time
	// of eight, for a command whose whole job is to be quick to reach for.
	{
		sites := store.Sites()
		results := make([]*Finding, len(sites))
		var wg sync.WaitGroup
		for i, site := range sites {
			if !e.FPMRunning(site.Slug) {
				continue
			}
			wg.Add(1)
			go func(i int, site *Site) {
				defer wg.Done()
				code, took, err := httpProbeHostTimed("http://127.0.0.1:"+fmt.Sprint(DefaultHTTPPort)+"/", site.Domain, siteProbeTimeout)
				switch {
				case err != nil:
					// Distinguish "nothing there" from "too slow to wait for": a
					// cold WordPress render is not a broken site, but it is worth
					// naming rather than silently making doctor take six seconds.
					detail := "fpm up but probe failed"
					if took >= siteProbeTimeout {
						detail = fmt.Sprintf("serving, but slower than %s — a cold cache or a slow plugin, not a broken site", siteProbeTimeout)
					}
					results[i] = &Finding{Check: "site:" + site.Slug, Status: "warn", Detail: detail}
				case code >= 500:
					results[i] = &Finding{Check: "site:" + site.Slug, Status: "warn", Detail: fmt.Sprintf("http %d", code)}
				case took > time.Second:
					results[i] = &Finding{Check: "site:" + site.Slug, Status: "warn",
						Detail: fmt.Sprintf("http %d but slow: %dms", code, took.Milliseconds())}
				default:
					results[i] = &Finding{Check: "site:" + site.Slug, Status: "ok", Detail: fmt.Sprintf("http %d", code)}
				}
			}(i, site)
		}
		wg.Wait()
		for _, f := range results {
			if f != nil {
				add(*f)
			}
		}
	}

	// A ".local" domain is resolved by mDNS on macOS, not by /etc/hosts alone, and
	// the AAAA lookup nothing answers costs five seconds — per request. It is not
	// our latency to fix, but it is ours to name: measured, ta.local spent 5.00s in
	// name lookup where a .test domain spent 0.009s.
	for _, site := range store.Sites() {
		if !strings.HasSuffix(site.Domain, ".local") {
			continue
		}
		add(Finding{Check: "dns:" + site.Slug, Status: "warn",
			Detail:  site.Domain + " ends in .local — macOS resolves that by mDNS, adding ~5s to every request",
			FixHint: "rename it: agent-local domain " + site.Slug + " " + strings.TrimSuffix(site.Domain, ".local") + store.Suffix()})
	}

	// Two local-dev tools, one pair of privileged ports. Ours is bound to the
	// loopback alias, which coexists with a wildcard listener — but only if the
	// wildcard got there first: BSD refuses a wildcard bind while any specific
	// address holds the port. So agent-local starting first stops LocalWP's nginx
	// from starting at all, and its sites go dark with nothing to explain it.
	if sites, err := ListLocalWPSites(); err == nil && len(sites) > 0 {
		if f := localwpFinding(len(sites), addrOpen(LoopbackAlias, 80), addrOpen("127.0.0.1", 80)); f != nil {
			add(*f)
		}
	}

	// An .htaccess uploads rewrite is invisible to the built-in router: it is an
	// Apache directive. Sites carrying one and no media fallback would silently
	// 404 every image the local database references but the disk does not have.
	for _, site := range store.Sites() {
		rule := htaccessUploadsRule(site.WPDir)
		switch {
		case site.MediaOff && rule != "":
			add(Finding{Check: "media:" + site.Slug, Status: "warn",
				Detail:  "turned off, so the .htaccess rule pointing at " + rule + " is ignored",
				FixHint: "agent-local media " + site.Slug + " --auto"})
		case EffectiveMediaFallback(site) != "":
			where := "from .htaccess"
			if site.MediaFallback != "" {
				where = "set here"
			}
			add(Finding{Check: "media:" + site.Slug, Status: "ok",
				Detail: "missing uploads → " + EffectiveMediaFallback(site) + " (" + where + ")"})
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

// localwpFinding judges the shared-port situation from two facts: whether our
// alias listener is up, and whether anything answers on 127.0.0.1. Kept separate
// from the dialling so the verdict can be checked without a second router
// installed on the machine.
func localwpFinding(sites int, ours, rival bool) *Finding {
	switch {
	case ours && !rival:
		// We are on the alias and nobody holds the wildcard: LocalWP's nginx
		// cannot bind while we are here, so its sites are unreachable.
		return &Finding{Check: "localwp", Status: "warn",
			Detail:  fmt.Sprintf("%d LocalWP sites configured, but nothing serves 127.0.0.1:80 — our bare-URL listener blocks nginx's wildcard bind", sites),
			FixHint: "agent-local yield 60, then start the site in Local — both run side by side once nginx is up",
			FixCmd:  "agent-local yield 60"}
	case ours && rival:
		return &Finding{Check: "localwp", Status: "ok",
			Detail: fmt.Sprintf("%d sites, sharing :80 with the other router", sites)}
	case !ours && rival:
		// Someone else has the ports and we stood aside: bare URLs are theirs,
		// ours answer on the high ports. Worth stating, not worth warning about.
		return &Finding{Check: "localwp", Status: "ok",
			Detail: fmt.Sprintf("%d sites hold :80; agent-local sites are on :%d/:%d", sites, DefaultHTTPPort, DefaultHTTPSPort)}
	default:
		return nil
	}
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
