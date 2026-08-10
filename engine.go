package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Engine drives databases, PHP-FPM pools, and the HTTP fronts.
type Engine struct {
	Store *Store
	// HostsInteractive lets CLI/TUI pop the macOS password dialog for
	// /etc/hosts edits; the daemon keeps this false to stay headless.
	HostsInteractive bool
}

// NewEngine wires an engine to the store.
func NewEngine(s *Store) *Engine { return &Engine{Store: s} }

// ---------- MySQL/MariaDB ----------

func (e *Engine) dbDir() string  { return filepath.Join(P().Engines(), "mysql", "data") }
func (e *Engine) dbSock() string { return P().Sock("mysql") }
func (e *Engine) dbLog() string  { return P().Log("mysql") }
func (e *Engine) dbPid() string  { return filepath.Join(P().Run(), "mysql.pid") }

// DBRunning reports whether the shared DB server is up.
func (e *Engine) DBRunning() bool { return portOpen(DefaultDBPort) }

func (e *Engine) EnsureDB() error {
	inv := e.Store.Inventory()
	if inv.MySQL.Bin == "" {
		return fmt.Errorf("no MySQL/MariaDB engine found; run: agent-local install mariadb")
	}
	if e.DBRunning() {
		// Cheap and idempotent: an install created before root had a password
		// gets migrated the first time anything touches the database.
		return e.secureRoot()
	}
	datadir := e.dbDir()
	if !fileExists(filepath.Join(datadir, "mysql")) {
		if err := e.initDB(datadir); err != nil {
			return err
		}
	}
	bindir := filepath.Dir(inv.MySQL.Bin)
	proc := &Proc{
		Name: "mysql",
		Args: []string{
			inv.MySQL.Bin,
			"--no-defaults",
			"--datadir=" + datadir,
			"--socket=" + e.dbSock(),
			"--port=" + fmt.Sprint(DefaultDBPort),
			"--bind-address=127.0.0.1",
			"--pid-file=" + e.dbPid(),
			"--skip-log-bin",
			"--log-error=" + e.dbLog(),
		},
		Env:   []string{"PATH=" + bindir + ":" + os.Getenv("PATH"), "MYSQL_HOME=" + filepath.Dir(datadir)},
		LogTo: e.dbLog(),
		PidTo: e.dbPid(),
	}
	if _, err := proc.Start(); err != nil {
		return err
	}
	if err := waitPort(DefaultDBPort, 20*time.Second); err != nil {
		return err
	}
	// Freshly initialised data dirs start with a passwordless root; close that
	// before anything else can connect.
	return e.secureRoot()
}

func (e *Engine) initDB(datadir string) error {
	if err := os.MkdirAll(datadir, 0o755); err != nil {
		return err
	}
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	var cmd *exec.Cmd
	if inv.MySQL.Kind == "mariadb" {
		installer := filepath.Join(bindir, "mariadb-install-db")
		if !fileExists(installer) {
			installer = filepath.Join(bindir, "mysql_install_db")
		}
		cmd = exec.Command(installer, "--defaults-file="+e.emptyDefaults(), "--datadir="+datadir, "--auth-root-authentication-method=normal")
	} else {
		cmd = exec.Command(inv.MySQL.Bin, "--no-defaults", "--initialize-insecure", "--datadir="+datadir)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("db init: %v\n%s", err, tail(string(out), 800))
	}
	return nil
}

// emptyDefaults writes an empty my.cnf so external configs (a broken
// mysql@8.4 my.cnf with mysqlx-* vars) never leak into our engine.
func (e *Engine) emptyDefaults() string {
	p := filepath.Join(P().Conf(), "empty.cnf")
	if !fileExists(p) {
		os.WriteFile(p, []byte("[mysqld]\n"), 0o644)
	}
	return p
}

// DB runs SQL against the shared server as root, with no default database.
func (e *Engine) DB(sql string) (string, error) { return e.DBIn("", sql) }

// DBIn runs SQL with an optional default database selected, returning the
// client's stdout as TSV with a header row.
func (e *Engine) DBIn(db, sql string) (string, error) {
	return e.dbExecIn(DBRootPassword(), db, sql)
}

// dbExec runs SQL as root with an explicit password ("" = no password). Used by
// the credential migration, which has to try both.
func (e *Engine) dbExec(pass, sql string) (string, error) { return e.dbExecIn(pass, "", sql) }

// dbExecIn is the one place a mysql client is spawned. The MariaDB client warns
// about passwordless logins on every invocation; that noise is dropped from
// results and only surfaced when the statement actually fails.
func (e *Engine) dbExecIn(pass, db, sql string) (string, error) {
	inv := e.Store.Inventory()
	bindir := filepath.Dir(inv.MySQL.Bin)
	client := filepath.Join(bindir, "mariadb")
	if !fileExists(client) {
		client = filepath.Join(bindir, "mysql")
	}
	if !fileExists(client) {
		client = "mysql"
	}
	args := []string{"--no-defaults", "-uroot", "-h127.0.0.1", "-P", fmt.Sprint(DefaultDBPort), "--batch"}
	if pass != "" {
		// Passed as an argument, not on stdin: the port is loopback-only and the
		// process list is already visible to this user, who owns the password
		// file anyway.
		args = append(args, "--password="+pass)
	}
	if db != "" {
		args = append(args, "-D", db)
	}
	args = append(args, "-e", sql)
	cmd := exec.Command(client, args...)
	cmd.Env = append(os.Environ(), "PATH="+bindir+":"+os.Getenv("PATH"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("mysql: %v: %s", err, tail(dropClientNoise(stderr.String()), 400))
	}
	return stdout.String(), nil
}

// dropClientNoise strips the MariaDB client's unconditional TLS/passwordless
// advisory so it never lands in an agent's result payload.
func dropClientNoise(s string) string {
	out := make([]string, 0, 4)
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "ssl-verify-server-cert") || strings.HasPrefix(l, "WARNING: option") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// CreateSiteDB provisions a database + user for a site.
// CREATE USER IF NOT EXISTS keeps a stale password, so ALTER USER follows
// to force the current creds even after a previous failed attempt.
func (e *Engine) CreateSiteDB(site *Site) error {
	if err := e.EnsureDB(); err != nil {
		return err
	}
	sql := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"+
			"CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY '%s';"+
			"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';"+
			"ALTER USER '%s'@'127.0.0.1' IDENTIFIED BY '%s';"+
			"ALTER USER '%s'@'localhost' IDENTIFIED BY '%s';"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1';"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';"+
			"FLUSH PRIVILEGES;",
		site.DBName, site.DBUser, site.DBPass, site.DBUser, site.DBPass,
		site.DBUser, site.DBPass, site.DBUser, site.DBPass,
		site.DBName, site.DBUser, site.DBName, site.DBUser)
	_, err := e.DB(sql)
	return err
}

// DropSiteDB removes the database + users.
func (e *Engine) DropSiteDB(site *Site) error {
	sql := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;"+
		"DROP USER IF EXISTS '%s'@'127.0.0.1';"+
		"DROP USER IF EXISTS '%s'@'localhost';",
		site.DBName, site.DBUser, site.DBUser)
	_, err := e.DB(sql)
	return err
}

// StopDB stops the shared DB server.
func (e *Engine) StopDB() error {
	proc := &Proc{Name: "mysql", PidTo: e.dbPid()}
	return proc.Stop()
}

// ---------- PHP-FPM ----------

// FPMName names the pool for a site or worktree.
func FPMName(id string) string { return "fpm-" + id }

func (e *Engine) fpmConf(id string) string { return filepath.Join(P().Conf(), FPMName(id)+".conf") }
func (e *Engine) fpmSock(id string) string { return P().Sock(FPMName(id)) }
func (e *Engine) fpmPid(id string) string  { return filepath.Join(P().Run(), FPMName(id)+".pid") }
func (e *Engine) fpmLog(id string) string  { return P().Log(FPMName(id)) }

// writeFPMConf renders a php-fpm pool config for one site/worktree.
func (e *Engine) writeFPMConf(id, wpdir, phpVersion string) error {
	rt := e.Store.Inventory().FindPHP(phpVersion)
	if rt == nil {
		return fmt.Errorf("php %s not installed", phpVersion)
	}
	if rt.FPM == "" {
		return fmt.Errorf("php %s has no php-fpm binary", phpVersion)
	}
	conf := fmt.Sprintf(`[global]
error_log = %s
daemonize = no

[%s]
user = %s
listen = %s
listen.owner = %s
pm = ondemand
pm.max_children = 8
pm.process_idle_timeout = 30s
pm.max_requests = 500
catch_workers_output = yes
clear_env = no
env[HOME] = %s
php_admin_value[error_log] = %s
php_admin_flag[log_errors] = on
php_value[memory_limit] = 512M
php_value[upload_max_filesize] = 128M
php_value[post_max_size] = 128M
php_value[max_execution_time] = 120
php_value[opcache.enable] = 1
php_value[opcache.memory_consumption] = 256
php_value[opcache.interned_strings_buffer] = 16
php_value[opcache.max_accelerated_files] = 20000
php_value[opcache.revalidate_freq] = 2
php_value[opcache.validate_timestamps] = 1
`,
		e.fpmLog(id), id, os.Getenv("USER"), e.fpmSock(id), os.Getenv("USER"),
		HomeDir(), e.fpmLog(id))
	return os.WriteFile(e.fpmConf(id), []byte(conf), 0o644)
}

// StartFPM launches a php-fpm pool for a site/worktree. It is idempotent: a
// healthy pool is left alone, and masters orphaned by earlier restarts (same
// pool config, no longer tracked by the pid file) are reaped so repeated
// starts cannot pile up processes holding unlinked sockets.
func (e *Engine) StartFPM(id, wpdir, phpVersion string) error {
	rt := e.Store.Inventory().FindPHP(phpVersion)
	if rt == nil {
		return fmt.Errorf("php %s not installed; run: agent-local install php %s", phpVersion, phpVersion)
	}
	tracked, isUp := (&Proc{PidTo: e.fpmPid(id)}).Pid()
	if isUp && fileExists(e.fpmSock(id)) {
		e.reapStrayFPM(id, tracked)
		return nil
	}
	if isUp {
		_ = e.StopFPM(id) // live master, dead socket: would deadlock
	}
	e.reapStrayFPM(id, 0)
	if err := e.writeFPMConf(id, wpdir, phpVersion); err != nil {
		return err
	}
	os.Remove(e.fpmSock(id)) // stale socket
	proc := &Proc{
		Name:  FPMName(id),
		Args:  []string{rt.FPM, "-y", e.fpmConf(id), "-F"},
		LogTo: e.fpmLog(id),
		PidTo: e.fpmPid(id),
	}
	if _, err := proc.Start(); err != nil {
		return err
	}
	// wait for socket
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fileExists(e.fpmSock(id)) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s: socket never appeared; see %s", FPMName(id), e.fpmLog(id))
}

// reapStrayFPM kills php-fpm masters running this pool's config other than
// keep. Orphans appear when a pid file is replaced while the old master lives.
func (e *Engine) reapStrayFPM(id string, keep int) {
	out, err := runCmdOut("pgrep", "-f", e.fpmConf(id))
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, f := range strings.Fields(out) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid == keep || pid == self {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGQUIT)
	}
}

// ensurePool boots the php-fpm pool for a site or worktree id if it is not
// already serving. Used by the router so a known domain never 503s just
// because a pool went away (reboot, daemon replacement, stale state).
func (e *Engine) ensurePool(id string) error {
	if fileExists(e.fpmSock(id)) {
		return nil
	}
	if e.Store.Site(id) != nil {
		return e.StartSite(id)
	}
	if _, ok := e.Store.Data.Worktrees[id]; ok {
		return e.StartWorktree(id)
	}
	return fmt.Errorf("unknown pool %s", id)
}

// StopFPM stops a pool. The generated config is left in place so a restart
// reuses it; RemovePool is the teardown that deletes it.
func (e *Engine) StopFPM(id string) error {
	proc := &Proc{Name: FPMName(id), PidTo: e.fpmPid(id)}
	err := proc.Stop()
	os.Remove(e.fpmSock(id))
	return err
}

// RemovePool deletes a pool's generated config and runtime files. Without this
// every deleted site left conf/fpm-<slug>.conf behind, so php-fpm parsed a
// growing pile of pools naming directories that no longer exist.
func (e *Engine) RemovePool(id string) {
	_ = e.StopFPM(id)
	os.Remove(e.fpmConf(id))
	os.Remove(e.fpmPid(id))
	os.Remove(e.fpmSock(id))
}

// SweepOrphanPools deletes pool configs with no matching site or worktree.
// sites.json is the authority for what exists, so anything else is residue from
// a delete that predates RemovePool.
func (e *Engine) SweepOrphanPools() int {
	live := map[string]bool{}
	for _, s := range e.Store.Sites() {
		live[s.Slug] = true
	}
	for id := range e.Store.Data.Worktrees {
		live[id] = true
	}
	entries, err := os.ReadDir(P().Conf())
	if err != nil {
		return 0
	}
	removed := 0
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "fpm-") || !strings.HasSuffix(name, ".conf") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "fpm-"), ".conf")
		if live[id] {
			continue
		}
		// A pool still serving is not an orphan: leave it and let its owner
		// stop it, rather than pulling the config out from under it.
		if e.FPMRunning(id) {
			continue
		}
		os.Remove(filepath.Join(P().Conf(), name))
		os.Remove(e.fpmPid(id))
		os.Remove(e.fpmSock(id))
		removed++
	}
	return removed
}

// FPMRunning reports pool liveness.
func (e *Engine) FPMRunning(id string) bool {
	proc := &Proc{Name: FPMName(id), PidTo: e.fpmPid(id)}
	_, ok := proc.Pid()
	return ok
}

// FPMRestart restarts a pool (php version switch).
func (e *Engine) FPMRestart(id, wpdir, phpVersion string) error {
	_ = e.StopFPM(id)
	return e.StartFPM(id, wpdir, phpVersion)
}

// Resolve returns the serving target for a domain: wpdir + fpm id.
// Checks worktrees first (more specific), then sites.
func (e *Engine) Resolve(domain string) (wpdir, fpmID, phpVersion string, ok bool) {
	for _, w := range e.Store.Data.Worktrees {
		if w.Domain == domain {
			site := e.Store.Site(w.Site)
			if site == nil {
				return "", "", "", false
			}
			return e.wtServeDir(w), w.ID, site.PHPVersion, true
		}
	}
	if site := e.Store.FindSiteByDomain(domain); site != nil {
		return site.WPDir, site.Slug, site.PHPVersion, true
	}
	return "", "", "", false
}

// wtServeDir is the docroot a worktree serves from: <path>/wp for
// WorkDir-style repos, the path itself when the repo root IS the docroot
// (imported checkouts like LocalWP's).
func (e *Engine) wtServeDir(w *Worktree) string {
	if fileExists(filepath.Join(w.Path, "wp", "wp-load.php")) {
		return w.Path + "/wp"
	}
	return w.Path
}

func tail(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	return "…" + strings.TrimSpace(s[len(s)-n:])
}
