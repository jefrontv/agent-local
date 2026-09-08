package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// The errors page is `agent-local errors <slug>` in a browser tab: the same
// deduplicated view of a site's PHP and WordPress logs, kept open beside the
// site while you work, refreshing itself. A crash loop writes the same fatal
// hundreds of times a minute, so counting beats tailing.

// hubErrLimit is how many distinct errors the page shows. Deduplication
// means this is a lot of log: a site with 60 different broken things has
// bigger problems than pagination.
const hubErrLimit = 100

func hubErrors(w http.ResponseWriter, req *http.Request, e *Engine, site *Site, base, title string) {
	since, window := hubSince(req)
	entries, scanned := e.SiteErrors(site, since, hubErrLimit)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	hubHead(&b, base, title, "errors", "errors")
	b.WriteString(`</span></div><main>`)
	b.WriteString(`<h2>Errors</h2>`)
	b.WriteString(hubNav(base, "/errors"))
	b.WriteString(`<p class=count>` + errCountLine(len(entries), scanned, window) +
		hubWindows(base, "/errors", window) + `</p>`)

	if len(entries) == 0 {
		b.WriteString(`<p class=empty>Nothing in the last ` + html.EscapeString(window) + `. Read from ` +
			errSources(site) + `.</p>`)
		if !WPDebugStatus(site).Enabled && site.IsWordPress() {
			b.WriteString(`<p class=empty>WP_DEBUG is off, so WordPress is not logging its own notices. ` +
				`Turn it on with <code>agent-local wpdebug ` + html.EscapeString(site.Slug) + ` on</code>.</p>`)
		}
		b.WriteString(`</main>`)
		fmt.Fprint(w, b.String())
		return
	}

	b.WriteString(`<table class=errs>`)
	for _, en := range entries {
		where := en.File
		if en.Line > 0 {
			where += ":" + fmt.Sprint(en.Line)
		}
		count := ""
		if en.Count > 1 {
			count = `<span class=x>×` + fmt.Sprint(en.Count) + `</span>`
		}
		b.WriteString(`<tr><td class=age>` + mailAge(en.Last) + `</td><td class=lvl><span class="tag ` +
			errLevelClass(en.Level) + `">` + html.EscapeString(en.Level) + `</span></td><td><strong>` +
			html.EscapeString(en.Message) + `</strong>` + count)
		if where != "" {
			b.WriteString(`<span class=dim><code>` + html.EscapeString(where) + `</code></span>`)
		}
		b.WriteString(`</td><td class=src>` + html.EscapeString(en.Source) + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	b.WriteString(`<p class=empty>Read from ` + errSources(site) + `. Identical errors share a row: the count is ` +
		`how many times it happened, the age is the most recent one.</p>`)
	b.WriteString(`</main>`)
	fmt.Fprint(w, b.String())
}

// errCountLine is the summary above the table, worded so an empty page is
// still informative about what was looked at.
func errCountLine(n, scanned int, window string) string {
	if n == 0 {
		return `nothing in the last ` + html.EscapeString(window) + ` · ` + fmt.Sprint(scanned) + ` log lines read`
	}
	word := "errors"
	if n == 1 {
		word = "error"
	}
	return fmt.Sprint(n) + ` distinct ` + word + ` in the last ` + html.EscapeString(window) +
		` · ` + fmt.Sprint(scanned) + ` log lines read · newest first`
}

// errSources names the logs an empty or full page was built from, so the
// reader can tell "nothing is broken" from "nothing is being logged".
func errSources(site *Site) string {
	names := []string{`the <code>fpm-` + html.EscapeString(site.Slug) + `</code> pool log`}
	if dbg := WPDebugStatus(site); dbg.Enabled && dbg.LogPath != "" {
		names = append(names, `the WordPress debug log`)
	}
	return strings.Join(names, " and ")
}

// errLevelClass maps a level to its tag colour: the two that mean the site
// is broken stand out, the rest stay quiet.
func errLevelClass(level string) string {
	switch level {
	case "fatal", "parse", "db":
		return "bad"
	case "warning":
		return "warn"
	default:
		return "mute"
	}
}

// lampOff is the class suffix that parks a lamp.
func lampOff(off bool) string {
	if off {
		return " off"
	}
	return ""
}
