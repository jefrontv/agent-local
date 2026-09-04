package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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

	// PHP runtimes. Scanned fresh, not read from the cached inventory: a keg
	// breaks when brew removes one of its dependencies, which happens without
	// anything telling us, and a day-stale scan would report the version as
	// simply absent — the same misreport that made a switch to 7.4 impossible.
	rescanPHP(store)
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

	for _, f := range brokenPHPFindings(store.Inventory()) {
		add(f)
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

	// bare-URL loopback alias (LocalWP binds wildcard :80). The alias alone is
	// not the check: a machine can carry 127.0.0.2 with nothing listening on it,
	// and then every bare URL refuses while the alias looks fine.
	if AliasActive() {
		switch {
		case dialable(LoopbackAlias, 80):
			add(Finding{Check: "bare-urls", Status: "ok", Detail: LoopbackAlias + " alias up, front daemon serving :80/:443"})
		case yieldActive():
			add(Finding{Check: "bare-urls", Status: "warn", Detail: "front daemon standing aside on request (agent-local yield); bare URLs resume when it ends"})
		case frontDaemonInstalled():
			add(Finding{Check: "bare-urls", Status: "fail", Detail: LoopbackAlias + " alias up but nothing serves :80/:443 on it — bare URLs refuse connections",
				FixCmd: "agent-local alias", AutoFix: true, FixRoot: true})
		default:
			add(Finding{Check: "bare-urls", Status: "fail", Detail: LoopbackAlias + " alias up with no front daemon under launchd (an orphan from an older setup) — bare URLs refuse connections",
				FixCmd: "agent-local alias", AutoFix: true, FixRoot: true})
		}
	} else if len(store.Sites()) > 0 {
		add(Finding{Check: "bare-urls", Status: "warn", Detail: "no " + LoopbackAlias + " alias; bare URLs hit other apps (Local)",
			FixCmd: "agent-local alias", AutoFix: true, FixRoot: true})
	}

	// passwordless root for the scoped commands is what lets site setup add
	// hosts entries and trust certs without stopping. An allowlist installed
	// by an older release names retired commands, so everything looks
	// configured until the first privileged write fails mid-setup — exactly
	// the failure this finding exists to get ahead of.
	switch allow := sudoAllowlist(); {
	case allow == "":
		add(Finding{Check: "sudo", Status: "warn", Detail: "no passwordless root — first hosts/cert write will stop and ask",
			FixCmd: "agent-local sudo", FixRoot: true})
	case !allowlistCurrent(allow):
		add(Finding{Check: "sudo", Status: "warn", Detail: "sudo allowlist predates this release — re-run agent-local sudo once",
			FixCmd: "agent-local sudo", FixRoot: true})
	default:
		add(Finding{Check: "sudo", Status: "ok", Detail: "passwordless root for hosts, alias, front, cert trust"})
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

	// The reverse: lines we manage for names no site or preview has any more.
	// Renames used to leave the old name behind (the store's domain index was
	// not rebuilt, so the old name still looked like ours), and each one keeps
	// resolving to this machine forever. Name them; --fix removes them.
	if orphans := orphanHostsEntries(store); len(orphans) > 0 {
		add(Finding{Check: "dns-orphans", Status: "warn",
			Detail: "managed /etc/hosts lines for domains no site serves: " + strings.Join(orphans, ", "),
			FixCmd: "agent-local doctor --fix", AutoFix: true, FixRoot: true})
	}

	// A domain can be "in hosts" and still resolve to LocalWP's leftover ::1 /
	// 127.0.0.1 lines that sit above ours. Then the printed URL grows :10443
	// and the bare name never hits 127.0.0.2:443.
	if hostsBody, err := os.ReadFile("/etc/hosts"); err == nil {
		var shadowed []string
		for _, d := range store.AllDomains() {
			if ips := hostsShadowedIPs(string(hostsBody), d); len(ips) > 0 {
				shadowed = append(shadowed, d+" ("+strings.Join(ips, ", ")+")")
			}
		}
		if len(shadowed) > 0 {
			add(Finding{Check: "dns-shadow", Status: "warn",
				Detail:  "older /etc/hosts lines win over ours: " + strings.Join(shadowed, "; "),
				FixHint: "LocalWP leftovers like ::1 / 127.0.0.1 sit above 127.0.0.2, so the bare URL misses the alias",
				FixCmd:  "agent-local doctor --fix", AutoFix: true, FixRoot: true})
		}
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
		alive := e.AliveAll()
		var wg sync.WaitGroup
		for i, site := range sites {
			if !alive[site.Slug] {
				continue
			}
			wg.Add(1)
			go func(i int, site *Site) {
				defer wg.Done()
				code, loc, took, err := httpProbeHostTimed("http://127.0.0.1:"+fmt.Sprint(DefaultHTTPPort)+"/", site.Domain, siteProbeTimeout)
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
				case code/100 == 3 && offSite(site, loc):
					// WordPress answering with a redirect to another machine is the
					// production-config-copied-in failure seen from the browser.
					// The url: finding below names which constant, and fixes it.
					results[i] = &Finding{Check: "site:" + site.Slug, Status: "warn",
						Detail: fmt.Sprintf("http %d → %s — not one of this site's names", code, loc)}
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
	// A ".local" domain is only slow while the AAAA question has to go to mDNS. With
	// the IPv6 alias and its hosts line, both families answer from the file and the
	// five-second wait disappears — so the check is whether that is in place, not
	// whether the suffix is ".local".
	if dual := Alias6Active(); true {
		for _, site := range store.Sites() {
			if !strings.HasSuffix(site.Domain, ".local") {
				continue
			}
			switch {
			case !dual:
				add(Finding{Check: "dns:" + site.Slug, Status: "warn",
					Detail:  site.Domain + " ends in .local and there is no IPv6 alias — macOS asks mDNS for the AAAA and waits ~5s per lookup",
					FixHint: "agent-local alias   (adds " + LoopbackAlias6 + " and the matching hosts line)",
					FixCmd:  "agent-local alias", AutoFix: true})
			case !hostsHasIP(site.Domain, LoopbackAlias6):
				add(Finding{Check: "dns:" + site.Slug, Status: "warn",
					Detail:  site.Domain + " has no IPv6 hosts line, so its AAAA lookup still waits on mDNS (~5s)",
					FixHint: "agent-local start " + site.Slug + "   (rewrites the entry)",
					FixCmd:  "agent-local start " + site.Slug, AutoFix: true})
			default:
				add(Finding{Check: "dns:" + site.Slug, Status: "ok",
					Detail: site.Domain + " answers both families from /etc/hosts (no mDNS wait)"})
			}
		}
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

	// Drop-ins generated on another machine embed that machine's docroot, and
	// the failure is a blank 200 with nothing in any log — the one shape the
	// user cannot debug from the browser. WP Rocket's is regenerable through
	// the plugin; the rest are named so the user knows which plugin to poke.
	for _, site := range store.Sites() {
		for _, d := range staleDropins(site.WPDir) {
			f := Finding{Check: "dropin:" + site.Slug, Status: "warn",
				Detail: d.File + " points at " + d.Paths + " (origin server), not this docroot"}
			if d.File == "wp-content/advanced-cache.php" {
				f.FixCmd, f.AutoFix = "agent-local doctor --fix", true
			} else {
				f.FixHint = "deactivate and reactivate the plugin that wrote it"
			}
			add(f)
		}
	}

	// A wp-config that pins WP_HOME to another machine's address — the
	// production config that came in with the files — beats the database:
	// WordPress 301s every request there, and the local URL only looks broken.
	// The database can hold the same mistake when an import's search-replace
	// stopped short. Both are what an import rewrites, so --fix finishes the
	// job. The database is asked once for every site, and only while it is up.
	{
		var stored map[string][]string
		if e.DBRunning() {
			stored = e.storedHostsAll(store.Sites())
		}
		for _, site := range store.Sites() {
			var dbHosts []string
			for _, h := range stored[site.Slug] {
				if !site.ownsHost(h) {
					dbHosts = append(dbHosts, h)
				}
			}
			if detail := urlPinDetail(foreignURLPins(site), dbHosts); detail != "" {
				add(Finding{Check: "url:" + site.Slug, Status: "warn", Detail: detail,
					FixCmd: "agent-local doctor --fix", AutoFix: true})
			}
		}
	}

	// A plugin that cannot run on the site's PHP dies with a fatal on every
	// request; with display off that is a white page. The pool log has the
	// answer, so surface it — count and the last message — instead of leaving
	// it to be found by hand.
	for _, site := range store.Sites() {
		if n, last := recentFatals(e.fpmLog(site.Slug), 400, 24*time.Hour); n > 0 {
			add(Finding{Check: "fatal:" + site.Slug, Status: "warn",
				Detail:  fmt.Sprintf("%d PHP fatal(s) in the recent pool log: %s", n, last),
				FixHint: "agent-local logs fpm-" + site.Slug + " · a plugin/PHP mismatch is the usual cause; try agent-local php " + site.Slug + " 8.3, or update the plugin"})
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

// hostsHasIP reports whether /etc/hosts maps a domain at a specific address. The
// IPv6 line is what keeps a .local name off the mDNS path, so its presence is a
// separate question from whether the domain resolves at all.
func hostsHasIP(domain, ip string) bool {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return false
	}
	return hostLineHasIP(string(b), domain, ip)
}

func hostsHas(domain string) bool {
	b, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
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

// brokenPHPFindings reports kegs that are installed but will not run. One of
// those is worse than an absent version: every site pointed at it fails to
// serve, and the version itself reads as missing.
func brokenPHPFindings(inv *Inventory) []Finding {
	out := make([]Finding, 0, len(inv.BrokenPHPs))
	for _, rt := range inv.BrokenPHPs {
		out = append(out, Finding{Check: "php:" + rt.Version, Status: "fail",
			Detail:  fmt.Sprintf("%s is installed at %s but will not run: %s", rt.Version, rt.Bin, rt.Broken),
			FixHint: "reinstalls the dependency Homebrew removed",
			FixCmd:  AppName + " install php " + rt.Version, AutoFix: true})
	}
	return out
}

// DoctorFix applies auto-fixable findings. Returns what it did, alongside
// any per-check errors so a caller can tell a fix that silently did nothing
// (nothing to fix) from one that tried and failed — a fix that ran and
// failed is reported as "failed: <check>: <error>" in the same slice, since
// changing this signature would ripple into every caller across the CLI,
// TUI and API for no functional gain.
func DoctorFix(store *Store, interactive bool) []string {
	var done []string
	rep := Doctor(store)
	for _, f := range rep.Findings {
		if !f.AutoFix || f.Status == "ok" {
			continue
		}
		switch {
		case f.Check == "dns-orphans":
			orphans := orphanHostsEntries(store)
			if err := RemoveHosts(interactive, orphans); err != nil {
				done = append(done, fmt.Sprintf("failed: dns-orphans: %v", err))
			} else {
				done = append(done, fmt.Sprintf("removed %d orphaned /etc/hosts name(s): %s", len(orphans), strings.Join(orphans, ", ")))
			}
		case strings.HasPrefix(f.Check, "dns"):
			// AllDomains, not just missing: EnsureHosts also comments leftover
			// LocalWP ::1 / 127.0.0.1 lines that shadow our alias.
			if n, err := EnsureHosts(interactive, store.AllDomains()); err != nil {
				done = append(done, fmt.Sprintf("failed: dns: %v", err))
			} else if n > 0 {
				done = append(done, fmt.Sprintf("updated %d /etc/hosts line(s)", n))
			}
		case f.Check == "tls":
			for _, d := range store.AllDomains() {
				cert, _, created, err := EnsureCert(d)
				if err != nil {
					done = append(done, fmt.Sprintf("failed: tls %s: %v", d, err))
					continue
				}
				if created {
					if terr := TrustCert(cert, interactive); terr != nil {
						done = append(done, fmt.Sprintf("failed: trust cert %s: %v", d, terr))
					}
					done = append(done, "issued cert for "+d)
				}
			}
		case strings.HasPrefix(f.Check, "dropin:"):
			slug := strings.TrimPrefix(f.Check, "dropin:")
			site := store.Site(slug)
			if site == nil {
				continue
			}
			fixed, left, err := NewEngine(store).RegenerateDropins(site)
			for _, name := range fixed {
				done = append(done, "regenerated "+name+" for "+slug)
			}
			if err != nil {
				done = append(done, fmt.Sprintf("failed: dropin %s: %v", slug, err))
			}
			for _, d := range left {
				done = append(done, fmt.Sprintf("left %s for %s: regenerate it from its plugin", d.File, slug))
			}
		case strings.HasPrefix(f.Check, "url:"):
			slug := strings.TrimPrefix(f.Check, "url:")
			site := store.Site(slug)
			if site == nil {
				continue
			}
			e := NewEngine(store)
			hosts := e.foreignHosts(site)
			if err := e.rewriteImportedURLs(site, hosts, func(string, string) {}); err != nil {
				done = append(done, fmt.Sprintf("failed: url %s: %v", slug, err))
				continue
			}
			names := make([]string, 0, len(hosts))
			for h := range hosts {
				names = append(names, h)
			}
			sort.Strings(names)
			done = append(done, fmt.Sprintf("repointed %s → %s for %s", strings.Join(names, ", "), site.Domain, slug))
		case strings.HasPrefix(f.Check, "php:"):
			v := strings.TrimPrefix(f.Check, "php:")
			if err := RepairPHP(store, v, nil); err != nil {
				done = append(done, fmt.Sprintf("failed: php %s: %v", v, err))
			} else {
				_ = store.Save()
				done = append(done, "repaired php "+v)
			}
		case f.Check == "http":
			if err := EnsureHTTPFront(store); err != nil {
				done = append(done, fmt.Sprintf("failed: http: %v", err))
			} else {
				done = append(done, "started http front")
			}
		case f.Check == "bare-urls":
			if err := EnsureLoopAlias(interactive); err != nil {
				done = append(done, fmt.Sprintf("failed: bare-urls: %v", err))
				continue
			}
			n, err := EnsureHosts(interactive, store.AllDomains())
			if err != nil {
				done = append(done, fmt.Sprintf("failed: bare-urls hosts: %v", err))
				continue
			}
			// daemon restart binds the alias :80/:443 listeners
			StopDaemons()
			if err := EnsureHTTPFront(store); err != nil {
				done = append(done, fmt.Sprintf("failed: bare-urls http front: %v", err))
				continue
			}
			done = append(done, fmt.Sprintf("bare URLs enabled (%d hosts lines on %s)", n, LoopbackAlias))
		}
	}
	return done
}

// urlPinDetail words the url: finding from its two causes. "" when there is
// nothing to say. Pins are named by constant so the user can see the line in
// their own wp-config; the database is one host per line of options.
func urlPinDetail(pins []URLPin, dbHosts []string) string {
	var parts []string
	if len(pins) > 0 {
		byURL := map[string][]string{}
		var order []string
		for _, p := range pins {
			if _, seen := byURL[p.URL]; !seen {
				order = append(order, p.URL)
			}
			byURL[p.URL] = append(byURL[p.URL], p.Name)
		}
		for _, u := range order {
			parts = append(parts, "wp-config pins "+strings.Join(byURL[u], ", ")+" to "+u)
		}
	}
	if len(dbHosts) > 0 {
		parts = append(parts, "database home is "+strings.Join(dbHosts, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + " — every request redirects there"
}

// offSite reports whether a redirect target leaves the site's own names. A
// relative Location, or one to the site itself (http → https, trailing slash),
// is WordPress doing its job; another host is the production URL leaking in.
func offSite(site *Site, location string) bool {
	h := hostFromURL(location)
	if h == "" || !strings.Contains(location, "://") {
		return false
	}
	return !site.ownsHost(h)
}

// RenderReport prints the report human-readable, in the CLI's register: one
// lamp per check, the check name in a dim column, the fix as a key.
func (r *DoctorReport) RenderReport() string {
	var b strings.Builder
	for _, f := range r.Findings {
		lampGlyph := stOK.Render("●")
		switch f.Status {
		case "warn":
			lampGlyph = stWarn.Render("●")
		case "fail":
			lampGlyph = stErr.Render("●")
		}
		b.WriteString("  " + lampGlyph + " " + stDim.Render(col(f.Check, 11)) + f.Detail + "\n")
		if f.Status != "ok" && f.FixCmd != "" {
			b.WriteString("    " + stDim.Render("fix") + "  " + stKey.Render(f.FixCmd) + "\n")
		}
	}
	return b.String()
}
