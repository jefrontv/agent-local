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

// EnsureHTTPFront guarantees the configured HTTP front is listening AND that
// the control daemon is alive. The agent API lives in the daemon, so it must
// keep running in apache mode too — otherwise switching front would leave
// agents with no way to switch back.
func EnsureHTTPFront(store *Store) error {
	if err := applyFront(store); err != nil {
		return err
	}
	return EnsureDaemon(store)
}

// applyFront brings up the serving layer only. Router mode is served inside
// the daemon process, so it has nothing to do here.
func applyFront(store *Store) error {
	if store.Data.Front == "apache" {
		return EnsureApacheFront(store)
	}
	return nil
}

// EnsureDaemon makes sure exactly one daemon is running and that it owns the
// HTTP ports when the front is the built-in router. A daemon started under
// the other front is replaced rather than trusted.
func EnsureDaemon(store *Store) error {
	wantPorts := FrontKind(store) == "router"
	apiUp, httpUp := portOpen(DefaultAPIPort), portOpen(DefaultHTTPPort)
	if apiUp && (!wantPorts || httpUp) {
		return nil
	}
	if apiUp {
		StopDaemons()
	}
	if err := spawnDaemon(); err != nil {
		return err
	}
	if err := waitPort(DefaultAPIPort, 10*time.Second); err != nil {
		return err
	}
	if wantPorts {
		return waitPort(DefaultHTTPPort, 10*time.Second)
	}
	return nil
}

// FrontKind returns the configured front name.
func FrontKind(store *Store) string {
	if store.Data.Front == "apache" {
		return "apache"
	}
	return "router"
}

// StopDaemons terminates every agent-local daemon (pid file plus any process
// orphaned by an earlier restart) and waits for it to be gone, so the next
// spawn cannot lose a bind race. The launchd job is booted out first: killing
// its process while the job stays loaded would only have launchd (KeepAlive
// on failure) race the next spawn for the ports — and a signalled exit counts
// as failure.
//
// The wait is for the processes, not just the API port. SIGTERM makes the
// daemon close its API listener first and then spend up to several seconds
// draining jobs and shares; a caller that stopped waiting at "port closed"
// spawned the replacement into that window, launchd saw two instances, and
// both restarted. That was the 8-second first call after every restart.
func StopDaemons() {
	self := os.Getpid()
	runCmdQuiet("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonAgentLabel))
	var pids []int
	if b, err := os.ReadFile(filepath.Join(P().Run(), "daemon.pid")); err == nil {
		var pid int
		fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid)
		if pid > 0 && pid != self && Alive(pid) {
			pids = append(pids, pid)
		}
	}
	if out, err := runCmdOut("pgrep", "-f", "agent-local daemon"); err == nil {
		for _, f := range strings.Fields(out) {
			pid, err := strconv.Atoi(f)
			if err != nil || pid == self {
				continue
			}
			pids = append(pids, pid)
		}
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		live := false
		for _, pid := range pids {
			if Alive(pid) {
				live = true
				break
			}
		}
		if !live && !portOpen(DefaultAPIPort) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	os.Remove(filepath.Join(P().Run(), "daemon.pid"))
}

// spawnDaemon brings the daemon up. No-op when one already answers (avoids
// duplicate-bind races).
//
// It goes through launchd, not a direct fork. A process forked from a
// terminal inherits that terminal's macOS file-access grants: launched from
// an app without Documents access, the daemon serves PHP fine (php-fpm's
// own grant) but every static file under ~/Documents comes back 403, and
// pools it starts fail to read their plugins. launchd runs the job in the
// user's session with the user's grants, which is the context sites live
// in. The direct fork remains as the fallback for the rare machine where
// the agent cannot load.
func spawnDaemon() error {
	if portOpen(DefaultAPIPort) {
		return nil
	}
	if err := EnsureDaemonAutostart(); err == nil {
		// EnsureDaemonAutostart bootstraps the agent and RunAtLoad starts it;
		// when the job was already loaded (just idle after a clean exit),
		// kickstart is what actually starts it.
		label := fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonAgentLabel)
		runCmdQuiet("launchctl", "kickstart", label)
		if waitPort(DefaultAPIPort, 8*time.Second) == nil {
			return nil
		}
		// Slow to answer but loaded: launchd owns it and will bring it up.
		// Forking a second daemon here is how an unsupervised instance ended
		// up holding the ports while the supervised one sat in standby. Only
		// fall through when launchd genuinely has no such job.
		if runCmdQuiet("launchctl", "print", label) == nil {
			return waitPort(DefaultAPIPort, 20*time.Second)
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(P().Log("daemon"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "daemon", "--background")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	return cmd.Start()
}

// ---------- Apache front ----------

// EnsureApacheFront renders config and starts httpd on the shared ports.
func EnsureApacheFront(store *Store) error {
	inv := store.Inventory()
	if inv.HTTP.Bin == "" {
		return fmt.Errorf("apache not installed; run: agent-local install apache")
	}
	conf := P().ApacheConf()
	if err := renderApacheConf(store); err != nil {
		return err
	}
	proc := &Proc{
		Name:  "apache",
		Args:  apacheStartArgs(inv.HTTP.Bin, conf),
		LogTo: P().Log("apache"),
		PidTo: P().ApachePid(),
	}
	if pid, ok := proc.Pid(); ok {
		// Re-rendered config; ask the running master to pick it up. New sites
		// used to 502 until someone ran `front apache` again.
		_ = syscall.Kill(pid, syscall.SIGUSR1)
		return nil
	}
	// Something else holds the port (usually the router after a front switch).
	if portOpen(DefaultHTTPPort) {
		return nil
	}
	if _, err := proc.Start(); err != nil {
		return err
	}
	return waitPort(DefaultHTTPPort, 8*time.Second)
}

// renderApacheConf writes a self-contained httpd.conf serving every site +
// worktree through mod_proxy_fcgi.
func renderApacheConf(store *Store) error {
	p := P()
	prefix := filepath.Dir(filepath.Dir(store.Inventory().HTTP.Bin))
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`# Generated by agent-local
ServerRoot "%s"
Listen 127.0.0.1:%d
Listen 127.0.0.1:%d

`, prefix, DefaultHTTPPort, DefaultHTTPSPort))
	// Load what this httpd actually ships. mod_access_compat and friends keep
	// production .htaccess files working (WordPress sites carry Apache 2.2-era
	// directives like `Order allow,deny`), and skipping absent modules keeps
	// the config portable across httpd builds.
	for _, m := range []string{
		"mpm_event", "authz_core", "authz_host", "authz_user", "authn_core",
		"auth_basic", "access_compat", "log_config", "env", "proxy",
		"proxy_fcgi", "proxy_http", "unixd", "dir", "mime", "rewrite", "alias", "filter",
		"headers", "setenvif", "expires", "deflate", "ssl", "socache_shmcb",
	} {
		rel := filepath.Join("lib", "httpd", "modules", "mod_"+m+".so")
		if fileExists(filepath.Join(prefix, rel)) {
			b.WriteString(fmt.Sprintf("LoadModule %s_module %s\n", m, rel))
		}
	}
	b.WriteString(fmt.Sprintf(`
PidFile %s
ErrorLog %s
LogLevel warn

User %s
Group staff

TypesConfig %s
DirectoryIndex index.php index.html

<Directory />
  Require all granted
  AllowOverride All
</Directory>
# The router's sensitivePath rule, for Apache: dot directories and dotfiles
# (the ACME directory excepted), wp-config variants other than the .php itself,
# and dumps, logs and editor leftovers.
<DirectoryMatch "/\.(?!well-known(/|$))">
  Require all denied
</DirectoryMatch>
<FilesMatch "^\.|^wp-config\.php.+|\.(sql|sql\.gz|sql\.zip|log|bak|old|orig|save|swp)$|~$">
  Require all denied
</FilesMatch>

`, p.ApachePid(), p.Log("apache"), os.Getenv("USER"), mimeTypesPath(prefix)))

	adminerDir := P().AdminerDir()
	emit := func(id, domain, docroot, sock, adminerBoot string) {
		vhost := func(port int, tls bool) {
			b.WriteString(fmt.Sprintf("<VirtualHost 127.0.0.1:%d>\n  ServerName %s\n  DocumentRoot \"%s\"\n", port, domain, docroot))
			if tls {
				crt, key := CertPaths(domain)
				b.WriteString(fmt.Sprintf("  SSLEngine on\n  SSLCertificateFile \"%s\"\n  SSLCertificateKeyFile \"%s\"\n", crt, key))
			}
			if adminerBoot != "" {
				b.WriteString(fmt.Sprintf("  Alias %s \"%s\"\n", AdminerPath, adminerBoot))
			}
			// The inbox UI is rendered by the daemon; apache only forwards.
			// The pool id keys the inbox, so previews keep their own.
			b.WriteString(fmt.Sprintf("  ProxyPass %s http://127.0.0.1:%d/mail-ui/%s\n  ProxyPassReverse %s http://127.0.0.1:%d/mail-ui/%s\n",
				MailPath, DefaultAPIPort, id, MailPath, DefaultAPIPort, id))
			// The tooling index is one exact path, not a subtree: anchor the
			// regex so the adminer Alias and the inbox ProxyPass above keep
			// winning their longer, more specific paths.
			b.WriteString(fmt.Sprintf("  ProxyPassMatch ^/\\.agent-local/?$ http://127.0.0.1:%d/hub-ui/%s\n",
				DefaultAPIPort, id))
			b.WriteString(fmt.Sprintf(`  <FilesMatch \.php$>
    SetHandler "proxy:unix:%s|fcgi://localhost"
  </FilesMatch>
  RewriteEngine On
  RewriteCond %%{REQUEST_URI} !^/\.agent-local/
  RewriteCond %%{REQUEST_URI} !^/\.agent-local/?$
  RewriteCond %%{REQUEST_FILENAME} !-f
  RewriteCond %%{REQUEST_FILENAME} !-d
  RewriteRule ^ /index.php [L]
</VirtualHost>

`, sock))
		}
		vhost(DefaultHTTPPort, false)
		// TLS needs a cert on disk; sites get one at create/import time.
		if crt, key := CertPaths(domain); fileExists(crt) && fileExists(key) {
			vhost(DefaultHTTPSPort, true)
		}
	}

	if adminerDir != "" {
		b.WriteString(fmt.Sprintf(`<Directory "%s">
  Require all granted
  AllowOverride None
</Directory>

`, adminerDir))
	}

	e := NewEngine(store)
	for _, site := range store.Sites() {
		boot := adminerBootIfReady(site)
		emit(site.Slug, site.Domain, site.WPDir, e.fpmSock(site.Slug), boot)
		for _, a := range site.Aliases {
			emit(site.Slug, a, site.WPDir, e.fpmSock(site.Slug), boot)
		}
	}
	for _, w := range store.Data.Worktrees {
		emit(w.ID, w.Domain, e.wtServeDir(w), e.fpmSock(w.ID), adminerBootIfReady(store.Site(w.Site)))
	}
	return os.WriteFile(p.ApacheConf(), []byte(b.String()), 0o644)
}

// apacheStartArgs is httpd in the foreground with a real MPM, not -X
// (single-process debug mode, which serialized every request).
func apacheStartArgs(bin, conf string) []string {
	return []string{bin, "-f", conf, "-DFOREGROUND"}
}

// mimeTypesPath finds httpd's mime.types. Homebrew's httpd ships it under the
// keg's own etc, not $(brew --prefix)/etc, and Apache refuses to start when
// TypesConfig points at a missing file.
func mimeTypesPath(prefix string) string {
	for _, c := range []string{
		filepath.Join(prefix, "etc", "mime.types"),
		filepath.Join(prefix, "conf", "mime.types"),
		"/opt/homebrew/etc/httpd/mime.types",
		"/etc/apache2/mime.types",
	} {
		if fileExists(c) {
			return c
		}
	}
	return filepath.Join(prefix, "conf", "mime.types")
}

// StopApache stops the apache front and waits for the shared HTTP port to
// free. Apache re-execs and rewrites its own pid file, so the pid file alone
// is not authoritative: any httpd still running our generated config is
// terminated too, otherwise the next front cannot bind.
func StopApache() error {
	proc := &Proc{Name: "apache", PidTo: P().ApachePid()}
	err := proc.Stop()
	if out, perr := runCmdOut("pgrep", "-f", P().ApacheConf()); perr == nil {
		self := os.Getpid()
		for _, f := range strings.Fields(out) {
			pid, cerr := strconv.Atoi(f)
			if cerr != nil || pid == self {
				continue
			}
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && portOpen(DefaultHTTPPort) {
		time.Sleep(150 * time.Millisecond)
	}
	if portOpen(DefaultHTTPPort) {
		return fmt.Errorf("port %d still held after stopping apache", DefaultHTTPPort)
	}
	return err
}

// RestartApache re-renders config and restarts the front.
func RestartApache(store *Store) error {
	_ = StopApache()
	time.Sleep(200 * time.Millisecond)
	return EnsureApacheFront(store)
}
