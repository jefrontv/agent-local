package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const plainMail = "To: you@example.test\r\nSubject: Password Reset\r\nFrom: WordPress <wp@s.test>\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nSomeone requested a password reset.\r\n"

const multipartMail = "To: shop@example.test\r\n" +
	"From: =?UTF-8?B?Sm9zw6k=?= <jose@s.test>\r\n" +
	"Subject: =?UTF-8?Q?Order_=E2=80=94_received?=\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=OUTER\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: multipart/alternative; boundary=INNER\r\n" +
	"\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Thanks =E2=80=94 your order is in.\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/html; charset=UTF-8\r\n" +
	"\r\n" +
	"<p>Thanks — your order is in.</p>\r\n" +
	"--INNER--\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf; name=invoice.pdf\r\n" +
	"Content-Disposition: attachment; filename=invoice.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0xLjQ=\r\n" +
	"--OUTER--\r\n"

func TestMailStoreListGet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// An inbox that never received anything is empty, not an error.
	sums, err := ListMail("s")
	if err != nil || len(sums) != 0 {
		t.Fatalf("fresh inbox = %v, %v", sums, err)
	}

	id1, err := StoreMail("s", []byte(plainMail))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := StoreMail("s", []byte(multipartMail))
	if err != nil {
		t.Fatal(err)
	}

	sums, err = ListMail("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 {
		t.Fatalf("listed %d, want 2", len(sums))
	}
	// Newest first: the second store leads.
	if sums[0].ID != id2 || sums[1].ID != id1 {
		t.Errorf("order = %s, %s", sums[0].ID, sums[1].ID)
	}
	// Encoded words come back readable at the summary level already.
	if sums[0].Subject != "Order — received" {
		t.Errorf("subject = %q", sums[0].Subject)
	}
	if !strings.Contains(sums[0].From, "José") {
		t.Errorf("from = %q", sums[0].From)
	}

	// Full parse: both bodies, decoded transfer encodings, attachment metadata.
	msg, err := GetMail("s", id2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Text, "Thanks — your order is in.") {
		t.Errorf("quoted-printable text not decoded: %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "<p>Thanks") {
		t.Errorf("html part missing: %q", msg.HTML)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "invoice.pdf" || msg.Attachments[0].Size != len("%PDF-1.4") {
		t.Errorf("attachments = %+v", msg.Attachments)
	}

	n, err := ClearMail("s")
	if err != nil || n != 2 {
		t.Fatalf("cleared %d, %v", n, err)
	}
	if c, _ := MailCount("s"); c != 0 {
		t.Errorf("count after clear = %d", c)
	}
}

func TestMailPrune(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := P().MailDir("s")
	os.MkdirAll(dir, 0o755)
	base := time.Now().Add(-time.Hour).UnixNano()
	for i := range 7 {
		name := fmt.Sprintf("%d.eml", base+int64(i))
		os.WriteFile(filepath.Join(dir, name), []byte(plainMail), 0o644)
	}
	pruneMail(dir, 5)
	sums, _ := ListMail("s")
	if len(sums) != 5 {
		t.Fatalf("kept %d, want 5", len(sums))
	}
	// The oldest two went.
	for _, s := range sums {
		if s.ID == fmt.Sprint(base) || s.ID == fmt.Sprint(base+1) {
			t.Errorf("oldest message survived pruning: %s", s.ID)
		}
	}
}

func TestMailFileRejectsTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	StoreMail("s", []byte(plainMail))
	for _, bad := range []string{"../s/123", "..", "a.eml", "x/y", ""} {
		if _, err := mailFile("s", bad); err == nil {
			t.Errorf("mailFile accepted %q", bad)
		}
	}
}

func TestMailCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if n, _ := MailCount("s"); n != 0 {
		t.Fatalf("fresh count = %d", n)
	}
	StoreMail("s", []byte(plainMail))
	id2, _ := StoreMail("s", []byte(plainMail))
	n, latest := MailCount("s")
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if want := mailTime(id2, time.Time{}); !latest.Equal(want) {
		t.Errorf("latest = %v, want %v", latest, want)
	}
}

func TestServeMailUI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id, _ := StoreMail("s", []byte(multipartMail))

	get := func(rest string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://s.test"+MailPath+rest, nil)
		serveMailUI(w, req, "s", MailPath, rest, "s.test")
		return w
	}

	// List names the message and escapes what it prints.
	w := get("")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Order — received") {
		t.Errorf("list = %d\n%s", w.Code, w.Body.String())
	}

	// Message view embeds the sandboxed html part and the text body.
	w = get("/msg/" + id)
	if !strings.Contains(w.Body.String(), "iframe sandbox") || !strings.Contains(w.Body.String(), "invoice.pdf") {
		t.Errorf("message view:\n%s", w.Body.String())
	}

	// The html part is served inert.
	w = get("/msg/" + id + "/html")
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("html part CSP = %q", csp)
	}

	// Raw round-trips the capture byte for byte.
	w = get("/msg/" + id + "/raw")
	if w.Body.String() != multipartMail {
		t.Error("raw did not round-trip")
	}

	// Clear is POST-only and redirects home.
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://s.test"+MailPath+"/clear", nil)
	serveMailUI(w, req, "s", MailPath, "/clear", "s.test")
	if w.Code != 303 {
		t.Errorf("clear = %d, want 303", w.Code)
	}
	if n, _ := MailCount("s"); n != 0 {
		t.Error("clear left mail behind")
	}
}
