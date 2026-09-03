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
	"time"
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
	httpSrv := &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	httpsSrv := &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	// Loopback only: these ports used to bind 0.0.0.0 and every local WordPress
	// was reachable from the LAN. Dual-stack: macOS can drop IPv4-mapped
	// connections on a single dual-stack listener.
	var bound []net.Listener
	for _, addr := range []string{
		fmt.Sprintf("127.0.0.1:%d", httpPort),
		fmt.Sprintf("[::1]:%d", httpPort),
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
		fmt.Sprintf("127.0.0.1:%d", httpsPort),
		fmt.Sprintf("[::1]:%d", httpsPort),
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
	// A tunnel host is not a vhost: resolve it through the share registry to
	// the site being shared, and remember that this request came from outside.
	// mediaHost is who to consult for per-site settings — the media fallback
	// looks a site up by domain, and the tunnel hostname matches none.
	shared, mediaHost := false, host
	if !ok {
		if sh := shares.ForHost(host); sh != nil {
			if site := r.engine.Store.Site(sh.Slug); site != nil {
				wpdir, fpmID, _, ok = r.engine.Resolve(site.Domain)
				shared, mediaHost = true, site.Domain
			} else {
				// The site went away under the tunnel (a CLI-side delete
				// cannot reach this registry): fold the share here.
				sh.shutdown()
			}
		}
	}
	if !ok {
		http.Error(w, "agent-local: no site for host "+host, http.StatusBadGateway)
		return
	}

	if isAdminerPath(req.URL.Path) || isMailPath(req.URL.Path) || isHubPath(req.URL.Path) {
		// A share exposes the site, not its tooling: the database GUI, the
		// inbox and this index stay local-only.
		if shared {
			http.NotFound(w, req)
			return
		}
		if isAdminerPath(req.URL.Path) {
			r.serveAdminer(w, req, host, wpdir)
			return
		}
		if isHubPath(req.URL.Path) {
			serveHubUI(w, HubPath, host)
			return
		}
		// The inbox is per serving pool, so a preview domain reads the
		// preview's own test mail rather than muddying the site's.
		serveMailUI(w, req, fpmID, MailPath, strings.TrimPrefix(req.URL.Path, MailPath), host)
		return
	}

	// Static fast path: real files that aren't PHP.
	if req.Method != "POST" && r.serveStatic(w, req, wpdir) {
		return
	}
	// A missing upload goes to the site's media fallback rather than to
	// WordPress, which would only render a 404 page for an image.
	if req.Method == "GET" && r.serveMediaFallback(w, req, mediaHost, wpdir) {
		return
	}
	// "/wp-admin" is a directory with its own index.php: add the slash the way
	// mod_dir does, so relative URLs inside it resolve against the right base.
	if req.Method == "GET" && !strings.HasSuffix(req.URL.Path, "/") && dirWithIndex(wpdir, req.URL.Path) {
		target := req.URL.Path + "/"
		if req.URL.RawQuery != "" {
			target += "?" + req.URL.RawQuery
		}
		http.Redirect(w, req, target, http.StatusMovedPermanently)
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

// uploadsPrefix is the one path a media fallback applies to. Scoping it here
// rather than to any missing file keeps genuine 404s visible.
const uploadsPrefix = "/wp-content/uploads/"

// serveMediaFallback redirects a missing upload to the site's configured origin,
// which is what the Apache-only ".htaccess" rewrite does on production hosts:
//
//	RewriteCond %{REQUEST_FILENAME} !-f
//	RewriteRule ^(.*)$ https://example.org/$1 [QSA,L]
//
// It is a redirect, not a proxy: the browser fetches from the origin, so nothing
// is cached or rewritten locally and the behaviour matches the .htaccess exactly.
func (r *Router) serveMediaFallback(w http.ResponseWriter, req *http.Request, host, wpdir string) bool {
	// Normalise first, then decide: "/wp-content/uploads/../../../etc/passwd"
	// starts with the uploads prefix but is not an upload, and sending a browser
	// to the origin for it is neither useful nor honest.
	clean := filepath.Clean("/" + req.URL.Path)
	if !strings.HasPrefix(clean, uploadsPrefix) {
		return false
	}
	site := r.engine.Store.FindSiteByDomain(host)
	if site == nil {
		return false
	}
	origin := EffectiveMediaFallback(site)
	if origin == "" {
		return false
	}
	// Only when it is genuinely absent locally: a real file was already served by
	// serveStatic, but a directory lands here too.
	if _, err := os.Stat(filepath.Join(wpdir, clean)); err == nil {
		return false
	}
	target := strings.TrimSuffix(origin, "/") + clean
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	http.Redirect(w, req, target, http.StatusFound)
	return true
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
	if sensitivePath(urlPath) {
		// Answered here rather than left to WordPress: a share tunnel makes the
		// docroot reachable from outside, and .git/ or a wp-config backup is
		// exactly what a visitor with the URL would go looking for.
		http.NotFound(w, req)
		return true
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

// sensitivePath reports paths that are never served as files, whichever front
// is in use: anything under a dot directory or a dotfile (.git, .env, .htaccess,
// .user.ini) except the ACME directory, every wp-config variant that is not
// the .php itself (backups hold the database password in plain text), and
// dumps, logs and editor leftovers. The same list drives the Apache config.
func sensitivePath(urlPath string) bool {
	clean := strings.ToLower(filepath.Clean("/" + urlPath))
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return true
		}
	}
	base := filepath.Base(clean)
	if strings.HasPrefix(base, "wp-config.php") && base != "wp-config.php" {
		return true
	}
	for _, suffix := range sensitiveSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

var sensitiveSuffixes = []string{".sql", ".sql.gz", ".sql.zip", ".log", ".bak", ".old", ".orig", ".save", ".swp", "~"}

// getCertificate lazily loads/creates a cert for the requested SNI host. Only
// hosts this router serves get one: the name becomes a filename under
// ~/.agent-local/certs, and a client can present any SNI it likes.
func (r *Router) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	r.mu.RLock()
	if c, ok := r.certs[host]; ok {
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()
	if !r.knownHost(host) {
		return nil, fmt.Errorf("agent-local: no site for host %q", host)
	}
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

// knownHost is the same resolution ServeHTTP performs: a site or preview
// domain (aliases included), or the hostname of a running share.
func (r *Router) knownHost(host string) bool {
	if !ValidDomain(host) {
		return false
	}
	if _, _, _, ok := r.engine.Resolve(host); ok {
		return true
	}
	return shares.ForHost(host) != nil
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

// resolveScript decides which PHP file runs a request, the way Apache and nginx
// do: the file itself when it is a real .php, else that directory's own
// index.php, and only then the site's front controller.
//
// Sending "/wp-admin/" to the front controller is what caused ERR_TOO_MANY_
// REDIRECTS: WordPress's router receives a URL it knows belongs to the admin,
// canonically redirects to /wp-admin/, and the whole thing repeats. The admin has
// its own index.php and must be allowed to answer for itself.
func resolveScript(wpdir, urlPath string) (script, scriptName string) {
	front := filepath.Join(wpdir, "index.php")
	clean := filepath.Clean("/" + urlPath)
	full := filepath.Join(wpdir, clean)
	// Traversal cannot reach outside the docroot.
	if !within(full, wpdir) {
		return front, "/index.php"
	}
	if strings.HasSuffix(strings.ToLower(clean), ".php") {
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			return full, clean
		}
		return front, "/index.php"
	}
	// DirectoryIndex: a directory holding its own index.php answers for itself.
	// Directories without one (uploads, a "downloads" folder that is really a
	// WordPress page) stay with the front controller, so permalinks still win.
	if st, err := os.Stat(full); err == nil && st.IsDir() {
		idx := filepath.Join(full, "index.php")
		if _, err := os.Stat(idx); err == nil {
			name := strings.TrimSuffix(clean, "/") + "/index.php"
			return idx, name
		}
	}
	return front, "/index.php"
}

// dirWithIndex reports whether a path is a real directory carrying its own
// index.php — the only case where adding a trailing slash is correct rather than
// a guess about someone's permalinks.
func dirWithIndex(wpdir, urlPath string) bool {
	if urlPath == "" || urlPath == "/" {
		return false
	}
	clean := filepath.Clean("/" + urlPath)
	// Anything that collapses to the docroot is already being served: "/../.."
	// must not earn a redirect to "/../../".
	if clean == "/" {
		return false
	}
	full := filepath.Join(wpdir, clean)
	if !within(full, wpdir) {
		return false
	}
	if st, err := os.Stat(full); err != nil || !st.IsDir() {
		return false
	}
	_, err := os.Stat(filepath.Join(full, "index.php"))
	return err == nil
}

// within reports whether a joined path stayed inside the docroot.
func within(full, root string) bool {
	root = filepath.Clean(root)
	return full == root || strings.HasPrefix(full, root+string(os.PathSeparator))
}

// proxyFCGI speaks FastCGI to the pool socket and streams back.
func (r *Router) proxyFCGI(w http.ResponseWriter, req *http.Request, wpdir, sock, host string) {
	script, scriptName := resolveScript(wpdir, req.URL.Path)
	r.proxyFCGIScript(w, req, wpdir, sock, host, script, scriptName)
}

// serveAdminer runs the downloaded Adminer wrapper through the site's PHP.
func (r *Router) serveAdminer(w http.ResponseWriter, req *http.Request, host, wpdir string) {
	site := r.engine.SiteForHost(host)
	if site == nil {
		http.Error(w, "agent-local: no site for host "+host, http.StatusBadGateway)
		return
	}
	boot, err := writeAdminerBoot(site)
	if err != nil {
		http.Error(w, "agent-local: adminer: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	// Adminer is our file, but it talks to the site's schema — use the site
	// pool (not a preview's) so a worktree host still opens the same database.
	sock := r.engine.fpmSock(site.Slug)
	if !fileExists(sock) {
		if err := r.engine.ensurePool(site.Slug); err != nil {
			http.Error(w, "agent-local: site could not start: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	r.proxyFCGIScript(w, req, wpdir, sock, host, boot, AdminerPath)
}

// proxyFCGIScript is proxyFCGI with an explicit script filename, used by
// permalinks and by Adminer (which lives outside the docroot).
func (r *Router) proxyFCGIScript(w http.ResponseWriter, req *http.Request, wpdir, sock, host, script, scriptName string) {
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		http.Error(w, "agent-local: php-fpm connect: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(130 * time.Second))

	reqID := uint16(1)
	https := ""
	serverPort := "80"
	if req.TLS != nil {
		https = "on"
		serverPort = "443"
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
