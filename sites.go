package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CreateOpts configures a new site.
type CreateOpts struct {
	Name       string
	Dir        string // where the site lives; empty → ~/.agent-local/sites/<slug>
	Domain     string // empty → slug.test
	PHPVersion string // empty → highest installed
	WPVersion  string // empty → "latest"
	Repo       string // optional git clone source instead of fresh download
	AdminUser  string // default "admin"
	AdminPass  string // default: random
	AdminEmail string // default admin@<domain>
	Title      string
	Progress   func(stage, detail string)
}

// CreateSite builds a site end-to-end: dirs, db, wordpress, config, install.
func (e *Engine) CreateSite(o CreateOpts) (*Site, error) {
	cb := o.Progress
	if cb == nil {
		cb = func(string, string) {}
	}
	slug, err := SanitizeName(o.Name)
	if err != nil {
		return nil, err
	}
	if e.Store.Site(slug) != nil {
		return nil, fmt.Errorf("site %q already exists", slug)
	}
	// The site root comes from the configured sites directory unless the caller
	// names one. The docroot stays one level in (<root>/wp) either way: branch
	// previews live at <root>/@/<branch> and must not sit inside what is served.
	root := e.Store.SiteDirFor(slug)
	if o.Dir != "" {
		abs, err := ResolveDir(o.Dir)
		if err != nil {
			return nil, err
		}
		if !DirUsable(abs) {
			return nil, fmt.Errorf("%s is not empty — attach it instead of installing into it", shortHome(abs))
		}
		root = abs
	}
	wpdir := filepath.Join(root, "wp")
	if fileExists(wpdir) {
		return nil, fmt.Errorf("directory already exists: %s", wpdir)
	}

	// PHP version
	if o.PHPVersion == "" {
		rts := e.Store.Inventory().Runtimes()
		if len(rts) == 0 {
			return nil, fmt.Errorf("no PHP installed; run: agent-local install php 8.3")
		}
		o.PHPVersion = rts[len(rts)-1]
	}
	if e.Store.Inventory().FindPHP(o.PHPVersion) == nil {
		return nil, fmt.Errorf("php %s not installed; run: agent-local install php %s", o.PHPVersion, o.PHPVersion)
	}

	domain := o.Domain
	if domain == "" {
		domain = e.Store.DefaultDomain(slug)
	}
	if !ValidDomain(domain) {
		return nil, fmt.Errorf("invalid domain %q", domain)
	}
	if !e.Store.DomainFree(domain) {
		return nil, fmt.Errorf("domain %q already in use", domain)
	}

	pass := o.AdminPass
	if pass == "" {
		pass = randomPass(16)
	}
	dbPass := randomPass(20)

	site := &Site{
		Name:       o.Name,
		Slug:       slug,
		WorkDir:    root,
		WPDir:      wpdir,
		Branch:     "main",
		Repo:       o.Repo,
		PHPVersion: o.PHPVersion,
		DBName:     "al_" + slug,
		DBUser:     "al_" + slug,
		DBPass:     dbPass,
		Domain:     domain,
		HTTPPort:   DefaultHTTPPort,
		HTTPSPort:  DefaultHTTPSPort,
		CreatedAt:  time.Now(),
		State:      StateStopped,
		Installed:  true,
	}

	cb("database", "provisioning "+site.DBName)
	if err := e.CreateSiteDB(site); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	cb("files", "fetching WordPress")
	if o.Repo != "" {
		if err := gitClone(o.Repo, site.WorkDir, cb); err != nil {
			e.DropSiteDB(site)
			return nil, fmt.Errorf("clone: %w", err)
		}
	} else {
		if err := downloadWP(wpdir, o.WPVersion, cb); err != nil {
			e.DropSiteDB(site)
			return nil, fmt.Errorf("download: %w", err)
		}
		if err := gitInitRepo(site.WorkDir, wpdir); err != nil {
			cb("warn", "git init failed: "+err.Error())
		}
	}
	if !fileExists(filepath.Join(wpdir, "wp-load.php")) {
		e.DropSiteDB(site)
		return nil, fmt.Errorf("wordpress core not found at %s (repo must contain wp/)", wpdir)
	}

	cb("config", "writing wp-config.php")
	if err := writeWPConfig(site, wpdir); err != nil {
		return nil, err
	}

	title := o.Title
	if title == "" {
		title = o.Name
	}
	adminUser := o.AdminUser
	if adminUser == "" {
		adminUser = "admin"
	}
	email := o.AdminEmail
	if email == "" {
		email = "admin@" + domain
	}

	// persist the row before the installer runs so the serving router
	// (daemon process) can resolve the domain immediately.
	site.State = StateRunning
	site.AdminUser, site.AdminPass = adminUser, pass
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		return nil, err
	}
	cb("install", "running WordPress installer")
	if err := e.startForInstall(site); err != nil {
		return nil, fmt.Errorf("boot for install: %w", err)
	}
	installURL := fmt.Sprintf("http://127.0.0.1:%d/wp-admin/install.php?step=2", site.HTTPPort)
	body, err := httpPost(installURL, url.Values{
		"weblog_title":    {title},
		"user_name":       {adminUser},
		"admin_password":  {pass},
		"admin_password2": {pass},
		"pw_weak":         {"1"},
		"admin_email":     {email},
		"blog_public":     {"0"},
	}, site.Domain)
	if err != nil {
		return nil, fmt.Errorf("installer: %w", err)
	}
	if !strings.Contains(body, "wp-login.php") && !strings.Contains(body, "already installed") && !strings.Contains(body, "Success") {
		return nil, fmt.Errorf("installer did not report success (see logs/%s)", slug)
	}

	cb("dns", "registering "+domain)
	if n, err := EnsureHosts(e.HostsInteractive, []string{domain}); err != nil {
		cb("warn", "hosts entry failed (need root): "+err.Error())
	} else if n > 0 {
		cb("dns", "added /etc/hosts entry")
	}
	if cert, _, created, err := EnsureCert(domain); err == nil && created {
		_ = TrustCert(cert, false) // best-effort; TUI offers trust action
	}
	cb("done", BareURL(site))
	return site, nil
}

// AttachOpts points a site at a directory that already exists on disk. Nothing
// is downloaded and no installer runs: the directory is served as it is and gets
// its own empty database to use.
type AttachOpts struct {
	Dir      string // required: the directory to serve
	Name     string // default: the directory's own name
	Domain   string // default: <slug><suffix>
	PHPVer   string // default: highest installed
	Progress func(stage, detail string)
}

// AttachSite registers an existing directory as a site. It is the counterpart to
// CreateSite for the case where the files are the user's: their wp-config.php is
// never overwritten, and the only thing provisioned is a database.
func (e *Engine) AttachSite(o AttachOpts) (*Site, error) {
	cb := o.Progress
	if cb == nil {
		cb = func(string, string) {}
	}
	dir, err := ResolveDir(o.Dir)
	if err != nil {
		return nil, err
	}
	// An absent directory is created rather than refused: picking a path that
	// does not exist yet is a normal way to say "put it here".
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := o.Name
	if name == "" {
		name = filepath.Base(dir)
	}
	slug, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	if e.Store.Site(slug) != nil {
		return nil, fmt.Errorf("site %q already exists", slug)
	}
	if o.PHPVer == "" {
		rts := e.Store.Inventory().Runtimes()
		if len(rts) == 0 {
			return nil, fmt.Errorf("no PHP installed; run: agent-local install php 8.3")
		}
		o.PHPVer = rts[len(rts)-1]
	}
	if e.Store.Inventory().FindPHP(o.PHPVer) == nil {
		return nil, fmt.Errorf("php %s not installed; run: agent-local install php %s", o.PHPVer, o.PHPVer)
	}
	domain := o.Domain
	if domain == "" {
		domain = e.Store.DefaultDomain(slug)
	}
	if !ValidDomain(domain) {
		return nil, fmt.Errorf("invalid domain %q", domain)
	}
	if !e.Store.DomainFree(domain) {
		return nil, fmt.Errorf("domain %q already in use", domain)
	}
	docroot := DocrootFor(dir)
	branch := "main"
	if b, err := runCmdOut("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if b = strings.TrimSpace(b); b != "" && b != "HEAD" {
			branch = b
		}
	}
	site := &Site{
		Name:       name,
		Slug:       slug,
		WorkDir:    dir,
		WPDir:      docroot,
		Branch:     branch,
		PHPVersion: o.PHPVer,
		DBName:     "al_" + slug,
		DBUser:     "al_" + slug,
		DBPass:     randomPass(20),
		Domain:     domain,
		HTTPPort:   DefaultHTTPPort,
		HTTPSPort:  DefaultHTTPSPort,
		CreatedAt:  time.Now(),
		State:      StateStopped,
		Attached:   true,
	}
	cb("database", "provisioning empty "+site.DBName)
	if err := e.CreateSiteDB(site); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	// Their config is theirs. Only write one when WordPress core is sitting
	// there with nothing to connect to, which is the one case where a missing
	// wp-config.php is the only thing between them and a working install.
	cfg := filepath.Join(docroot, "wp-config.php")
	switch {
	case fileExists(cfg):
		cb("config", "keeping the existing wp-config.php")
	case fileExists(filepath.Join(docroot, "wp-load.php")):
		cb("config", "writing wp-config.php for "+site.DBName)
		if err := writeWPConfig(site, docroot); err != nil {
			e.DropSiteDB(site)
			return nil, err
		}
	default:
		cb("config", "no wordpress here yet — database is ready when you are")
	}
	site.State = StateRunning
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		return nil, err
	}
	cb("dns", "registering "+domain)
	if n, err := EnsureHosts(e.HostsInteractive, []string{domain}); err != nil {
		cb("warn", "hosts entry failed (need root): "+err.Error())
	} else if n > 0 {
		cb("dns", "added /etc/hosts entry")
	}
	if cert, _, created, err := EnsureCert(domain); err == nil && created {
		_ = TrustCert(cert, false)
	}
	if err := e.StartSite(slug); err != nil {
		return site, fmt.Errorf("start: %w", err)
	}
	cb("done", BareURL(site))
	return site, nil
}

// managedDir reports whether a path is one of ours to remove wholesale: inside
// the app's own tree, or inside the configured sites directory. Anything else is
// the user's, and only what we added to it may be undone.
func (e *Engine) managedDir(path string) bool {
	for _, root := range []string{P().Sites(), e.Store.SitesDir()} {
		if strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// htaccessUploadsRule looks for the one .htaccess pattern worth understanding:
// "if this upload is missing, send it to that origin". It is deliberately not a
// rewrite engine — it recognises the shape people already use so the setting can
// be adopted instead of retyped, and reports nothing when unsure.
//
//	RewriteCond %{REQUEST_URI} ^/wp-content/uploads/…
//	RewriteCond %{REQUEST_FILENAME} !-f
//	RewriteRule ^(.*)$ https://origin.example/$1 [QSA,L]
func htaccessUploadsRule(docroot string) string {
	b, err := os.ReadFile(filepath.Join(docroot, ".htaccess"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	uploads, missing := false, false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "#"):
			continue
		case strings.HasPrefix(t, "RewriteCond") && strings.Contains(t, "wp-content/uploads"):
			uploads = true
		case strings.HasPrefix(t, "RewriteCond") && strings.Contains(t, "!-f"):
			missing = true
		case strings.HasPrefix(t, "RewriteRule"):
			if uploads && missing {
				if u := firstURL(t); u != "" {
					return u
				}
			}
			// A rule ends the group it belonged to.
			uploads, missing = false, false
		}
	}
	return ""
}

// firstURL pulls the origin out of a RewriteRule target, dropping the trailing
// substitution and flags: "https://x.org/$1 [QSA,L]" -> "https://x.org".
func firstURL(rule string) string {
	i := strings.Index(rule, "http")
	if i < 0 {
		return ""
	}
	rest := strings.Fields(rule[i:])
	if len(rest) == 0 {
		return ""
	}
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSuffix(rest[0], "$1"), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
}

// SetMediaFallback points a site's missing uploads at an origin. An empty value
// turns it off; "auto" adopts whatever the docroot's .htaccess already says.
func (e *Engine) SetMediaFallback(slug, origin string) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	switch strings.TrimSpace(origin) {
	case "":
		site.MediaFallback = ""
	case "auto":
		found := htaccessUploadsRule(site.WPDir)
		if found == "" {
			return "", fmt.Errorf("no uploads rewrite found in %s/.htaccess — pass a URL instead", shortHome(site.WPDir))
		}
		site.MediaFallback = found
	default:
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", fmt.Errorf("media fallback must be an http(s) URL, got %q", origin)
		}
		site.MediaFallback = u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
	}
	e.Store.PutSite(site)
	return site.MediaFallback, e.Store.Save()
}

// MediaFallbackHint is what a site's .htaccess implies, for surfaces that want to
// offer it. Empty when there is nothing to adopt.
func (e *Engine) MediaFallbackHint(slug string) string {
	site := e.Store.Site(slug)
	if site == nil {
		return ""
	}
	return htaccessUploadsRule(site.WPDir)
}

// authoredByUs reports whether a wp-config.php is one we generated, by the
// header writeWPConfig stamps on it. Used so deleting an attached site removes
// only the config it added.
func authoredByUs(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "Generated by agent-local")
}

// ResolveDir turns user input into an absolute path, expanding ~ and $HOME.
func ResolveDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("directory required")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return filepath.Abs(p)
}

// DirUsable reports whether a fresh install can go here: the path is missing, or
// it is an empty directory. Dotfiles count as content — a git checkout is not
// empty just because everything in it is hidden.
func DirUsable(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return os.IsNotExist(err)
	}
	return len(ents) == 0
}

// DocrootFor finds the directory that should be served for a checkout: the path
// itself when WordPress is right there, else the usual nesting conventions.
func DocrootFor(dir string) string {
	if fileExists(filepath.Join(dir, "wp-load.php")) {
		return dir
	}
	for _, sub := range []string{"wp", filepath.Join("app", "public"), "public", "web", "www", "htdocs", "public_html"} {
		if fileExists(filepath.Join(dir, sub, "wp-load.php")) {
			return filepath.Join(dir, sub)
		}
	}
	return dir
}

// DeleteOpts controls how much of a site goes away.
type DeleteOpts struct {
	// KeepFiles leaves the checkout on disk (an imported external directory is
	// never removed regardless; its wp-config is restored from our backup).
	KeepFiles bool
	// KeepDB leaves the schema and user in place, so a detached folder whose
	// wp-config still points here can be re-adopted without recreating it.
	KeepDB bool
	// InteractiveHosts allows a password prompt for the /etc/hosts edit.
	InteractiveHosts bool
}

// DeleteSite removes a site. Files and database are independently keepable:
// dropping the schema while keeping files left a wp-config pointing at nothing,
// and re-adopting that folder then failed in the copy step.
func (e *Engine) DeleteSite(slug string, o DeleteOpts) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	_ = e.StopSite(slug)
	for _, w := range e.Store.WorktreesFor(slug) {
		_ = e.RemoveWorktree(w.ID)
	}
	// Drop the generated pool config too, or php-fpm keeps parsing a pool whose
	// work_dir no longer exists on every later start.
	e.RemovePool(slug)
	if !o.KeepDB {
		if err := e.DropSiteDB(site); err != nil {
			return fmt.Errorf("drop db: %w", err)
		}
	}
	interactiveHosts := o.InteractiveHosts
	withFiles := !o.KeepFiles
	domains := append([]string{site.Domain}, site.Aliases...)
	if err := RemoveHosts(interactiveHosts, domains); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove /etc/hosts entries: %v\n", err)
	}
	if withFiles {
		switch {
		case e.managedDir(site.WorkDir):
			// A directory this app owns — our own tree, or the sites directory the
			// user pointed us at. All of it goes.
			if err := os.RemoveAll(site.WorkDir); err != nil {
				return fmt.Errorf("remove files: %w", err)
			}
		case site.Installed:
			// A site we installed into a directory the user picked. It was empty
			// when we took it, so everything we put there can go — and only
			// that. Leaving a whole WordPress install behind is a nasty surprise;
			// deleting a folder we never filled is worse, so the root itself goes
			// only when nothing else is left in it.
			for _, ours := range []string{site.WPDir, filepath.Join(site.WorkDir, "@"),
				filepath.Join(site.WorkDir, ".git"), filepath.Join(site.WorkDir, ".gitignore")} {
				os.RemoveAll(ours)
			}
			if ents, err := os.ReadDir(site.WorkDir); err == nil && len(ents) == 0 {
				os.Remove(site.WorkDir)
			}
		default:
			// Imported or attached: the files are the user's. Undo only the
			// wp-config we pointed at our database — never their content. Sites
			// predating the Installed flag land here, which is the safe side.
			cfg := filepath.Join(site.WPDir, "wp-config.php")
			switch {
			case fileExists(cfg + ".agent-local.bak"):
				// restore the config we overwrote, then drop our copy
				os.Rename(cfg+".agent-local.bak", cfg)
			case authoredByUs(cfg):
				// An attached directory that had none: we wrote it, so removing
				// it leaves the folder exactly as it was found.
				os.Remove(cfg)
			}
		}
	}
	e.Store.DelSite(slug)
	return e.Store.Save()
}

// StartSite boots DB + FPM + HTTP front for a site.
func (e *Engine) StartSite(slug string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if err := e.EnsureDB(); err != nil {
		return err
	}
	if err := e.StartFPM(site.Slug, site.WPDir, site.PHPVersion); err != nil {
		site.State = StateError
		e.Store.Save()
		return err
	}
	if err := EnsureHTTPFront(e.Store); err != nil {
		return err
	}
	if _, err := EnsureHosts(e.HostsInteractive, []string{site.Domain}); err != nil {
		return fmt.Errorf("hosts entry for %s needs root: run `agent-local doctor --fix`", site.Domain)
	}
	site.State = StateRunning
	return e.Store.Save()
}

// StopSite stops the FPM pool (DB stays shared).
func (e *Engine) StopSite(slug string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	err := e.StopFPM(site.Slug)
	site.State = StateStopped
	e.Store.Save()
	return err
}

// startForInstall boots minimal stack without hosts mutation.
func (e *Engine) startForInstall(site *Site) error {
	if err := e.EnsureDB(); err != nil {
		return err
	}
	if err := e.StartFPM(site.Slug, site.WPDir, site.PHPVersion); err != nil {
		return err
	}
	if err := EnsureHTTPFront(e.Store); err != nil {
		return err
	}
	return waitPort(site.HTTPPort, 10*time.Second)
}

// SwitchPHP changes a site's PHP version and restarts its pool.
func (e *Engine) SwitchPHP(slug, version string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if e.Store.Inventory().FindPHP(version) == nil {
		return fmt.Errorf("php %s not installed", version)
	}
	site.PHPVersion = version
	if e.FPMRunning(site.Slug) {
		if err := e.FPMRestart(site.Slug, site.WPDir, version); err != nil {
			return err
		}
	}
	for _, w := range e.Store.WorktreesFor(slug) {
		if e.FPMRunning(w.ID) {
			if err := e.FPMRestart(w.ID, e.wtServeDir(w), version); err != nil {
				return err
			}
		}
	}
	return e.Store.Save()
}

// SetDomain changes a site's domain (hosts + cert follow).
func (e *Engine) SetDomain(slug, domain string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if !ValidDomain(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	old := site.Domain
	if domain != old && !e.Store.DomainFree(domain) {
		return fmt.Errorf("domain %q already in use", domain)
	}
	site.Domain = domain
	if err := e.Store.Save(); err != nil {
		return err
	}
	_, _ = EnsureHosts(e.HostsInteractive, []string{domain})
	// The name we just left behind would otherwise keep resolving to us forever,
	// so /etc/hosts grows a dead entry per rename — and the old address still
	// answers, which is worse than not resolving at all.
	if old != "" && old != domain && e.Store.DomainFree(old) {
		if err := RemoveHosts(e.HostsInteractive, []string{old}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove the old %s hosts entry: %v\n", old, err)
		}
	}
	if cert, _, created, err := EnsureCert(domain); err == nil && created {
		_ = TrustCert(cert, false)
	}
	if site.State == StateRunning {
		_ = e.StopSite(slug)
		_ = e.StartSite(slug)
	}
	// WordPress stores its own URLs, so a rename that stops here leaves every
	// request redirecting straight back to the old domain — the new one only looks
	// like it does not work. Rewrite them the way an import does.
	if old != "" && old != domain {
		e.rewriteSiteURLs(site, old, domain)
	}
	return nil
}

// rewriteSiteURLs points the site's stored URLs at a new domain. Best-effort:
// a site whose database is unreachable still gets the rename, and says so.
func (e *Engine) rewriteSiteURLs(site *Site, old, domain string) {
	if err := e.EnsureDB(); err != nil {
		return
	}
	for _, scheme := range []string{"https://", "http://"} {
		if out, err := wpCLI(site, "search-replace", scheme+old, scheme+domain,
			"--all-tables", "--skip-columns=guid"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not rewrite %s URLs in the database: %s\n", scheme+old, tail(out, 160))
		}
	}
	// A theme or wp-config constant can pin the old domain just as hard.
	rewriteWPConfigDomains(filepath.Join(site.WPDir, "wp-config.php"), map[string]bool{old: true}, domain)
}

// ---------- Worktrees ----------

// AddWorktree creates a git worktree for a branch and serves it on its own domain.
func (e *Engine) AddWorktree(slug, branch string) (*Worktree, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	bSlug := BranchSlug(branch)
	id := slug + "--" + bSlug
	if _, ok := e.Store.Data.Worktrees[id]; ok {
		return nil, fmt.Errorf("worktree exists: %s", id)
	}
	repoDir := siteRepoDir(site)
	if repoDir == "" {
		return nil, fmt.Errorf("%s is not a git repo; worktrees need git (run: git init inside it)", site.WPDir)
	}
	if hasRemote(repoDir) {
		_, _ = runCmdOut("git", "-C", repoDir, "fetch", "--all", "--prune")
	}
	wtPath := filepath.Join(repoDir, "@", bSlug)
	if fileExists(wtPath) {
		return nil, fmt.Errorf("path exists: %s", wtPath)
	}
	args := []string{"-C", repoDir, "worktree", "add", wtPath}
	if branchExists(repoDir, branch) {
		args = append(args, branch)
	} else if remoteBranchExists(repoDir, branch) {
		args = append(args, "-b", branch, "--track", "origin/"+branch)
	} else {
		args = append(args, "-b", branch)
	}
	if out, err := runCmdOut("git", args...); err != nil {
		return nil, fmt.Errorf("git worktree: %v %s", err, tail(out, 300))
	}
	// Serving root: docroot-repo worktrees serve from their own path;
	// WorkDir-style repos serve from <path>/wp.
	serveDir := wtPath
	if repoDir == site.WorkDir {
		serveDir = wtPath + "/wp"
	}
	if !fileExists(serveDir) {
		if err := os.Symlink(site.WPDir, serveDir); err != nil {
			return nil, err
		}
	}
	// Overlay: the branch checkout wins for every file it tracks; everything
	// else (WP core, plugins, uploads, sibling themes) symlinks to the main
	// docroot. Theme-only repos therefore boot a full site with zero copying.
	if !isSymlink(serveDir) {
		overlayDir(site.WPDir, serveDir)
	}
	// wp-config always ours: pins the DB and the preview domain so WordPress
	// never redirects the branch back to the base site.
	domain := bSlug + "." + site.Domain
	if err := writeWorktreeWPConfig(site, serveDir, domain); err != nil {
		return nil, err
	}
	if !isSymlink(serveDir) {
		// Constants alone lose to plugins that filter option_home (iThemes
		// Security's SSL module does): pin the URL from an mu-plugin at max
		// priority. Needs mu-plugins to be ours, not the base site's dir.
		if err := writePreviewMU(site, serveDir, domain); err != nil {
			return nil, err
		}
		// Own page cache: previews must not serve or poison base cache files.
		privateDir(filepath.Join(serveDir, "wp-content", "cache"))
	}
	w := &Worktree{ID: id, Site: slug, Branch: branch, Path: wtPath, Domain: domain}
	e.Store.PutWorktree(w)
	if err := e.Store.Save(); err != nil {
		return nil, err
	}
	_, _ = EnsureHosts(e.HostsInteractive, []string{domain})
	if cert, _, created, err := EnsureCert(domain); err == nil && created {
		_ = TrustCert(cert, false)
	}
	if err := e.StartWorktree(id); err != nil {
		return w, err
	}
	return w, nil
}

// privateDir makes path a real, empty directory owned by this worktree,
// replacing an inherited symlink to the base site.
func privateDir(path string) {
	if isSymlink(path) {
		os.Remove(path)
	}
	os.MkdirAll(path, 0o755)
}

// materializeDir turns an inherited symlink into a real directory whose
// entries symlink back to the source, so we can add files of our own
// without writing into the base site.
func materializeDir(src, dst string) error {
	if isSymlink(dst) {
		os.Remove(dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return nil // base had no such dir; empty is fine
	}
	for _, e := range ents {
		d := filepath.Join(dst, e.Name())
		if _, err := os.Lstat(d); err != nil {
			os.Symlink(filepath.Join(src, e.Name()), d)
		}
	}
	return nil
}

// writePreviewMU drops an mu-plugin that forces WordPress to render the
// branch domain. Runs at PHP_INT_MAX so security/SSL plugins that rewrite
// option_home to the canonical site URL cannot drag the preview back.
func writePreviewMU(site *Site, wpdir, domain string) error {
	mu := filepath.Join(wpdir, "wp-content", "mu-plugins")
	if err := materializeDir(filepath.Join(site.WPDir, "wp-content", "mu-plugins"), mu); err != nil {
		return err
	}
	body := fmt.Sprintf(`<?php
/**
 * Plugin Name: agent-local branch preview
 * Description: Pins this worktree to %s. Managed by agent-local.
 */
$al_preview_url = '%s';
foreach ( array( 'option_home', 'option_siteurl', 'pre_option_home', 'pre_option_siteurl' ) as $al_f ) {
	add_filter( $al_f, function () use ( $al_preview_url ) { return $al_preview_url; }, PHP_INT_MAX );
}
// Never bounce the preview to the canonical host.
remove_action( 'template_redirect', 'redirect_canonical' );
add_filter( 'redirect_canonical', '__return_false', PHP_INT_MAX );
`, domain, "http://"+domain)
	return os.WriteFile(filepath.Join(mu, "000-agent-local-preview.php"), []byte(body), 0o644)
}

// isSymlink reports whether path itself is a symlink.
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// siteRepoDir returns the git repo backing a site: its work dir for
// agent-local-created sites, else the docroot (imported checkouts keep .git
// in the WordPress root). Empty when the site is not a repo.
func siteRepoDir(site *Site) string {
	if isGitRepo(site.WorkDir) {
		return site.WorkDir
	}
	if isGitRepo(site.WPDir) {
		return site.WPDir
	}
	return ""
}

// SiteForPath resolves a filesystem path to a managed site. Integrators (a UI,
// an editor) key sites by the checkout directory a user picked, while we key
// them by slug — so match the path against every root we know, then walk up:
// a file deep inside a docroot still identifies its site.
//
// The match is reported so a caller can tell an exact hit from an ancestor one.
func (e *Engine) SiteForPath(path string) (*Site, string, *Worktree) {
	want := normalizePath(path)
	if want == "" {
		return nil, "", nil
	}
	// Worktrees first: their paths live inside a site's repo, so a site root
	// would otherwise shadow the more specific preview.
	for _, w := range e.Store.Data.Worktrees {
		if p := normalizePath(w.Path); p != "" && pathWithin(want, p) {
			return e.Store.Site(w.Site), "worktree", w
		}
	}
	best, bestField, bestLen := (*Site)(nil), "", 0
	for _, s := range e.Store.Sites() {
		for field, root := range map[string]string{"wp_dir": s.WPDir, "work_dir": s.WorkDir} {
			p := normalizePath(root)
			if p == "" || !pathWithin(want, p) {
				continue
			}
			// Deepest root wins: wp_dir is inside work_dir for our own sites.
			if len(p) > bestLen {
				best, bestField, bestLen = s, field, len(p)
			}
		}
	}
	return best, bestField, nil
}

// normalizePath resolves symlinks where it can and strips a trailing separator
// so two spellings of the same directory compare equal. macOS paths are
// compared case-insensitively, matching the default filesystem.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return strings.ToLower(strings.TrimRight(p, string(filepath.Separator)))
}

// pathWithin reports whether want is root or sits underneath it. Both must
// already be normalized; a prefix test alone would match /a/bc against /a/b.
func pathWithin(want, root string) bool {
	return want == root || strings.HasPrefix(want, root+string(filepath.Separator))
}

// SitesUnderPath lists sites whose roots sit beneath a directory. Used when a
// caller hands us a repo root that contains a site rather than a path inside
// one; the caller decides what a multi-site answer means.
func (e *Engine) SitesUnderPath(path string) []*Site {
	want := normalizePath(path)
	if want == "" {
		return nil
	}
	var out []*Site
	for _, s := range e.Store.Sites() {
		for _, root := range []string{s.WPDir, s.WorkDir} {
			p := normalizePath(root)
			if p == "" || !pathWithin(p, want) {
				continue
			}
			out = append(out, s)
			break
		}
	}
	return out
}

// ResolvePath is the one path→site resolution every surface uses: exact match
// or inside a site first, then a directory containing exactly one site.
// Ambiguity is returned rather than guessed, so the caller decides whether that
// is a 409, a prompt, or an error.
//
// Both the HTTP route and the CLI go through this. When the ancestor case lived
// only in the handler, `agent-local resolve <repo root>` failed while
// `GET /resolve` on the same path succeeded — surfaces must not disagree.
func (e *Engine) ResolvePath(path string) (site *Site, matched string, wt *Worktree, candidates []*Site) {
	if s, m, w := e.SiteForPath(path); s != nil {
		return s, m, w, nil
	}
	below := e.SitesUnderPath(path)
	if len(below) == 1 {
		return below[0], "contains", nil, nil
	}
	return nil, "", nil, below
}

// Branches lists the site repo's branches (local + remote-only), marking the
// one checked out at the base docroot, so agents can pick a preview target.
func (e *Engine) Branches(slug string) (map[string]interface{}, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	repo := siteRepoDir(site)
	if repo == "" {
		return nil, fmt.Errorf("%s is not a git repo", site.WPDir)
	}
	local := gitLines(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	remote := []string{}
	for _, r := range gitLines(repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes") {
		name := strings.TrimPrefix(r, "origin/")
		if name == "HEAD" || name == r {
			continue
		}
		if !contains(local, name) {
			remote = append(remote, name)
		}
	}
	cur, _ := runCmdOut("git", "-C", repo, "branch", "--show-current")
	return map[string]interface{}{
		"repo":     repo,
		"current":  strings.TrimSpace(cur),
		"local":    local,
		"remote":   remote,
		"previews": e.Store.WorktreesFor(slug),
	}, nil
}

func gitLines(dir string, args ...string) []string {
	out, err := runCmdOut("git", append([]string{"-C", dir}, args...)...)
	if err != nil {
		return []string{}
	}
	lines := []string{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// sharedContent are the only paths a preview symlinks back to the base site:
// media is large and identical, so it is shared rather than cloned.
var sharedContent = map[string]bool{"uploads": true}

// overlayDir fills dst from src for everything the branch checkout does not
// track, recursing wherever both sides have a real directory, so gitignored
// build output (wp-content/themes/x/assets/dist, vendor/, node_modules) and
// untracked plugins are present in the preview too.
//
// Code is clone-copied, never symlinked: PHP resolves __FILE__ through
// symlinks, so a symlinked wp-load.php would set ABSPATH to the base install
// and silently run the base site's wp-config. On APFS the clone is
// copy-on-write — instant and effectively free. Recursion only continues
// along paths the checkout also has, so it never walks the cloned subtrees.
func overlayDir(src, dst string) {
	ents, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		switch name {
		case "wp-config.php", "wp-config.php.agent-local.bak", ".git", "@":
			continue
		}
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		if _, err := os.Lstat(d); err != nil {
			if sharedContent[name] || isSymlink(s) {
				os.Symlink(s, d)
			} else {
				cloneCopy(s, d)
			}
			continue
		}
		if e.IsDir() && !isSymlink(d) {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				overlayDir(s, d)
			}
		}
	}
}

// cloneCopy copies src to dst, preferring APFS copy-on-write clones.
func cloneCopy(src, dst string) error {
	if err := exec.Command("cp", "-cR", src, dst).Run(); err == nil {
		return nil
	}
	os.RemoveAll(dst)
	return exec.Command("cp", "-R", src, dst).Run()
}

// writeWorktreeWPConfig writes the preview's own wp-config: same database as
// the base site, but WP_HOME/WP_SITEURL pinned to the branch domain so
// WordPress renders links locally instead of redirecting to the base site.
func writeWorktreeWPConfig(site *Site, wpdir, domain string) error {
	if err := writeWPConfig(site, wpdir); err != nil {
		return err
	}
	target := filepath.Join(wpdir, "wp-config.php")
	b, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	src := string(b)
	// Drop any inherited URL pins, then add ours ahead of wp-settings.
	for _, c := range []string{"WP_HOME", "WP_SITEURL", "EFRONT_URL_OVERRIDE"} {
		src = regexp.MustCompile(`(?m)^.*define\(\s*'`+c+`'.*\n`).ReplaceAllString(src, "")
	}
	url := "http://" + domain
	pins := fmt.Sprintf("define( 'WP_HOME', '%s' );\ndefine( 'WP_SITEURL', '%s' );\ndefine( 'EFRONT_URL_OVERRIDE', '%s' );\n", url, url, url)
	if i := strings.Index(src, "require_once ABSPATH"); i >= 0 {
		src = src[:i] + pins + "\n" + src[i:]
	} else {
		src += "\n" + pins
	}
	return os.WriteFile(target, []byte(src), 0o644)
}

// StartWorktree boots the FPM pool for a worktree.
func (e *Engine) StartWorktree(id string) error {
	w, ok := e.Store.Data.Worktrees[id]
	if !ok {
		return fmt.Errorf("no such worktree: %s", id)
	}
	site := e.Store.Site(w.Site)
	if err := e.EnsureDB(); err != nil {
		return err
	}
	if err := e.StartFPM(w.ID, e.wtServeDir(w), site.PHPVersion); err != nil {
		return err
	}
	return EnsureHTTPFront(e.Store)
}

// StopWorktree stops a worktree's pool.
func (e *Engine) StopWorktree(id string) error {
	return e.StopFPM(id)
}

// RemoveWorktree stops + prunes the git worktree.
func (e *Engine) RemoveWorktree(id string) error {
	w, ok := e.Store.Data.Worktrees[id]
	if !ok {
		return fmt.Errorf("no such worktree: %s", id)
	}
	_ = e.StopWorktree(id)
	e.RemovePool(id) // same reason as DeleteSite: don't leave a dead pool config
	site := e.Store.Site(w.Site)
	if site != nil {
		repoDir := siteRepoDir(site)
		_, _ = runCmdOut("git", "-C", repoDir, "worktree", "remove", "--force", w.Path)
		if fileExists(w.Path) {
			os.RemoveAll(w.Path)
		}
	}
	_ = RemoveHosts(false, []string{w.Domain})
	e.Store.DelWorktree(id)
	return e.Store.Save()
}

// ---------- WordPress plumbing ----------

func downloadWP(dst, version string, cb func(string, string)) error {
	if version == "" {
		version = "latest"
	}
	tarURL := "https://wordpress.org/latest.tar.gz"
	if version != "latest" {
		tarURL = fmt.Sprintf("https://wordpress.org/wordpress-%s.tar.gz", version)
	}
	cb("files", "GET "+tarURL)
	resp, err := http.Get(tarURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", tarURL, resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "wp-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return err
	}
	tmp.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	out, err := runCmdOut("tar", "-xzf", tmp.Name(), "-C", dst, "--strip-components=1")
	if err != nil {
		return fmt.Errorf("extract: %v %s", err, tail(out, 300))
	}
	return nil
}

var saltNames = []string{
	"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY",
	"AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT",
}

func writeWPConfig(site *Site, wpdir string) error {
	var salts strings.Builder
	for _, n := range saltNames {
		salts.WriteString(fmt.Sprintf("define('%s', '%s');\n", n, randomPass(48)))
	}
	conf := fmt.Sprintf(`<?php
// Generated by agent-local. Do not edit the DB block.
define( 'DB_NAME', '%s' );
define( 'DB_USER', '%s' );
define( 'DB_PASSWORD', '%s' );
define( 'DB_HOST', '127.0.0.1:%d' );
define( 'DB_CHARSET', 'utf8mb4' );
define( 'DB_COLLATE', '' );

%s
$table_prefix = 'wp_';

define( 'WP_DEBUG', true );
define( 'WP_DEBUG_LOG', true );
define( 'WP_DEBUG_DISPLAY', false );
define( 'WP_ENVIRONMENT_TYPE', 'local' );

if ( ! defined( 'ABSPATH' ) ) {
	define( 'ABSPATH', __DIR__ . '/' );
}
require_once ABSPATH . 'wp-settings.php';
`, site.DBName, site.DBUser, site.DBPass, DefaultDBPort, salts.String())

	// If wp-config already exists (cloned repo), rewrite only the DB block.
	target := filepath.Join(wpdir, "wp-config.php")
	if fileExists(target) {
		b, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		src := string(b)
		src = regexp.MustCompile(`(?m)define\(\s*'DB_NAME'.*\n`).ReplaceAllString(src, fmt.Sprintf("define( 'DB_NAME', '%s' );\n", site.DBName))
		src = regexp.MustCompile(`(?m)define\(\s*'DB_USER'.*\n`).ReplaceAllString(src, fmt.Sprintf("define( 'DB_USER', '%s' );\n", site.DBUser))
		src = regexp.MustCompile(`(?m)define\(\s*'DB_PASSWORD'.*\n`).ReplaceAllString(src, fmt.Sprintf("define( 'DB_PASSWORD', '%s' );\n", site.DBPass))
		src = regexp.MustCompile(`(?m)define\(\s*'DB_HOST'.*\n`).ReplaceAllString(src, fmt.Sprintf("define( 'DB_HOST', '127.0.0.1:%d' );\n", DefaultDBPort))
		return os.WriteFile(target, []byte(src), 0o644)
	}
	return os.WriteFile(target, []byte(conf), 0o644)
}

func httpPost(u string, form url.Values, hostHeader string) (string, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = hostHeader
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func randomPass(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

// ---------- git helpers ----------

func gitClone(repo, dst string, cb func(string, string)) error {
	cb("files", "git clone "+repo)
	cmd := exec.Command("git", "clone", repo, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v %s", err, tail(string(out), 300))
	}
	return nil
}

func gitInitRepo(workdir, wpdir string) error {
	if _, err := runCmdOut("git", "-C", workdir, "init", "-b", "main"); err != nil {
		return err
	}
	gitignore := filepath.Join(workdir, ".gitignore")
	if !fileExists(gitignore) {
		os.WriteFile(gitignore, []byte("@/\nwp/wp-config.php\n"), 0o644)
	}
	runCmdQuiet("git", "-C", workdir, "add", "-A")
	runCmdQuiet("git", "-C", workdir, "-c", "user.email=agent@local", "-c", "user.name=agent-local",
		"commit", "-m", "initial wordpress checkout")
	return nil
}

func hasRemote(dir string) bool {
	out, err := runCmdOut("git", "-C", dir, "remote")
	return err == nil && strings.TrimSpace(out) != ""
}

func isGitRepo(dir string) bool {
	err := runCmdQuiet("git", "-C", dir, "rev-parse", "--git-dir")
	return err == nil
}

func branchExists(dir, branch string) bool {
	err := runCmdQuiet("git", "-C", dir, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

func remoteBranchExists(dir, branch string) bool {
	err := runCmdQuiet("git", "-C", dir, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	return err == nil
}

// SiteDirSize returns a human-readable size of a site directory.
func SiteDirSize(slug string) string {
	out, err := runCmdOut("du", "-sh", filepath.Join(P().Sites(), slug))
	if err != nil {
		return "?"
	}
	fields := strings.Fields(out)
	if len(fields) > 0 {
		return fields[0]
	}
	return "?"
}
