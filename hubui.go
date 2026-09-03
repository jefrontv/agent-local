package main

import (
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"strings"
)

// HubPath is the reserved URL path serving a site's local tooling index —
// links to the database GUI and the mail inbox for whatever site the Host
// header resolved to. Like those pages it is rendered by this binary, kept
// out of the WordPress tree so a permalink cannot swallow it, and stays
// local-only on shares.
const HubPath = "/.agent-local"

// isHubPath reports whether a request URL is the tooling index itself: the
// exact path, with or without a trailing slash. Anything deeper belongs to
// Adminer, the inbox, or WordPress.
func isHubPath(urlPath string) bool {
	clean := strings.TrimSuffix(filepath.Clean("/"+urlPath), "/")
	return clean == HubPath
}

// hubCSS extends the inbox frame with cards, in the same tokens — panel,
// hairlines, mono kickers, the green lamp on hover. Kept separate so the
// inbox stylesheet stays untouched.
const hubCSS = `<style>
  .cards { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-top: 4px; }
  .card { display: block; background: var(--panel); border: 1px solid var(--hair); border-radius: 12px; padding: 24px; }
  .card:hover { border-color: var(--lamp); }
  .card .kicker { display: block; margin-bottom: 12px; font: 500 11px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .card strong { display: block; margin-bottom: 8px; font: 700 19px/1.25 var(--sans); font-variation-settings: "wdth" 112; }
  .card:hover strong { color: var(--lamp); }
  .card .desc { color: var(--dim); font-size: 13px; }
  .card .go { display: block; margin-top: 16px; font: 500 11px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .card:hover .go { color: var(--lamp); }
  @media (max-width: 720px) { .cards { grid-template-columns: 1fr; } }
</style>`

// serveHubUI renders the tooling index. base is the browser-facing mount
// (HubPath on the router, also HubPath through the apache ProxyPass — the
// same convention serveMailUI follows), title the site domain shown.
func serveHubUI(w http.ResponseWriter, base, title string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><title>tools — " + html.EscapeString(title) + "</title>" + mailCSS + hubCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span>agent-local <span class=dim>` + html.EscapeString(title) + `</span></h1>`)
	b.WriteString(`<span class=crumb>` + html.EscapeString(title) + ` » tools</span></div><main>`)
	b.WriteString(`<h2>Local tools</h2>`)
	b.WriteString(`<div class=cards>`)
	card := func(href, kicker, name, desc string) {
		b.WriteString(`<a class=card href="` + base + href + `"><span class=kicker>` + kicker + `</span><strong>` + name + `</strong><span class=desc>` + desc + `</span><span class=go>open →</span></a>`)
	}
	card("/adminer", "database", "Adminer", "Browse and edit this site's tables, straight in the browser.")
	card("/mail", "mail", "Inbox", "Every email the site sends — resets, receipts, forms — caught here.")
	b.WriteString(`</div>`)
	b.WriteString(`</main>`)
	fmt.Fprint(w, b.String())
}
