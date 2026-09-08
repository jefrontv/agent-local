package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// The request page is the other half of the errors page: what this site
// served, in order, with the PHP errors each request produced attached to
// it. Between them, "the page is blank" becomes a line with a status, a
// duration and a fatal.
//
// It renders once on the server so it works with JavaScript off, then a
// small script takes over: it polls for records newer than the newest one on
// screen and prepends them. Pause stops the polling and counts what arrives
// meanwhile. Clicking a row opens the exchange — general, response headers,
// request headers, the PHP file that answered, and any errors — from the
// JSON already in the page, so opening a row costs nothing.

// hubReqLimit is how many requests the first screen shows.
const hubReqLimit = 200

// hubReqLive is how many rows the live feed keeps in the DOM before dropping
// the oldest: past this, scrolling is the problem, not the log.
const hubReqLive = 300

func hubRequests(w http.ResponseWriter, req *http.Request, e *Engine, site *Site, wt *Worktree, base, title string) {
	hosts := reqHosts(e, site, wt)

	if req.Method == http.MethodPost {
		if !sameOrigin(req) {
			http.Error(w, "agent-local: cross-origin request refused", http.StatusForbidden)
			return
		}
		reqlog.clear(hosts)
		http.Redirect(w, req, base+"/requests", http.StatusSeeOther)
		return
	}

	since, window := hubSince(req)
	view := req.URL.Query().Get("view")
	f := reqFilter{Hosts: hosts, Since: since, Limit: hubReqLimit}
	switch view {
	case "errors":
		f.ErrorsOnly = true
	case "failed":
		f.MinStatus = 400
	}

	// The live poll asks this same page for records newer than what it has.
	if req.URL.Query().Get("format") == "json" {
		f.After, _ = strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
		f.Limit = hubReqLive
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"requests":  reqlog.list(f),
			"recording": FrontKind(e.Store) != "apache",
		})
		return
	}

	recs := reqlog.list(f)
	apache := FrontKind(e.Store) == "apache"
	// Live only makes sense on an unfiltered view of a recording front: a
	// feed that silently drops what does not match its filter is worse than
	// no feed.
	live := !apache && view == "" && req.URL.Query().Get("since") == ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	hubHead(&b, base, title, "requests", "requests")
	if live {
		b.WriteString(`<button type=button id=pause></button>`)
	}
	b.WriteString(`<form method=post action="` + base + `/requests"><button>clear</button></form>`)
	b.WriteString(`</span></div><main>`)
	b.WriteString(`<h2>Requests</h2>`)
	b.WriteString(hubNav(base, "/requests"))

	if apache {
		b.WriteString(`<p class=empty>Apache is serving this site. Requests are recorded by the built-in router, ` +
			`so this page stays empty until you switch back: <code>agent-local front router</code>.</p></main>`)
		fmt.Fprint(w, b.String())
		return
	}

	b.WriteString(`<p class=count><span id=count>` + reqCountLine(len(recs), window, view, live) + `</span>` +
		hubViews(base, window, view) + hubWindows(base, "/requests", window) + `</p>`)
	b.WriteString(`<table class=reqs><tbody id=rows>`)
	for _, rec := range recs {
		b.WriteString(reqRow(rec))
	}
	b.WriteString(`</tbody></table>`)
	if len(recs) == 0 {
		b.WriteString(`<p class=empty id=none>Nothing yet. Load a page on this site and it appears here: ` +
			`path, status, how long it took, and any PHP error it logged. This page is not recorded.</p>`)
	}
	b.WriteString(`<p class=empty>PHP errors are the lines the pool log gained while a request was being served, ` +
		`so two requests running at once can show the same line. The newest ` + fmt.Sprint(reqLogSize) +
		` requests are kept in memory; a restart starts a fresh log.</p>`)
	if live {
		b.WriteString(reqLiveScript(base))
	}
	b.WriteString(`</main>`)
	fmt.Fprint(w, b.String())
}

// reqRow is one row of the table, carrying its own record as JSON so the
// detail panel needs no second request. The same markup is built in the
// browser for rows that arrive live, which is why the shape is simple.
func reqRow(rec requestRecord) string {
	blob, _ := json.Marshal(rec)
	var b strings.Builder
	b.WriteString(`<tr class=req data-id="` + fmt.Sprint(rec.ID) + `"><td class=age>` + mailAge(rec.At) + `</td>`)
	b.WriteString(`<td class=meth>` + html.EscapeString(rec.Method) + `</td>`)
	b.WriteString(`<td class=path><button type=button class=open>` + html.EscapeString(rec.Path) + `</button>`)
	for _, line := range rec.PHPErrors {
		b.WriteString(`<span class=phperr>` + html.EscapeString(line) + `</span>`)
	}
	b.WriteString(`<script type="application/json" class=rec>` + jsonForHTML(blob) + `</script></td>`)
	b.WriteString(`<td class=lvl><span class="tag ` + statusClass(rec.Status) + `">` + fmt.Sprint(rec.Status) + `</span></td>`)
	b.WriteString(`<td class=size>` + fmt.Sprintf("%.0fms", rec.Ms) + `</td>`)
	b.WriteString(`<td class=size>` + humanBytes(rec.Bytes) + `</td>`)
	b.WriteString(`<td class=src>` + html.EscapeString(rec.Served) + `</td></tr>`)
	return b.String()
}

// jsonForHTML makes a JSON blob safe to sit inside a <script> element: only
// a literal "</script" can end the block early.
func jsonForHTML(b []byte) string {
	return strings.ReplaceAll(string(b), "<", `\u003c`)
}

// reqHosts is the set of Host headers this page reports on: a preview shows
// only its own traffic, a site shows itself, its aliases and its previews.
func reqHosts(e *Engine, site *Site, wt *Worktree) []string {
	if wt != nil {
		return []string{wt.Domain}
	}
	return siteHosts(e.Store, site)
}

// reqCountLine is the summary above the table.
func reqCountLine(n int, window, view string, live bool) string {
	word := "requests"
	if n == 1 {
		word = "request"
	}
	what := ""
	switch view {
	case "errors":
		what = " that logged a PHP error"
	case "failed":
		what = " that failed"
	}
	tail := ` in the last ` + html.EscapeString(window) + ` · newest first`
	if live {
		tail = ` · newest first · live`
	}
	return fmt.Sprint(n) + " " + word + what + tail
}

// hubViews renders the filter links, keeping the window across a switch.
func hubViews(base, window, current string) string {
	var b strings.Builder
	b.WriteString(`<span class=windows>`)
	for _, v := range []struct{ key, name string }{{"", "all"}, {"errors", "php errors"}, {"failed", "4xx/5xx"}} {
		cls := ""
		if v.key == current {
			cls = ` class=on`
		}
		q := ""
		if v.key != "" {
			q = "?since=" + window + "&view=" + v.key
		}
		b.WriteString(`<a href="` + base + `/requests` + q + `"` + cls + `>` + v.name + `</a>`)
	}
	b.WriteString(`</span>`)
	return b.String()
}

// statusClass colours a status by class: server errors and client errors
// read as problems, redirects as neutral, 2xx quiet.
func statusClass(status int) string {
	switch {
	case status >= 500:
		return "bad"
	case status >= 400:
		return "warn"
	default:
		return "mute"
	}
}
