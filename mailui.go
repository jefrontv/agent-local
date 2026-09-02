package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// The inbox UI is rendered by this binary directly — no PHP, nothing to
// download. The router serves it at /.agent-local/mail on every site domain;
// under the apache front the same pages come through a ProxyPass to the
// daemon (/mail-ui/<id>), so switching fronts never loses the inbox.

// The same frame as the database GUI: a 52px top bar carrying the lamp, the
// title and the session actions, content padded below it, hairline rows.
// The site's fonts and palette. Message HTML itself renders in a sandboxed
// white iframe — that is the recipient's view, and it should look like their
// inbox, not ours.
const mailCSS = `<style>
  @import url("https://fonts.googleapis.com/css2?family=Archivo:wdth,wght@62..125,100..900&family=IBM+Plex+Mono:wght@400;500;600&display=swap");
  :root { color-scheme: dark;
          --bg: #0e0e0c; --panel: #141412; --lit: #1a1a17; --hair: #26251f; --mark: #45443e;
          --dim: #8b887c; --fg: #e9e6de; --lamp: #8fce9b;
          --mono: "IBM Plex Mono", ui-monospace, "SF Mono", Menlo, monospace;
          --sans: "Archivo", -apple-system, "Helvetica Neue", Arial, sans-serif; }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--fg); font: 13.5px/1.55 var(--sans); -webkit-font-smoothing: antialiased; }
  a { color: inherit; text-decoration: none; } a:hover { color: var(--lamp); }
  .bar { position: fixed; top: 0; left: 0; right: 0; height: 52px; z-index: 2; display: flex; align-items: center; gap: 24px;
         padding: 0 40px; background: var(--bg); border-bottom: 1px solid var(--hair); }
  .bar h1 { margin: 0; display: flex; align-items: center; gap: 10px; font: 500 11px var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--fg); }
  .bar h1 .dim { color: var(--dim); }
  .bar .crumb { font: 500 11px var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--dim); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bar form { margin-left: auto; }
  .lamp { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: var(--lamp); box-shadow: 0 0 9px rgba(143,206,155,.4); }
  main { padding: 84px 40px 64px; max-width: 1100px; }
  .dim { color: var(--dim); }
  .empty { color: var(--dim); margin: 24px 0; max-width: 60ch; line-height: 1.8; }
  h2 { margin: 0 0 20px; padding: 0 0 20px; border-bottom: 1px solid var(--hair); font: 700 22px/1.15 var(--sans); font-variation-settings: "wdth" 112; }
  .count { font: 500 11px var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--dim); display: flex; align-items: center; gap: 10px; margin: 0 0 4px; }
  table { border-collapse: collapse; width: 100%; font-family: var(--mono); font-size: 12.5px; }
  td { padding: 14px 16px 14px 0; border-bottom: 1px solid var(--hair); vertical-align: top; }
  td.age { width: 88px; white-space: nowrap; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; color: var(--dim); padding-top: 16px; }
  td.size { width: 64px; text-align: right; font-size: 11px; color: var(--dim); padding: 16px 0 14px; }
  a.msg { display: block; } a.msg strong { display: block; font: 600 14px/1.4 var(--sans); }
  a.msg .dim { font-size: 12px; } tr:hover a.msg strong { color: var(--lamp); }
  button { font: 500 10.5px var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--fg);
           background: none; border: 1px solid var(--mark); border-radius: 6px; padding: 6px 12px; cursor: pointer; }
  button:hover { color: var(--lamp); border-color: var(--lamp); }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 10px 28px; margin: 0 0 28px; font-family: var(--mono); font-size: 12.5px; }
  dt { color: var(--dim); font-size: 10.5px; letter-spacing: .14em; text-transform: uppercase; line-height: 1.9; }
  dd { margin: 0; word-break: break-word; }
  .label { display: flex; align-items: center; gap: 10px; margin: 28px 0 12px; font: 500 11px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--dim); }
  .label a { color: var(--dim); margin-left: auto; letter-spacing: .1em; } .label a:hover { color: var(--lamp); }
  pre { margin: 0; white-space: pre-wrap; word-break: break-word; font: 13px/1.6 var(--mono); color: var(--fg);
        background: var(--panel); border: 1px solid var(--hair); border-radius: 10px; padding: 20px 24px; }
  iframe { display: block; width: 100%; height: 62vh; border: 1px solid var(--hair); border-radius: 10px; background: #fff; }
  @media (max-width: 720px) { .bar { padding: 0 20px; } main { padding: 76px 20px 48px; } td.age { width: 64px; } .bar .crumb { display: none; } }
</style>`

// serveMailUI answers everything under one inbox UI. base is where the inbox
// is mounted, rest the path below it: "" list, /msg/<id> view, /msg/<id>/html
// the HTML part for the iframe, /msg/<id>/raw the .eml, POST /clear empties.
func serveMailUI(w http.ResponseWriter, req *http.Request, id, base, rest, title string) {
	switch {
	case rest == "" || rest == "/":
		mailUIList(w, id, base, title)
	case rest == "/clear" && req.Method == "POST":
		ClearMail(id)
		http.Redirect(w, req, base, http.StatusSeeOther)
	case strings.HasPrefix(rest, "/msg/"):
		mid, sub, _ := strings.Cut(strings.TrimPrefix(rest, "/msg/"), "/")
		switch sub {
		case "":
			mailUIMessage(w, id, base, title, mid)
		case "html":
			msg, err := GetMail(id, mid)
			if err != nil || msg.HTML == "" {
				http.NotFound(w, req)
				return
			}
			// The message is arbitrary third-party HTML: render it inert.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src http: https: data: cid:; style-src 'unsafe-inline'")
			fmt.Fprint(w, msg.HTML)
		case "raw":
			raw, err := GetMailRaw(id, mid)
			if err != nil {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(raw)
		default:
			http.NotFound(w, req)
		}
	default:
		http.NotFound(w, req)
	}
}

func mailUIList(w http.ResponseWriter, id, base, title string) {
	sums, err := ListMail(id)
	if err != nil {
		http.Error(w, "agent-local: mail: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><meta http-equiv=refresh content=5><title>mail — " + html.EscapeString(title) + "</title>" + mailCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span><a href="` + base + `">mail</a> <span class=dim>` + html.EscapeString(title) + `</span></h1>`)
	b.WriteString(`<span class=crumb>` + html.EscapeString(title) + ` » inbox</span>`)
	if len(sums) > 0 {
		b.WriteString(`<form method=post action="` + base + `/clear"><button>clear ` + fmt.Sprint(len(sums)) + `</button></form>`)
	}
	b.WriteString(`</div><main>`)
	if len(sums) == 0 {
		b.WriteString(`<h2>Nothing yet</h2><p class=empty>Every email this site sends — password resets, form
notifications, WooCommerce receipts — is caught here instead of being lost, the moment the site sends it.</p></main>`)
		fmt.Fprint(w, b.String())
		return
	}
	b.WriteString(`<p class=count><span class=lamp></span>` + fmt.Sprint(len(sums)) + ` captured · newest first · refreshes every 5s</p>`)
	b.WriteString("<table>")
	for _, s := range sums {
		subject := s.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		b.WriteString(fmt.Sprintf(`<tr><td class=age>%s</td><td><a class=msg href="%s/msg/%s"><strong>%s</strong><span class=dim>%s → %s</span></a></td><td class=size>%s</td></tr>`,
			mailAge(s.Date), base, s.ID, html.EscapeString(subject),
			html.EscapeString(s.From), html.EscapeString(s.To), humanBytes(s.Size)))
	}
	b.WriteString("</table></main>")
	fmt.Fprint(w, b.String())
}

func mailUIMessage(w http.ResponseWriter, id, base, title, mid string) {
	msg, err := GetMail(id, mid)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	b.WriteString("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width,initial-scale=1\"><title>" + html.EscapeString(subject) + "</title>" + mailCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span><a href="` + base + `">mail</a> <span class=dim>` + html.EscapeString(title) + `</span></h1>`)
	b.WriteString(`<span class=crumb>` + html.EscapeString(title) + ` » <a href="` + base + `">inbox</a> » ` + html.EscapeString(subject) + `</span>`)
	b.WriteString(`<form action="` + base + `"><button>back to inbox</button></form></div><main>`)
	b.WriteString("<h2>" + html.EscapeString(subject) + "</h2><dl>")
	for _, row := range [][2]string{
		{"from", msg.From}, {"to", msg.To},
		{"date", msg.Date.Format("2006-01-02 15:04:05") + " (" + mailAge(msg.Date) + ")"},
	} {
		if row[1] != "" {
			b.WriteString("<dt>" + row[0] + "</dt><dd>" + html.EscapeString(row[1]) + "</dd>")
		}
	}
	for _, a := range msg.Attachments {
		b.WriteString("<dt>attachment</dt><dd>" + html.EscapeString(a.Filename) + " <span class=dim>" + a.MIMEType + ", " + humanBytes(int64(a.Size)) + "</span></dd>")
	}
	b.WriteString(`<dt>raw</dt><dd><a href="` + base + `/msg/` + msg.ID + `/raw">.eml</a></dd></dl>`)
	if msg.HTML != "" {
		b.WriteString(`<p class=label><span class=lamp></span>html, as the recipient sees it<a href="` + base + `/msg/` + msg.ID + `/html">open on its own ↗</a></p>`)
		b.WriteString(`<iframe sandbox src="` + base + `/msg/` + msg.ID + `/html"></iframe>`)
	}
	if msg.Text != "" {
		b.WriteString(`<p class=label><span class=lamp></span>text</p><pre>` + html.EscapeString(msg.Text) + "</pre>")
	}
	if msg.HTML == "" && msg.Text == "" {
		b.WriteString("<p class=empty>No readable body — see the raw message.</p>")
	}
	b.WriteString("</main>")
	fmt.Fprint(w, b.String())
}

// mailAge renders how long ago a message arrived, at the resolution a human
// scanning an inbox cares about.
func mailAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
