package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AdminerPath is the reserved URL path that serves the database GUI for
// whatever site the Host header resolved to. Kept out of the WordPress tree
// so a permalink cannot swallow it and so deleting a site never leaves a
// PHP admin tool in the user's checkout.
const AdminerPath = "/.agent-local/adminer"

// adminerDownload is the official single-file Adminer 4.8.1 release.
const adminerDownload = "https://github.com/vrana/adminer/releases/download/v4.8.1/adminer-4.8.1.php"

func (p Paths) AdminerDir() string             { return filepath.Join(p.Root, "lib", "adminer") }
func (p Paths) AdminerPHP() string             { return filepath.Join(p.AdminerDir(), "adminer.php") }
func (p Paths) AdminerBoot(slug string) string { return filepath.Join(p.AdminerDir(), slug+".php") }

// AdminerURL is the browser URL for a site's database GUI.
func AdminerURL(domain string) string {
	return strings.TrimRight(BareDomainURL(domain), "/") + AdminerPath
}

// EnsureAdminer downloads Adminer once into ~/.agent-local/lib/adminer.
func EnsureAdminer() error {
	p := P()
	if err := os.MkdirAll(p.AdminerDir(), 0o755); err != nil {
		return err
	}
	dst := p.AdminerPHP()
	if fileExists(dst) {
		return nil
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(adminerDownload)
	if err != nil {
		return fmt.Errorf("download adminer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download adminer: HTTP %s", resp.Status)
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	return os.Rename(tmp, dst)
}

// adminerBootIfReady writes the wrapper only when Adminer is already on disk,
// so generating apache config never hits the network.
func adminerBootIfReady(site *Site) string {
	if site == nil || !fileExists(P().AdminerPHP()) {
		return ""
	}
	boot, err := writeAdminerBoot(site)
	if err != nil {
		return ""
	}
	return boot
}

// writeAdminerBoot writes a per-site wrapper that auto-logs into that site's
// schema. The wrapper lives next to adminer.php, never in the WordPress tree.
func writeAdminerBoot(site *Site) (string, error) {
	if site == nil {
		return "", fmt.Errorf("no site")
	}
	if err := EnsureAdminer(); err != nil {
		return "", err
	}
	dst := P().AdminerBoot(site.Slug)
	host := fmt.Sprintf("127.0.0.1:%d", DefaultDBPort)
	body := fmt.Sprintf(`<?php
/**
 * agent-local database GUI for %s. Auto-generated — do not edit.
 *
 * login() returning true is not enough: Adminer still paints the form until
 * it sees $_POST['auth']. Inject that on the first GET so the first response
 * is a redirect into the database, not a login page.
 */
if (empty($_GET['username']) && empty($_POST['auth'])) {
	$_POST['auth'] = array(
		'driver' => 'server',
		'server' => %s,
		'username' => %s,
		'password' => %s,
		'db' => %s,
	);
}
function adminer_object() {
	class AgentLocalAdminer extends Adminer {
		function name() { return %s; }
		function credentials() { return array(%s, %s, %s); }
		function database() { return %s; }
		function login($login, $password) { return true; }
	}
	return new AgentLocalAdminer;
}
include __DIR__ . '/adminer.php';
`,
		site.Slug,
		phpQuote(host), phpQuote(site.DBUser), phpQuote(site.DBPass), phpQuote(site.DBName),
		phpQuote("agent-local · "+site.Slug),
		phpQuote(host), phpQuote(site.DBUser), phpQuote(site.DBPass),
		phpQuote(site.DBName),
	)
	if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// phpQuote is a single-quoted PHP string, safe for passwords.
func phpQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// isAdminerPath reports whether a request URL is the database GUI.
func isAdminerPath(urlPath string) bool {
	clean := strings.TrimSuffix(filepath.Clean("/"+urlPath), "/")
	return clean == AdminerPath
}
