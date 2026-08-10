package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Root credentials for the embedded MariaDB.
//
// The control API is defended by a bearer token in a 0600 file, but for a long
// time the database underneath it was not defended at all: root had no password
// on 127.0.0.1:10360, so any process running as this user — an npm postinstall
// script, an editor extension — could read or drop every site's data without
// ever seeing the token. The token was not a security boundary for the data.
//
// So root gets a generated password, kept beside the token at 0600, and the
// hostname-based root account (root@<laptop>.local, which follows the machine
// onto any network) is removed.

var (
	rootPassOnce sync.Once
	rootPassVal  string
)

// dbRootPassPath is the 0600 file holding the generated root password.
func dbRootPassPath() string { return filepath.Join(P().Root, "db-root-pass") }

// DBRootPassword reads (or creates) the root password. Cached: it is read on
// every DB call.
func DBRootPassword() string {
	rootPassOnce.Do(func() {
		if b, err := os.ReadFile(dbRootPassPath()); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				rootPassVal = s
				return
			}
		}
		rootPassVal = randomPass(32)
		if err := os.MkdirAll(P().Root, 0o755); err == nil {
			// 0600: same posture as the API token. A world-readable password
			// file would defeat the point of having one.
			_ = os.WriteFile(dbRootPassPath(), []byte(rootPassVal), 0o600)
		}
	})
	return rootPassVal
}

// secureRoot brings the server's root accounts in line with the password file.
// Runs after every start, and is a no-op once the state matches.
//
// Three states are possible and all are handled: already password-protected
// with our password (nothing to do), still passwordless (set it — the migration
// for installs created before this existed), or password-protected with a
// password we do not have (report it, with the recovery, instead of failing
// obscurely on every later call).
func (e *Engine) secureRoot() error {
	pass := DBRootPassword()
	if _, err := e.dbExec(pass, "SELECT 1"); err == nil {
		return e.pruneRootHosts(pass)
	}
	if _, err := e.dbExec("", "SELECT 1"); err != nil {
		return fmt.Errorf("cannot authenticate to the local database as root. "+
			"If %s was lost, stop the server (agent-local stop-db) and re-create it, "+
			"or reset the password manually with --skip-grant-tables", dbRootPassPath())
	}
	// Passwordless: set our password on every root account that exists.
	hosts, err := e.rootHosts("")
	if err != nil {
		return err
	}
	var sb strings.Builder
	for _, h := range hosts {
		fmt.Fprintf(&sb, "ALTER USER 'root'@'%s' IDENTIFIED BY '%s';", h, pass)
	}
	sb.WriteString("FLUSH PRIVILEGES;")
	if _, err := e.dbExec("", sb.String()); err != nil {
		return fmt.Errorf("set root password: %w", err)
	}
	return e.pruneRootHosts(pass)
}

// pruneRootHosts drops root accounts that are not loopback. A
// root@<hostname>.local account is reachable from whatever network the machine
// joins next, which is not what a local dev database should offer.
func (e *Engine) pruneRootHosts(pass string) error {
	hosts, err := e.rootHosts(pass)
	if err != nil {
		return nil // not fatal: the password is set, which is the point
	}
	keep := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	var sb strings.Builder
	for _, h := range hosts {
		if !keep[h] {
			fmt.Fprintf(&sb, "DROP USER IF EXISTS 'root'@'%s';", h)
		}
	}
	if sb.Len() == 0 {
		return nil
	}
	sb.WriteString("FLUSH PRIVILEGES;")
	_, err = e.dbExec(pass, sb.String())
	return err
}

// rootHosts lists the hosts root accounts exist for.
func (e *Engine) rootHosts(pass string) ([]string, error) {
	out, err := e.dbExec(pass, "SELECT host FROM mysql.user WHERE user='root'")
	if err != nil {
		return nil, err
	}
	var hosts []string
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i == 0 {
			continue // header
		}
		if h := strings.TrimSpace(line); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts, nil
}
