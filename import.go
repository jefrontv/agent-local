package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Import brings an existing local WordPress checkout (e.g. a LocalWP site)
// under agent-local management: provision a DB on our engine, copy the
// database from the source MySQL, point wp-config at us, and serve it.
//
// Two copy modes:
//   - in-place (default): serve the docroot where it already lives. No
//     7 GB copy. wp-config.php is edited in place (a .agent-local.bak
//     backup is written first).
//   - copy: rsync the docroot into our sites dir first.

// LocalWPSite is the slice of LocalWP's sites.json we care about.
type LocalWPSite struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Domain   string `json:"domain"`
	Services struct {
		MySQL struct {
			Version string `json:"version"`
			Ports   struct {
				MYSQL []int `json:"MYSQL"`
			} `json:"ports"`
		} `json:"mysql"`
		PHP struct {
			Version string `json:"version"`
		} `json:"php"`
	} `json:"services"`
	MySQL struct {
		Database string `json:"database"`
		User     string `json:"user"`
		Password string `json:"password"`
	} `json:"mysql"`
}

// Socket returns the site's MySQL unix socket if it exists on disk.
func (s LocalWPSite) Socket() string {
	for _, name := range []string{"mysqld.sock", "mysql.sock"} {
		p := filepath.Join(HomeDir(), "Library", "Application Support", "Local", "run", s.ID, "mysql", name)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// LocalWPConfigPath is where LocalWP keeps its site registry.
func LocalWPConfigPath() string {
	return filepath.Join(HomeDir(), "Library", "Application Support", "Local", "sites.json")
}

// ListLocalWPSites parses LocalWP's sites.json.
func ListLocalWPSites() ([]LocalWPSite, error) {
	b, err := os.ReadFile(LocalWPConfigPath())
	if err != nil {
		return nil, fmt.Errorf("LocalWP sites.json not found (%s) — is Local installed?", err)
	}
	var m map[string]LocalWPSite
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := make([]LocalWPSite, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return out, nil
}

// ImportOpts configures an import.
type ImportOpts struct {
	Name      string // agent-local slug/name (default: source name)
	Source    string // either a LocalWP site name OR a path to a docroot
	Domain    string // target domain (default: <slug>.test)
	PHPVer    string
	InPlace   bool   // default true
	Copy      bool   // force copy mode
	DBHost    string // explicit source DB host (default: detect)
	DBPort    int    // explicit source DB port
	DBUser    string // explicit source DB user
	DBPass    string // explicit source DB password
	DBName    string // explicit source DB name
	SQLDump   string // import from a .sql dump file instead of a live server
	ServeOnly bool   // don't touch any database; serve the dir with its own wp-config
	Progress  func(stage, detail string)
}

// ImportSite runs the import. It returns the created site.
func (e *Engine) ImportSite(o ImportOpts) (*Site, error) {
	cb := o.Progress
	if cb == nil {
		cb = func(string, string) {}
	}

	// Resolve source: LocalWP name or docroot path.
	var docroot string
	var srcDBName, srcHost, srcUser, srcPass, srcSocket string
	var srcPort int
	var lwName string

	if sites, err := ListLocalWPSites(); err == nil {
		for _, s := range sites {
			// Match by name/id, or by path: an integrator hands us the docroot
			// it already knows, and LocalWP's wp-config says DB_HOST=localhost
			// while its mysqld actually listens on a per-site socket. Without
			// the registry we would aim at the default socket and fail.
			sitePath := normalizePath(s.Path)
			want := normalizePath(o.Source)
			byPath := sitePath != "" && want != "" && (pathWithin(want, sitePath) || pathWithin(sitePath, want))
			if s.Name != o.Source && s.ID != o.Source && !byPath {
				continue
			}
			lwName = s.Name
			docroot = filepath.Join(s.Path, "app", "public")
			srcUser, srcPass = s.MySQL.User, s.MySQL.Password
			if srcUser == "" {
				srcUser, srcPass = "root", "root"
			}
			srcSocket = s.Socket()
			if len(s.Services.MySQL.Ports.MYSQL) > 0 {
				srcPort = s.Services.MySQL.Ports.MYSQL[0]
			}
			srcHost = "127.0.0.1"
			break
		}
	}
	if docroot == "" {
		// treat Source as a docroot path
		if st, err := os.Stat(o.Source); err == nil && st.IsDir() {
			docroot = o.Source
			base := filepath.Base(docroot)
			switch base {
			case "public", "web", "www", "htdocs", "app":
				lwName = filepath.Base(filepath.Dir(docroot))
			default:
				lwName = base
			}
		}
	}
	if docroot == "" {
		return nil, fmt.Errorf("source %q not found as a LocalWP site or directory", o.Source)
	}
	if !fileExists(filepath.Join(docroot, "wp-load.php")) {
		return nil, fmt.Errorf("no WordPress at %s (missing wp-load.php)", docroot)
	}

	// Source connection: explicit flags win, else read the docroot's own
	// wp-config.php. Reading only DB_NAME (as this did) left host, user and
	// password empty, so the dump fell back to a unix socket on "localhost"
	// and failed with 2002 before it ever reached the real server.
	cfgPath := filepath.Join(docroot, "wp-config.php")
	if srcDBName == "" {
		srcDBName = readWPConfigConst(cfgPath, "DB_NAME")
	}
	if srcUser == "" {
		srcUser = readWPConfigConst(cfgPath, "DB_USER")
	}
	if srcPass == "" {
		srcPass = readWPConfigConst(cfgPath, "DB_PASSWORD")
	}
	if srcHost == "" && srcSocket == "" {
		// DB_HOST carries three shapes: "host", "host:port", "host:/path/sock".
		host, port, socket := parseWPDBHost(readWPConfigConst(cfgPath, "DB_HOST"))
		srcHost, srcSocket = host, socket
		if port != 0 && srcPort == 0 {
			srcPort = port
		}
	}
	if o.DBName != "" {
		srcDBName = o.DBName
	}
	if o.DBHost != "" {
		srcHost = o.DBHost
		srcSocket = ""
	}
	if o.DBPort != 0 {
		srcPort = o.DBPort
	}
	if o.DBUser != "" {
		srcUser = o.DBUser
	}
	if o.DBPass != "" {
		srcPass = o.DBPass
	}
	if srcDBName == "" {
		return nil, fmt.Errorf("could not determine the source database name (set --db-name, or add a wp-config.php)")
	}
	if srcHost == "" && srcSocket == "" {
		srcHost = "127.0.0.1"
	}
	if srcPort == 0 {
		srcPort = 3306
	}

	name := o.Name
	if name == "" {
		name = lwName
	}
	slug, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	if e.Store.Site(slug) != nil {
		return nil, fmt.Errorf("site %q already exists", slug)
	}

	domain := o.Domain
	if domain == "" {
		domain = e.Store.DefaultDomain(slug)
	}
	if !e.Store.DomainFree(domain) {
		return nil, fmt.Errorf("domain %q already in use", domain)
	}

	// PHP version: requested → LocalWP's → highest installed.
	php := o.PHPVer
	if php == "" {
		php = matchInstalledPHP(e.Store, "8.2")
	}
	if php == "" {
		rts := e.Store.Inventory().Runtimes()
		if len(rts) == 0 {
			return nil, fmt.Errorf("no PHP installed; run: agent-local install php 8.3")
		}
		php = rts[len(rts)-1]
	}
	if e.Store.Inventory().FindPHP(php) == nil {
		return nil, fmt.Errorf("php %s not installed", php)
	}

	// Target location.
	targetWPDir := docroot
	if o.Copy {
		targetWPDir = filepath.Join(P().Sites(), slug, "wp")
		cb("files", "copying docroot → "+targetWPDir)
		if err := os.MkdirAll(filepath.Dir(targetWPDir), 0o755); err != nil {
			return nil, err
		}
		if err := runCmdQuiet("cp", "-R", docroot, targetWPDir); err != nil {
			return nil, fmt.Errorf("copy docroot: %w", err)
		}
	}

	dbPass := randomPass(20)
	site := &Site{
		Name:       name,
		Slug:       slug,
		WorkDir:    filepath.Dir(targetWPDir),
		WPDir:      targetWPDir,
		Branch:     "main",
		PHPVersion: php,
		DBName:     "al_" + slug,
		DBUser:     "al_" + slug,
		DBPass:     dbPass,
		Domain:     domain,
		HTTPPort:   DefaultHTTPPort,
		HTTPSPort:  DefaultHTTPSPort,
		CreatedAt:  time.Now(),
		State:      StateStopped,
	}

	// Database stage: serve-only keeps whatever wp-config already points at.
	if !o.ServeOnly {
		cb("database", "provisioning "+site.DBName)
		if err := e.CreateSiteDB(site); err != nil {
			return nil, fmt.Errorf("provision db: %w", err)
		}
		switch {
		case o.SQLDump != "":
			cb("database", "loading "+o.SQLDump)
			if err := e.loadSQLFile(site, o.SQLDump); err != nil {
				e.DropSiteDB(site)
				return nil, fmt.Errorf("load sql: %w", err)
			}
		case e.ownDatabase(srcHost, srcPort, srcSocket) && srcDBName == site.DBName:
			// The folder already lives on our MariaDB, in the schema this site
			// will use: the data is where it needs to be. Copying it onto
			// itself would drop the tables it is reading from.
			cb("database", "already on the local engine as "+srcDBName+" — keeping it")
		default:
			from := srcSocket
			if from == "" {
				from = fmt.Sprintf("%s:%d", srcHost, srcPort)
			}
			user, pass := srcUser, srcPass
			if e.ownDatabase(srcHost, srcPort, srcSocket) {
				// We own this server, so authenticate as root: the wp-config
				// credentials for a site we just provisioned are already stale.
				user, pass = "root", DBRootPassword()
			}
			cb("database", fmt.Sprintf("dumping %s from %s", srcDBName, from))
			if err := e.copyDatabase(site, srcDBName, srcSocket, srcHost, srcPort, user, pass, cb); err != nil {
				e.DropSiteDB(site)
				return nil, fmt.Errorf("copy database %s from %s as %s: %w", srcDBName, from, user, err)
			}
		}
		cb("config", "rewriting wp-config.php")
		if err := rewriteWPConfigDB(filepath.Join(targetWPDir, "wp-config.php"), site); err != nil {
			e.DropSiteDB(site)
			return nil, err
		}
	} else {
		cb("config", "serve-only: keeping the existing wp-config.php database settings")
		cfg := filepath.Join(targetWPDir, "wp-config.php")
		if v := readWPConfigConst(cfg, "DB_NAME"); v != "" {
			site.DBName = v
		}
		if v := readWPConfigConst(cfg, "DB_USER"); v != "" {
			site.DBUser = v
		}
		if v := readWPConfigConst(cfg, "DB_PASSWORD"); v != "" {
			site.DBPass = v
		}
	}

	// 4) persist + serve
	site.State = StateRunning
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		return nil, err
	}
	if err := e.StartSite(slug); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	oldDomain := ""
	if sites, err := ListLocalWPSites(); err == nil {
		for _, s := range sites {
			if s.Name == o.Source || s.ID == o.Source {
				oldDomain = s.Domain
			}
		}
	}
	// 5) rewrite stored URLs/domains so the copied content serves on the new
	// domain. Source domains come from the DB itself (siteurl/home) because
	// LocalWP sites often carry staging subdomains, plus the registry domain.
	oldDomains := map[string]bool{}
	for _, h := range e.siteHostsFromDB(site) {
		oldDomains[h] = true
	}
	if oldDomain != "" {
		oldDomains[oldDomain] = true
	}
	for _, h := range []string{domain, ""} {
		delete(oldDomains, h)
	}
	for old := range oldDomains {
		cb("urls", fmt.Sprintf("search-replace %s → %s", old, domain))
		for _, scheme := range []string{"https://", "http://"} {
			if out, err := wpCLI(site, "search-replace",
				scheme+old, scheme+domain,
				"--all-tables", "--skip-columns=guid"); err != nil {
				cb("warn", "search-replace: "+tail(out, 200))
			}
		}
	}

	// 6) theme-level overrides (EFront: EFRONT_URL_OVERRIDE → WP_HOME/WP_SITEURL)
	// pin the old domain in wp-config.php; rewrite any constant holding it.
	if len(oldDomains) > 0 {
		rewriteWPConfigDomains(filepath.Join(targetWPDir, "wp-config.php"), oldDomains, domain)
	}

	cb("dns", "registering "+domain)
	if n, err := EnsureHosts(e.HostsInteractive, []string{domain}); err != nil {
		cb("warn", "hosts entry needs root: "+err.Error())
	} else if n > 0 {
		cb("dns", "added /etc/hosts entry")
	}
	if cert, _, created, err := EnsureCert(domain); err == nil && created {
		_ = TrustCert(cert, false)
	}

	cb("done", BareURL(site))
	return site, nil
}

// copyDatabase dumps the source DB and replays it into our engine,
// normalizing MySQL-8 collations to MariaDB-compatible ones. The dump
// streams through a transform goroutine — memory stays flat regardless of DB size.
func (e *Engine) copyDatabase(site *Site, srcDB, srcSocket, srcHost string, srcPort int, srcUser, srcPass string, cb func(string, string)) error {
	if err := e.EnsureDB(); err != nil {
		return err
	}
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	dumpBin := filepath.Join(bindir, "mariadb-dump")
	if !fileExists(dumpBin) {
		dumpBin = filepath.Join(bindir, "mysqldump")
	}
	clientBin := filepath.Join(bindir, "mariadb")
	if !fileExists(clientBin) {
		clientBin = filepath.Join(bindir, "mysql")
	}

	// --no-defaults: a my.cnf carrying `socket=` or `protocol=socket` would
	// otherwise override the transport chosen below.
	dumpArgs := []string{
		"--no-defaults",
		"--user=" + srcUser, "--password=" + srcPass,
		"--single-transaction", "--quick", "--add-drop-table",
		"--skip-lock-tables", "--no-tablespaces",
		"--default-character-set=utf8mb4",
	}
	if srcSocket != "" {
		dumpArgs = append(dumpArgs, "--socket="+srcSocket)
	} else {
		dumpArgs = append(dumpArgs, "--host="+srcHost, fmt.Sprintf("--port=%d", srcPort))
	}
	dumpArgs = append(dumpArgs, srcDB)
	dump := exec.Command(dumpBin, dumpArgs...)
	load := exec.Command(clientBin, "--no-defaults",
		"-uroot", "--password="+DBRootPassword(), "-h127.0.0.1", "-P", fmt.Sprint(DefaultDBPort), site.DBName)

	dumpOut, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	var dumpErr strings.Builder
	dump.Stderr = &dumpErr
	loadIn, err := load.StdinPipe()
	if err != nil {
		return err
	}
	var loadErr strings.Builder
	load.Stderr = &loadErr

	if err := dump.Start(); err != nil {
		return fmt.Errorf("start dump: %w", err)
	}
	if err := load.Start(); err != nil {
		dump.Process.Kill()
		return fmt.Errorf("start load: %w", err)
	}

	// dump → collation fixer → load
	sc := bufio.NewScanner(dumpOut)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	streamErr := func() error {
		defer loadIn.Close()
		for sc.Scan() {
			line := fixCollations(sc.Bytes())
			if _, err := loadIn.Write(line); err != nil {
				return err
			}
			if _, err := loadIn.Write([]byte("\n")); err != nil {
				return err
			}
		}
		return sc.Err()
	}()

	dumpWait := dump.Wait()
	loadWait := load.Wait()
	if dumpWait != nil {
		return fmt.Errorf("dump: %w (%s)", dumpWait, tail(dumpErr.String(), 300))
	}
	if streamErr != nil {
		return fmt.Errorf("stream: %w", streamErr)
	}
	if loadWait != nil {
		return fmt.Errorf("load: %w (%s)", loadWait, tail(loadErr.String(), 300))
	}
	return nil
}

var collationRe = regexp.MustCompile(`utf8mb4_0900_ai_ci`)

func fixCollations(b []byte) []byte {
	return collationRe.ReplaceAll(b, []byte("utf8mb4_unicode_ci"))
}

// loadSQLFile streams a .sql dump into the site's database through the same
// collation fixer the live copy uses. Flat memory regardless of dump size.
// Gzipped dumps are detected by magic bytes, not by file extension.
func (e *Engine) loadSQLFile(site *Site, path string) error {
	if err := e.EnsureDB(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var src io.Reader = f
	head, _ := bufio.NewReader(f).Peek(2)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return fmt.Errorf("gzip: %w", gerr)
		}
		defer gz.Close()
		src = gz
	}
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	clientBin := filepath.Join(bindir, "mariadb")
	if !fileExists(clientBin) {
		clientBin = filepath.Join(bindir, "mysql")
	}
	load := exec.Command(clientBin, "--no-defaults",
		"-uroot", "--password="+DBRootPassword(), "-h127.0.0.1", "-P", fmt.Sprint(DefaultDBPort), site.DBName)
	loadIn, err := load.StdinPipe()
	if err != nil {
		return err
	}
	var loadErr strings.Builder
	load.Stderr = &loadErr
	if err := load.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	streamErr := func() error {
		defer loadIn.Close()
		for sc.Scan() {
			line := fixCollations(sc.Bytes())
			if _, err := loadIn.Write(line); err != nil {
				return err
			}
			if _, err := loadIn.Write([]byte("\n")); err != nil {
				return err
			}
		}
		return sc.Err()
	}()
	if waitErr := load.Wait(); waitErr != nil {
		return fmt.Errorf("%w (%s)", waitErr, tail(loadErr.String(), 300))
	}
	return streamErr
}

// ImportSQL loads a .sql (or .sql.gz) dump into an existing site's database,
// replacing what is there: the dump's own CREATE TABLE statements would
// otherwise collide with surviving tables from the previous contents.
// With rewriteURLs, the domains the dump was recorded under are search-replaced
// to this site's domain, so an imported dump serves on its own host instead of
// redirecting to wherever it came from.
func (e *Engine) ImportSQL(slug, path string, rewriteURLs bool) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	if !fileExists(path) {
		return "", fmt.Errorf("no such file: %s", path)
	}
	if err := e.ResetDB(slug); err != nil {
		return "", err
	}
	if err := e.loadSQLFile(site, path); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("imported %s into %s (%d tables)", filepath.Base(path), site.DBName, e.tableCount(site))
	if !rewriteURLs {
		return msg, nil
	}
	olds := map[string]bool{}
	for _, h := range e.siteHostsFromDB(site) {
		if h != site.Domain {
			olds[h] = true
		}
	}
	for old := range olds {
		for _, scheme := range []string{"https://", "http://"} {
			_, _ = wpCLI(site, "search-replace", scheme+old, scheme+site.Domain,
				"--all-tables", "--skip-columns=guid", "--skip-plugins", "--skip-themes")
		}
		msg += fmt.Sprintf(", urls %s → %s", old, site.Domain)
	}
	if warn := e.missingActiveAssets(site); warn != "" {
		msg += "; " + warn
	}
	return msg, nil
}

// missingActiveAssets reports theme/plugin files the imported database expects
// but the target does not have. A dump alone carries no files, and the usual
// symptom is a blank 200 from a missing active theme — say so instead of
// leaving the agent to guess.
func (e *Engine) missingActiveAssets(site *Site) string {
	hosts := []string{}
	for _, opt := range []string{"template", "stylesheet"} {
		out, err := e.DBIn(site.DBName, fmt.Sprintf(
			"SELECT option_value FROM wp_options WHERE option_name='%s'", opt))
		if err != nil {
			return ""
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) < 2 {
			continue
		}
		name := strings.TrimSpace(lines[1])
		if name == "" {
			continue
		}
		if !fileExists(filepath.Join(site.WPDir, "wp-content", "themes", name)) && !contains(hosts, name) {
			hosts = append(hosts, name)
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	return fmt.Sprintf("WARNING: active theme %q is not in wp-content/themes — the site will render blank until the files are added (a dump carries no files; use import_site to bring files+DB together)", strings.Join(hosts, "/"))
}

// tableCount counts tables in a site's database.
func (e *Engine) tableCount(site *Site) int {
	out, err := e.DB(fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='%s'", site.DBName))
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(lines[1]))
	return n
}

// siteHostsFromDB reads the hosts WordPress currently believes it serves,
// straight from the database. wp-cli is unusable here: `option get` rejects
// --format=plain, and a dump can reference plugins whose files are absent.
// The options table is found by suffix so any $table_prefix works.
func (e *Engine) siteHostsFromDB(site *Site) []string {
	out, err := e.DB(fmt.Sprintf(
		"SELECT table_name FROM information_schema.tables WHERE table_schema='%s' "+
			"AND table_name LIKE '%%options' ORDER BY LENGTH(table_name) LIMIT 1", site.DBName))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil
	}
	table := strings.TrimSpace(lines[1])
	vals, err := e.DB(fmt.Sprintf(
		"SELECT option_value FROM `%s`.`%s` WHERE option_name IN ('siteurl','home')", site.DBName, table))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	hosts := []string{}
	for i, l := range strings.Split(strings.TrimSpace(vals), "\n") {
		if i == 0 {
			continue // header
		}
		if h := hostFromURL(strings.TrimSpace(l)); h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// ExportSQL dumps a site's database to path (default: <root>/dumps/<slug>-<ts>.sql).
func (e *Engine) ExportSQL(slug, path string) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	if err := e.EnsureDB(); err != nil {
		return "", err
	}
	if path == "" {
		dir := filepath.Join(P().Root, "dumps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%s.sql", slug, time.Now().Format("20060102-150405")))
	}
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	dump := filepath.Join(bindir, "mariadb-dump")
	if !fileExists(dump) {
		dump = filepath.Join(bindir, "mysqldump")
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd := exec.Command(dump, "--no-defaults", "-uroot", "--password="+DBRootPassword(), "-h127.0.0.1",
		"-P", fmt.Sprint(DefaultDBPort), "--single-transaction", "--quick",
		"--default-character-set=utf8mb4", site.DBName)
	var errb strings.Builder
	cmd.Stdout, cmd.Stderr = f, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dump: %v: %s", err, tail(dropClientNoise(errb.String()), 300))
	}
	fi, _ := f.Stat()
	return fmt.Sprintf("%s (%d bytes)", path, fi.Size()), nil
}

// ResetDB drops and recreates a site's database, keeping its user and grants.
func (e *Engine) ResetDB(slug string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if err := e.EnsureDB(); err != nil {
		return err
	}
	_, err := e.DB(fmt.Sprintf(
		"DROP DATABASE IF EXISTS `%s`;"+
			"CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1';"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';FLUSH PRIVILEGES;",
		site.DBName, site.DBName, site.DBName, site.DBUser, site.DBName, site.DBUser))
	return err
}

// readWPConfigConst extracts a define('NAME', 'value') from wp-config.php.
func readWPConfigConst(path, name string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`define\(\s*['"]` + name + `['"]\s*,\s*['"]([^'"]*)['"]`)
	if m := re.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// rewriteWPConfigDB points an existing wp-config.php at our DB, backing up first.
func rewriteWPConfigDB(path string, site *Site) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	bak := path + ".agent-local.bak"
	if !fileExists(bak) {
		os.WriteFile(bak, b, 0o644)
	}
	src := string(b)
	src = setWPConst(src, "DB_NAME", site.DBName)
	src = setWPConst(src, "DB_USER", site.DBUser)
	src = setWPConst(src, "DB_PASSWORD", site.DBPass)
	src = setWPConst(src, "DB_HOST", fmt.Sprintf("127.0.0.1:%d", DefaultDBPort))
	return os.WriteFile(path, []byte(src), 0o644)
}

// parseWPDBHost splits a WordPress DB_HOST value. WordPress accepts three
// shapes and they are not interchangeable at the client level:
//
//	"127.0.0.1"                 → TCP, default port
//	"127.0.0.1:10360"           → TCP, explicit port
//	"localhost:/tmp/mysql.sock" → unix socket (a colon then an absolute path)
//
// Returning the socket separately matters: a client given --host=localhost
// silently prefers a socket, which is how an import aimed at a TCP server ends
// up reporting "Can't connect to server on 'localhost'".
func parseWPDBHost(v string) (host string, port int, socket string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0, ""
	}
	h, rest, found := strings.Cut(v, ":")
	if !found {
		return h, 0, ""
	}
	if strings.HasPrefix(rest, "/") {
		return h, 0, rest
	}
	if n, err := strconv.Atoi(rest); err == nil {
		return h, n, ""
	}
	return h, 0, ""
}

// ownDatabase reports whether a source connection points at the MariaDB this
// app runs. Adopting a folder whose wp-config already targets us is the common
// case, and it must not be dumped through stale credentials: provisioning the
// site rotates that user's password moments earlier, so the old wp-config
// values are guaranteed wrong ("Access denied … using password: YES").
func (e *Engine) ownDatabase(host string, port int, socket string) bool {
	if socket != "" {
		return normalizePath(socket) == normalizePath(e.dbSock())
	}
	if port != DefaultDBPort {
		return false
	}
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}
func setWPConst(src, name, val string) string {
	re := regexp.MustCompile(`(?m)(define\(\s*['"]` + name + `['"]\s*,\s*)['"][^'"]*['"]`)
	return re.ReplaceAllString(src, `${1}'`+val+`'`)
}

// hostFromURL extracts the host (no port) from a URL-ish string.
func hostFromURL(u string) string {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

// rewriteWPConfigDomains replaces every occurrence of the old domains
// (scheme-prefixed or bare) inside wp-config.php with the new domain.
func rewriteWPConfigDomains(path string, olds map[string]bool, newDomain string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(b)
	for old := range olds {
		src = strings.ReplaceAll(src, "https://"+old, "https://"+newDomain)
		src = strings.ReplaceAll(src, "http://"+old, "https://"+newDomain)
		src = strings.ReplaceAll(src, old, newDomain)
	}
	return os.WriteFile(path, []byte(src), 0o644)
}

// matchInstalledPHP returns the closest installed PHP to want, or "".
func matchInstalledPHP(s *Store, want string) string {
	if s.Inventory().FindPHP(want) != nil {
		return want
	}
	return ""
}
