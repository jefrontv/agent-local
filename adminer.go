package main

import (
	_ "embed"
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

// adminerVersion pins the official single-file release (MySQL driver,
// English). 4.8.1 was the last for years and breaks on PHP 8.5 / MariaDB 11
// ("array offset on null", every table stat a question mark); 5.x and 6.x
// track current PHP. The file on disk carries the version, so bumping this
// constant upgrades an existing install on its next request.
const adminerVersion = "6.0.1"

var adminerDownload = "https://github.com/vrana/adminer/releases/download/v" + adminerVersion + "/adminer-" + adminerVersion + "-mysql-en.php"

// adminerThemeCSS is the site's palette applied over Adminer's dark theme,
// served by the per-site wrapper at ?theme= so it works on either front.
//
//go:embed assets/adminer-theme.css
var adminerThemeCSS string

func (p Paths) AdminerDir() string { return filepath.Join(p.Root, "lib", "adminer") }
func (p Paths) AdminerPHP() string {
	return filepath.Join(p.AdminerDir(), "adminer-"+adminerVersion+".php")
}
func (p Paths) AdminerTheme() string           { return filepath.Join(p.AdminerDir(), "agent-local.css") }
func (p Paths) AdminerBoot(slug string) string { return filepath.Join(p.AdminerDir(), slug+".php") }

// AdminerURL is the browser URL for a site's database GUI.
func AdminerURL(domain string) string {
	return strings.TrimRight(BareDomainURL(domain), "/") + AdminerPath
}

// EnsureAdminer downloads the pinned Adminer once into
// ~/.agent-local/lib/adminer, drops any older release left there, and keeps
// the theme stylesheet in step with this binary.
func EnsureAdminer() error {
	p := P()
	if err := os.MkdirAll(p.AdminerDir(), 0o755); err != nil {
		return err
	}
	if cur, _ := os.ReadFile(p.AdminerTheme()); string(cur) != adminerThemeCSS {
		if err := os.WriteFile(p.AdminerTheme(), []byte(adminerThemeCSS), 0o644); err != nil {
			return err
		}
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
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	// Superseded releases are only a cache; an old one lying beside the new
	// is a trap for anyone reading the directory.
	old, _ := filepath.Glob(filepath.Join(p.AdminerDir(), "adminer*.php"))
	for _, f := range old {
		if f != dst {
			os.Remove(f)
		}
	}
	return nil
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
 * ?theme= serves the palette stylesheet from beside this file, so it reaches
 * the browser through whichever front is routing the GUI.
 *
 * Adminer 6 connects when the URL names a user and its session already holds
 * that user's password; the login form is what happens otherwise, and its
 * POST is CSRF-protected, so it cannot be faked. Adminer also only starts a
 * session when none is active. So: start its session first, under its name
 * and cookie path, seed the password, and send the browser to the URL it
 * expects. The first response is a redirect into the database.
 */
$server = %s;
$user = %s;
$pass = %s;
$db = %s;
$path = preg_replace('~\?.*~', '', $_SERVER['REQUEST_URI']);
if (isset($_GET['theme'])) {
	header('Content-Type: text/css; charset=utf-8');
	header('Cache-Control: public, max-age=86400');
	readfile(__DIR__ . '/agent-local.css');
	exit;
}
if (!isset($_GET['username']) && empty($_POST['logout'])) {
	header('Location: ' . $path . '?server=' . rawurlencode($server) . '&username=' . rawurlencode($user) . '&db=' . rawurlencode($db), true, 302);
	exit;
}
$https = (!empty($_SERVER['HTTPS']) && strcasecmp($_SERVER['HTTPS'], 'off')) || filter_var(ini_get('session.cookie_secure'), FILTER_VALIDATE_BOOLEAN);
session_cache_limiter('');
session_name('adminer_sid');
session_set_cookie_params(array('lifetime' => 0, 'path' => strtr($path, array(';' => '%%3B', ',' => '%%2C')), 'domain' => '', 'secure' => $https, 'httponly' => true, 'samesite' => 'lax'));
session_start();
if (!isset($_SESSION['pwds']['server'][$server][$user])) {
	$_SESSION['pwds']['server'][$server][$user] = $pass;
	$_SESSION['db']['server'][$server][$user][$db] = true;
}
// Hand the session back: Adminer tunes session ini settings, which PHP
// refuses while one is active, and then reopens this same session itself.
session_write_close();
function adminer_object() {
	class AgentLocalAdminer extends Adminer\Adminer {
		function name() { return %s; }
		function credentials() { return array(%s, %s, %s); }
		function database() { return %s; }
		function login($login, $password) { return true; }
		// Declared as the dark stylesheet: Adminer lays its own dark base
		// underneath, and this recolours it to the site's palette.
		function css() {
			$base = preg_replace('~\?.*~', '', $_SERVER['REQUEST_URI']);
			return array($base . '?theme=' . filemtime(__DIR__ . '/agent-local.css') => 'dark');
		}
	}
	return new AgentLocalAdminer;
}
include __DIR__ . '/adminer-%s.php';
`,
		site.Slug,
		phpQuote(host), phpQuote(site.DBUser), phpQuote(site.DBPass), phpQuote(site.DBName),
		phpQuote("agent-local · "+site.Slug),
		phpQuote(host), phpQuote(site.DBUser), phpQuote(site.DBPass),
		phpQuote(site.DBName),
		adminerVersion,
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
