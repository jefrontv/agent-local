package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Generic wp-config.php constant access. wpdebug.go already owns the three
// debug constants with their own semantics (log path resolution, "on"
// implies WP_DEBUG_DISPLAY off); this file is for everything else an agent
// might want to read or set — WP_MEMORY_LIMIT, WP_HOME, FS_METHOD, and so
// on — without hand-rolling a regexp against wp-config.php every time.

// wpConstNameRe is what a PHP constant identifier looks like when WordPress
// itself defines it: SCREAMING_SNAKE_CASE. Anything else is either not a
// real constant name or someone trying to smuggle PHP through the name
// field instead of the value field.
var wpConstNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// wpConstSalts are the eight secret keys WordPress expects to be random,
// unique, and never touched by hand — changing one invalidates every logged
// in session and cookie. There is no agent-facing reason to set these via
// this endpoint instead of a fresh copy from the secret-key API.
var wpConstSalts = map[string]bool{
	"AUTH_KEY": true, "SECURE_AUTH_KEY": true, "LOGGED_IN_KEY": true, "NONCE_KEY": true,
	"AUTH_SALT": true, "SECURE_AUTH_SALT": true, "LOGGED_IN_SALT": true, "NONCE_SALT": true,
}

// checkWPConstName refuses the constants that have their own dedicated tool
// (DB_*, via db_creds) or that WordPress needs generated randomly (the
// salts, ABSPATH). Anything else that looks like a real PHP constant name
// is fair game.
func checkWPConstName(name string) error {
	if !wpConstNameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid PHP constant name (expected SCREAMING_SNAKE_CASE)", name)
	}
	if strings.HasPrefix(name, "DB_") {
		return fmt.Errorf("%s is set by agent-local; see db_creds", name)
	}
	if wpConstSalts[name] {
		return fmt.Errorf("%s is a secret key; agent-local does not manage salts through this endpoint", name)
	}
	if name == "ABSPATH" {
		return fmt.Errorf("ABSPATH is set by WordPress itself and cannot be overridden here")
	}
	return nil
}

// phpLiteral renders a value as the PHP expression that belongs on the
// right-hand side of a define(). "auto" guesses booleans/null/numbers from
// their text and quotes everything else; "string" always quotes, for a
// value that happens to read as a number or "true" but is meant literally
// (e.g. a WP_ENVIRONMENT_TYPE of "true" is absurd but not our call to make);
// "raw" passes the text through untouched, the escape hatch for expressions
// like WP_HOME . '/path' that no literal encoding can express — the caller
// owns correctness of raw PHP.
func phpLiteral(value, typ string) (string, error) {
	switch typ {
	case "", "auto":
		switch strings.ToLower(value) {
		case "true", "false", "null":
			return strings.ToLower(value), nil
		}
		if _, err := strconv.ParseInt(value, 10, 64); err == nil {
			return value, nil
		}
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			return value, nil
		}
		return wpConstQuote(value), nil
	case "string":
		return wpConstQuote(value), nil
	case "raw":
		return value, nil
	default:
		return "", fmt.Errorf("unknown value type %q (want auto, string, or raw)", typ)
	}
}

// wpConstQuote single-quotes a string for a PHP source literal, escaping the two
// characters that matter inside PHP single quotes: backslash and the quote
// itself.
func wpConstQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// wpConstant is one define() line as it actually reads in wp-config.php —
// Value is the raw PHP expression, not a decoded Go value, because the
// source can hold anything from `true` to `WP_HOME . '/sub'`.
type wpConstant struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Line  int    `json:"line"`
}

// wpConstDefineRe matches a define() that starts a line (leading whitespace
// only) with a quoted constant name, capturing the name and everything
// between the comma and the closing paren. Restricting to line starts is
// deliberate: it skips the same define() mentioned inside a comment on a
// continuation line without having to parse PHP.
var wpConstDefineRe = regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]\s*,(.*)\)\s*;?\s*$`)

// listWPConstants scans wp-config.php source for every top-level define(),
// skipping commented-out lines so a disabled example in the file (WordPress
// ships several) doesn't masquerade as live config.
func listWPConstants(src string) []wpConstant {
	var out []wpConstant
	lines := strings.Split(src, "\n")
	lineStart := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "*") {
			lineStart += len(line) + 1
			continue
		}
		if m := wpConstDefineRe.FindStringSubmatch(line); m != nil {
			out = append(out, wpConstant{
				Name:  m[1],
				Value: strings.TrimSpace(m[2]),
				Line:  i + 1,
			})
		}
		lineStart += len(line) + 1
	}
	return out
}

// WPConstants lists every define() found in a site's wp-config.php. The salts
// are listed by name with their value withheld: nothing an agent does needs
// them, and a transcript is no place for a site's cookie-signing secrets.
func (e *Engine) WPConstants(site *Site) ([]wpConstant, error) {
	cfg, err := wpConfigPath(site)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return nil, err
	}
	list := listWPConstants(string(b))
	for i := range list {
		if wpConstSalts[list[i].Name] {
			list[i].Value = "'…'"
		}
	}
	return list, nil
}

// SetWPConstant writes or replaces a define() in a site's wp-config.php.
// setWPConstRaw already knows where to insert a missing constant (above the
// "stop editing" marker, WordPress's own convention); this just supplies
// the validated name and encoded literal.
func (e *Engine) SetWPConstant(site *Site, name, value, typ string) (wpConstant, error) {
	if err := checkWPConstName(name); err != nil {
		return wpConstant{}, err
	}
	literal, err := phpLiteral(value, typ)
	if err != nil {
		return wpConstant{}, err
	}
	cfg, err := wpConfigPath(site)
	if err != nil {
		return wpConstant{}, err
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return wpConstant{}, err
	}
	src := setWPConstRaw(string(b), name, literal)
	if err := writeWPConfigSrc(cfg, b, []byte(src)); err != nil {
		return wpConstant{}, err
	}
	for _, c := range listWPConstants(src) {
		if c.Name == name {
			return c, nil
		}
	}
	return wpConstant{}, fmt.Errorf("wrote %s but could not read it back", name)
}

// wpConstRemoveRe deletes the define() line for one constant by name; built
// per call since the name is caller-supplied and must not be reused across
// sites/constants.
func wpConstRemoveRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]` + regexp.QuoteMeta(name) + `['"].*\)\s*;?\s*\n?`)
}

// RemoveWPConstant deletes a constant's define() line entirely, rather than
// setting it to null — some constants (e.g. WP_CACHE) are read with isset()
// checks where "defined and null" and "undefined" behave differently.
func (e *Engine) RemoveWPConstant(site *Site, name string) (bool, error) {
	if err := checkWPConstName(name); err != nil {
		return false, err
	}
	cfg, err := wpConfigPath(site)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return false, err
	}
	src := string(b)
	re := wpConstRemoveRe(name)
	if !re.MatchString(src) {
		return false, nil
	}
	newSrc := re.ReplaceAllString(src, "")
	if err := writeWPConfigSrc(cfg, b, []byte(newSrc)); err != nil {
		return false, err
	}
	return true, nil
}

// handleWPConstants lists every define() in a site's wp-config.php.
func (a *APIServer) handleWPConstants(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	list, err := a.engine.WPConstants(site)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, list)
}

type setWPConstReq struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Type   string `json:"type"`
	Remove bool   `json:"remove"`
}

// handleSetWPConstant sets, updates, or removes a single wp-config.php
// constant.
func (a *APIServer) handleSetWPConstant(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req setWPConstReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad request body: "+err.Error())
		return
	}
	if req.Remove {
		removed, err := a.engine.RemoveWPConstant(site, req.Name)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		ok(w, map[string]interface{}{"removed": removed, "name": req.Name})
		return
	}
	c, err := a.engine.SetWPConstant(site, req.Name, req.Value, req.Type)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ok(w, c)
}
