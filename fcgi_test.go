package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fcgiPool is a real FastCGI responder on a unix socket, standing in for a
// php-fpm pool: it echoes the CGI params and body it was handed.
func fcgiPool(t *testing.T) string {
	t.Helper()
	// macOS caps a unix socket path at 104 bytes; t.TempDir() can exceed it.
	dir, err := os.MkdirTemp("/tmp", "alfcgi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "pool.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := fcgi.ProcessEnv(r)
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "gone")
			return
		case "/redirect":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
			return
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("x", 200<<10))
			return
		}
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		fmt.Fprintf(w, "method=%s script=%s https=%s query=%s host=%s len=%d body=%s ua=%s",
			r.Method, env["SCRIPT_FILENAME"], tlsFlag(r), r.URL.RawQuery, r.Host, len(body), body, r.Header.Get("User-Agent"))
	}))
	return sock
}

func TestRouterSpeaksFastCGI(t *testing.T) {
	e := testEngine(t)
	rt := &Router{engine: e}
	sock := fcgiPool(t)
	run := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		rt.proxyFCGIScript(w, req, "/srv/demo", sock, "demo", "demo.test", "/srv/demo/index.php", "/index.php")
		return w
	}

	get := httptest.NewRequest("GET", "https://demo.test/page?x=1", nil)
	get.Header.Set("User-Agent", "probe")
	w := run(get)
	body := w.Body.String()
	for _, want := range []string{"method=GET", "script=/srv/demo/index.php", "https=on", "query=x=1", "host=demo.test", "ua=probe"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET: missing %q in %q", want, body)
		}
	}
	if w.Code != 200 || len(w.Result().Header.Values("Set-Cookie")) != 2 {
		t.Errorf("GET: status %d cookies %v", w.Code, w.Result().Header.Values("Set-Cookie"))
	}

	post := httptest.NewRequest("POST", "http://demo.test/form", strings.NewReader("name=al"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if body := run(post).Body.String(); !strings.Contains(body, "len=7 body=name=al") || !strings.Contains(body, "https=off ") {
		t.Errorf("POST: %q", body)
	}

	// A chunked upload has no length up front; the router buffers it first.
	chunked := httptest.NewRequest("POST", "http://demo.test/upload", io.NopCloser(strings.NewReader("streamed")))
	chunked.ContentLength = -1
	if body := run(chunked).Body.String(); !strings.Contains(body, "len=8 body=streamed") {
		t.Errorf("chunked: %q", body)
	}

	if w := run(httptest.NewRequest("GET", "http://demo.test/missing", nil)); w.Code != 404 || w.Body.String() != "gone" {
		t.Errorf("404: %d %q", w.Code, w.Body.String())
	}
	if w := run(httptest.NewRequest("GET", "http://demo.test/redirect", nil)); w.Code != 302 || w.Header().Get("Location") != "/elsewhere" {
		t.Errorf("302: %d %v", w.Code, w.Header())
	}
	if w := run(httptest.NewRequest("GET", "http://demo.test/big", nil)); w.Body.Len() != 200<<10 {
		t.Errorf("big body: %d bytes", w.Body.Len())
	}
}

func TestRouterReportsDeadPool(t *testing.T) {
	e := testEngine(t)
	rt := &Router{engine: e}
	w := httptest.NewRecorder()
	rt.proxyFCGIScript(w, httptest.NewRequest("GET", "http://demo.test/", nil), "/srv", "/tmp/al-no-such.sock", "nope", "demo.test", "/srv/index.php", "/index.php")
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "php-fpm connect") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func tlsFlag(r *http.Request) string {
	if r.TLS != nil {
		return "on"
	}
	return "off"
}
