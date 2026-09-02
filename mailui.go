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

// The palette is the site's: charcoal, warm gray ink, one green lamp, mono
// labels. Message HTML itself renders in a sandboxed white iframe — that is
// the recipient's view, and it should look like their inbox, not ours.
const mailCSS = `<style>
  :root { color-scheme: dark; }
  body { font: 13px/1.6 "IBM Plex Mono", ui-monospace, "SF Mono", Menlo, monospace;
         background: #0e0e0c; color: #e9e6de; margin: 0 auto; max-width: 960px;
         padding: 40px clamp(20px, 4vw, 56px) 64px; }
  a { color: inherit; text-decoration: none; } a:hover { color: #8fce9b; }
  .bar { display: flex; justify-content: space-between; align-items: center; gap: 16px;
         padding-bottom: 18px; border-bottom: 1px solid #26251f; }
  h1 { margin: 0; font-size: 11px; font-weight: 500; letter-spacing: .14em; text-transform: uppercase; color: #8b887c; }
  h1 a { color: #e9e6de; } h1 a:hover { color: #8fce9b; }
  .lamp { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: #8fce9b;
          margin-right: 10px; vertical-align: 1px; box-shadow: 0 0 9px rgba(143,206,155,.4); }
  .dim { color: #8b887c; }
  .empty { color: #8b887c; margin: 48px 0; max-width: 60ch; line-height: 1.8; }
  table { border-collapse: collapse; width: 100%; }
  td { padding: 16px 16px 16px 0; border-bottom: 1px solid #26251f; vertical-align: top; }
  td.age { width: 72px; white-space: nowrap; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; padding-top: 18px; }
  td.size { width: 56px; text-align: right; font-size: 11px; padding-right: 0; padding-top: 18px; }
  a.msg { display: block; } a.msg strong { display: block; font-weight: 600; }
  a.msg .dim { font-size: 12px; } tr:hover a.msg strong { color: #8fce9b; }
  button { font: inherit; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; color: #8b887c;
           background: none; border: 1px solid #26251f; border-radius: 4px; padding: 6px 12px; cursor: pointer; }
  button:hover { color: #e9e6de; border-color: #8b887c; }
  pre { white-space: pre-wrap; word-break: break-word; font: inherit; color: #e9e6de;
        background: #141412; border: 1px solid #26251f; border-radius: 10px; padding: 20px 24px; }
  iframe { width: 100%; height: 60vh; border: 1px solid #26251f; border-radius: 10px; background: #fff; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 8px 24px; margin: 28px 0; }
  dt { color: #8b887c; font-size: 11px; letter-spacing: .14em; text-transform: uppercase; line-height: 1.9; }
  dd { margin: 0; word-break: break-word; }
  .views { display: flex; gap: 20px; margin: 8px 0 12px; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; }
  .views a { color: #8b887c; } .views a:hover { color: #8fce9b; }
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
	b.WriteString("<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=5><title>mail — " + html.EscapeString(title) + "</title>" + mailCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span><a href="` + base + `">mail</a> <span class=dim>` + html.EscapeString(title) + `</span></h1>`)
	if len(sums) > 0 {
		b.WriteString(`<form method=post action="` + base + `/clear"><button>clear ` + fmt.Sprint(len(sums)) + `</button></form>`)
	}
	b.WriteString(`</div>`)
	if len(sums) == 0 {
		b.WriteString(`<p class=empty>Nothing yet. Every email this site sends — password resets, form
notifications, WooCommerce receipts — is caught here instead of being lost, the moment the site sends it.</p>`)
		fmt.Fprint(w, b.String())
		return
	}
	b.WriteString("<table>")
	for _, s := range sums {
		subject := s.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		b.WriteString(fmt.Sprintf(`<tr><td class="dim age">%s</td><td><a class=msg href="%s/msg/%s"><strong>%s</strong><span class=dim>%s → %s</span></a></td><td class="dim size">%s</td></tr>`,
			mailAge(s.Date), base, s.ID, html.EscapeString(subject),
			html.EscapeString(s.From), html.EscapeString(s.To), humanBytes(s.Size)))
	}
	b.WriteString("</table>")
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
	b.WriteString("<!doctype html><meta charset=utf-8><title>" + html.EscapeString(subject) + "</title>" + mailCSS)
	b.WriteString(`<div class=bar><h1><span class=lamp></span><a href="` + base + `">mail</a> <span class=dim>` + html.EscapeString(title) + `</span></h1></div>`)
	b.WriteString("<dl>")
	for _, row := range [][2]string{
		{"subject", subject}, {"from", msg.From}, {"to", msg.To},
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
		b.WriteString(`<p class=views><span class=dim>html</span><a href="` + base + `/msg/` + msg.ID + `/html">open on its own</a></p>`)
		b.WriteString(`<p><iframe sandbox src="` + base + `/msg/` + msg.ID + `/html"></iframe></p>`)
	}
	if msg.Text != "" {
		b.WriteString("<pre>" + html.EscapeString(msg.Text) + "</pre>")
	}
	if msg.HTML == "" && msg.Text == "" {
		b.WriteString("<p class=empty>No readable body — see the raw message.</p>")
	}
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
