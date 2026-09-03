package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WP debug toggling: WP_DEBUG on, with the log pointed at our own logs
// directory instead of wp-content/debug.log. The usual loop — white screen,
// ssh around for debug.log, remember the constant spelling — becomes
// `wpdebug on`, then `logs wp-<slug>`. Errors never render into pages
// (WP_DEBUG_DISPLAY stays off): the log is the surface, for humans and for
// agents reading it through get_logs.

// WPDebugLogName is the log key for a site's WordPress debug log, usable
// with `agent-local logs` and the get_logs tool.
func WPDebugLogName(slug string) string { return "wp-" + slug }

// WPDebugState is what a site's wp-config.php currently says.
type WPDebugState struct {
	Enabled bool   `json:"enabled"`
	LogPath string `json:"log_path,omitempty"`
	LogName string `json:"log_name,omitempty"` // tail it: agent-local logs <log_name>
}

// wpConfigPath finds a site's wp-config.php: in the docroot, or one level
// above it — WordPress itself accepts both.
func wpConfigPath(site *Site) (string, error) {
	in := filepath.Join(site.WPDir, "wp-config.php")
	if fileExists(in) {
		return in, nil
	}
	above := filepath.Join(filepath.Dir(site.WPDir), "wp-config.php")
	if fileExists(above) {
		return above, nil
	}
	return "", fmt.Errorf("no wp-config.php at %s (or one level up)", site.WPDir)
}

// WPDebugStatus reads a site's current debug state from wp-config.php.
func WPDebugStatus(site *Site) WPDebugState {
	cfg, err := wpConfigPath(site)
	if err != nil {
		return WPDebugState{}
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return WPDebugState{}
	}
	src := string(b)
	st := WPDebugState{Enabled: readWPConstRaw(src, "WP_DEBUG") == "true"}
	switch logv := readWPConstRaw(src, "WP_DEBUG_LOG"); logv {
	case "", "false":
	case "true":
		// WordPress's default target when the constant is boolean true.
		st.LogPath = filepath.Join(site.WPDir, "wp-content", "debug.log")
	default:
		st.LogPath = strings.Trim(logv, `'"`)
	}
	if st.LogPath != "" {
		if name := strings.TrimSuffix(filepath.Base(st.LogPath), ".log"); filepath.Dir(st.LogPath) == P().Logs() {
			st.LogName = name
		}
	}
	return st
}

// SetWPDebug flips WP_DEBUG for a site. On points WP_DEBUG_LOG at
// ~/.agent-local/logs/wp-<slug>.log and keeps WP_DEBUG_DISPLAY off, so
// notices land in a tailable file instead of the middle of a rendered page.
// Off flips only WP_DEBUG — the other two constants are inert without it.
func (e *Engine) SetWPDebug(slug string, on bool) (WPDebugState, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return WPDebugState{}, fmt.Errorf("no such site: %s", slug)
	}
	cfg, err := wpConfigPath(site)
	if err != nil {
		return WPDebugState{}, err
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return WPDebugState{}, err
	}
	src := string(b)
	if on {
		logPath := P().Log(WPDebugLogName(slug))
		if err := os.MkdirAll(P().Logs(), 0o755); err != nil {
			return WPDebugState{}, err
		}
		src = setWPConstRaw(src, "WP_DEBUG", "true")
		src = setWPConstRaw(src, "WP_DEBUG_LOG", "'"+logPath+"'")
		src = setWPConstRaw(src, "WP_DEBUG_DISPLAY", "false")
	} else {
		src = setWPConstRaw(src, "WP_DEBUG", "false")
	}
	if err := writeWPConfigSrc(cfg, b, []byte(src)); err != nil {
		return WPDebugState{}, err
	}
	return WPDebugStatus(site), nil
}

// writeWPConfig replaces a wp-config.php with new content. A fresh backup of
// the previous bytes is written every time — this toggle is meant to be
// flipped back and forth, so a stale first-write-only backup would stop
// matching whatever is actually running — and the new content lands via a
// tmp file + rename, so a crash mid-write never leaves wp-config.php
// truncated.
func writeWPConfigSrc(path string, oldBytes, newBytes []byte) error {
	bak := path + ".agent-local.bak"
	if err := os.WriteFile(bak, oldBytes, 0o644); err != nil {
		return fmt.Errorf("back up wp-config.php: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, newBytes, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// readWPConstRaw extracts the raw PHP expression of a define('NAME', …):
// "true", "false", "'a string'". readWPConfigConst only sees quoted strings,
// and the debug constants are usually booleans.
func readWPConstRaw(src, name string) string {
	re := regexp.MustCompile(`define\(\s*['"]` + name + `['"]\s*,\s*([^)]+?)\s*\)`)
	if m := re.FindStringSubmatch(src); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// setWPConstRaw replaces define('NAME', …) with a raw PHP expression, or —
// unlike setWPConst — inserts the define when it is absent, where WordPress
// convention expects extras: above the "stop editing" marker, else above the
// wp-settings.php require, else at the end.
func setWPConstRaw(src, name, expr string) string {
	re := regexp.MustCompile(`(?m)(define\(\s*['"]` + name + `['"]\s*,\s*)[^)]+?(\s*\))`)
	if re.MatchString(src) {
		return re.ReplaceAllString(src, "${1}"+expr+"${2}")
	}
	line := fmt.Sprintf("define( '%s', %s );\n", name, expr)
	for _, anchor := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^/\* That's all`),
		regexp.MustCompile(`(?m)^\s*require_once\s+ABSPATH\s*\.\s*'wp-settings\.php'`),
	} {
		if loc := anchor.FindStringIndex(src); loc != nil {
			return src[:loc[0]] + line + src[loc[0]:]
		}
	}
	return src + "\n" + line
}
