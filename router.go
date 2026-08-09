package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Router is the built-in vhost reverse proxy: one listener per scheme,
// Host-header routing, FastCGI to per-site php-fpm sockets, TLS with
// per-domain self-signed certs.
type Router struct {
	engine *Engine
	mu     sync.RWMutex
	certs  map[string]*tls.Certificate
}

// NewRouter builds a router bound to the engine/store.
func NewRouter(e *Engine) *Router { return &Router{engine: e, certs: map[string]*tls.Certificate{}} }

// ListenAndServe starts HTTP on :httpPort and HTTPS on :httpsPort.
func (r *Router) ListenAndServe(httpPort, httpsPort int) error {
	tlsCfg := &tls.Config{
		GetCertificate: r.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	httpSrv := &http.Server{Handler: r}
	httpsSrv := &http.Server{Handler: r}
	// macOS dual-stack listeners can drop IPv4-mapped connections; bind each
	// family explicitly so both curl http://127.0.0.1 and ::1 work.
	var bound []net.Listener
	for _, addr := range []string{
		fmt.Sprintf("0.0.0.0:%d", httpPort),
		fmt.Sprintf("[::]:%d", httpPort),
	} {
		if l, err := net.Listen("tcp", addr); err == nil {
			bound = append(bound, l)
			go httpSrv.Serve(l)
		}
	}
	if len(bound) == 0 {
		return fmt.Errorf("cannot bind :%d", httpPort)
	}
	boundTLS := 0
	for _, addr := range []string{
		fmt.Sprintf("0.0.0.0:%d", httpsPort),
		fmt.Sprintf("[::]:%d", httpsPort),
	} {
		if l, err := net.Listen("tcp", addr); err == nil {
			boundTLS++
			go httpsSrv.Serve(tls.NewListener(l, tlsCfg))
		}
	}
	if boundTLS == 0 {
		return fmt.Errorf("cannot bind :%d", httpsPort)
	}
	return nil
}

// ServeHTTP routes by Host header. Static files stream straight from disk;
// PHP scripts and WordPress permalinks go to the site's php-fpm pool.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.engine.Store.ReloadIfChanged()
	host := req.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	wpdir, fpmID, _, ok := r.engine.Resolve(host)
	if !ok {
		http.Error(w, "agent-local: no site for host "+host, http.StatusBadGateway)
		return
	}

	// Static fast path: real files that aren't PHP.
	if req.Method != "POST" && r.serveStatic(w, req, wpdir) {
		return
	}

	sock := r.engine.fpmSock(fpmID)
	if !fileExists(sock) {
		// A known host whose pool is down (machine rebooted, daemon replaced,
		// stale state) is served by starting the pool instead of erroring:
		// the request pays the ~1s boot, every later one is warm.
		if err := r.engine.ensurePool(fpmID); err != nil {
			http.Error(w, "agent-local: site "+host+" could not start: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	r.proxyFCGI(w, req, wpdir, sock, host)
}

// serveStatic serves a real file from disk. Returns true if it handled the
// request (file found and served). Guards against path traversal.
func (r *Router) serveStatic(w http.ResponseWriter, req *http.Request, wpdir string) bool {
	urlPath := req.URL.Path
	if urlPath == "" || urlPath == "/" {
		return false
	}
	if strings.HasSuffix(strings.ToLower(urlPath), ".php") {
		return false
	}
	clean := filepath.Clean("/" + urlPath)
	full := filepath.Join(wpdir, clean)
	// traversal guard: joined path must stay inside wpdir
	if !strings.HasPrefix(full, filepath.Clean(wpdir)+string(os.PathSeparator)) && full != filepath.Clean(wpdir) {
		return false
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		return false // let FPM / WordPress handle it (404 or rewrite)
	}
	http.ServeFile(w, req, full)
	return true
}

// getCertificate lazily loads/creates a cert for the requested SNI host.
func (r *Router) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	r.mu.RLock()
	if c, ok := r.certs[host]; ok {
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()
	certPath, keyPath, _, err := EnsureCert(host)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.certs[host] = &cert
	r.mu.Unlock()
	return &cert, nil
}

// ---------- FastCGI client (minimal FCGI/1.0 responder) ----------

const (
	fcgiVersion1      = 1
	fcgiBeginRequest  = 1
	fcgiParams        = 4
	fcgiStdin         = 5
	fcgiStdout        = 6
	fcgiStderr        = 7
	fcgiEndRequest    = 3
	fcgiRoleResponder = 1
	fcgiMaxContent    = 65535
)

// proxyFCGI speaks FastCGI to the pool socket and streams back.
func (r *Router) proxyFCGI(w http.ResponseWriter, req *http.Request, wpdir, sock, host string) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		http.Error(w, "agent-local: php-fpm connect: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()

	reqID := uint16(1)
	https := ""
	serverPort := "80"
	if req.TLS != nil {
		https = "on"
		serverPort = "443"
	}
	script := wpdir + req.URL.Path
	if strings.HasSuffix(req.URL.Path, "/") || !strings.Contains(req.URL.Path, ".") {
		// WordPress pretty permalinks → route through index.php
		script = wpdir + "/index.php"
	}
	if _, err := os.Stat(script); err != nil {
		script = wpdir + "/index.php"
	}
	scriptName := strings.TrimPrefix(script, wpdir)
	if scriptName == "" {
		scriptName = "/index.php"
	}
	contentLength := "0"
	if req.ContentLength >= 0 {
		contentLength = fmt.Sprint(req.ContentLength)
	}
	params := map[string]string{
		"GATEWAY_INTERFACE": "FastCGI/1.0",
		"REQUEST_METHOD":    req.Method,
		"REQUEST_URI":       req.URL.RequestURI(),
		"SCRIPT_FILENAME":   script,
		"SCRIPT_NAME":       scriptName,
		"QUERY_STRING":      req.URL.RawQuery,
		"SERVER_PROTOCOL":   req.Proto,
		"SERVER_SOFTWARE":   AppName + "/" + Version,
		"SERVER_NAME":       host,
		"SERVER_PORT":       serverPort,
		"SERVER_ADDR":       "127.0.0.1",
		"REMOTE_ADDR":       clientIP(req),
		"REMOTE_PORT":       remotePort(req),
		"HTTPS":             https,
		"CONTENT_TYPE":      req.Header.Get("Content-Type"),
		"CONTENT_LENGTH":    contentLength,
		"DOCUMENT_ROOT":     wpdir,
		"REDIRECT_STATUS":   "200",
		"HTTP_HOST":         req.Host,
	}
	for k, vs := range req.Header {
		hk := strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		if hk == "CONTENT_TYPE" || hk == "CONTENT_LENGTH" {
			continue
		}
		params["HTTP_"+hk] = strings.Join(vs, ", ")
	}

	// BEGIN_REQUEST
	begin := make([]byte, 8)
	binary.BigEndian.PutUint16(begin[0:2], fcgiRoleResponder)
	if err := fcgiWrite(conn, fcgiBeginRequest, reqID, begin); err != nil {
		http.Error(w, "agent-local: fcgi begin: "+err.Error(), http.StatusBadGateway)
		return
	}
	// PARAMS
	var pb []byte
	for k, v := range params {
		pb = append(pb, fcgiNV(k, v)...)
	}
	if err := fcgiWrite(conn, fcgiParams, reqID, pb); err != nil {
		return
	}
	fcgiWrite(conn, fcgiParams, reqID, nil) // terminate params
	// STDIN (request body)
	if req.Body != nil {
		buf := make([]byte, 16*1024)
		for {
			n, rerr := req.Body.Read(buf)
			if n > 0 {
				if err := fcgiWrite(conn, fcgiStdin, reqID, buf[:n]); err != nil {
					return
				}
			}
			if rerr != nil {
				break
			}
		}
	}
	fcgiWrite(conn, fcgiStdin, reqID, nil) // EOF stdin

	// Stream the response: accumulate STDOUT until the header block is
	// complete, then forward the body chunk-by-chunk — no full buffering.
	br := bufio.NewReaderSize(conn, 64*1024)
	var hdrBuf []byte
	headersSent := false
	var sendErr error
	flusher, _ := w.(http.Flusher)

	for {
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(br, hdr); err != nil {
			break
		}
		rtype := hdr[1]
		rid := binary.BigEndian.Uint16(hdr[2:4])
		clen := int(binary.BigEndian.Uint16(hdr[4:6]))
		plen := int(hdr[6])
		if rid != reqID {
			discard(br, clen+plen)
			continue
		}
		payload := make([]byte, clen)
		if _, err := io.ReadFull(br, payload); err != nil && clen > 0 {
			break
		}
		if plen > 0 {
			discard(br, plen)
		}
		switch rtype {
		case fcgiStdout:
			if headersSent {
				if _, err := w.Write(payload); err != nil {
					sendErr = err
				} else if flusher != nil {
					flusher.Flush()
				}
				continue
			}
			hdrBuf = append(hdrBuf, payload...)
			if idx, sep := findHeaderEnd(hdrBuf); idx >= 0 {
				hdrs, status := parseFCGIHeaders(string(hdrBuf[:idx]))
				for k, vs := range hdrs {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(status)
				if rest := hdrBuf[idx+sep:]; len(rest) > 0 {
					w.Write(rest)
				}
				headersSent = true
			}
		case fcgiEndRequest:
			if !headersSent {
				if len(hdrBuf) == 0 {
					http.Error(w, "agent-local: empty response from php-fpm", http.StatusBadGateway)
				} else {
					http.Error(w, "agent-local: malformed php-fpm response (no header terminator)", http.StatusBadGateway)
				}
			}
			return
		}
		if sendErr != nil {
			return // client went away
		}
	}
	if !headersSent {
		http.Error(w, "agent-local: php-fpm closed connection mid-response", http.StatusBadGateway)
	}
}

// findHeaderEnd locates the header/body separator in a CGI response.
// Returns (index, separator length); index < 0 = not found yet.
func findHeaderEnd(b []byte) (int, int) {
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		return i, 4
	}
	if i := bytes.Index(b, []byte("\n\n")); i >= 0 {
		return i, 2
	}
	return -1, 0
}

// parseFCGIHeaders parses CGI response headers, extracting the Status line.
// Identical Set-Cookie lines are collapsed — some plugins spam hundreds of
// duplicate cookies per request, which overflows strict HTTP parsers.
func parseFCGIHeaders(text string) (http.Header, int) {
	hdr := http.Header{}
	seenCookie := map[string]bool{}
	status := 200
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.EqualFold(k, "Status") {
			fmt.Sscanf(v, "%d", &status)
			continue
		}
		if strings.EqualFold(k, "Set-Cookie") {
			if seenCookie[v] {
				continue
			}
			seenCookie[v] = true
		}
		hdr.Add(k, v)
	}
	return hdr, status
}

func fcgiNV(k, v string) []byte {
	kl, vl := len(k), len(v)
	var b []byte
	if kl < 128 {
		b = append(b, byte(kl))
	} else {
		b = append(b, byte(kl>>24)|0x80, byte(kl>>16), byte(kl>>8), byte(kl))
	}
	if vl < 128 {
		b = append(b, byte(vl))
	} else {
		b = append(b, byte(vl>>24)|0x80, byte(vl>>16), byte(vl>>8), byte(vl))
	}
	b = append(b, k...)
	b = append(b, v...)
	return b
}

func fcgiWrite(conn net.Conn, rtype byte, reqID uint16, payload []byte) error {
	for {
		chunk := payload
		if len(chunk) > fcgiMaxContent {
			chunk = payload[:fcgiMaxContent]
		}
		hdr := make([]byte, 8)
		hdr[0] = fcgiVersion1
		hdr[1] = rtype
		binary.BigEndian.PutUint16(hdr[2:4], reqID)
		binary.BigEndian.PutUint16(hdr[4:6], uint16(len(chunk)))
		pad := (8 - len(chunk)%8) % 8
		hdr[6] = byte(pad)
		if _, err := conn.Write(append(hdr, chunk...)); err != nil {
			return err
		}
		if pad > 0 {
			conn.Write(make([]byte, pad))
		}
		if len(payload) <= fcgiMaxContent {
			return nil
		}
		payload = payload[fcgiMaxContent:]
	}
}

func discard(br *bufio.Reader, n int) {
	for n > 0 {
		b := make([]byte, min(n, 4096))
		m, err := br.Read(b)
		n -= m
		if err != nil {
			return
		}
	}
}

func clientIP(req *http.Request) string {
	host, _, _ := net.SplitHostPort(req.RemoteAddr)
	if host == "" {
		return req.RemoteAddr
	}
	return host
}

func remotePort(req *http.Request) string {
	_, port, _ := net.SplitHostPort(req.RemoteAddr)
	return port
}
