package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mail capture: every message a site sends lands in a per-site inbox instead
// of macOS's usually-dead postfix, where it silently vanished — password
// resets on local sites simply did not work. The mechanism is one line of
// pool config: PHP's mail() hands the message to sendmail_path, which is this
// binary running `sendmail --site <id>`. Nothing inside the site changes, so
// every plugin that sends mail the normal way is caught.
//
// Messages are raw .eml files under ~/.agent-local/mail/<id>/ — readable by
// the router UI at /.agent-local/mail, the CLI, and agents through list_mail/
// get_mail, which closes the loop on "did submitting that form send the
// right email".

// MailPath is the reserved URL path that serves a site's inbox, kept out of
// the WordPress tree like the database GUI.
const MailPath = "/.agent-local/mail"

// mailKeep is how many captured messages survive per inbox. Old mail is a
// cache of past behaviour, not a record; pruning on write keeps the directory
// from growing for as long as a form plugin misfires.
const mailKeep = 200

// mailMaxSize caps one captured message (attachments included). PHP blocks
// on the pipe, so the cap also bounds how long a runaway mail() call holds a
// worker.
const mailMaxSize = 32 << 20

// MailDir is a pool's inbox: sites file under their slug, branch previews
// under their worktree id, so a preview's test mail never muddies the site's.
func (p Paths) MailDir(id string) string { return filepath.Join(p.Root, "mail", id) }

// MailURL is the browser URL for a site's inbox.
func MailURL(domain string) string {
	return strings.TrimRight(BareDomainURL(domain), "/") + MailPath
}

// isMailPath reports whether a request URL is the mail UI.
func isMailPath(urlPath string) bool {
	return urlPath == MailPath || strings.HasPrefix(urlPath, MailPath+"/")
}

// runSendmail is the sendmail_path target: read one message from stdin, file
// it, exit. Extra argv (PHP appends recipients, `-t -i` conventions) is
// tolerated and ignored — the message's own headers are the record. A
// non-zero exit makes PHP's mail() return false, so failing loudly here is
// what makes a plugin report "could not send" instead of pretending.
func runSendmail(args []string) error {
	id := flagValue(args, "--site")
	if id == "" {
		return fmt.Errorf("sendmail: --site required")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, mailMaxSize))
	if err != nil {
		return fmt.Errorf("sendmail: read: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("sendmail: empty message")
	}
	_, err = StoreMail(id, raw)
	return err
}

// StoreMail files one raw message into an inbox and prunes old ones. The
// filename is the capture instant, which doubles as the message id and the
// sort key.
func StoreMail(id string, raw []byte) (string, error) {
	dir := P().MailDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	msgID := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(filepath.Join(dir, msgID+".eml"), raw, 0o644); err != nil {
		return "", err
	}
	pruneMail(dir, mailKeep)
	return msgID, nil
}

// pruneMail keeps the newest n messages. Best-effort by design: pruning
// failing must never fail the send that triggered it.
func pruneMail(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := []string{}
	for _, ent := range ents {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".eml") {
			names = append(names, ent.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names) // same-width unix nanos sort chronologically
	for _, name := range names[:len(names)-keep] {
		os.Remove(filepath.Join(dir, name))
	}
}

// MailSummary is one inbox line.
type MailSummary struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"` // capture time, not the (often absent) Date header
	Size    int64     `json:"size"`
}

// MailAttachment describes an attachment without carrying its bytes.
type MailAttachment struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Size     int    `json:"size"`
}

// MailMessage is one fully parsed message.
type MailMessage struct {
	MailSummary
	Text        string            `json:"text,omitempty"`
	HTML        string            `json:"html,omitempty"`
	Headers     map[string]string `json:"headers"`
	Attachments []MailAttachment  `json:"attachments,omitempty"`
}

// mailFile resolves a message id to its file, rejecting anything that is not
// a bare id — the id doubles as a path segment in URLs.
func mailFile(id, msgID string) (string, error) {
	if msgID == "" || strings.ContainsAny(msgID, "/\\.") {
		return "", fmt.Errorf("bad message id: %q", msgID)
	}
	path := filepath.Join(P().MailDir(id), msgID+".eml")
	if !fileExists(path) {
		return "", fmt.Errorf("no message %s in %s", msgID, id)
	}
	return path, nil
}

// ListMail returns an inbox newest-first. A missing directory is an empty
// inbox, not an error — most sites have never sent anything.
func ListMail(id string) ([]MailSummary, error) {
	dir := P().MailDir(id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []MailSummary{}, nil
		}
		return nil, err
	}
	out := []MailSummary{}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".eml") {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		msgID := strings.TrimSuffix(ent.Name(), ".eml")
		sum := MailSummary{ID: msgID, Date: mailTime(msgID, fi.ModTime()), Size: fi.Size()}
		if f, err := os.Open(filepath.Join(dir, ent.Name())); err == nil {
			if m, err := mail.ReadMessage(f); err == nil {
				sum.From = decodeMIMEHeader(m.Header.Get("From"))
				sum.To = decodeMIMEHeader(m.Header.Get("To"))
				sum.Subject = decodeMIMEHeader(m.Header.Get("Subject"))
			}
			f.Close()
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// MailCount is the cheap inbox stat surfaces show without parsing anything:
// how many messages, and when the last one arrived.
func MailCount(id string) (int, time.Time) {
	ents, err := os.ReadDir(P().MailDir(id))
	if err != nil {
		return 0, time.Time{}
	}
	n, latest := 0, time.Time{}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".eml") {
			continue
		}
		n++
		msgID := strings.TrimSuffix(ent.Name(), ".eml")
		mt := time.Time{}
		if fi, err := ent.Info(); err == nil {
			mt = fi.ModTime()
		}
		if t := mailTime(msgID, mt); t.After(latest) {
			latest = t
		}
	}
	return n, latest
}

// GetMail parses one message fully: decoded text and HTML bodies, headers,
// attachment metadata.
func GetMail(id, msgID string) (*MailMessage, error) {
	path, err := mailFile(id, msgID)
	if err != nil {
		return nil, err
	}
	return parseMailFile(path)
}

// GetMailRaw returns one message exactly as captured.
func GetMailRaw(id, msgID string) ([]byte, error) {
	path, err := mailFile(id, msgID)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// ClearMail empties an inbox, reporting how many messages went.
func ClearMail(id string) (int, error) {
	dir := P().MailDir(id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, ent := range ents {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".eml") {
			if os.Remove(filepath.Join(dir, ent.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

// mailTime reads the capture instant a message id encodes, falling back to
// the file's mtime for anything dropped in by hand.
func mailTime(msgID string, fallback time.Time) time.Time {
	if ns, err := strconv.ParseInt(msgID, 10, 64); err == nil && ns > 0 {
		return time.Unix(0, ns)
	}
	return fallback
}

// parseMailFile parses a raw .eml into bodies, headers and attachments.
func parseMailFile(path string) (*MailMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := mail.ReadMessage(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	fi, _ := f.Stat()
	msgID := strings.TrimSuffix(filepath.Base(path), ".eml")
	msg := &MailMessage{
		MailSummary: MailSummary{
			ID:      msgID,
			From:    decodeMIMEHeader(m.Header.Get("From")),
			To:      decodeMIMEHeader(m.Header.Get("To")),
			Subject: decodeMIMEHeader(m.Header.Get("Subject")),
			Date:    mailTime(msgID, fi.ModTime()),
			Size:    fi.Size(),
		},
		Headers: map[string]string{},
	}
	for k, v := range m.Header {
		msg.Headers[k] = decodeMIMEHeader(strings.Join(v, ", "))
	}
	walkMailPart(msg, m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Header.Get("Content-Disposition"), m.Body)
	return msg, nil
}

// walkMailPart recurses through the MIME structure, filling the first
// text/plain and text/html bodies it meets and recording everything else as
// an attachment. Unknown structure degrades to attachment metadata rather
// than an error — a mis-built plugin email is exactly the mail worth seeing.
func walkMailPart(msg *MailMessage, ctype, cte, disp string, body io.Reader) {
	mediaType, params, err := mime.ParseMediaType(ctype)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			walkMailPart(msg, p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p.Header.Get("Content-Disposition"), p)
		}
	}
	decoded, err := io.ReadAll(io.LimitReader(decodeCTE(body, cte), mailMaxSize))
	if err != nil {
		return
	}
	dispType, dispParams, _ := mime.ParseMediaType(disp)
	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	attached := dispType == "attachment" || filename != ""
	switch {
	case !attached && mediaType == "text/plain" && msg.Text == "":
		msg.Text = string(decoded)
	case !attached && mediaType == "text/html" && msg.HTML == "":
		msg.HTML = string(decoded)
	default:
		if filename == "" {
			filename = "(inline " + mediaType + ")"
		}
		msg.Attachments = append(msg.Attachments, MailAttachment{
			Filename: filename, MIMEType: mediaType, Size: len(decoded),
		})
	}
}

// decodeCTE unwraps a Content-Transfer-Encoding. Anything unrecognized
// passes through untouched.
func decodeCTE(r io.Reader, cte string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

// decodeMIMEHeader renders =?UTF-8?...?= encoded words readable, passing
// plain values through.
func decodeMIMEHeader(v string) string {
	dec := mime.WordDecoder{}
	if out, err := dec.DecodeHeader(v); err == nil {
		return out
	}
	return v
}
