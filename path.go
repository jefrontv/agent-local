package main

import (
	"os"
	"strings"
)

// Every runtime lookup in this binary goes through exec.LookPath: brew, httpd,
// php, php-fpm, mariadbd. Under launchd that PATH is /usr/bin:/bin:/usr/sbin:
// /sbin — no Homebrew — so the daemon finds no toolchain at all, writes an
// empty inventory, and every site becomes "php not installed".
//
// Normalising here rather than in the LaunchAgent plist covers every way this
// process can be started (launchd job, direct fork, MCP spawn, a login shell
// with a trimmed PATH) and every child it spawns: php-fpm pools, httpd, wp-cli,
// and the detached `agent-local front` switcher. It also avoids rewriting the
// plist, which would boot out and restart the running daemon on upgrade.
var toolchainDirs = []string{
	"/opt/homebrew/bin",
	"/opt/homebrew/sbin",
	"/usr/local/bin",
	"/usr/local/sbin",
}

// normalizePATH appends the standard toolchain directories that are missing
// from PATH. Appending, not prepending: a user's own PATH still wins, so this
// can only add lookups that would otherwise fail, never shadow a chosen tool.
// The list is fixed rather than derived from the environment so the result does
// not vary with whichever shell invoked us.
func normalizePATH() {
	cur := os.Getenv("PATH")
	have := make(map[string]bool, 8)
	for _, d := range strings.Split(cur, ":") {
		if d != "" {
			have[strings.TrimSuffix(d, "/")] = true
		}
	}
	add := make([]string, 0, len(toolchainDirs))
	for _, d := range toolchainDirs {
		if !have[d] && dirExists(d) {
			add = append(add, d)
		}
	}
	if len(add) == 0 {
		return
	}
	if cur != "" {
		add = append([]string{cur}, add...)
	}
	os.Setenv("PATH", strings.Join(add, ":"))
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
