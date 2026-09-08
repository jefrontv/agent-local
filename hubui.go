package main

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HubPath is the reserved URL path serving a site's local tooling — an index
// with what the daemon knows about the site, links to the database GUI and
// the mail inbox, and its own pages for the error log and the request log.
// Like those pages it is rendered by this binary, kept out of the WordPress
// tree so a permalink cannot swallow it, and stays local-only on shares.
const HubPath = "/.agent-local"

// hubPages are the paths under HubPath this binary answers itself. Anything
// else below HubPath is a 404 rather than a fall-through to WordPress: the
// whole prefix is ours, and a typo should say so instead of rendering the
// site's 404 template.
var hubPages = map[string]bool{"": true, "/errors": true, "/requests": true, "/login": true}

// isHubPath reports whether a request URL is the tooling index itself: the
// exact path, with or without a trailing slash.
func isHubPath(urlPath string) bool {
	return hubRest(urlPath) == ""
}

// underHub reports whether a request URL belongs to the hub at all — the
// index or any path below it. Adminer and the inbox live under the same
// prefix and are matched before this, so they are excluded here.
func underHub(urlPath string) bool {
	clean := strings.TrimSuffix(filepath.Clean("/"+urlPath), "/")
	return clean == HubPath || strings.HasPrefix(clean, HubPath+"/")
}

// hubRest is the path below the hub index: "" for the index itself, "/errors"
// for a page. Returns "\x00" for something under the prefix that is not a
// page, which serveHub answers as a 404.
func hubRest(urlPath string) string {
	clean := strings.TrimSuffix(filepath.Clean("/"+urlPath), "/")
	if clean == HubPath {
		return ""
	}
	rest := strings.TrimPrefix(clean, HubPath)
	if rest == clean || !hubPages[rest] {
		return "\x00"
	}
	return rest
}

// hubCSS extends the inbox frame with cards and the site card, in the same
// tokens — panel, hairlines, mono kickers, the green lamp on hover. Kept
// separate so the inbox stylesheet stays untouched.
const hubCSS = `<style>
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 16px; margin-top: 4px; align-items: stretch; }
  .card { display: block; height: 100%; background: var(--panel); border: 1px solid var(--hair); border-radius: 12px; padding: 24px; }
  .card:hover { border-color: var(--lamp); }
  .card.off { opacity: .55; } .card.off:hover { border-color: var(--hair); }
  .card .kicker { display: block; margin-bottom: 12px; font: 500 11px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .card strong { display: block; margin-bottom: 8px; font: 700 19px/1.25 var(--sans); font-variation-settings: "wdth" 112; letter-spacing: normal; text-transform: none; }
  .card:hover strong { color: var(--lamp); }
  .card.off:hover strong { color: var(--fg); }
  .card .desc { color: var(--dim); font-size: 13px; letter-spacing: normal; text-transform: none; }
  .card .go { display: block; margin-top: 16px; font: 500 11px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .card:hover .go { color: var(--lamp); }
  /* The card that submits a form is still a card: the bar's small-button
     type must not reach it. */
  form.card { padding: 0; border: 0; background: none; }
  form.card button { display: block; width: 100%; height: 100%; text-align: left; background: var(--panel);
                     border: 1px solid var(--hair); border-radius: 12px; padding: 24px; cursor: pointer;
                     font: 13.5px/1.55 var(--sans); letter-spacing: normal; text-transform: none; color: inherit; }
  form.card button:hover { border-color: var(--lamp); }
  form.card button:hover strong { color: var(--lamp); }
  form.card button:hover .go { color: var(--lamp); }
  /* nav and window selectors: the same mono labels, one lit */
  .label a { color: var(--dim); margin: 0 18px 0 0; letter-spacing: .14em; }
  .label a.on { color: var(--lamp); }
  .windows { margin-left: auto; display: flex; gap: 14px; }
  .windows a { color: var(--dim); letter-spacing: .1em; } .windows a.on { color: var(--fg); }
  .lamp.off { background: var(--mark); box-shadow: none; }
  /* level and status tags */
  .tag { display: inline-block; min-width: 62px; text-align: center; padding: 3px 8px; border-radius: 5px;
         font: 500 10px var(--mono); letter-spacing: .1em; text-transform: uppercase;
         border: 1px solid var(--hair); color: var(--dim); }
  .tag.bad { color: light-dark(#b64a4a, #e08a8a); border-color: light-dark(#e2c4c4, #3a2323); background: light-dark(#f6e6e6, #1d1414); }
  .tag.warn { color: light-dark(#8a6a2e, #c9a97a); border-color: light-dark(#e0d6bf, #33301f); background: light-dark(#f6f1e4, #1d1b14); }
  /* the two log tables */
  .errs td, .reqs td { padding: 13px 14px 13px 0; vertical-align: top; }
  .errs td.lvl, .reqs td.lvl { width: 84px; padding-top: 14px; }
  .errs td strong, .reqs td strong { display: block; font: 600 13px/1.5 var(--sans); word-break: break-word; }
  .errs td .dim, .reqs td .dim { display: block; font-size: 11.5px; margin-top: 3px; }
  .errs code, .reqs code { font: 11.5px var(--mono); }
  .errs .x { display: inline-block; margin-top: 3px; font: 500 10.5px var(--mono); letter-spacing: .08em; color: var(--dim); }
  .reqs td.meth { width: 56px; font-size: 11px; letter-spacing: .08em; color: var(--dim); padding-top: 15px; }
  .reqs .phperr { display: block; margin-top: 6px; font: 11.5px/1.5 var(--mono); color: light-dark(#b64a4a, #e08a8a); word-break: break-word; }
  /* a request row opens: the path is the control, the panel sits under it */
  .reqs button.open { display: block; width: 100%; text-align: left; padding: 0; border: 0; background: none;
                      cursor: pointer; font: 500 12.5px/1.5 var(--mono); color: var(--fg); word-break: break-all; }
  .reqs button.open:hover { color: var(--lamp); }
  .reqs tr.open button.open { color: var(--lamp); }
  .reqs tr.detail td { padding: 0 14px 18px 0; border-top: 0; }
  .panel { background: var(--panel); border: 1px solid var(--hair); border-radius: 10px; padding: 18px 20px; }
  .phead { margin: 16px 0 8px; font: 500 10.5px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .phead:first-child { margin-top: 0; }
  .panel .none { margin: 0; font: 11.5px var(--mono); color: var(--mark); }
  dl.hdrs { display: grid; grid-template-columns: max-content 1fr; gap: 5px 20px; margin: 0; font: 11.5px/1.6 var(--mono); }
  dl.hdrs dt { font-size: 11px; letter-spacing: .04em; text-transform: none; line-height: 1.6; color: var(--dim); }
  dl.hdrs dd { margin: 0; word-break: break-all; }
  pre.perr { margin: 0; padding: 12px 14px; font: 11.5px/1.6 var(--mono); color: light-dark(#b64a4a, #e08a8a);
             background: var(--lit); border: 1px solid var(--hair); border-radius: 8px; white-space: pre-wrap; }
  #pause.on { color: var(--lamp); border-color: var(--lamp); }
  @media (max-width: 720px) { .cards { grid-template-columns: 1fr; } .windows { margin: 8px 0 0; width: 100%; }
    .count { flex-wrap: wrap; } .errs td.src, .reqs td.src { display: none; }
    dl.hdrs { grid-template-columns: 1fr; gap: 0 0; } dl.hdrs dd { margin-bottom: 8px; } }
</style>`

// serveHub answers the hub index and every page under it. base is the
// browser-facing mount (HubPath on the router, also HubPath through the
// apache ProxyPass — the convention serveMailUI follows), site the site the
// Host resolved to, wt the preview when the host was a branch domain, and
// title the domain shown.
func serveHub(w http.ResponseWriter, req *http.Request, e *Engine, site *Site, wt *Worktree, base, title string) {
	switch hubRest(req.URL.Path) {
	case "":
		hubIndex(w, e, site, wt, base, title)
	case "/errors":
		hubErrors(w, req, e, site, base, title)
	case "/requests":
		hubRequests(w, req, e, site, wt, base, title)
	case "/login":
		hubLogin(w, req, e, site, base)
	default:
		http.NotFound(w, req)
	}
}

// hubHead is the opening of every hub page: the theme switch, the shared
// stylesheet, and the top bar with the lamp, the site and a crumb. The
// caller closes the bar's actions span and opens <main>.
func hubHead(b *strings.Builder, base, title, docTitle, crumb string) {
	b.WriteString("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><title>" +
		html.EscapeString(docTitle) + " — " + html.EscapeString(title) + "</title>" + themeScript(".bar .actions") + mailCSS + hubCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span><a href="` + base + `">agent-local</a> <span class=dim>` + html.EscapeString(title) + `</span></h1>`)
	b.WriteString(`<span class=crumb>` + html.EscapeString(title) + ` » ` + html.EscapeString(crumb) + `</span><span class=actions>`)
}

// hubNav is the row of page links every hub page carries, so the errors and
// request pages are one click from each other and from the index.
func hubNav(base, current string) string {
	var b strings.Builder
	b.WriteString(`<p class=label>`)
	for _, p := range []struct{ href, name string }{
		{"", "tools"}, {"/errors", "errors"}, {"/requests", "requests"},
	} {
		cls := ""
		if p.href == current {
			cls = ` class=on`
		}
		b.WriteString(`<a href="` + base + p.href + `"` + cls + `>` + p.name + `</a>`)
	}
	b.WriteString(`</p>`)
	return b.String()
}

func hubIndex(w http.ResponseWriter, e *Engine, site *Site, wt *Worktree, base, title string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	hubHead(&b, base, title, "tools", "tools")
	b.WriteString(`</span></div><main>`)
	b.WriteString(`<h2>` + html.EscapeString(title) + `</h2>`)
	b.WriteString(hubSiteCard(e, site, wt))
	b.WriteString(`<p class=label>tools</p>`)
	b.WriteString(`<div class=cards>`)
	card := func(href, kicker, name, desc string) {
		b.WriteString(`<a class=card href="` + base + href + `"><span class=kicker>` + kicker + `</span><strong>` + name +
			`</strong><span class=desc>` + desc + `</span><span class=go>open →</span></a>`)
	}
	card("/adminer", "database", "Adminer", "Browse and edit this site's database.")
	card("/mail", "mail", "Inbox", "Email this site sent, kept here instead of being delivered.")
	card("/errors", "diagnose", "Errors", "PHP errors from this site's logs, grouped and counted.")
	card("/requests", "diagnose", "Requests", "Every request this site served, with timing and errors.")
	if site.IsWordPress() {
		// A POST, not a link: it mints a one-time session, and a GET that
		// logs someone in is something another page could trigger.
		b.WriteString(`<form class=card method=post action="` + base + `/login"><button><span class=kicker>wordpress</span>` +
			`<strong>Log in</strong><span class=desc>Open wp-admin as an administrator. No password.</span>` +
			`<span class=go>open wp-admin →</span></button></form>`)
	} else {
		b.WriteString(`<span class="card off"><span class=kicker>wordpress</span><strong>Log in</strong><span class=desc>` +
			`This is a ` + html.EscapeString(site.Kind.Label()) + ` site. wp-admin is WordPress only.</span></span>`)
	}
	b.WriteString(`</div></main>`)
	fmt.Fprint(w, b.String())
}

// wpVersionRe pulls the version out of wp-includes/version.php, which is a
// plain assignment WordPress keeps for exactly this kind of read.
var wpVersionRe = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)

// wpVersionOnDisk reads a WordPress version without booting WordPress. The
// card has to render instantly on every page load, so wp-cli (about a
// second, and able to fatal on a broken plugin) is not an option here.
func wpVersionOnDisk(site *Site) string {
	b, err := os.ReadFile(filepath.Join(site.WPDir, "wp-includes", "version.php"))
	if err != nil {
		return ""
	}
	if m := wpVersionRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// hubSiteCard is what the daemon knows about a site without asking anything
// else: the site row plus a wp-config parse, one file read and two directory
// listings. Everything a person opens a terminal to look up.
func hubSiteCard(e *Engine, site *Site, wt *Worktree) string {
	var b strings.Builder
	b.WriteString(`<dl>`)
	row := func(k, v string) {
		if v != "" {
			b.WriteString(`<dt>` + k + `</dt><dd>` + v + `</dd>`)
		}
	}
	esc := html.EscapeString
	poolID := site.Slug
	if wt != nil {
		poolID = wt.ID
	}
	row("app", esc(site.Kind.Label()))
	row("state", outStateHTML(e.FPMRunning(poolID)))
	row("php", esc(site.PHPVersion))
	if wt != nil {
		row("branch", esc(wt.Branch)+` <span class=dim>preview of `+esc(site.Domain)+`</span>`)
		row("docroot", `<code>`+esc(wt.Path)+`</code>`)
	} else {
		row("docroot", `<code>`+esc(site.WPDir)+`</code>`)
		if len(site.Aliases) > 0 {
			row("aliases", esc(strings.Join(site.Aliases, ", ")))
		}
	}
	db := esc(site.DBName) + ` <span class=dim>as ` + esc(site.DBUser) + ` @ 127.0.0.1:` + fmt.Sprint(DefaultDBPort) + `</span>`
	if !e.DBRunning() {
		db += ` <span class=dim>· server down</span>`
	}
	row("database", db)
	if site.IsWordPress() {
		if v := wpVersionOnDisk(site); v != "" {
			row("wordpress", esc(v))
		}
		dbg := WPDebugStatus(site)
		if dbg.Enabled {
			row("wp_debug", `on <span class=dim>· log: agent-local logs `+esc(dbg.LogName)+`</span>`)
		} else {
			row("wp_debug", `<span class=dim>off</span>`)
		}
	}
	if cps, err := e.ListCheckpoints(site.Slug); err == nil && len(cps) > 0 {
		newest := cps[0]
		label := newest.Label
		if label == "" {
			label = newest.Name
		}
		row("checkpoints", fmt.Sprint(len(cps))+` <span class=dim>· newest `+esc(label)+`, `+mailAge(newest.CreatedAt)+`</span>`)
	}
	if snaps, err := e.Snapshots(site.Slug); err == nil && len(snaps) > 0 {
		row("snapshots", fmt.Sprint(len(snaps))+` <span class=dim>· newest `+mailAge(snaps[0].CreatedAt)+`</span>`)
	}
	if fb := EffectiveMediaFallback(site); fb != "" {
		row("media", `missing uploads → <code>`+esc(fb)+`</code>`)
	}
	row("front", esc(FrontKind(e.Store)))
	b.WriteString(`</dl>`)
	return b.String()
}

// outStateHTML is outState for a browser: the same lamp, lit or parked.
func outStateHTML(running bool) string {
	if running {
		return `<span class=lamp></span> running`
	}
	return `<span class="lamp off"></span> <span class=dim>stopped</span>`
}

// hubLogin mints a one-time wp-admin link and sends the browser to it. POST
// only, and same-origin only: minting a session is a write, and a GET (or a
// form on another origin) must not be able to trigger it.
func hubLogin(w http.ResponseWriter, req *http.Request, e *Engine, site *Site, base string) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "agent-local: log in is a POST", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(req) {
		http.Error(w, "agent-local: cross-origin request refused", http.StatusForbidden)
		return
	}
	if !site.IsWordPress() {
		http.Error(w, notWordPress(site, "magic login").Error(), http.StatusConflict)
		return
	}
	link, err := e.MagicLogin(site, "")
	if err != nil {
		hubError(w, base, site.Domain, "Log in", "Could not mint a login link: "+err.Error())
		return
	}
	http.Redirect(w, req, link.URL, http.StatusSeeOther)
}

// sameOrigin reports whether a browser says this request came from our own
// page. Fetch metadata is sent by every current browser and is the cheapest
// thing that stops another origin's page from driving a write here; a
// request with no such header at all (curl, a tool) is allowed through,
// since it is not a browser being used against its user.
func sameOrigin(req *http.Request) bool {
	switch req.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}

// hubError renders a failure as one of our own pages rather than a bare
// http.Error, so a click that fails still lands somewhere with a way back.
func hubError(w http.ResponseWriter, base, title, crumb, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	var b strings.Builder
	hubHead(&b, base, title, crumb, crumb)
	b.WriteString(`</span></div><main><h2>` + html.EscapeString(crumb) + ` failed</h2><p class=empty>` +
		html.EscapeString(msg) + `</p>` + hubNav(base, "") + `</main>`)
	fmt.Fprint(w, b.String())
}

// hubSince reads the ?since= window shared by the errors and request pages,
// defaulting to an hour — long enough to cover "what just happened" without
// scanning a whole log.
func hubSince(req *http.Request) (time.Duration, string) {
	raw := req.URL.Query().Get("since")
	if raw == "" {
		raw = "1h"
	}
	d, err := parseSince(raw)
	if err != nil {
		return time.Hour, "1h"
	}
	return d, raw
}

// hubWindows renders the window selector both log pages carry.
func hubWindows(base, page, current string) string {
	var b strings.Builder
	b.WriteString(`<span class=windows>`)
	for _, w := range []string{"15m", "1h", "24h", "7d"} {
		cls := ""
		if w == current {
			cls = ` class=on`
		}
		b.WriteString(`<a href="` + base + page + `?since=` + w + `"` + cls + `>` + w + `</a>`)
	}
	b.WriteString(`</span>`)
	return b.String()
}
