package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Magic login: an agent that just provisioned a site wants to hand a human a
// working wp-admin session without ever knowing (or setting) a password. That
// needs code running inside WordPress — a mu-plugin — because auth cookies are
// only mintable from PHP. The plugin is dropped just-in-time, consumed once,
// and removed on every path out (success, expiry, or the token going missing),
// so nothing that can log someone in survives past the minutes it's valid for.

// loginTokenPath is where a site's live magic-login token is kept, one per
// site slug (a second call to MagicLogin replaces it, it does not stack).
func loginTokenPath(slug string) string {
	return filepath.Join(P().Run(), "login-"+slug+".json")
}

// loginToken is the on-disk shape read by both Go (to know what it wrote)
// and the mu-plugin (to know what to accept).
type loginToken struct {
	Token   string    `json:"token"`
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

// loginMUName is the mu-plugin file the token redeems against. mu-plugins load
// unconditionally, which is what lets a bare query-string hit on ?p= (any URL)
// run our init hook before anything else has a say.
const loginMUName = "agent-local-login.php"

// LoginLink is what MagicLogin hands back: a one-time URL good for a few
// minutes.
type LoginLink struct {
	URL     string    `json:"url"`
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

// loginMUTemplate is rendered with the token file's absolute path baked in as
// a single-quoted PHP literal. It reads its own instructions from that file
// rather than baking the token in directly, so a repeat MagicLogin call can
// overwrite just the (cheap) token file and leave the plugin file alone.
const loginMUTemplate = `<?php
/**
 * agent-local magic login — installed by MagicLogin, valid for a few minutes.
 *
 * This file grants a WordPress session to whoever holds the token in
 * %s. It exists only for the window the token is valid: every
 * path through this hook — success, an expired token, a missing token file,
 * an unknown user — deletes both the token and this file before returning,
 * so there is nothing left here for anyone to find or reuse afterward.
 */

add_action('init', function () {
	if (empty($_GET['agent_local_login'])) {
		return;
	}

	$token_file = %s;
	$cleanup = function () use ($token_file) {
		@unlink($token_file);
		@unlink(__FILE__);
	};

	$raw = @file_get_contents($token_file);
	if ($raw === false) {
		// The token is already gone (used, expired-and-swept, or replaced by
		// a newer MagicLogin call). Nothing left to check against — stop
		// existing.
		@unlink(__FILE__);
		return;
	}

	$data = json_decode($raw, true);
	$expires = isset($data['expires']) ? strtotime($data['expires']) : 0;
	$want = isset($data['token']) ? (string) $data['token'] : '';
	$got = (string) $_GET['agent_local_login'];

	if (!$expires || time() > $expires || !hash_equals($want, $got)) {
		$cleanup();
		wp_die('login link expired', 'Login link expired', array('response' => 410));
	}

	$who = isset($data['user']) ? (string) $data['user'] : '';
	$user = is_numeric($who) ? get_user_by('id', (int) $who) : get_user_by('login', $who);
	if (!$user) {
		$user = get_user_by('email', $who);
	}
	if (!$user) {
		$cleanup();
		wp_die('login link expired', 'Login link expired', array('response' => 404));
	}

	wp_set_current_user($user->ID);
	wp_set_auth_cookie($user->ID, true, is_ssl());
	$cleanup();
	wp_safe_redirect(admin_url());
	exit;
}, 0);
`

// muPluginsDir is the directory the login mu-plugin lives in.
func muPluginsDir(site *Site) string {
	return filepath.Join(site.WPDir, "wp-content", "mu-plugins")
}

func loginMUPath(site *Site) string {
	return filepath.Join(muPluginsDir(site), loginMUName)
}

// phpSingleQuote turns an absolute path into a PHP single-quoted string
// literal, escaping the two characters that matter inside single quotes.
func phpSingleQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

// RemoveLoginMU best-effort deletes the login mu-plugin and its token file.
// Safe to call when neither exists (e.g. a repeat sweep, or before writing a
// fresh token so a stale plugin never coexists with a new one).
func RemoveLoginMU(site *Site) {
	_ = os.Remove(loginMUPath(site))
	_ = os.Remove(loginTokenPath(site.Slug))
}

// MagicLogin mints a one-time login link for a site. user is a wp-cli login
// or numeric ID; empty means "the first administrator", resolved via wp-cli.
func (e *Engine) MagicLogin(site *Site, user string) (*LoginLink, error) {
	RemoveLoginMU(site)

	user = strings.TrimSpace(user)
	if user == "" {
		out, err := wpCLI(site, "user", "list", "--role=administrator", "--field=user_login", "--orderby=ID", "--order=ASC")
		if err != nil {
			return nil, fmt.Errorf("listing administrators: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
			return nil, fmt.Errorf("no administrator user found on %s", site.Slug)
		}
		user = strings.TrimSpace(lines[0])
	}

	tok := loginToken{
		Token:   randomPass(32),
		User:    user,
		Expires: time.Now().Add(5 * time.Minute),
	}
	tokPath := loginTokenPath(site.Slug)
	if err := os.MkdirAll(filepath.Dir(tokPath), 0o755); err != nil {
		return nil, err
	}
	buf, err := json.Marshal(tok)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(tokPath, buf, 0o600); err != nil {
		return nil, err
	}

	muDir := muPluginsDir(site)
	if err := os.MkdirAll(muDir, 0o755); err != nil {
		return nil, err
	}
	plugin := fmt.Sprintf(loginMUTemplate, tokPath, phpSingleQuote(tokPath))
	if err := os.WriteFile(loginMUPath(site), []byte(plugin), 0o644); err != nil {
		return nil, err
	}

	return &LoginLink{
		URL:     BareURL(site) + "/?agent_local_login=" + tok.Token,
		User:    user,
		Expires: tok.Expires,
	}, nil
}

// handleMagicLogin issues a one-time login link for a site.
// POST /sites/{slug}/login  body {"user": "admin"}  (user optional)
func (a *APIServer) handleMagicLogin(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req struct {
		User string `json:"user"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, 400, "bad json: "+err.Error())
			return
		}
	}
	link, err := a.engine.MagicLogin(site, req.User)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, link)
}
