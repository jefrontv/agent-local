package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

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
	case "connect":
		err = cmdConnect(rest)
	case "front":
		err = cmdFront(rest)
	case "import":
		err = cmdImport(rest)
	case "localwp-sites":
		err = cmdLocalWPSites()
	case "ddev-projects":
		err = cmdDDEVProjects()
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
	case "wpdebug":
		err = cmdWPDebug(rest)
	case "mail":
		err = cmdMail(rest)
	case "sendmail":
		err = runSendmail(rest)
	case "share":
		err = cmdShare(rest)
	case "autostart":
		err = cmdAutostart(rest)
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
	case "jobs":
		err = cmdJobs()
	case "job":
		err = cmdJob(rest)
	case "logs":
		err = cmdLogs(rest)
	case "probe":
		err = cmdProbe(rest)
	case "errors":
		err = cmdErrors(rest)
	case "checkpoint":
		err = cmdCheckpoint(rest)
	case "rollback":
		err = cmdRollback(rest)
	case "login":
		err = cmdLogin(rest)
	case "wpinfo":
		err = cmdWPInfo(rest)
	case "wpconst":
		err = cmdWPConst(rest)
	case "version", "--version":
		outTitle(AppName, Version)
		if buildCommit != "" {
			outRow("commit", buildCommit)
			outRow("built", buildDate)
		}
		// From the daemon's daily check - never a network call from here.
		if latest, _ := availableUpdate(); latest != "" {
			outRow("latest", stName.Render(strings.TrimPrefix(latest, "v"))+"  "+stDim.Render("install: ")+stKey.Render(AppName+" update"))
		}
	case "update", "upgrade":
		err = cmdUpdate(rest)
	case "autoupdate":
		err = cmdAutoupdate(rest)
	case "restart-daemon":
		err = cmdRestartDaemon()
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintln(os.Stderr, stErr.Render("unknown command: "+cmd))
		printHelp()
		os.Exit(2)
	}
	if err != nil {
		outFail(err.Error())
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

// cmdAutostart installs or removes the login agent that brings the stack back
// after a reboot.
func cmdAutostart(args []string) error {
	if hasFlag(args, "--off") {
		RemoveDaemonAutostart()
		outTitle(AppName, "autostart")
		outRow("state", "off")
		outNote("the daemon runs only while something asks for it")
		return nil
	}
	if err := EnsureDaemonAutostart(); err != nil {
		return err
	}
	outTitle(AppName, "autostart")
	outRow("state", stOK.Render("●")+" on")
	outRow("agent", shortHome(daemonAgentPath()))
	outNote("sites that were running at shutdown come back with it")
	return nil
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
		outTitle(AppName, "media", slug)
		switch eff := EffectiveMediaFallback(site); {
		case eff == "" && site.MediaOff:
			outRow("fallback", dimf("off by request; missing uploads 404"))
			if hint := e.MediaFallbackHint(slug); hint != "" {
				outRow("htaccess", hint)
				outHint("honour", AppName+" media "+slug+" --auto")
			}
		case eff == "":
			outRow("fallback", dimf("none; missing uploads 404"))
			outNote("no uploads rewrite in this site's .htaccess; pass a URL to set one")
		case site.MediaFallback != "":
			outRow("fallback", eff+"  "+dimf("set here"))
		default:
			outRow("fallback", eff+"  "+dimf("from this site's .htaccess"))
		}
		return nil
	}
	got, err := e.SetMediaFallback(slug, set)
	if err != nil {
		return err
	}
	outTitle(AppName, "media", slug)
	if got == "" {
		outRow("fallback", dimf("off; missing uploads 404"))
		return nil
	}
	outRow("fallback", got)
	outNote("missing uploads on " + slug + " now redirect there")
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
		outTitle(AppName, "sites-dir")
		outRow("dir", store.SitesDir())
		outNote("new sites are created here; existing sites stay where they are")
		return nil
	}
	if len(pos) == 0 {
		outTitle(AppName, "sites-dir")
		outRow("dir", store.SitesDir())
		outHint("change", AppName+" sites-dir ~/Sites")
		outHint("reset", AppName+" sites-dir --default")
		return nil
	}
	if err := store.SetSitesDir(pos[0]); err != nil {
		return err
	}
	outTitle(AppName, "sites-dir")
	outRow("dir", store.SitesDir())
	outNote("new sites are created here; existing sites stay where they are")
	return nil
}

func cmdSuffix(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	pos := positional(args)
	if len(pos) == 0 {
		outTitle(AppName, "suffix")
		outRow("suffix", store.Suffix())
		outHint("change", AppName+" suffix .test")
		return nil
	}
	if err := store.SetSuffix(pos[0]); err != nil {
		return err
	}
	outTitle(AppName, "suffix")
	outRow("suffix", store.Suffix())
	outNote("new sites and previews use it; existing sites keep their domains")
	outHint("rename", AppName+" domain SLUG name"+store.Suffix())
	return nil
}

// cmdAutoupdate shows or sets whether the daemon installs releases itself.
func cmdAutoupdate(args []string) error {
	store, err := OpenStore()
	if err != nil {
		return err
	}
	pos := positional(args)
	state := func() string {
		if store.Data.AutoUpdate {
			return stOK.Render("on") + stDim.Render("  the daemon installs each release it finds")
		}
		return stDim.Render("off  releases are reported, never installed unasked")
	}
	outTitle(AppName, "autoupdate")
	if len(pos) == 0 {
		outRow("state", state())
		if latest, _ := availableUpdate(); latest != "" {
			outRow("available", stName.Render(strings.TrimPrefix(latest, "v"))+stDim.Render("  running "+Version))
		}
		outHint("change", AppName+" autoupdate on|off")
		return nil
	}
	switch pos[0] {
	case "on":
		store.Data.AutoUpdate = true
	case "off":
		store.Data.AutoUpdate = false
	default:
		return fmt.Errorf("usage: %s autoupdate [on|off]", AppName)
	}
	if err := store.Save(); err != nil {
		return err
	}
	outRow("state", state())
	if store.Data.AutoUpdate {
		self, _ := os.Executable()
		if managedByHomebrew(self) {
			outNote("this binary is Homebrew's, so the daemon can only report a release; install with: brew upgrade " + AppName)
		} else {
			outNote("the daemon checks GitHub once a day and installs what it finds once no job is running")
			if n := len(sitesInProtectedFolders(store)); n > 0 {
				outNote(fmt.Sprintf("%d site(s) live in ~/Documents, ~/Desktop or ~/Downloads: macOS will ask once per new build before the daemon can read them; static files 403 until you allow it", n))
			}
			outHint("now", AppName+" update")
		}
	}
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
	outTitle(AppName, "create", pos[0])
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
		Progress:   outStage,
	})
	if err != nil {
		return err
	}
	outBlank()
	outRow("url", BareURL(site))
	outRow("admin", BareURL(site)+"/wp-admin  "+dimf(site.AdminUser+" / "+site.AdminPass))
	outRow("php", site.PHPVersion)
	outRow("db", site.DBName)
	outHint("next", AppName+" open "+site.Slug)
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
	outTitle(AppName, "attach", pos[0])
	site, err := e.AttachSite(AttachOpts{
		Dir:      pos[0],
		Name:     flagValue(args, "--name"),
		Domain:   flagValue(args, "--domain"),
		PHPVer:   flagValue(args, "--php"),
		Progress: outStage,
	})
	if err != nil {
		return err
	}
	outBlank()
	outRow("url", BareURL(site))
	outRow("docroot", site.WPDir)
	outRow("php", site.PHPVersion)
	outRow("db", fmt.Sprintf("%s  %s", site.DBName, dimf(fmt.Sprintf("%s / %s @ 127.0.0.1:%d", site.DBUser, site.DBPass, DefaultDBPort))))
	outHint("next", AppName+" open "+site.Slug)
	return nil
}

func cmdList() error {
	store, e, err := openEnv()
	if err != nil {
		return err
	}
	sites := store.Sites()
	if len(sites) == 0 {
		outTitle(AppName, "list")
		outNote("no sites yet")
		outHint("create", AppName+" create mysite")
		return nil
	}
	alive := e.AliveAll()
	var rows [][]string
	for _, s := range sites {
		rows = append(rows, []string{s.Slug, s.PHPVersion, outState(alive[s.Slug]), BareURL(s)})
		for _, w := range store.WorktreesFor(s.Slug) {
			rows = append(rows, []string{dimf("  └ ") + w.Branch, dimf(s.PHPVersion), outState(alive[w.ID]), BareDomainURL(w.Domain)})
		}
	}
	outTable([]string{"site", "php", "state", "url"}, rows)
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
	outTitle(AppName, "start", slug)
	outRow("state", outState(true))
	outRow("url", BareURL(site))
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
	if err := e.StopSite(slug); err != nil {
		return err
	}
	outTitle(AppName, "stop", slug)
	outRow("state", outState(false))
	return nil
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
	if err := e.StartSite(slug); err != nil {
		return err
	}
	outTitle(AppName, "restart", slug)
	outRow("state", outState(true))
	outRow("url", BareURL(e.Store.Site(slug)))
	return nil
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
	outTitle(AppName, "delete", slug)
	if !hasFlag(args, "--yes") {
		fmt.Print(stOutLbl.Render("confirm") + "  " + "delete " + slug + dimf(" (database and files; a snapshot is saved first)") + " [y/N] ")
		var ans string
		fmt.Scanln(&ans)
		if strings.ToLower(ans) != "y" {
			return fmt.Errorf("aborted")
		}
	}
	if err := e.DeleteSite(slug, DeleteOpts{
		KeepFiles:        hasFlag(args, "--keep-files"),
		KeepDB:           hasFlag(args, "--keep-db"),
		NoSnapshot:       hasFlag(args, "--no-snapshot"),
		InteractiveHosts: true,
	}); err != nil {
		return err
	}
	outStep("deleted " + slug)
	if !hasFlag(args, "--no-snapshot") && !hasFlag(args, "--keep-db") {
		outNote("its database snapshot is under " + shortHome(P().Root) + "/snapshots/" + slug)
	}
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
	if hasFlag(args, "--db") {
		if _, err := writeAdminerBoot(site); err != nil {
			return err
		}
		url = AdminerURL(site.Domain)
	}
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
				return fmt.Errorf("usage: agent-local db SLUG import FILE.sql[.gz] [--keep-urls] [--no-snapshot]")
			}
			outTitle(AppName, "db", slug, "import")
			msg, err := e.ImportSQL(slug, pos[2], !hasFlag(args, "--keep-urls"), !hasFlag(args, "--no-snapshot"))
			if err != nil {
				return err
			}
			outStep(msg)
			return nil
		case "export":
			out := ""
			if len(pos) > 2 {
				out = pos[2]
			}
			outTitle(AppName, "db", slug, "export")
			msg, err := e.ExportSQL(slug, out)
			if err != nil {
				return err
			}
			outStep(msg)
			return nil
		case "snapshot":
			label := ""
			if len(pos) > 2 {
				label = pos[2]
			}
			outTitle(AppName, "db", slug, "snapshot")
			snap, err := e.SnapshotDB(slug, label)
			if err != nil {
				return err
			}
			outStep("saved " + snap.Name + "  " + dimf(humanBytes(snap.Size)))
			outRow("path", shortHome(snap.Path))
			outHint("restore", AppName+" db "+slug+" restore "+snap.Name)
			return nil
		case "snapshots":
			snaps, err := e.Snapshots(slug)
			if err != nil {
				return err
			}
			outTitle(AppName, "db", slug, "snapshots")
			if len(snaps) == 0 {
				outNote("no snapshots yet; automatic ones are taken before import, reset, restore and delete")
				outHint("take one", AppName+" db "+slug+" snapshot")
				return nil
			}
			var rows [][]string
			for _, s := range snaps {
				rows = append(rows, []string{s.Name, dimf(humanBytes(s.Size)), dimf(s.CreatedAt.Format("2006-01-02 15:04"))})
			}
			outTable([]string{"snapshot", "size", "taken"}, rows)
			outHint("restore", AppName+" db "+slug+" restore [NAME]")
			return nil
		case "restore":
			name := ""
			if len(pos) > 2 {
				name = pos[2]
			}
			outTitle(AppName, "db", slug, "restore")
			msg, err := e.RestoreSnapshot(slug, name, !hasFlag(args, "--no-snapshot"))
			if err != nil {
				return err
			}
			outStep(msg)
			return nil
		case "reset":
			outTitle(AppName, "db", slug, "reset")
			msg, err := e.ResetDBBackup(slug, !hasFlag(args, "--no-snapshot"))
			if err != nil {
				return err
			}
			outStep(msg)
			return nil
		case "tables":
			out, err := e.DB(fmt.Sprintf("SELECT table_name, table_rows, ROUND(data_length/1024) AS kb "+
				"FROM information_schema.tables WHERE table_schema='%s' ORDER BY table_name", site.DBName))
			fmt.Print(out)
			return err
		case "gui", "adminer":
			if !e.FPMRunning(site.Slug) {
				_ = e.StartSite(slug)
			}
			if _, err := writeAdminerBoot(site); err != nil {
				return err
			}
			url := AdminerURL(site.Domain)
			outTitle(AppName, "db", slug, "gui")
			outRow("url", url)
			return exec.Command("open", url).Start()
		case "search":
			if len(pos) < 3 {
				return fmt.Errorf("usage: agent-local db SLUG search NEEDLE")
			}
			outTitle(AppName, "db", slug, "search")
			rep, err := e.DBSearch(site, pos[2])
			if err != nil {
				return err
			}
			printSearchReport(rep)
			return nil
		case "search-replace":
			if len(pos) < 4 {
				return fmt.Errorf("usage: agent-local db SLUG search-replace OLD NEW [--apply]")
			}
			outTitle(AppName, "db", slug, "search-replace")
			rep, err := e.SearchReplace(site, pos[2], pos[3], !hasFlag(args, "--apply"))
			if err != nil {
				return err
			}
			printSearchReport(rep)
			if rep.DryRun {
				outHint("apply", AppName+" db "+slug+" search-replace "+pos[2]+" "+pos[3]+" --apply")
			}
			return nil
		}
		// anything else: raw SQL against this site's schema; the result is
		// data, printed as the server returned it.
		out, err := e.DBIn(site.DBName, strings.Join(pos[1:], " "))
		fmt.Print(out)
		return err
	}
	outTitle(AppName, "db", slug)
	outRow("host", fmt.Sprintf("127.0.0.1:%d", DefaultDBPort))
	outRow("db", site.DBName)
	outRow("user", site.DBUser)
	outRow("pass", site.DBPass)
	outHint("gui", AppName+" db "+slug+" gui")
	return nil
}

// printSearchReport renders a db search / search-replace result: one row per
// table.column hit, then the total.
func printSearchReport(rep *searchReport) {
	for _, h := range rep.Hits {
		fmt.Printf("  %s%s  %d\n", stName.Render(h.Table), stDim.Render("."+h.Column), h.Count)
	}
	what := "occurrence(s)"
	if rep.Replacement != "" {
		what = "replacement(s)"
		if rep.DryRun {
			what += " to be made"
		}
	}
	outStep(fmt.Sprintf("%d %s across %d column(s)", rep.Total, what, len(rep.Hits)))
	if rep.ConfigPinsRewritten {
		outStep("wp-config URL pins repointed")
	}
	if len(rep.Hits) == 0 && rep.RawTail != "" {
		outNote(rep.RawTail)
	}
}

// siteEnv is openEnv plus the site named by the first positional argument.
func siteEnv(args []string) (*Store, *Engine, *Site, error) {
	slug, err := slugArg(args)
	if err != nil {
		return nil, nil, nil, err
	}
	store, e, err := openEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	site := store.Site(slug)
	if site == nil {
		return nil, nil, nil, fmt.Errorf("no such site: %s", slug)
	}
	return store, e, site, nil
}

// cmdProbe is probe_site: the standard set of requests and a verdict.
func cmdProbe(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	pos := positional(args)
	outTitle(AppName, "probe", site.Slug)
	if len(pos) > 1 {
		// One URL, in full.
		r := e.requestSite(site, "GET", pos[1], hasFlag(args, "--follow"), 4096)
		printRequestReport(pos[1], r)
		return nil
	}
	rep := e.probeSite(site)
	for _, p := range probePaths {
		printRequestReport(p, rep.Requests[p])
	}
	fmt.Println()
	if rep.Verdict == "healthy" {
		outStep("healthy")
	} else {
		outWarn(rep.Verdict + ": " + rep.Reason)
	}
	return nil
}

// printRequestReport is one probed path on one line, lamp coloured by what
// came back: a path is too long for the label gutter and reads better as the
// subject of its own line.
func printRequestReport(path string, r requestReport) {
	lamp := stOK.Render("●")
	switch {
	case r.Error != "" || r.Status >= 500:
		lamp = stErr.Render("●")
	case r.Status >= 400:
		lamp = stWarn.Render("●")
	case r.Status >= 300:
		lamp = stDim.Render("●")
	}
	if r.Error != "" {
		fmt.Println("  " + lamp + " " + path + "  " + stErr.Render(r.Error))
		return
	}
	line := fmt.Sprintf("  %s %s  %d  %dms  %s", lamp, path, r.Status, r.Ms, humanBytes(r.BodyBytes))
	if r.Location != "" {
		line += "  → " + r.Location
	}
	if r.Title != "" {
		line += "  " + dimf(r.Title)
	}
	fmt.Println(line)
	for _, e := range r.PHPErrors {
		outSub(e)
	}
}

// cmdErrors is get_errors: the site's PHP errors, deduplicated.
func cmdErrors(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	since, err := parseSince(flagValue(args, "--since"))
	if err != nil {
		return err
	}
	outTitle(AppName, "errors", site.Slug)
	entries, scanned := e.SiteErrors(site, since, 50)
	if len(entries) == 0 {
		outStep(fmt.Sprintf("no PHP errors in the last %s (%d lines read)", since, scanned))
		return nil
	}
	for _, en := range entries {
		where := en.File
		if en.Line > 0 {
			where += fmt.Sprintf(":%d", en.Line)
		}
		outRow(fmt.Sprintf("%dx", en.Count), en.Level+"  "+en.Message)
		if where != "" {
			outSub(where + "  " + dimf("last "+en.Last.Format("15:04:05")))
		}
	}
	return nil
}

// cmdCheckpoint saves or lists whole-site restore points.
func cmdCheckpoint(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	pos := positional(args)
	if hasFlag(args, "--list") {
		list, err := e.ListCheckpoints(site.Slug)
		if err != nil {
			return err
		}
		outTitle(AppName, "checkpoint", site.Slug, "list")
		if len(list) == 0 {
			outNote("none yet")
		}
		for _, c := range list {
			db := "no db snapshot"
			if c.DBSnapshot != "" {
				db = "db + files"
			}
			outRow(c.CreatedAt.Format("01-02 15:04"), c.Name+"  "+dimf(c.Scope+", "+db))
		}
		return nil
	}
	label, scope := "", flagValue(args, "--scope")
	if len(pos) > 1 {
		label = pos[1]
	}
	outTitle(AppName, "checkpoint", site.Slug)
	info, err := e.Checkpoint(site.Slug, label, scope)
	if err != nil {
		return err
	}
	outStep("saved " + info.Name + "  " + dimf(info.Scope+" ("+info.SizeHint+")"))
	if info.DBSnapshot != "" {
		outStep("database snapshot " + info.DBSnapshot)
	} else if info.Warning != "" {
		outWarn(info.Warning)
	}
	outHint("undo", AppName+" rollback "+site.Slug+" "+info.Name)
	return nil
}

// cmdRollback puts a site back to a checkpoint.
func cmdRollback(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	pos := positional(args)
	if len(pos) < 2 {
		return fmt.Errorf("usage: agent-local rollback SLUG CHECKPOINT   (agent-local checkpoint SLUG --list)")
	}
	outTitle(AppName, "rollback", site.Slug, pos[1])
	rep, err := e.Rollback(site.Slug, pos[1])
	if err != nil {
		return err
	}
	if rep.DBRestored {
		outStep("database restored  " + dimf(rep.DBMessage))
	}
	if rep.FilesRestored {
		outStep("files restored")
		if rep.PreviousFiles != "" {
			outNote("what was there is kept at " + shortHome(rep.PreviousFiles))
		}
	}
	if rep.Restarted {
		outStep("pool restarted")
	}
	return nil
}

// cmdLogin mints a one-time admin login URL.
func cmdLogin(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	pos := positional(args)
	user := ""
	if len(pos) > 1 {
		user = pos[1]
	}
	link, err := e.MagicLogin(site, user)
	if err != nil {
		return err
	}
	outTitle(AppName, "login", site.Slug)
	outRow("user", link.User)
	outRow("url", link.URL)
	outNote("one use, valid until " + link.Expires.Format("15:04:05"))
	if hasFlag(args, "--open") {
		return exec.Command("open", link.URL).Start()
	}
	return nil
}

// cmdWPInfo prints wp_info as JSON: it is a document, not a few rows.
func cmdWPInfo(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	info, err := e.WPInfo(site)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))
	return nil
}

// cmdWPConst lists, sets or removes wp-config constants.
func cmdWPConst(args []string) error {
	_, e, site, err := siteEnv(args)
	if err != nil {
		return err
	}
	pos := positional(args)
	switch {
	case len(pos) == 1:
		list, err := e.WPConstants(site)
		if err != nil {
			return err
		}
		outTitle(AppName, "wpconst", site.Slug)
		for _, c := range list {
			printWPConstant(c)
		}
		outHint("set", AppName+" wpconst "+site.Slug+" NAME VALUE [--string|--raw]  · --remove")
		return nil
	case hasFlag(args, "--remove"):
		removed, err := e.RemoveWPConstant(site, pos[1])
		if err != nil {
			return err
		}
		outTitle(AppName, "wpconst", site.Slug)
		if removed {
			outStep("removed " + pos[1])
		} else {
			outNote(pos[1] + " was not defined")
		}
		return nil
	case len(pos) >= 3:
		typ := "auto"
		if hasFlag(args, "--string") {
			typ = "string"
		} else if hasFlag(args, "--raw") {
			typ = "raw"
		}
		c, err := e.SetWPConstant(site, pos[1], strings.Join(pos[2:], " "), typ)
		if err != nil {
			return err
		}
		outTitle(AppName, "wpconst", site.Slug)
		printWPConstant(c)
		return nil
	}
	return fmt.Errorf("usage: agent-local wpconst SLUG [NAME VALUE [--string|--raw] | NAME --remove]")
}

// printWPConstant is one `NAME = value` line: constant names run past the
// label gutter, and a define is read as an assignment anyway.
func printWPConstant(c wpConstant) {
	fmt.Println("  " + stName.Render(c.Name) + stDim.Render(" = ") + c.Value)
}

func cmdPHP(args []string) error {
	pos := positional(args)
	if len(pos) < 2 {
		store, _, err := openEnv()
		if err != nil {
			return err
		}
		rescanPHP(store)
		_ = store.Save()
		outTitle(AppName, "php")
		outRow("installed", strings.Join(store.Inventory().Runtimes(), "  "))
		for _, rt := range store.Inventory().BrokenPHPs {
			outWarn(rt.Version + " at " + rt.Bin + " will not run: " + rt.Broken)
		}
		outRow("available", dimf(strings.Join(PHPVersions, "  ")))
		outHint("switch", AppName+" php SLUG VERSION")
		return nil
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	// Install when missing, as the help promises and as the API and the TUI
	// already did; the CLI used to be the one surface that stopped at "not
	// installed". --tap additionally allows the versioned tap for releases
	// Homebrew has dropped (7.4, 8.0).
	outTitle(AppName, "php", pos[0], pos[1])
	if err := e.SwitchPHPEnsure(pos[0], pos[1], true, hasFlag(args, "--tap"), outSub); err != nil {
		return err
	}
	outStep(pos[0] + " is on PHP " + NormalizePHPVersion(pos[1]))
	return nil
}

func cmdImport(args []string) error {
	pos := positional(args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: agent-local import <localwp-name|ddev-project|docroot> [--name n] [--domain d] [--php v] [--copy] [--sql file] [--serve-only] [--keep-ddev] [--db-host h] [--db-port p] [--db-user u] [--db-pass p] [--db-name n]")
	}
	_, e, err := openEnv()
	if err != nil {
		return err
	}
	outTitle(AppName, "import", pos[0])
	site, err := e.ImportSite(ImportOpts{
		Source:    pos[0],
		Name:      flagValue(args, "--name"),
		Domain:    flagValue(args, "--domain"),
		PHPVer:    flagValue(args, "--php"),
		Copy:      hasFlag(args, "--copy"),
		SQLDump:   flagValue(args, "--sql"),
		ServeOnly: hasFlag(args, "--serve-only"),
		KeepDDEV:  hasFlag(args, "--keep-ddev"),
		DBHost:    flagValue(args, "--db-host"),
		DBPort:    atoi0(flagValue(args, "--db-port")),
		DBUser:    flagValue(args, "--db-user"),
		DBPass:    flagValue(args, "--db-pass"),
		DBName:    flagValue(args, "--db-name"),
		Progress:  outStage,
	})
	if err != nil {
		return err
	}
	outBlank()
	outRow("url", BareURL(site))
	outRow("admin", BareURL(site)+"/wp-admin")
	outRow("php", site.PHPVersion)
	outRow("db", site.DBName)
	outHint("next", AppName+" open "+site.Slug)
	return nil
}

func cmdLocalWPSites() error {
	sites, err := ListLocalWPSites()
	if err != nil {
		return err
	}
	outTitle(AppName, "localwp-sites")
	if len(sites) == 0 {
		outNote("no LocalWP sites found")
		return nil
	}
	var rows [][]string
	for _, s := range sites {
		rows = append(rows, []string{s.Name, s.Domain, dimf(shortHome(s.Path))})
	}
	outTable([]string{"name", "domain", "path"}, rows)
	outHint("import", AppName+" import NAME")
	return nil
}

func cmdDDEVProjects() error {
	ps, err := ListDDEVProjects()
	if err != nil {
		return err
	}
	outTitle(AppName, "ddev-projects")
	if len(ps) == 0 {
		outNote("no DDEV projects found")
		return nil
	}
	var rows [][]string
	for _, p := range ps {
		rows = append(rows, []string{p.Name, p.Status, p.Type, dimf(shortHome(p.AppRoot))})
	}
	outTable([]string{"name", "state", "type", "path"}, rows)
	outHint("import", AppName+" import NAME [--keep-ddev]")
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
	if err := e.SetDomain(pos[0], pos[1]); err != nil {
		return err
	}
	outTitle(AppName, "domain", pos[0])
	outRow("url", BareDomainURL(pos[1]))
	outNote("hosts entry and certificate follow the new name")
	return nil
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
	outTitle(AppName, "yield")
	if !AliasActive() {
		outNote("bare URLs are not enabled; nothing holds :80/:443")
		return nil
	}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	if err := os.MkdirAll(P().Run(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(frontYieldPath(), []byte(fmt.Sprint(deadline.Unix())), 0o644); err != nil {
		return err
	}
	outStep(fmt.Sprintf("released :80/:443 for %ds; start the other app now", secs))
	outNote(fmt.Sprintf("bare URLs resume on their own; sites stay reachable on :%d throughout", DefaultHTTPPort))
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
	outTitle(AppName, "resolve", target)
	if wt != nil {
		outRow("site", site.Slug+"  "+dimf("preview of "+wt.Branch))
		outRow("url", BareDomainURL(wt.Domain))
		return nil
	}
	outRow("site", site.Slug+"  "+dimf("matched "+matched))
	outRow("url", BareURL(site))
	outRow("php", site.PHPVersion)
	outRow("db", site.DBName)
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
	outTitle(AppName, "cert", domain)
	state := stWarn.Render("●") + " missing"
	switch {
	case st.Exists && st.Trusted:
		state = stOK.Render("●") + " trusted"
	case st.Exists:
		state = stWarn.Render("●") + " issued, not trusted"
	}
	outRow("state", state)
	if st.Exists {
		outRow("expires", st.NotAfter)
		outRow("path", shortHome(st.CertPath))
	}
	if !st.Trusted {
		outHint("trust", AppName+" cert "+domain+" --trust")
	}
	return nil
}

// cmdJobs lists recent long-running jobs as a table; cmdJob shows one with
// its steps. Machine-readable JSON is what the API is for.
func cmdJobs() error {
	EnsureRouterDaemonQuiet()
	data, isErr := apiGet("/jobs")
	if isErr {
		return fmt.Errorf("%v", data)
	}
	var jobs []JobView
	b, _ := json.Marshal(data)
	_ = json.Unmarshal(b, &jobs)
	outTitle(AppName, "jobs")
	if len(jobs) == 0 {
		outNote("no jobs yet; create, import and db import run as jobs")
		return nil
	}
	var rows [][]string
	for _, j := range jobs {
		rows = append(rows, []string{j.ID, j.Op, jobStateCell(string(j.Status)), dimf(j.StartedAt.Local().Format("15:04:05"))})
	}
	outTable([]string{"id", "op", "status", "started"}, rows)
	outHint("detail", AppName+" job ID")
	return nil
}

func jobStateCell(status string) string {
	switch status {
	case "running":
		return stWarn.Render("●") + " running"
	case "failed", "error":
		return stErr.Render("●") + " " + status
	case "done", "succeeded", "ok":
		return stOK.Render("●") + " " + status
	}
	return stGutter.Render("●") + " " + status
}

func cmdJob(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("usage: agent-local job ID")
	}
	EnsureRouterDaemonQuiet()
	data, isErr := apiGet("/jobs/" + pos[0])
	if isErr {
		return fmt.Errorf("%v", data)
	}
	var j JobView
	b, _ := json.Marshal(data)
	_ = json.Unmarshal(b, &j)
	outTitle(AppName, "job", j.ID)
	outRow("op", j.Op)
	outRow("status", jobStateCell(string(j.Status)))
	outRow("started", j.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if j.FinishedAt != nil {
		outRow("took", j.FinishedAt.Sub(j.StartedAt).Round(time.Millisecond*100).String())
	}
	for _, s := range j.Steps {
		outStage(s.Stage, s.Detail)
	}
	if j.Error != "" {
		outFail(j.Error)
	}
	if j.Result != nil {
		rb, _ := json.MarshalIndent(j.Result, "  ", "  ")
		outRow("result", "")
		fmt.Println("  " + dimf(string(rb)))
	}
	return nil
}

// cmdUpdate checks GitHub for a newer release and installs it. `--check` only
// reports, for a shell prompt or a CI guard. Output is the TUI's register: a
// labelled header, then one lamp per stage.
func cmdUpdate(args []string) error {
	rel, err := LatestRelease()
	if err != nil {
		return err
	}
	row := func(label, value string) {
		fmt.Println(stLabel.Render(label) + "  " + value)
	}
	fmt.Println(stName.Render(AppName + " update"))
	row("running", stVersion.Render(Version))
	if !UpdateAvailable(Version, rel) {
		row("latest", stVersion.Render(rel.TagName)+"  "+stOK.Render("●")+" "+stDim.Render("up to date"))
		return nil
	}
	row("latest", stName.Render(rel.TagName)+"  "+stDim.Render(rel.HTMLURL))
	if hasFlag(args, "--check") {
		fmt.Println()
		fmt.Println(stDim.Render("install it with: ") + stKey.Render(AppName+" update"))
		return nil
	}
	fmt.Println()
	installed, err := SelfUpdate(func(stage string) {
		fmt.Println("  " + stOK.Render("●") + " " + stage)
	})
	if err != nil {
		return err
	}
	// The daemon in memory is still the old binary, and an old daemon can be
	// actively broken against new state (the root password migration is one such
	// change), so hand over now instead of leaving it to the user.
	if portOpen(DefaultAPIPort) {
		if err := cmdRestartDaemon(); err != nil {
			fmt.Println("  " + stWarn.Render("●") + " daemon still on the old build; finish with: " + stKey.Render(AppName+" restart-daemon"))
		}
	}
	fmt.Println()
	fmt.Println(stOK.Render("updated to " + installed))
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
		fmt.Println("  " + stOK.Render("●") + " daemon restarted on " + v)
	} else {
		fmt.Println("  " + stOK.Render("●") + " daemon restarted")
	}
	// The bare-URL front is its own root process on the old binary until it is
	// reloaded; do that here so an update reaches both. Silent through the
	// allowlist; otherwise leave the command, since prompting mid-update is worse.
	if AliasActive() {
		if err := installFrontDaemon(false); err != nil {
			fmt.Println("  " + stWarn.Render("●") + " bare-URL front still on the old build; reload it with: " + stKey.Render(AppName+" alias"))
		} else {
			fmt.Println("  " + stOK.Render("●") + " bare-URL front restarted")
		}
	}
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
		outTitle(AppName, "worktree", pos[0], pos[1])
		outStep("preview removed; the branch is kept")
		return nil
	}
	outTitle(AppName, "worktree", pos[0], pos[1])
	w, err := e.AddWorktree(pos[0], pos[1])
	if err != nil {
		return err
	}
	outRow("url", BareDomainURL(w.Domain))
	outNote("same database as " + pos[0] + "; the branch's files win, everything else comes from the site")
	outHint("remove", AppName+" worktree "+pos[0]+" "+pos[1]+" --remove")
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
	outTitle(AppName, "branches", slug)
	outRow("repo", shortHome(fmt.Sprint(res["repo"])))
	outRow("current", fmt.Sprint(res["current"]))
	outRow("local", strings.Join(res["local"].([]string), "  "))
	if remote := res["remote"].([]string); len(remote) > 0 {
		outRow("remote", dimf(strings.Join(remote, "  ")))
	}
	outHint("preview", AppName+" worktree "+slug+" BRANCH")
	return nil
}

// cmdWPDebug shows or flips a site's WP_DEBUG. On points the log at
// ~/.agent-local/logs/wp-<slug>.log so `agent-local logs wp-<slug>` tails it.
func cmdWPDebug(args []string) error {
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
	if len(pos) > 1 {
		on := false
		switch pos[1] {
		case "on":
			on = true
		case "off":
		default:
			return fmt.Errorf("usage: agent-local wpdebug SLUG [on|off]")
		}
		st, err := e.SetWPDebug(slug, on)
		if err != nil {
			return err
		}
		outTitle(AppName, "wpdebug", slug)
		if st.Enabled {
			outRow("state", stOK.Render("●")+" on")
			outRow("log", shortHome(st.LogPath))
			outHint("tail", AppName+" logs "+st.LogName)
		} else {
			outRow("state", outStateWord(false, "off"))
		}
		return nil
	}
	st := WPDebugStatus(site)
	outTitle(AppName, "wpdebug", slug)
	if !st.Enabled {
		outRow("state", outStateWord(false, "off"))
		outHint("enable", AppName+" wpdebug "+slug+" on")
		return nil
	}
	outRow("state", stOK.Render("●")+" on")
	outRow("log", shortHome(st.LogPath))
	if st.LogName != "" {
		outHint("tail", AppName+" logs "+st.LogName)
	}
	return nil
}

// cmdMail lists or shows a site's captured emails. Capture is automatic —
// every pool routes PHP mail() back into `agent-local sendmail` — so this
// only ever reads.
func cmdMail(args []string) error {
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
	pos := positional(args)
	switch {
	case hasFlag(args, "--clear"):
		n, err := ClearMail(slug)
		if err != nil {
			return err
		}
		outTitle(AppName, "mail", slug)
		outStep(fmt.Sprintf("cleared %d message(s)", n))
		return nil
	case hasFlag(args, "--open"):
		url := MailURL(site.Domain)
		outTitle(AppName, "mail", slug)
		outRow("url", url)
		return exec.Command("open", url).Start()
	case len(pos) > 1:
		msg, err := GetMail(slug, pos[1])
		if err != nil {
			return err
		}
		mailLinks(site.Domain, msg)
		outTitle(AppName, "mail", slug, msg.ID)
		outRow("subject", msg.Subject)
		outRow("from", msg.From)
		outRow("to", msg.To)
		outRow("date", msg.Date.Format("2006-01-02 15:04:05")+"  "+dimf(mailAge(msg.Date)))
		for _, a := range msg.Attachments {
			outRow("attached", a.Filename+"  "+dimf(a.MIMEType+", "+humanBytes(int64(a.Size))))
		}
		outRow("view", msg.URL)
		switch {
		case msg.Text != "":
			outBlank()
			fmt.Println(msg.Text)
		case msg.HTML != "":
			outBlank()
			outNote("html only; open it in the inbox to read it as the recipient would")
		}
		return nil
	}
	sums, err := ListMail(slug)
	if err != nil {
		return err
	}
	outTitle(AppName, "mail", slug)
	if len(sums) == 0 {
		outNote("nothing captured yet; every email this site sends lands here")
		outRow("inbox", MailURL(site.Domain))
		return nil
	}
	var rows [][]string
	for _, s := range sums {
		subject := s.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		rows = append(rows, []string{dimf(mailAge(s.Date)), subject, dimf(s.To), dimf(s.ID)})
	}
	outTable([]string{"when", "subject", "to", "id"}, rows)
	outHint("read", AppName+" mail "+slug+" ID")
	outHint("inbox", MailURL(site.Domain))
	return nil
}

// cmdShare opens, reports or closes a site's public tunnel. Shares are owned
// by the daemon — they must outlive this command — so the CLI is a client
// of the same API agents use. Opening is idempotent: a second call prints
// the URL of the share already running.
func cmdShare(args []string) error {
	slug, err := slugArg(args)
	if err != nil {
		return err
	}
	EnsureRouterDaemonQuiet()
	outTitle(AppName, "share", slug)
	if hasFlag(args, "--off") {
		res, isErr := apiDelete("/sites/" + slug + "/share")
		if isErr {
			return fmt.Errorf("%s", apiErrMsg(res))
		}
		outStep("share closed; the public URL no longer resolves")
		return nil
	}
	body := map[string]interface{}{}
	switch {
	case hasFlag(args, "--forever"):
		body["minutes"] = -1
	case flagValue(args, "--minutes") != "":
		body["minutes"] = atoi0(flagValue(args, "--minutes"))
	}
	res, isErr := apiPost("/sites/"+slug+"/share", body)
	if isErr {
		return fmt.Errorf("%s", apiErrMsg(res))
	}
	sh, _ := res.(map[string]interface{})
	if sh == nil || sh["url"] == nil {
		fmt.Println(res)
		return nil
	}
	outRow("url", stOK.Render(fmt.Sprint(sh["url"])))
	outNote("verified end to end before being handed over; anyone with the link can open the site")
	if exp, ok := sh["expires_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, exp); err == nil {
			outRow("expires", t.Format("15:04")+"  "+dimf("in "+time.Until(t).Round(time.Minute).String()))
			outHint("stop", AppName+" share "+slug+" --off")
			return nil
		}
	}
	outRow("expires", dimf("when stopped"))
	outHint("stop", AppName+" share "+slug+" --off")
	return nil
}

// apiErrMsg digs the error string out of an apiDo failure payload.
func apiErrMsg(res interface{}) string {
	switch m := res.(type) {
	case map[string]interface{}:
		if s, ok := m["error"].(string); ok {
			return s
		}
	case map[string]string:
		return m["error"]
	}
	return fmt.Sprint(res)
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
	wts := store.WorktreesFor(slug)
	outTitle(AppName, "worktrees", slug)
	if len(wts) == 0 {
		outNote("no branch previews")
		outHint("create", AppName+" worktree "+slug+" BRANCH")
		return nil
	}
	alive := e.AliveAll()
	var rows [][]string
	for _, w := range wts {
		rows = append(rows, []string{w.Branch, outState(alive[w.ID]), BareDomainURL(w.Domain)})
	}
	outTable([]string{"branch", "state", "url"}, rows)
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
		return fmt.Errorf("usage: agent-local install brew|php VERSION [--tap]|mariadb|apache|wp-cli")
	}
	outTitle(AppName, "install", strings.Join(pos, " "))
	switch pos[0] {
	case "brew", "homebrew":
		err = InstallBrew(outSub)
	case "php":
		v := latestBrewPHP()
		if len(pos) > 1 {
			v = NormalizePHPVersion(pos[1])
		}
		err = InstallPHP(store, v, hasFlag(args, "--tap"), outSub)
	case "mariadb", "mysql":
		err = InstallMySQL(store, outSub)
	case "apache", "httpd":
		err = InstallApache(store, outSub)
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
	outStep(strings.Join(pos, " ") + " installed")
	return nil
}

func cmdDoctor(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	rep := Doctor(store)
	outTitle(AppName, "doctor")
	fmt.Print(rep.RenderReport())
	if hasFlag(args, "--fix") {
		outBlank()
		done := DoctorFix(store, true)
		if len(done) == 0 {
			outNote("nothing here is fixable without you")
		}
		for _, d := range done {
			outStep(d)
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
		outTitle(AppName, "front")
		outRow("front", FrontKind(store))
		outHint("switch", AppName+" front router|apache")
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
	outTitle(AppName, "front", want)
	if same {
		outStep(want + " re-applied: config re-rendered, front restarted")
	} else {
		outStep("front switched to " + want)
	}
	return nil
}

func cmdAlias(args []string) error {
	store, _, err := openEnv()
	if err != nil {
		return err
	}
	outTitle(AppName, "alias")
	if hasFlag(args, "--off") {
		if err := RemoveLoopAlias(true); err != nil {
			outWarn("alias removal: " + err.Error())
		}
		if _, err := EnsureHosts(false, store.AllDomains()); err != nil {
			outWarn("hosts revert: " + err.Error())
		}
		outStep(fmt.Sprintf("bare URLs off; sites are served with the :%d suffix", DefaultHTTPPort))
		return nil
	}
	if err := EnsureLoopAlias(true); err != nil {
		return err
	}
	outStep(LoopbackAlias + " alias on lo0, front daemon under launchd")
	n, err := EnsureHosts(true, store.AllDomains())
	if err != nil {
		return err
	}
	outStep(fmt.Sprintf("%d hosts line(s) point at it", n))
	// restart the daemon so the :80/:443 alias listeners bind
	StopDaemons()
	if err := EnsureHTTPFront(store); err != nil {
		return err
	}
	outStep("router restarted")
	outBlank()
	var rows [][]string
	for _, s := range store.Sites() {
		rows = append(rows, []string{s.Slug, BareURL(s)})
	}
	outTable([]string{"site", "url"}, rows)
	return nil
}

// cmdSudoSetup installs a scoped sudoers drop-in so all future root
// operations (hosts writes, pf/alias setup, cert trust) run via `sudo -n`
// silently — no more osascript dialogs. One authorization installs it.
//
// Cert trust is allowed on one fixed, root-owned staging path only. The
// content reaches that path through `tee` on stdin (the same TOCTOU-free
// route the hosts file uses), so nothing else can swap the bytes in, and the
// trust command names the path exactly — not a wildcard. The earlier
// `add-trusted-cert *` entry let any local process trust any certificate.
func cmdSudoSetup() error {
	user := os.Getenv("USER")
	dst := "/Library/LaunchDaemons/local.agent-local.front.plist"
	content := fmt.Sprintf(`# agent-local: scoped passwordless root. Exact commands only.
%[1]s ALL=(root) NOPASSWD: /usr/bin/tee /etc/hosts
%[1]s ALL=(root) NOPASSWD: /usr/bin/tee /etc/pf.conf
%[1]s ALL=(root) NOPASSWD: /usr/bin/tee %[2]s
%[1]s ALL=(root) NOPASSWD: /usr/sbin/chown root\:wheel %[2]s
%[1]s ALL=(root) NOPASSWD: /bin/chmod 644 %[2]s
%[1]s ALL=(root) NOPASSWD: /bin/launchctl load %[2]s
%[1]s ALL=(root) NOPASSWD: /bin/launchctl unload %[2]s
%[1]s ALL=(root) NOPASSWD: /bin/rm %[2]s
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 alias 127.0.0.2
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 -alias 127.0.0.2
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 inet6 fd00\:a10c\:\:2 prefixlen 128 alias
%[1]s ALL=(root) NOPASSWD: /sbin/ifconfig lo0 inet6 fd00\:a10c\:\:2 -alias
%[1]s ALL=(root) NOPASSWD: /sbin/pfctl -d
%[1]s ALL=(root) NOPASSWD: /usr/bin/tee %[3]s
%[1]s ALL=(root) NOPASSWD: /usr/bin/security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain %[3]s
`, user, dst, trustStagePath)
	tmp := P().Run() + "/sudoers.agent-local"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	// One privileged step, validated on the root-owned copy. Checking the
	// user-writable file and then copying it left a window in which its content
	// could change between the two; here the staged file is root's before
	// visudo reads it. The staging name carries a dot, which sudo ignores, so
	// a file that fails validation never applies and is removed.
	const script = `set -e
stage=/etc/sudoers.d/.agent-local.tmp
cp "$1" "$stage"
chown root:wheel "$stage"
chmod 440 "$stage"
if /usr/sbin/visudo -cf "$stage"; then mv "$stage" /etc/sudoers.d/agent-local; else rm -f "$stage"; exit 1; fi`
	if err := RunPrivileged(true, "sh", "-c", script, "_", tmp); err != nil {
		return fmt.Errorf("sudoers install failed: %w", err)
	}
	outTitle(AppName, "sudo")
	outStep("installed /etc/sudoers.d/agent-local; root operations run without a prompt")
	outNote("exact commands only: hosts, the loopback alias, the front daemon, and trusting our own certs via one fixed path")
	outHint("remove", "sudo rm /etc/sudoers.d/agent-local")
	return nil
}
