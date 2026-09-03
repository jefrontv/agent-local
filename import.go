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
func (s LocalWPSite) Socket() string { return localWPSocketFor(s.ID) }

// localWPSocketFor is the socket a site's mysqld listens on, once it is running.
// A halted site has none, so this has to be asked again after starting one —
// resolving it only up front left the dump talking TCP, which LocalWP's MySQL
// rejects with "host is not allowed to connect".
func localWPSocketFor(id string) string {
	if id == "" {
		return ""
	}
	for _, name := range []string{"mysqld.sock", "mysql.sock"} {
		p := filepath.Join(HomeDir(), "Library", "Application Support", "Local", "run", id, "mysql", name)
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
	Source    string // a LocalWP site name, a DDEV project name, or a path to a docroot
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
	KeepDDEV  bool   // leave a DDEV source project registered and running; default moves it out
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
	var lwName, lwID, lwDomain, lwPHP string

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
			lwName, lwID, lwDomain = s.Name, s.ID, s.Domain
			lwPHP = s.Services.PHP.Version
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
	// DDEV project, by name or by path. Its database is only reachable while
	// the containers run, so the credentials come after ensureDDEVRunning.
	var ddevProj *DDEVProject
	if docroot == "" {
		if p := findDDEVProject(o.Source); p != nil {
			ddevProj = p
			lwName, lwPHP = p.Name, p.PHPVersion
			lwDomain = hostFromURL(p.PrimaryURL)
			docroot = p.DocrootPath()
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
		return nil, fmt.Errorf("source %q not found as a LocalWP site, a DDEV project, or a directory", o.Source)
	}
	if !fileExists(filepath.Join(docroot, "wp-load.php")) {
		if found := DocrootFor(docroot); fileExists(filepath.Join(found, "wp-load.php")) {
			docroot = found
		}
	}
	if !fileExists(filepath.Join(docroot, "wp-load.php")) {
		return nil, fmt.Errorf("no WordPress at %s (missing wp-load.php)", docroot)
	}

	// A halted LocalWP site has no mysqld, so the dump would fail with a bare
	// "can't connect" and leave the user to work out that they had to press Start.
	// Ask Local to start it and wait, unless this import never touches the source
	// database anyway.
	needsSourceDB := !o.ServeOnly && o.SQLDump == ""
	if needsSourceDB && lwID != "" {
		sock, err := ensureLocalWPRunning(lwID, lwName, srcSocket, srcHost, srcPort, cb)
		if err != nil {
			return nil, err
		}
		srcSocket = sock
	}
	if needsSourceDB && ddevProj != nil {
		full, err := ensureDDEVRunning(ddevProj, cb)
		if err != nil {
			return nil, err
		}
		ddevProj = full
		srcPort, srcUser, srcPass, srcDBName = full.dbCreds()
		srcHost = "127.0.0.1"
		lwPHP = full.PHPVersion
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

	// PHP version: requested → LocalWP's (major.minor) → 8.2 → highest installed.
	php := NormalizePHPVersion(o.PHPVer)
	if php == "" {
		php = matchInstalledPHP(e.Store, lwPHP)
	}
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
			os.RemoveAll(targetWPDir)
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
		if err := rewriteWPConfigDB(filepath.Join(targetWPDir, "wp-config.php"), site, e.tablePrefixFromDB(site)); err != nil {
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

	// 4) persist, then rewrite URLs *before* the site answers HTTP — otherwise
	// the first request can 301 to production and look like a failed import.
	site.State = StateStopped
	e.Store.PutSite(site)
	if err := e.Store.Save(); err != nil {
		return nil, err
	}

	if !o.ServeOnly {
		oldDomains := map[string]bool{}
		for _, h := range e.siteHostsFromDB(site) {
			oldDomains[h] = true
		}
		if lwDomain != "" {
			oldDomains[lwDomain] = true
		}
		for _, h := range []string{domain, ""} {
			delete(oldDomains, h)
		}
		if err := e.rewriteImportedURLs(site, oldDomains, cb); err != nil {
			return nil, fmt.Errorf("rewrite urls: %w", err)
		}
	}

	if err := e.StartSite(slug); err != nil {
		return nil, fmt.Errorf("start: %w", err)
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

	// The site is served from here now. Unless asked to keep it, the DDEV
	// project goes: containers, database volume and registration, with DDEV's
	// own snapshot as the way back. Files and .ddev/ stay untouched. A failure
	// here is reported, not fatal — the import itself already succeeded.
	if ddevProj != nil && !o.ServeOnly {
		if o.KeepDDEV {
			cb("ddev", ddevProj.Name+" left in DDEV — its wp-config now points here; restore wp-config.php.agent-local.bak (or `ddev snapshot restore`) to serve it from DDEV again")
		} else if err := detachDDEVProject(ddevProj.Name, cb); err != nil {
			cb("warn", "could not remove the DDEV project: "+err.Error()+" — finish with `ddev delete "+ddevProj.Name+"`")
		} else {
			cb("ddev", ddevProj.Name+" removed from DDEV; undo with `ddev start "+ddevProj.Name+"` then `ddev snapshot restore`")
		}
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
		// MariaDB 11+ clients insist on TLS over TCP unless told otherwise, and
		// a local source (a DDEV container, a LocalWP TCP port) has none to
		// offer: "SSL is required, but the server does not support it".
		// The loopback carries the traffic; TLS adds nothing here.
		if strings.HasPrefix(filepath.Base(dumpBin), "mariadb") {
			dumpArgs = append(dumpArgs, "--skip-ssl")
		}
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
		dump.Wait()
		return fmt.Errorf("start load: %w", err)
	}

	// dump → collation fixer → load. Byte stream, not line-scanner: a single
	// INSERT from postmeta routinely exceeds bufio.Scanner's token cap.
	streamErr := func() error {
		defer loadIn.Close()
		return streamFixCollations(loadIn, dumpOut)
	}()

	dumpWait := dump.Wait()
	loadWait := load.Wait()
	switch {
	case loadWait != nil:
		// A broken-pipe streamErr is usually just this: the load process
		// already exited and closed its stdin. The load's own error is the
		// one worth surfacing, not the write failure it caused.
		return fmt.Errorf("load: %w (%s)", loadWait, tail(loadErr.String(), 300))
	case dumpWait != nil:
		return fmt.Errorf("dump: %w (%s)", dumpWait, tail(dumpErr.String(), 300))
	case streamErr != nil:
		return fmt.Errorf("stream: %w", streamErr)
	}
	return nil
}

// collationReplacer maps MySQL-8+ collations MariaDB will refuse onto ones it
// ships. Longest token is 22 bytes; streamFixCollations keeps that much overlap
// so a name split across two reads still rewrites.
var collationReplacer = strings.NewReplacer(
	"utf8mb4_0900_ai_ci", "utf8mb4_unicode_ci",
	"utf8mb4_0900_as_ci", "utf8mb4_unicode_ci",
	"utf8mb4_0900_as_cs", "utf8mb4_bin",
	"utf8mb4_0900_bin", "utf8mb4_bin",
	"utf8mb4_uca1400_ai_ci", "utf8mb4_unicode_ci",
	"utf8mb4_uca1400_as_ci", "utf8mb4_unicode_ci",
	"utf8mb4_uca1400_as_cs", "utf8mb4_bin",
)

func fixCollations(b []byte) []byte {
	return []byte(collationReplacer.Replace(string(b)))
}

const collationOverlap = 32

// streamFixCollations copies src to dst, rewriting MySQL-8 collations as it
// goes. Memory stays one buffer regardless of dump size or line length.
func streamFixCollations(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 64*1024)
	var hold []byte
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := append(hold, buf[:n]...)
			hold = nil
			if err == nil {
				if len(chunk) > collationOverlap {
					hold = append([]byte(nil), chunk[len(chunk)-collationOverlap:]...)
					chunk = chunk[:len(chunk)-collationOverlap]
				} else {
					hold = chunk
					chunk = nil
				}
			}
			if len(chunk) > 0 {
				if _, werr := dst.Write(fixCollations(chunk)); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			if len(hold) > 0 {
				_, werr := dst.Write(fixCollations(hold))
				return werr
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
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
	streamErr := func() error {
		defer loadIn.Close()
		return streamFixCollations(loadIn, src)
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
// With snapshot, the current contents are saved first — the dump about to be
// replaced is often the only copy of local work.
func (e *Engine) ImportSQL(slug, path string, rewriteURLs, snapshot bool) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	if !fileExists(path) {
		return "", fmt.Errorf("no such file: %s", path)
	}
	saved := ""
	var preImportPath string
	if snapshot {
		took, err := e.autoSnapshot(slug, "import")
		if err != nil {
			return "", fmt.Errorf("pre-import snapshot: %w (--no-snapshot / no_snapshot skips it)", err)
		}
		if took != "" {
			saved = "saved " + took + ", "
			preImportPath = filepath.Join(P().SnapshotsDir(slug), took+".sql.gz")
		}
	}
	if err := e.ResetDB(slug); err != nil {
		return "", err
	}
	if err := e.loadSQLFile(site, path); err != nil {
		if preImportPath != "" {
			if rerr := e.rollbackLoad(slug, site, preImportPath); rerr != nil {
				return "", fmt.Errorf("import %s failed: %w; auto-restore of pre-import snapshot also failed: %v", filepath.Base(path), err, rerr)
			}
			return "", fmt.Errorf("import %s failed: %w; automatically rolled back to the pre-import snapshot", filepath.Base(path), err)
		}
		return "", err
	}
	msg := fmt.Sprintf("%simported %s into %s (%d tables)", saved, filepath.Base(path), site.DBName, e.tableCount(site))
	if !rewriteURLs {
		return msg, nil
	}
	olds := map[string]bool{}
	for _, h := range e.siteHostsFromDB(site) {
		if h != site.Domain {
			olds[h] = true
		}
	}
	if err := e.rewriteImportedURLs(site, olds, func(string, string) {}); err != nil {
		return msg, fmt.Errorf("imported %s but url rewrite failed: %w", site.DBName, err)
	}
	for old := range olds {
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
	// The options table carries whatever prefix the dump was written with;
	// assuming wp_ silenced this warning for exactly the hardened sites that
	// rename it.
	prefix := e.tablePrefixFromDB(site)
	table := prefix + "options"
	if !sqlIdentRe.MatchString(table) {
		return ""
	}
	hosts := []string{}
	for _, opt := range []string{"template", "stylesheet"} {
		out, err := e.DBIn(site.DBName, fmt.Sprintf(
			"SELECT option_value FROM %s WHERE option_name='%s'", quoteIdent(table), opt))
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

// sqlIdentRe matches an identifier safe to interpolate when backtick-quoted
// (or, for DBUser, single-quoted) in a SQL string. DBName/DBUser are normally
// ours ("al_"+slug, and slugs contain hyphens), but a serve-only import reads
// them straight out of someone else's wp-config.php — trust nothing that
// reaches SQL text without checking it. Backtick quoting makes hyphens safe;
// backticks, quotes, semicolons and whitespace stay rejected.
var sqlIdentRe = regexp.MustCompile(`^[A-Za-z0-9_$-]+$`)

// quoteIdent backtick-quotes a SQL identifier, doubling any embedded
// backtick. Belt-and-suspenders alongside requireSQLIdent: cheap, and it
// keeps a table name read back from information_schema from ever landing
// unescaped in a query we build.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// requireSQLIdent fails closed on any DBName/DBUser/prefix that is not a
// plain identifier, rather than trusting it inside a SQL string.
func requireSQLIdent(kind, name string) error {
	if !sqlIdentRe.MatchString(name) {
		return fmt.Errorf("refusing to use %q as a %s: not a plain identifier", name, kind)
	}
	return nil
}

// tableCount counts tables in a site's database.
func (e *Engine) tableCount(site *Site) int {
	if requireSQLIdent("database name", site.DBName) != nil {
		return 0
	}
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

// tablePrefixFromDB reads the $table_prefix a copied database was written
// with, from the shortest table ending in "options" (wp_options → "wp_").
// "" when the schema has no options table yet.
func (e *Engine) tablePrefixFromDB(site *Site) string {
	if requireSQLIdent("database name", site.DBName) != nil {
		return ""
	}
	out, err := e.DB(fmt.Sprintf(
		"SELECT table_name FROM information_schema.tables WHERE table_schema='%s' "+
			"AND table_name LIKE '%%options' ORDER BY LENGTH(table_name) LIMIT 1", site.DBName))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(lines[1]), "options")
}

// siteHostsFromDB reads the hosts WordPress currently believes it serves,
// straight from the database. wp-cli is unusable here: `option get` rejects
// --format=plain, and a dump can reference plugins whose files are absent.
// The options table is found by suffix so any $table_prefix works.
func (e *Engine) siteHostsFromDB(site *Site) []string {
	if requireSQLIdent("database name", site.DBName) != nil {
		return nil
	}
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
	if !sqlIdentRe.MatchString(table) {
		return nil
	}
	vals, err := e.DB(fmt.Sprintf(
		"SELECT option_value FROM %s.%s WHERE option_name IN ('siteurl','home')", quoteIdent(site.DBName), quoteIdent(table)))
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
	if path == "" {
		dir := filepath.Join(P().Root, "dumps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%s.sql", slug, time.Now().Format("20060102-150405")))
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	err = e.dumpDB(site, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		return "", statErr
	}
	return fmt.Sprintf("%s (%d bytes)", path, fi.Size()), nil
}

// dumpDB streams a logical dump of a site's database into w — the one place
// mariadb-dump is spawned, shared by exports and snapshots.
func (e *Engine) dumpDB(site *Site, w io.Writer) error {
	if err := e.EnsureDB(); err != nil {
		return err
	}
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	dump := filepath.Join(bindir, "mariadb-dump")
	if !fileExists(dump) {
		dump = filepath.Join(bindir, "mysqldump")
	}
	cmd := exec.Command(dump, "--no-defaults", "-uroot", "--password="+DBRootPassword(), "-h127.0.0.1",
		"-P", fmt.Sprint(DefaultDBPort), "--single-transaction", "--quick",
		"--default-character-set=utf8mb4", site.DBName)
	var errb strings.Builder
	cmd.Stdout, cmd.Stderr = w, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dump: %v: %s", err, tail(dropClientNoise(errb.String()), 300))
	}
	return nil
}

// ResetDB drops and recreates a site's database, keeping its user and grants.
func (e *Engine) ResetDB(slug string) error {
	site := e.Store.Site(slug)
	if site == nil {
		return fmt.Errorf("no such site: %s", slug)
	}
	if err := requireSQLIdent("database name", site.DBName); err != nil {
		return err
	}
	if err := requireSQLIdent("database user", site.DBUser); err != nil {
		return err
	}
	if err := e.EnsureDB(); err != nil {
		return err
	}
	db := quoteIdent(site.DBName)
	_, err := e.DB(fmt.Sprintf(
		"DROP DATABASE IF EXISTS %s;"+
			"CREATE DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"+
			"GRANT ALL PRIVILEGES ON %s.* TO '%s'@'127.0.0.1';"+
			"GRANT ALL PRIVILEGES ON %s.* TO '%s'@'localhost';FLUSH PRIVILEGES;",
		db, db, db, site.DBUser, db, site.DBUser))
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

// rewriteWPConfigDB points an existing wp-config.php at our DB, backing up
// first. A define that is not there is added: DDEV keeps its DB_* constants
// in wp-config-ddev.php behind an environment check, so its wp-config.php
// has none of them, and neither a $table_prefix.
func rewriteWPConfigDB(path string, site *Site, tablePrefix string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// The backup is what `delete` restores and what a user reaches for when an
	// import goes wrong; overwriting their config after a failed backup would
	// lose the only copy silently.
	bak := path + ".agent-local.bak"
	if !fileExists(bak) {
		if err := os.WriteFile(bak, b, 0o644); err != nil {
			return fmt.Errorf("back up wp-config.php: %w", err)
		}
	}
	src := string(b)
	src = setWPConst(src, "DB_NAME", site.DBName)
	src = setWPConst(src, "DB_USER", site.DBUser)
	src = setWPConst(src, "DB_PASSWORD", site.DBPass)
	src = setWPConst(src, "DB_HOST", fmt.Sprintf("127.0.0.1:%d", DefaultDBPort))
	if tablePrefix != "" && !regexp.MustCompile(`(?m)^\s*\$table_prefix\s*=`).MatchString(src) {
		src = insertAfterPHPOpen(src, fmt.Sprintf("$table_prefix = '%s';\n", strings.ReplaceAll(tablePrefix, "'", `\'`)))
	}
	return os.WriteFile(path, []byte(src), 0o644)
}

// insertAfterPHPOpen places a line right after the opening <?php tag, before
// anything the file itself does, with a marker so the addition is recognisable.
func insertAfterPHPOpen(src, line string) string {
	const marker = "// added by agent-local\n"
	i := strings.Index(src, "<?php")
	if i < 0 {
		return "<?php\n" + marker + line + src
	}
	end := i + len("<?php")
	if nl := strings.IndexByte(src[end:], '\n'); nl >= 0 {
		end += nl + 1
	} else {
		src += "\n"
		end = len(src)
	}
	if strings.HasPrefix(src[end:], marker) {
		end += len(marker)
		return src[:end] + line + src[end:]
	}
	return src[:end] + marker + line + src[end:]
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

// setWPConst rewrites define('NAME', '…') in place, or adds it near the top
// of the file when the constant is not defined at all.
func setWPConst(src, name, val string) string {
	re := regexp.MustCompile(`(?m)(define\(\s*['"]` + name + `['"]\s*,\s*)['"][^'"]*['"]`)
	if re.MatchString(src) {
		return re.ReplaceAllString(src, `${1}'`+val+`'`)
	}
	return insertAfterPHPOpen(src, fmt.Sprintf("define( '%s', '%s' );\n", name, strings.ReplaceAll(val, "'", `\'`)))
}

// hostFromURL extracts the host[:port] from a URL-ish string. The port stays:
// search-replace of "https://x.com" will not hit "https://x.com:8443".
func hostFromURL(u string) string {
	s := strings.TrimSpace(u)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// rewriteImportedURLs points stored URLs at the new local domain. Failures
// are returned rather than swallowed — an import that still redirects to
// production is not a successful import.
func (e *Engine) rewriteImportedURLs(site *Site, olds map[string]bool, cb func(string, string)) error {
	if len(olds) == 0 {
		return nil
	}
	if err := e.EnsureDB(); err != nil {
		return err
	}
	var first error
	for old := range olds {
		cb("urls", fmt.Sprintf("search-replace %s → %s", old, site.Domain))
		for _, scheme := range []string{"https://", "http://"} {
			out, err := wpCLI(site, "search-replace", scheme+old, scheme+site.Domain,
				"--all-tables", "--skip-columns=guid", "--skip-plugins", "--skip-themes")
			if err != nil && first == nil {
				first = fmt.Errorf("%s%s: %s", scheme, old, tail(out, 200))
			}
		}
	}
	if err := rewriteWPConfigDomains(filepath.Join(site.WPDir, "wp-config.php"), olds, site.Domain); err != nil && first == nil {
		first = err
	}
	return first
}

// rewriteWPConfigDomains rewrites scheme-prefixed URLs, then bare hostnames
// only inside define() string values. A global ReplaceAll of the hostname used
// to smash AUTH_KEY salts that happened to contain it.
func rewriteWPConfigDomains(path string, olds map[string]bool, newDomain string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(b)
	for old := range olds {
		if old == "" || old == newDomain {
			continue
		}
		src = strings.ReplaceAll(src, "https://"+old, "https://"+newDomain)
		src = strings.ReplaceAll(src, "http://"+old, "http://"+newDomain)
		// Bare host only in URL-ish constants. AUTH_KEY salts that happen to
		// contain the hostname must stay intact.
		re := regexp.MustCompile(`(define\(\s*'(?:WP_HOME|WP_SITEURL|EFRONT_URL_OVERRIDE|DOMAIN_CURRENT_SITE)'\s*,\s*')([^']*)` +
			regexp.QuoteMeta(old) + `([^']*'\s*\))`)
		src = re.ReplaceAllString(src, `${1}${2}`+newDomain+`${3}`)
	}
	return os.WriteFile(path, []byte(src), 0o644)
}

// matchInstalledPHP returns the closest installed PHP to want, or "".
// LocalWP reports "8.2.24"; our inventory keys are "8.2".
func matchInstalledPHP(s *Store, want string) string {
	want = strings.TrimSpace(want)
	if want == "" {
		return ""
	}
	if s.Inventory().FindPHP(want) != nil {
		return want
	}
	parts := strings.Split(want, ".")
	if len(parts) >= 2 {
		mm := parts[0] + "." + parts[1]
		if s.Inventory().FindPHP(mm) != nil {
			return mm
		}
	}
	return ""
}
