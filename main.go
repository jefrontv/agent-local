package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const usage = `agent-local — local WordPress for humans and agents

USAGE
  agent-local                    open TUI
  agent-local tui                open TUI
  agent-local create NAME [opts] create + install a WordPress site
  agent-local list               list sites
  agent-local attach DIR [opts]    serve a directory you already have + an empty DB
  agent-local import SOURCE [opts] import a LocalWP site or docroot
  agent-local localwp-sites    list importable LocalWP sites
  agent-local start SLUG         start site stack
  agent-local stop SLUG          stop site
  agent-local restart SLUG       restart site
  agent-local delete SLUG [--yes] [--keep-files] [--keep-db]   remove a site
  agent-local open SLUG          open site in browser
  agent-local db SLUG [sql]      print DB creds or run SQL
  agent-local logs NAME [lines] tail a log (mysql, apache, daemon, fpm-SLUG…)
  agent-local php SLUG VERSION   switch PHP version
  agent-local yield [secs]       free :80/:443 briefly (let LocalWP start)
  agent-local resolve [PATH]     which site owns a path (default: cwd)
  agent-local cert DOMAIN [--trust]   TLS state for a domain
  agent-local db SLUG [sql|import F|export [F]|reset|tables]   database ops
  agent-local media SLUG [URL]       missing uploads -> origin (--auto reads .htaccess, --off)
  agent-local sites-dir [PATH]       show/set where new sites are created
  agent-local suffix [.test]         show/set default domain suffix
  agent-local domain SLUG NAME   change site domain
  agent-local worktree SLUG BRANCH [--remove]   branch worktree on its own URL
  agent-local worktrees SLUG     list worktrees
  agent-local branches SLUG      git branches of the site repo
  agent-local wp SLUG -- ARGS    run wp-cli (e.g. wp core version)
  agent-local install WHAT       brew|php VERSION|mariadb|apache|wp-cli
  agent-local front [router|apache]   show/switch HTTP front
  agent-local doctor [--fix]     health checks
  agent-local daemon             run daemon in foreground
  agent-local daemon --background run daemon detached
  agent-local mcp                MCP server (stdio, JSON-RPC)
  agent-local api-token          print agent API token
  agent-local update [--check]   install the latest release
  agent-local restart-daemon     reload the daemon after an update
  agent-local version

CREATE OPTIONS
  --domain mysite.test  --php 8.2  --wp-version latest
  --repo <git-url>      --admin-user admin --admin-pass secret
  --admin-email a@b.c   --title "My Site"
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		runTUI()
		return
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "tui":
		if hasFlag(args, "--frame") {
			err = renderFrame(args)
		} else {
			runTUI()
		}
	case "create":
		err = cmdCreate(rest)
	case "attach":
		err = cmdAttach(rest)
	case "list", "ls":
		err = cmdList()
	case "start":
		err = cmdStart(rest)
	case "stop":
		err = cmdStop(rest)
	case "restart":
		err = cmdRestart(rest)
	case "delete", "rm":
		err = cmdDelete(rest)
	case "open":
		err = cmdOpen(rest)
	case "db":
		err = cmdDB(rest)
	case "php":
		err = cmdPHP(rest)
	case "domain":
		err = cmdDomain(rest)
	case "worktree":
		err = cmdWorktree(rest)
	case "worktrees":
		err = cmdWorktrees(rest)
	case "branches":
		err = cmdBranches(rest)
	case "wp":
		err = cmdWP(rest)
	case "install":
		err = cmdInstall(rest)
	case "doctor":
		err = cmdDoctor(rest)
	case "daemon":
		err = RunDaemon(hasFlag(rest, "--background"))
	case "mcp":
		err = runMCP(rest)
	case "front":
		err = cmdFront(rest)
	case "import":
		err = cmdImport(rest)
	case "localwp-sites":
		err = cmdLocalWPSites()
	case "alias":
		err = cmdAlias(rest)
	case "resolve":
		err = cmdResolve(rest)
	case "cert":
		err = cmdCert(rest)
	case "yield":
		err = cmdYield(rest)
	case "sudo":
		err = cmdSudoSetup()
	case "front-daemon":
		err = RunFrontDaemon(rest)
	case "media":
		err = cmdMedia(rest)
	case "sites-dir":
		err = cmdSitesDir(rest)
	case "suffix":
		err = cmdSuffix(rest)
	case "api-token":
		tok, e := APIToken()
		if e == nil {
			fmt.Println(tok)
		}
		err = e
	case "logs":
		err = cmdLogs(rest)
	case "version", "--version":
		fmt.Printf("agent-local %s\n", Version)
		if buildCommit != "" {
			fmt.Printf("  commit %s  built %s\n", buildCommit, buildDate)
		}
	case "update", "upgrade":
		err = cmdUpdate(rest)
	case "restart-daemon":
		err = cmdRestartDaemon()
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintln(os.Stderr, "unknown command: "+cmd)
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func flagValue(args []string, f string) string {
	for i, a := range args {
		if a == f && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				// flags that take values skip their value
				switch a {
				case "--domain", "--php", "--wp-version", "--repo", "--admin-user", "--admin-pass", "--admin-email", "--title", "--name", "--sql", "--db-host", "--db-port", "--db-user", "--db-pass", "--db-name":
					i++
				}
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func atoi0(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// cmdMedia shows or sets where a site's missing uploads come from.
func cmdMedia(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local media SLUG [URL | --auto | --off]")
	}
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	slug := pos[0]
	site := store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	set := ""
	switch {
	case hasFlag(args, "--off"):
		set = ""
	case hasFlag(args, "--auto"):
		set = "auto"
	case len(pos) > 1:
		set = pos[1]
	default:
		// Report, and say what the site's own .htaccess implies.
		switch eff := EffectiveMediaFallback(site); {
		case eff == "" && site.MediaOff:
			fmt.Println("media fallback: off by request — missing uploads 404")
			if hint := e.MediaFallbackHint(slug); hint != "" {
				fmt.Println(".htaccess asks for: " + hint + "   (agent-local media " + slug + " --auto to honour it)")
			}
		case eff == "":
			fmt.Println("media fallback: none — missing uploads 404")
			fmt.Println("no uploads rewrite in this site's .htaccess; pass a URL to set one")
		case site.MediaFallback != "":
			fmt.Println("media fallback: " + eff + "   (set here)")
		default:
			fmt.Println("media fallback: " + eff + "   (honouring this site's .htaccess)")
		}
		return nil
	}
	got, err := e.SetMediaFallback(slug, set)
	if err != nil {
		return err
	}
	if got == "" {
		fmt.Println("media fallback off for " + slug)
		return nil
	}
	fmt.Println("missing uploads on " + slug + " now redirect to " + got)
	return nil
}

// cmdSitesDir shows or sets the parent directory new sites are created in.
func cmdSitesDir(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	pos := positional(args)
	if hasFlag(args, "--default") {
		if err := store.SetSitesDir(""); err != nil {
			return err
		}
		fmt.Println("new sites will be created in " + store.SitesDir())
		return nil
	}
	if len(pos) == 0 {
		fmt.Println("sites directory: " + store.SitesDir())
		fmt.Println("change it: agent-local sites-dir ~/Sites      (new sites only)")
		fmt.Println("reset it:  agent-local sites-dir --default")
		return nil
	}
	dir := pos[0]
	if hasFlag(args, "--default") {
		dir = ""
	}
	if err := store.SetSitesDir(dir); err != nil {
		return err
	}
	fmt.Println("new sites will be created in " + store.SitesDir())
	fmt.Println("existing sites stay where they are")
	return nil
}

func cmdSuffix(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	pos := positional(args)
	if len(pos) == 0 {
		fmt.Println("default domain suffix: " + store.Suffix())
		fmt.Println("change it: agent-local suffix .localhost   (new sites + worktrees)")
		return nil
	}
	if err := store.SetSuffix(pos[0]); err != nil {
		return err
	}
	fmt.Println("default suffix set to " + store.Suffix())
	fmt.Println("existing sites keep their domains; change one with: agent-local domain SLUG new" + store.Suffix())
	return nil
}

// openEnv boots store+engine with fresh inventory discovery.
func openEnv() (*Store, *Engine, error) {
	store, err := OpenStore()
	if err != nil {
		return nil, nil, err
	}
	EnsureInventory(store)
	e := NewEngine(store)
	e.HostsInteractive = true
	return store, e, nil
}

func cmdCreate(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local create NAME [--dir path] [--domain d] [--php v] [--repo url]")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	site, err := e.CreateSite(CreateOpts{
		Name:       pos[0],
		Dir:        flagValue(args, "--dir"),
		Domain:     flagValue(args, "--domain"),
		PHPVersion: flagValue(args, "--php"),
		WPVersion:  flagValue(args, "--wp-version"),
		Repo:       flagValue(args, "--repo"),
		AdminUser:  flagValue(args, "--admin-user"),
		AdminPass:  flagValue(args, "--admin-pass"),
		AdminEmail: flagValue(args, "--admin-email"),
		Title:      flagValue(args, "--title"),
		Progress: func(stage, detail string) {
			fmt.Printf("  [%s] %s\n", stage, detail)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nSite ready: %s\n", BareURL(site))
	fmt.Printf("  https:  %s\n", site.SURL())
	fmt.Printf("  admin:  %s/wp-admin  user=%s pass=%s\n", BareURL(site), site.AdminUser, site.AdminPass)
	fmt.Printf("  php:    %s   db: %s\n", site.PHPVersion, site.DBName)
	return nil
}

// cmdAttach serves a directory the user already has. Unlike import it copies no
// database: the site gets an empty one and their files are left alone.
func cmdAttach(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local attach DIR [--name n] [--domain d] [--php v]")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	site, err := e.AttachSite(AttachOpts{
		Dir:    pos[0],
		Name:   flagValue(args, "--name"),
		Domain: flagValue(args, "--domain"),
		PHPVer: flagValue(args, "--php"),
		Progress: func(stage, detail string) {
			fmt.Printf("  [%s] %s\n", stage, detail)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nAttached: %s\n", BareURL(site))
	fmt.Printf("  docroot: %s\n", site.WPDir)
	fmt.Printf("  db:      %s user=%s pass=%s host=127.0.0.1:%d\n", site.DBName, site.DBUser, site.DBPass, DefaultDBPort)
	fmt.Printf("  php:     %s\n", site.PHPVersion)
	return nil
}

func cmdList() error {
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	sites := store.Sites()
	if len(sites) == 0 {
		fmt.Println("no sites. create one: agent-local create mysite")
		return nil
	}
	fmt.Printf("%-20s %-8s %-24s %-8s %s\n", "SLUG", "PHP", "DOMAIN", "STATE", "URL")
	for _, s := range sites {
		state := "stopped"
		if e.FPMRunning(s.Slug) {
			state = "running"
		}
		fmt.Printf("%-20s %-8s %-24s %-8s %s\n", s.Slug, s.PHPVersion, s.Domain, state, BareURL(s))
		for _, w := range store.WorktreesFor(s.Slug) {
			wstate := "stopped"
			if e.FPMRunning(w.ID) {
				wstate = "running"
			}
			fmt.Printf("  └─ %-16s %-24s %-8s %s\n", w.Branch, w.Domain, wstate, BareDomainURL(w.Domain))
		}
	}
	return nil
}

func slugArg(args []string) (string, error) {
	pos := positional(args)
	if len(pos) < 1 {
		return "", fmt.Errorf("missing SLUG")
	}
	return pos[0], nil
}

func cmdStart(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	if err := e.StartSite(slug); err != nil {
		return err
	}
	site := e.Store.Site(slug)
	fmt.Printf("running: %s\n", BareURL(site))
	return nil
}

func cmdStop(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	return e.StopSite(slug)
}

func cmdRestart(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	_ = e.StopSite(slug)
	return e.StartSite(slug)
}

func cmdDelete(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	if !hasFlag(args, "--yes") {
		fmt.Printf("delete site %q (database + files)? [y/N] ", slug)
		var ans string
		fmt.Scanln(&ans)
		if strings.ToLower(ans) != "y" {
			return fmt.Errorf("aborted")
		}
	}
	if err := e.DeleteSite(slug, DeleteOpts{
		KeepFiles:        hasFlag(args, "--keep-files"),
		KeepDB:           hasFlag(args, "--keep-db"),
		InteractiveHosts: true,
	}); err != nil {
		return err
	}
	fmt.Println("deleted " + slug)
	return nil
}

func cmdOpen(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	site := store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if !e.FPMRunning(site.Slug) {
		_ = e.StartSite(slug)
	}
	url := BareURL(site)
	return exec.Command("open", url).Start()
}

func cmdDB(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	site := store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	pos := positional(args)
	if err := e.EnsureDB(); err != nil {
		return err
	}
	if len(pos) > 1 {
		switch pos[1] {
		case "import":
			if len(pos) < 3 {
				return fmt.Errorf("usage: agent-local db SLUG import FILE.sql[.gz] [--keep-urls]")
			}
			msg, err := e.ImportSQL(slug, pos[2], !hasFlag(args, "--keep-urls"))
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		case "export":
			out := ""
			if len(pos) > 2 {
				out = pos[2]
			}
			msg, err := e.ExportSQL(slug, out)
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		case "reset":
			if err := e.ResetDB(slug); err != nil {
				return err
			}
			fmt.Println("database emptied: " + site.DBName)
			return nil
		case "tables":
			out, err := e.DB(fmt.Sprintf("SELECT table_name, table_rows, ROUND(data_length/1024) AS kb "+
				"FROM information_schema.tables WHERE table_schema='%s' ORDER BY table_name", site.DBName))
			fmt.Print(out)
			return err
		}
		// anything else: raw SQL against this site's schema
		out, err := e.DBIn(site.DBName, strings.Join(pos[1:], " "))
		fmt.Print(out)
		return err
	}
	fmt.Printf("host=127.0.0.1 port=%d db=%s user=%s pass=%s\n",
		DefaultDBPort, site.DBName, site.DBUser, site.DBPass)
	return nil
}

func cmdPHP(args []string) error {
	pos := positional(args)
	if len(pos) < 2 {
		store, _, err := openEnv()
		if err != nil {
			return err
		}
		fmt.Println("installed:", strings.Join(store.Inventory().Runtimes(), " "))
		return fmt.Errorf("usage: agent-local php SLUG VERSION")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	return e.SwitchPHP(pos[0], pos[1])
}

func cmdImport(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local import <localwp-name|docroot> [--name n] [--domain d] [--php v] [--copy] [--sql file] [--serve-only] [--db-host h] [--db-port p] [--db-user u] [--db-pass p] [--db-name n]")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	site, err := e.ImportSite(ImportOpts{
		Source:    pos[0],
		Name:      flagValue(args, "--name"),
		Domain:    flagValue(args, "--domain"),
		PHPVer:    flagValue(args, "--php"),
		Copy:      hasFlag(args, "--copy"),
		SQLDump:   flagValue(args, "--sql"),
		ServeOnly: hasFlag(args, "--serve-only"),
		DBHost:    flagValue(args, "--db-host"),
		DBPort:    atoi0(flagValue(args, "--db-port")),
		DBUser:    flagValue(args, "--db-user"),
		DBPass:    flagValue(args, "--db-pass"),
		DBName:    flagValue(args, "--db-name"),
		Progress: func(stage, detail string) {
			fmt.Printf("  [%s] %s\n", stage, detail)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nImported: %s\n", BareURL(site))
	fmt.Printf("  admin: %s/wp-admin\n", BareURL(site))
	fmt.Printf("  php:   %s   db: %s\n", site.PHPVersion, site.DBName)
	return nil
}

func cmdLocalWPSites() error {
	sites, err := ListLocalWPSites()
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %-26s %s\n", "NAME", "DOMAIN", "PATH")
	for _, s := range sites {
		fmt.Printf("%-24s %-26s %s\n", s.Name, s.Domain, s.Path)
	}
	return nil
}

func cmdDomain(args []string) error {
	pos := positional(args)
	if len(pos) < 2 {
		return fmt.Errorf("usage: agent-local domain SLUG newdomain.test")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	return e.SetDomain(pos[0], pos[1])
}

// cmdYield frees the bare-URL ports for a window so another local-dev app
// (LocalWP) can pass its port pre-check and bind :80/:443 first. Our front
// daemon re-binds its specific address afterwards, which the kernel allows
// even with a wildcard listener present — so both keep serving.
func cmdYield(args []string) error {
	pos := positional(args)
	secs := 45
	if len(pos) > 0 {
		if n, err := strconv.Atoi(pos[0]); err == nil && n > 0 {
			secs = n
		}
	}
	if !AliasActive() {
		fmt.Println("bare URLs are not enabled — nothing holds :80/:443")
		return nil
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	if err := os.MkdirAll(P().Run(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(frontYieldPath(), []byte(fmt.Sprint(deadline.Unix())), 0o644); err != nil {
		return err
	}
	fmt.Printf("released :80/:443 for %ds — start your other app now\n", secs)
	fmt.Println("bare URLs resume automatically; sites stay reachable on :" + fmt.Sprint(DefaultHTTPPort) + " throughout")
	return nil
}

// cmdResolve prints which site owns a path — the CLI face of GET /resolve, for
// shell integrations and for debugging an editor/UI that disagrees with us.
func cmdResolve(args []string) error {
	pos := positional(args)
	target := "."
	if len(pos) > 0 {
		target = pos[0]
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	site, matched, wt, candidates := e.ResolvePath(target)
	if site == nil {
		if len(candidates) > 1 {
			slugs := make([]string, 0, len(candidates))
			for _, s := range candidates {
				slugs = append(slugs, s.Slug)
			}
			return fmt.Errorf("%s contains %d sites: %s — name one of them", target, len(candidates), strings.Join(slugs, ", "))
		}
		return fmt.Errorf("no site manages %s", target)
	}
	if wt != nil {
		fmt.Printf("%s (worktree %s) %s\n", site.Slug, wt.Branch, BareDomainURL(wt.Domain))
		return nil
	}
	fmt.Printf("%s  matched=%s  %s  php=%s  db=%s\n", site.Slug, matched, BareURL(site), site.PHPVersion, site.DBName)
	return nil
}

// cmdCert shows a domain's TLS state, or trusts it with --trust.
func cmdCert(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("usage: agent-local cert DOMAIN [--trust]")
	}
	domain := pos[0]
	if hasFlag(args, "--trust") {
		cert, _, _, err := EnsureCert(domain)
		if err != nil {
			return err
		}
		if err := TrustCert(cert, true); err != nil {
			return err
		}
	}
	st := InspectCert(domain)
	fmt.Printf("%s  exists=%t trusted=%t expires=%s\n  %s\n", st.Domain, st.Exists, st.Trusted, st.NotAfter, st.CertPath)
	return nil
}

// cmdUpdate checks GitHub for a newer release and installs it. `--check` only
// reports, for a shell prompt or a CI guard.
func cmdUpdate(args []string) error {
	rel, err := LatestRelease()
	if err != nil {
		return err
	}
	if !UpdateAvailable(Version, rel) {
		fmt.Printf("up to date (%s)\n", Version)
		return nil
	}
	fmt.Printf("%s available (running %s)\n", rel.TagName, Version)
	if hasFlag(args, "--check") {
		fmt.Println(rel.HTMLURL)
		return nil
	}
	installed, err := SelfUpdate(func(stage string) { fmt.Println("  " + stage) })
	if err != nil {
		return err
	}
	fmt.Println("updated to " + installed)
	// The daemon in memory is still the old binary, and an old daemon can be
	// actively broken against new state (the root password migration is one such
	// change), so hand over now instead of leaving it to the user.
	if portOpen(DefaultAPIPort) {
		if err := cmdRestartDaemon(); err != nil {
			fmt.Println("  finish with: agent-local restart-daemon")
		}
	}
	return nil
}

// cmdRestartDaemon replaces the running daemon with the binary on disk. After
// an update the old process is still serving, so this is how the new build
// takes over without a reboot.
func cmdRestartDaemon() error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	StopDaemons()
	if err := EnsureHTTPFront(store); err != nil {
		return err
	}
	// Report what the daemon says, not our own Version: the process doing the
	// restart is often the older binary that just performed an update, and
	// printing its version claims the wrong thing.
	if v := daemonVersion(); v != "" {
		fmt.Println("daemon restarted on " + v)
		return nil
	}
	fmt.Println("daemon restarted")
	return nil
}

// daemonVersion asks the running daemon what build it is, "" if unreachable.
func daemonVersion() string {
	out, err := apiGet("/status")
	if err {
		return ""
	}
	m, ok := out.(map[string]interface{})
	if !ok {
		return ""
	}
	v, _ := m["version"].(string)
	return v
}

func cmdWorktree(args []string) error {
	pos := positional(args)
	if len(pos) < 2 {
		return fmt.Errorf("usage: agent-local worktree SLUG BRANCH [--remove]")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	if hasFlag(args, "--remove") {
		id := pos[0] + "--" + BranchSlug(pos[1])
		if err := e.RemoveWorktree(id); err != nil {
			return err
		}
		fmt.Println("removed " + id)
		return nil
	}
	w, err := e.AddWorktree(pos[0], pos[1])
	if err != nil {
		return err
	}
	fmt.Println("worktree ready: " + BareDomainURL(w.Domain))
	return nil
}

func cmdBranches(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	res, err := e.Branches(slug)
	if err != nil {
		return err
	}
	fmt.Printf("repo    %s\ncurrent %s\nlocal   %s\nremote  %s\n",
		res["repo"], res["current"],
		strings.Join(res["local"].([]string), " "),
		strings.Join(res["remote"].([]string), " "))
	return nil
}

func cmdLogs(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local logs NAME [lines]   (mysql|apache|daemon|fpm-<slug>|<slug>)")
	}
	name := pos[0]
	path := P().Log(name)
	if !fileExists(path) {
		return fmt.Errorf("no log named %q (%s)", name, path)
	}
	lines := 40
	if len(pos) > 1 {
		fmt.Sscanf(pos[1], "%d", &lines)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	fmt.Println(strings.Join(all, "\n"))
	return nil
}

func cmdWorktrees(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	for _, w := range store.WorktreesFor(slug) {
		state := "stopped"
		if e.FPMRunning(w.ID) {
			state = "running"
		}
		fmt.Printf("%-24s %-8s %s\n", w.Branch, state, BareDomainURL(w.Domain))
	}
	return nil
}

func cmdWP(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	site := store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	// everything after --
	var wpArgs []string
	for i, a := range args {
		if a == "--" {
			wpArgs = args[i+1:]
			break
		}
	}
	if len(wpArgs) == 0 {
		wpArgs = []string{"core", "version"}
	}
	out, err := wpCLI(site, wpArgs...)
	fmt.Print(out)
	return err
}

func cmdInstall(args []string) error {
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local install brew|php VERSION|mariadb|apache|wp-cli")
	}
	cb := func(line string) { fmt.Println("  " + line) }
	switch pos[0] {
	case "brew", "homebrew":
		err = InstallBrew(cb)
	case "php":
		v := "8.3"
		if len(pos) > 1 {
			v = pos[1]
		}
		err = InstallPHP(store, v, cb)
	case "mariadb", "mysql":
		err = InstallMySQL(store, cb)
	case "apache", "httpd":
		err = InstallApache(store, cb)
	case "wp-cli", "wp":
		_, err = EnsureWPCLI()
	default:
		return fmt.Errorf("unknown install target: %s", pos[0])
	}
	if err != nil {
		return err
	}
	DiscoverInventory(store)
	store.Save()
	_ = e
	fmt.Println("installed.")
	return nil
}

func cmdDoctor(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	rep := Doctor(store)
	fmt.Print(rep.RenderReport())
	if hasFlag(args, "--fix") {
		done := DoctorFix(store, true)
		if len(done) == 0 {
			fmt.Println("nothing auto-fixable.")
		}
		for _, d := range done {
			fmt.Println("fixed: " + d)
		}
	}
	return nil
}

func cmdFront(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	pos := positional(args)
	if len(pos) == 0 {
		fmt.Println("http front: " + FrontKind(store))
		fmt.Println("switch: agent-local front router|apache")
		return nil
	}
	want := pos[0]
	if want != "router" && want != "apache" {
		return fmt.Errorf("front must be router or apache")
	}
	if want == "apache" && store.Inventory().HTTP.Bin == "" {
		return fmt.Errorf("apache not installed; run: agent-local install apache")
	}
	// Re-applying the current front is a repair, not a no-op: config is
	// re-rendered and the front restarted.
	same := store.Data.Front == want
	store.Data.Front = want
	if err := store.Save(); err != nil {
		return err
	}
	// Both fronts must let go of the shared ports before the new one binds.
	StopDaemons()
	_ = StopApache()
	if err := EnsureHTTPFront(store); err != nil {
		return err
	}
	if same {
		fmt.Println("front re-applied: " + want + " (config re-rendered, restarted)")
	} else {
		fmt.Println("front switched to " + want)
	}
	return nil
}

func cmdAlias(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	if hasFlag(args, "--off") {
		if err := RemoveLoopAlias(true); err != nil {
			fmt.Println("alias remove:", err)
		}
		if _, err := EnsureHosts(false, store.AllDomains()); err != nil {
			fmt.Println("hosts revert:", err)
		}
		fmt.Println("bare URLs disabled — sites served with the :" + fmt.Sprint(DefaultHTTPPort) + " suffix")
		return nil
	}
	if err := EnsureLoopAlias(true); err != nil {
		return err
	}
	n, err := EnsureHosts(true, store.AllDomains())
	if err != nil {
		return err
	}
	fmt.Printf("alias up (%s); %d hosts line(s) migrated\n", LoopbackAlias, n)
	// restart the daemon so the :80/:443 alias listeners bind
	StopDaemons()
	if err := EnsureHTTPFront(store); err != nil {
		return err
	}
	for _, s := range store.Sites() {
		fmt.Println("  " + BareURL(s))
	}
	return nil
}

// cmdSudoSetup installs a scoped sudoers drop-in so all future root
// operations (hosts writes, pf/alias setup, cert trust) run via `sudo -n`
// silently — no more osascript dialogs. One authorization installs it.
func cmdSudoSetup() error {
	root := Root()
	user := os.Getenv("USER")
	dst := "/Library/LaunchDaemons/local.agent-local.front.plist"
	content := fmt.Sprintf(`# agent-local: scoped passwordless root. Exact commands only.
%[1]s ALL=(root) NOPASSWD: /bin/cp %[2]s/run/hosts.tmp /etc/hosts
%[1]s ALL=(root) NOPASSWD: /bin/cp %[2]s/run/pf.conf.new /etc/pf.conf
%[1]s ALL=(root) NOPASSWD: /bin/cp %[2]s/run/front.plist %[3]s
%[1]s ALL=(root) NOPASSWD: /usr/sbin/chown root\:wheel %[3]s
%[1]s ALL=(root) NOPASSWD: /bin/chmod 644 %[3]s
%[1]s ALL=(root) NOPASSWD: /bin/launchctl load %[3]s
%[1]s ALL=(root) NOPASSWD: /bin/launchctl unload %[3]s
%[1]s ALL=(root) NOPASSWD: /bin/rm %[3]s
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 alias 127.0.0.2
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 -alias 127.0.0.2
%[1]s ALL=(root) NOPASSWD: /usr/bin/security add-trusted-cert *
%[1]s ALL=(root) NOPASSWD: /sbin/pfctl -d
`, user, root, dst)
	tmp := P().Run() + "/sudoers.agent-local"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := RunPrivileged(true, "/usr/sbin/visudo", "-cf", tmp); err != nil {
		return fmt.Errorf("sudoers validation failed: %w", err)
	}
	if err := RunPrivileged(true, "sh", "-c",
		fmt.Sprintf("cp %s /etc/sudoers.d/agent-local && chown root:wheel /etc/sudoers.d/agent-local && chmod 440 /etc/sudoers.d/agent-local", tmp)); err != nil {
		return err
	}
	fmt.Println("installed /etc/sudoers.d/agent-local — root ops are now passwordless")
	fmt.Println("remove it anytime: sudo rm /etc/sudoers.d/agent-local")
	return nil
}
