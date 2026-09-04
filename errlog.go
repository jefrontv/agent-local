package main

import (
	"bufio"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errlog.go turns raw PHP/WordPress logs into a short, deduplicated list of
// what is actually breaking a site. A crash loop writes the same fatal
// hundreds of times a minute; nobody wants to read that log by hand, and
// tailing it just shows the same line scrolling past. This groups by
// level+message+file+line, counts occurrences, and keeps first/last seen so
// the caller can tell "still happening" from "happened once yesterday".

// errorEntry is one deduplicated error as reported by the /errors endpoint.
type errorEntry struct {
	Level   string    `json:"level"` // fatal | parse | warning | notice | deprecated | db
	Message string    `json:"message"`
	File    string    `json:"file"`
	Line    int       `json:"line"`
	Count   int       `json:"count"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Source  string    `json:"source"` // fpm | wp-debug
}

// phpLevelLabels maps the label PHP writes in a log line to our short level
// name, in the order they must be tried: "Parse error" would also match a
// naive search for "error", so the more specific labels come first.
var phpLevelLabels = []struct {
	label string
	level string
}{
	{"PHP Fatal error:", "fatal"},
	{"PHP Parse error:", "parse"},
	{"PHP Warning:", "warning"},
	{"PHP Notice:", "notice"},
	{"PHP Deprecated:", "deprecated"},
}

// parsePHPLogLine pulls the level, message, and file:line out of one PHP or
// WordPress log line. It returns ok=false for anything that is not an error
// line at all: FPM's own "NOTICE: fpm is running" chatter, and the
// stack-trace continuation lines ("#0 ...", "Stack trace:", "  thrown in")
// that follow a fatal — those belong to the entry the header line already
// produced, not entries of their own.
func parsePHPLogLine(line string) (level, message, file string, lineNo int, ok bool) {
	body := line
	if len(line) > 0 && line[0] == '[' {
		if end := strings.IndexByte(line, ']'); end >= 0 {
			body = strings.TrimSpace(line[end+1:])
		}
	}
	if body == "" {
		return "", "", "", 0, false
	}
	if strings.HasPrefix(body, "#") || body == "Stack trace:" || strings.HasPrefix(strings.TrimSpace(body), "thrown in") {
		return "", "", "", 0, false
	}

	if strings.HasPrefix(body, "WordPress database error") {
		msg := strings.TrimSpace(strings.TrimPrefix(body, "WordPress database error"))
		msg = strings.TrimPrefix(msg, " ")
		return "db", msg, "", 0, true
	}

	for _, l := range phpLevelLabels {
		if !strings.HasPrefix(body, l.label) {
			continue
		}
		msg := strings.TrimSpace(body[len(l.label):])
		msg, file, lineNo = stripPHPLocation(msg)
		return l.level, msg, file, lineNo, true
	}
	return "", "", "", 0, false
}

// stripPHPLocation removes the trailing " in /path/file.php on line N" or
// " in /path/file.php:N" that PHP appends to error messages, returning the
// clean message plus the file/line it named.
func stripPHPLocation(msg string) (clean, file string, line int) {
	clean = msg
	if idx := strings.LastIndex(msg, " in "); idx >= 0 {
		loc := msg[idx+len(" in "):]
		if onIdx := strings.LastIndex(loc, " on line "); onIdx >= 0 {
			file = loc[:onIdx]
			if n, err := strconv.Atoi(strings.TrimSpace(loc[onIdx+len(" on line "):])); err == nil {
				line = n
				clean = strings.TrimSpace(msg[:idx])
				return clean, file, line
			}
		}
		if colonIdx := strings.LastIndex(loc, ":"); colonIdx >= 0 {
			if n, err := strconv.Atoi(loc[colonIdx+1:]); err == nil {
				file = loc[:colonIdx]
				line = n
				clean = strings.TrimSpace(msg[:idx])
				return clean, file, line
			}
		}
	}
	return clean, "", 0
}

// errTailLines bounds how much of a log file collectErrors reads. Pool and
// debug logs are never rotated, so without a cap a long-lived site's log
// grows without bound and every scan gets slower forever.
const errTailLines = 5000

// collectErrors reads the tail of each path in paths (source per index in
// sources), keeps lines within `since` of now, parses them, and returns a
// deduplicated, count-tracked, most-recent-first list capped at limit.
// Lines without their own timestamp (stack-trace continuations, multi-line
// dumps) inherit the timestamp of the most recent header line above them;
// anything before the first timestamped line in the tail has no reliable
// age and is dropped rather than guessed at.
func collectErrors(paths []string, sources []string, since time.Duration, limit int) (entries []errorEntry, scanned int) {
	if limit <= 0 {
		limit = 50
	}
	cutoff := time.Now().Add(-since)
	type key struct {
		level, message, file string
		line                 int
	}
	dedup := map[key]*errorEntry{}

	for i, path := range paths {
		source := ""
		if i < len(sources) {
			source = sources[i]
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		ring := make([]string, errTailLines)
		n := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			ring[n%errTailLines] = sc.Text()
			n++
		}
		f.Close()
		scanned += n

		start := 0
		if n > errTailLines {
			start = n - errTailLines
		}
		var curTime time.Time
		haveTime := false
		for i := start; i < n; i++ {
			line := ring[i%errTailLines]
			if t, ok := fpmLogTime(line); ok {
				curTime = t
				haveTime = true
			} else if !haveTime {
				continue
			}
			if curTime.Before(cutoff) {
				continue
			}
			level, message, file, lineNo, ok := parsePHPLogLine(line)
			if !ok {
				continue
			}
			k := key{level, message, file, lineNo}
			if e, exists := dedup[k]; exists {
				e.Count++
				if curTime.After(e.Last) {
					e.Last = curTime
				}
				if curTime.Before(e.First) {
					e.First = curTime
				}
				continue
			}
			dedup[k] = &errorEntry{
				Level: level, Message: message, File: file, Line: lineNo,
				Count: 1, First: curTime, Last: curTime, Source: source,
			}
		}
	}

	for _, e := range dedup {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Last.After(entries[j].Last) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, scanned
}

// SiteErrors gathers deduplicated errors from a site's FPM pool log and, if
// WP_DEBUG_LOG is on, its wp-content debug log.
func (e *Engine) SiteErrors(site *Site, since time.Duration, limit int) (entries []errorEntry, scanned int) {
	var paths, sources []string
	if p := e.fpmLog(site.Slug); p != "" {
		paths = append(paths, p)
		sources = append(sources, "fpm")
	}
	if dbg := WPDebugStatus(site).LogPath; dbg != "" {
		paths = append(paths, dbg)
		sources = append(sources, "wp-debug")
	}
	return collectErrors(paths, sources, since, limit)
}

// parseSince accepts Go's normal duration syntax plus a bare "Nd" days
// suffix, since "since=7d" is what someone actually types for "the last
// week" and time.ParseDuration has no notion of days.
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return time.Hour, nil
	}
	if strings.HasSuffix(s, "d") && !strings.HasSuffix(s, "ns") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// handleErrors serves GET /sites/{slug}/errors?since=1h&limit=50 — a short,
// deduplicated list of what is currently breaking the site, pulled from its
// FPM pool log and (when enabled) its WordPress debug log.
func (a *APIServer) handleErrors(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		fail(w, 400, "bad since: "+err.Error())
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	var sourcePaths []string
	if p := a.engine.fpmLog(site.Slug); p != "" {
		if _, err := os.Stat(p); err == nil {
			sourcePaths = append(sourcePaths, p)
		}
	}
	if dbg := WPDebugStatus(site).LogPath; dbg != "" {
		if _, err := os.Stat(dbg); err == nil {
			sourcePaths = append(sourcePaths, dbg)
		}
	}

	entries, scanned := a.engine.SiteErrors(site, since, limit)
	ok(w, map[string]any{
		"entries": entries,
		"scanned": scanned,
		"since":   since.String(),
		"sources": sourcePaths,
	})
}
