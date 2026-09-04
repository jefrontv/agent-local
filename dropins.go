package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WordPress drop-ins (advanced-cache.php, object-cache.php, db.php) are
// generated files a plugin writes once, and several bake in the absolute
// docroot of the machine that wrote them. Nothing in WordPress corrects them
// after a move: the plugin only rewrites on activation or a settings save. An
// imported site therefore carries `/home/<user>/public_html/...` into a docroot
// that lives nowhere near it, and the failure is silent — WP Rocket's cache
// layer opens an output buffer against a class it cannot load and the page
// ends as HTTP 200 with zero bytes. This detects the shape at import and in
// doctor, and offers the one regeneration we know how to do safely.

// dropinFiles are the drop-ins that carry absolute paths in practice.
var dropinFiles = []string{"advanced-cache.php", "object-cache.php", "db.php"}

// absPathRe matches a quoted absolute path a drop-in would embed for another
// machine's docroot. Anchored on the roots web hosts and dev tools actually
// use, because a plain "starts with /" also matches fragments concatenated
// onto a constant — `WP_PLUGIN_DIR . '/wp-optimize/cache'` — which are not
// paths at all. Two segments minimum after the root.
var absPathRe = regexp.MustCompile(`['"]((?:/home|/Users|/var/www|/srv|/www|/data|/app|/mnt|/nas|/volume\d*|/opt/bitnami|/kunden|/customers|/httpdocs|/htdocs)/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)+/?)['"]`)

// StaleDropin is one drop-in whose embedded paths do not point into the
// site's docroot.
type StaleDropin struct {
	File  string `json:"file"`  // wp-content/<name>
	Paths string `json:"paths"` // first foreign path root seen, for the message
}

// staleDropins lists drop-ins under wpdir/wp-content that reference absolute
// paths outside wpdir. Only paths that look like a docroot prefix count — a
// path *inside* this docroot is what a correct drop-in looks like.
func staleDropins(wpdir string) []StaleDropin {
	root := filepath.Clean(wpdir)
	var out []StaleDropin
	for _, name := range dropinFiles {
		p := filepath.Join(root, "wp-content", name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		foreign := firstForeignPath(string(b), root)
		if foreign == "" {
			continue
		}
		out = append(out, StaleDropin{File: "wp-content/" + name, Paths: foreign})
	}
	return out
}

// firstForeignPath returns the first embedded docroot-shaped path in src that
// is not under root, trimmed to the origin's docroot; "" when every path is
// local. The path *inside* this docroot is what a correct drop-in looks like.
func firstForeignPath(src, root string) string {
	for _, m := range absPathRe.FindAllStringSubmatch(src, -1) {
		p := filepath.Clean(m[1])
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			continue
		}
		// Report the part before wp-content when present: that is the origin's
		// docroot, which is what the user recognises.
		if i := strings.Index(p, "/wp-content"); i > 0 {
			return p[:i]
		}
		return p
	}
	return ""
}

// RegenerateDropins rewrites the drop-ins we know how to: WP Rocket's
// advanced-cache.php, through the plugin's own generator so the result is
// exactly what a settings save would have produced. Anything else is
// reported, not touched — object-cache.php from a Redis plugin, say, needs
// that plugin's own regeneration and possibly a live Redis to talk to.
// Returns what was regenerated and what still needs the user.
func (e *Engine) RegenerateDropins(site *Site) (fixed []string, left []StaleDropin, err error) {
	for _, d := range staleDropins(site.WPDir) {
		switch d.File {
		case "wp-content/advanced-cache.php":
			if !fileExists(filepath.Join(site.WPDir, "wp-content", "plugins", "wp-rocket", "wp-rocket.php")) {
				left = append(left, d)
				continue
			}
			out, werr := wpCLI(site, "eval",
				`if (function_exists("rocket_generate_advanced_cache_file")) { rocket_generate_advanced_cache_file(); echo "ok"; } else { echo "missing"; }`)
			if werr != nil || !strings.Contains(out, "ok") {
				left = append(left, d)
				if err == nil {
					err = fmt.Errorf("wp-rocket regenerate: %s", strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
				}
				continue
			}
			fixed = append(fixed, d.File)
		default:
			left = append(left, d)
		}
	}
	return fixed, left, err
}

// recentFatals counts PHP fatal errors in the tail of a pool's log that are
// newer than `within`. A plugin that cannot run on the site's PHP (WP Rocket
// 3.4 on PHP 8, say) dies with a fatal on every request, and with
// WP_DEBUG_DISPLAY off that is a white page or a 500 with no clue — the log
// is where the answer was all along. The age bound matters: pool logs are
// never rotated, so a crash from last week would otherwise warn forever.
// Returns the count and the last message, trimmed.
func recentFatals(logPath string, tailLines int, within time.Duration) (int, string) {
	f, err := os.Open(logPath)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	// Keep a ring of the last N lines; logs can be large and only the tail
	// says anything about the site as it is now.
	ring := make([]string, tailLines)
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		ring[n%tailLines] = sc.Text()
		n++
	}
	start := 0
	if n > tailLines {
		start = n - tailLines
	}
	cutoff := time.Now().Add(-within)
	count, last := 0, ""
	for i := start; i < n; i++ {
		line := ring[i%tailLines]
		if !strings.Contains(line, "PHP Fatal error:") {
			continue
		}
		if at, ok := fpmLogTime(line); ok && at.Before(cutoff) {
			continue
		}
		count++
		last = line
	}
	if last != "" {
		if i := strings.Index(last, "PHP Fatal error:"); i >= 0 {
			last = strings.TrimSpace(last[i+len("PHP Fatal error:"):])
		}
		if len(last) > 160 {
			last = last[:160] + "…"
		}
	}
	return count, last
}

// fpmLogTime parses the "[04-Sep-2026 05:37:02 UTC]" prefix php-fpm writes.
// A line without one (a stack-trace continuation) reports false and is
// counted, since its timestamp is the header line's.
func fpmLogTime(line string) (time.Time, bool) {
	if len(line) < 2 || line[0] != '[' {
		return time.Time{}, false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse("02-Jan-2006 15:04:05 MST", line[1:end])
	return t, err == nil
}
