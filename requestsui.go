package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// The request page is the other half of the errors page: what this site
// actually served, in order, with the PHP errors each request produced
// attached to it. Between them, "the page is blank" becomes a line with a
// status, a duration and a fatal.

// hubReqLimit is how many requests the page shows at once — a few page
// loads' worth, which is the window anyone is actually reasoning about.
const hubReqLimit = 200

func hubRequests(w http.ResponseWriter, req *http.Request, e *Engine, site *Site, wt *Worktree, base, title string) {
	if req.Method == http.MethodPost {
		if !sameOrigin(req) {
			http.Error(w, "agent-local: cross-origin request refused", http.StatusForbidden)
			return
		}
		reqlog.clear(reqHosts(e, site, wt))
		http.Redirect(w, req, base+"/requests", http.StatusSeeOther)
		return
	}

	since, window := hubSince(req)
	view := req.URL.Query().Get("view")
	f := reqFilter{Hosts: reqHosts(e, site, wt), Since: since, Limit: hubReqLimit}
	switch view {
	case "errors":
		f.ErrorsOnly = true
	case "failed":
		f.MinStatus = 400
	}
	recs := reqlog.list(f)
	apache := FrontKind(e.Store) == "apache"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	hubHead(&b, base, title, "requests", "requests")
	if len(recs) > 0 {
		b.WriteString(`<form method=post action="` + base + `/requests"><button>clear</button></form>`)
	}
	b.WriteString(`</span></div><main>`)
	b.WriteString(`<h2>Requests</h2>`)
	b.WriteString(hubNav(base, "/requests"))

	if apache {
		b.WriteString(`<p class=empty>Apache is serving this site, and requests are recorded by the built-in ` +
			`router — so this page stays empty until you switch back: <code>agent-local front router</code>.</p></main>`)
		fmt.Fprint(w, b.String())
		return
	}

	b.WriteString(`<p class=count><span class="lamp` + lampOff(len(recs) == 0) + `"></span>` +
		reqCountLine(len(recs), window, view) + hubViews(base, window, view) + hubWindows(base, "/requests", window) + `</p>`)

	if len(recs) == 0 {
		b.WriteString(`<p class=empty>Nothing recorded in the last ` + html.EscapeString(window) +
			`. Load a page on this site and it appears here — path, status, how long it took, how it was served, ` +
			`and any PHP error it logged on the way. Tool pages like this one are not recorded.</p></main>`)
		fmt.Fprint(w, b.String())
		return
	}

	b.WriteString(`<table class=reqs>`)
	for _, rec := range recs {
		b.WriteString(`<tr><td class=age>` + mailAge(rec.At) + `</td>`)
		b.WriteString(`<td class=meth>` + html.EscapeString(rec.Method) + `</td>`)
		b.WriteString(`<td><a class=msg href="` + html.EscapeString(reqURL(e, rec)) + `"><strong>` +
			html.EscapeString(rec.Path) + `</strong></a>`)
		if rec.Shared {
			b.WriteString(`<span class=dim>through the share tunnel</span>`)
		}
		for _, line := range rec.PHPErrors {
			b.WriteString(`<span class=phperr>` + html.EscapeString(line) + `</span>`)
		}
		b.WriteString(`</td>`)
		b.WriteString(`<td class=lvl><span class="tag ` + statusClass(rec.Status) + `">` + fmt.Sprint(rec.Status) + `</span></td>`)
		b.WriteString(`<td class=size>` + fmt.Sprintf("%.0fms", rec.Ms) + `</td>`)
		b.WriteString(`<td class=size>` + humanBytes(rec.Bytes) + `</td>`)
		b.WriteString(`<td class=src>` + html.EscapeString(rec.Served) + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	b.WriteString(`<p class=empty>PHP errors are the lines the pool log gained while a request was being served, ` +
		`so under simultaneous requests the same line can appear on two of them. The newest ` +
		fmt.Sprint(reqLogSize) + ` requests are kept, in memory — a restart starts a fresh log.</p>`)
	b.WriteString(`</main>`)
	fmt.Fprint(w, b.String())
}

// reqHosts is the set of Host headers this page reports on: a preview shows
// only its own traffic, a site shows itself, its aliases and its previews.
func reqHosts(e *Engine, site *Site, wt *Worktree) []string {
	if wt != nil {
		return []string{wt.Domain}
	}
	return siteHosts(e.Store, site)
}

// reqURL rebuilds the URL a record was requested on, so a row is clickable
// straight back into the site.
func reqURL(e *Engine, rec requestRecord) string {
	return strings.TrimRight(BareDomainURL(rec.Host), "/") + rec.Path
}

// reqCountLine is the summary above the table.
func reqCountLine(n int, window, view string) string {
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
	return fmt.Sprint(n) + " " + word + what + ` in the last ` + html.EscapeString(window) + ` · newest first`
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
		q := "?since=" + window
		if v.key != "" {
			q += "&view=" + v.key
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
