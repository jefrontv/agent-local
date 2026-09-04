package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// requestReport is the outcome of one HTTP probe of a site, kept small
// enough to round-trip over JSON without dumping full response bodies at an
// agent - the excerpt and title are usually all that is needed to tell "this
// rendered" from "this is a white screen of death".
type requestReport struct {
	Status      int      `json:"status"`
	Location    string   `json:"location,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	SetCookies  int      `json:"set_cookies"`
	Redirects   []string `json:"redirects,omitempty"`
	Ms          int64    `json:"ms"`
	BodyBytes   int64    `json:"body_bytes"`
	Title       string   `json:"title,omitempty"`
	Excerpt     string   `json:"excerpt,omitempty"`
	PHPErrors   []string `json:"php_errors,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// probeReport is the result of walking a small fixed set of well-known WP
// paths on one site and rendering a verdict an agent can act on without
// having to interpret raw HTTP itself.
type probeReport struct {
	Verdict   string                   `json:"verdict"`
	Reason    string                   `json:"reason,omitempty"`
	Requests  map[string]requestReport `json:"requests"`
	CheckedAt time.Time                `json:"checked_at"`
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// phpErrorLine matches the log lines worth surfacing to an agent: the ones
// that mean the site is actually broken, not routine PHP notices about a
// third-party plugin's deprecated array access.
var phpErrorLine = regexp.MustCompile(`PHP (Fatal error|Parse error|Warning|Notice|Deprecated)|WordPress database error`)

// requestSite fetches one path from a running site over the loopback HTTPS
// listener, falling back to plain HTTP when TLS cannot be dialed (e.g. no
// cert issued yet). bodyMax bounds how much of the body is kept for the
// excerpt/title; the full byte count is still tallied by draining the rest.
func (e *Engine) requestSite(site *Site, method, path string, follow bool, bodyMax int) requestReport {
	if bodyMax <= 0 {
		bodyMax = 4096
	}
	if method == "" {
		method = http.MethodGet
	}

	// PHP errors racing this request from other traffic can leak into the
	// report - this tool assumes a local single-user dev box where that is
	// an acceptable, rare false positive rather than something to fix here.
	fpmPath := e.fpmLog(site.Slug)
	fpmFrom := fileSize(fpmPath)
	dbgPath := WPDebugStatus(site).LogPath
	var dbgFrom int64
	if dbgPath != "" {
		dbgFrom = fileSize(dbgPath)
	}

	start := time.Now()
	report := doRequest(fmt.Sprintf("https://127.0.0.1:%d", DefaultHTTPSPort), site, method, path, follow, bodyMax, true)
	if report.Error != "" {
		report = doRequest(fmt.Sprintf("http://127.0.0.1:%d", DefaultHTTPPort), site, method, path, follow, bodyMax, false)
	}
	report.Ms = time.Since(start).Milliseconds()

	var errs []string
	errs = append(errs, logDelta(fpmPath, fpmFrom)...)
	if dbgPath != "" {
		errs = append(errs, logDelta(dbgPath, dbgFrom)...)
	}
	report.PHPErrors = errs
	return report
}

// doRequest performs the actual round trip against base ("scheme://host:port")
// with req.Host pinned to the site's domain, so vhost routing sees the real
// site even though we dialed loopback directly.
func doRequest(base string, site *Site, method, path string, follow bool, bodyMax int, tlsMode bool) requestReport {
	var redirects []string
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			redirects = append(redirects, req.URL.String())
			return nil
		},
	}
	if tlsMode {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: site.Domain},
		}
	}

	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		return requestReport{Error: err.Error()}
	}
	req.Host = site.Domain

	resp, err := client.Do(req)
	if err != nil {
		return requestReport{Error: err.Error()}
	}
	defer resp.Body.Close()

	buf := make([]byte, bodyMax)
	n, _ := io.ReadFull(resp.Body, buf)
	if n < 0 {
		n = 0
	}
	head := buf[:n]
	rest, _ := io.Copy(io.Discard, resp.Body)
	bodyBytes := int64(n) + rest

	title := ""
	if m := titleRe.FindSubmatch(head); m != nil {
		title = collapseWhitespace(html.UnescapeString(string(m[1])))
	}

	// An excerpt is for reading: markup, JSON, scripts. Bytes of an image or
	// a font are noise in a JSON response and would only be escaped.
	excerpt := string(head)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/") &&
		!strings.Contains(ct, "json") && !strings.Contains(ct, "xml") && !strings.Contains(ct, "javascript") {
		excerpt = ""
	}
	report := requestReport{
		Status:      resp.StatusCode,
		Location:    resp.Header.Get("Location"),
		ContentType: resp.Header.Get("Content-Type"),
		SetCookies:  len(resp.Header.Values("Set-Cookie")),
		Redirects:   redirects,
		BodyBytes:   bodyBytes,
		Title:       title,
		Excerpt:     excerpt,
	}
	return report
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// logDelta reads the bytes appended to path since offset "from" and keeps
// only the lines that look like a genuine PHP/WP error, capped so a runaway
// log can't blow up the response.
func logDelta(path string, from int64) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	if from < 0 {
		from = 0
	}
	if from > info.Size() {
		from = info.Size()
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !phpErrorLine.MatchString(line) {
			continue
		}
		if len(line) > 300 {
			line = line[:300]
		}
		out = append(out, line)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// probePaths is the fixed, ordered set of well-known WP URLs a probe walks.
// Order matters for reproducibility, not for the verdict (which scans the
// whole map), but keeping it sequential avoids hammering php-fpm with
// concurrent cold-start requests right after a site boots.
var probePaths = []string{
	"/",
	"/wp-login.php",
	"/wp-admin/",
	"/wp-json/",
	"/wp-includes/js/jquery/jquery.min.js",
}

// probeSite walks probePaths sequentially and renders a single verdict, so
// an agent can ask "is this site okay?" without learning HTTP status codes.
func (e *Engine) probeSite(site *Site) probeReport {
	reqs := make(map[string]requestReport, len(probePaths))
	for _, p := range probePaths {
		r := e.requestSite(site, http.MethodGet, p, false, 4096)
		// The verdict and title come from the first 4KB; the report carries a
		// glimpse, not five pages of markup an agent has to read past.
		if len(r.Excerpt) > 240 {
			r.Excerpt = r.Excerpt[:240] + "…"
		}
		reqs[p] = r
	}
	verdict, reason := probeVerdict(reqs, site)
	return probeReport{
		Verdict:   verdict,
		Reason:    reason,
		Requests:  reqs,
		CheckedAt: time.Now(),
	}
}

// probeVerdict is pure so the priority order (a fatal PHP error always beats
// merely being slow, etc.) can be tested directly against constructed
// reports instead of a live site.
func probeVerdict(reqs map[string]requestReport, site *Site) (verdict, reason string) {
	for _, p := range probePaths {
		r, ok := reqs[p]
		if !ok {
			continue
		}
		for _, line := range r.PHPErrors {
			if strings.Contains(line, "Fatal error") || strings.Contains(line, "Parse error") {
				return "fatal", line
			}
		}
	}

	for _, p := range probePaths {
		r, ok := reqs[p]
		if !ok {
			continue
		}
		if r.Status >= 300 && r.Status < 400 && r.Location != "" && offSite(site, r.Location) {
			return "redirects_offsite", hostFromURL(r.Location)
		}
	}

	if r, ok := reqs["/"]; ok {
		if r.Status == 200 && r.BodyBytes == 0 {
			return "blank", "\"/\" returned 200 with an empty body"
		}
		if r.Error != "" || r.Status == 0 {
			reason = r.Error
			if reason == "" {
				reason = "no response"
			}
			return "down", reason
		}
		if r.Status >= 500 {
			return "error", fmt.Sprintf("\"/\" returned %d", r.Status)
		}
	}

	if r, ok := reqs["/wp-includes/js/jquery/jquery.min.js"]; ok && r.Status == 404 {
		return "asset_404", "jquery.min.js is missing"
	}

	if r, ok := reqs["/"]; ok && r.Ms > 3000 {
		return "slow", fmt.Sprintf("\"/\" took %dms", r.Ms)
	}

	return "healthy", ""
}

// requestBody is what handleRequest accepts from callers.
type requestBody struct {
	Path            string `json:"path"`
	Method          string `json:"method"`
	FollowRedirects bool   `json:"follow_redirects"`
	BodyMax         int    `json:"body_max"`
}

// handleRequest lets an agent fetch one URL from a running site the same way
// probeSite does internally, for ad hoc checks outside the fixed path list.
func (a *APIServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req requestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	if !strings.HasPrefix(req.Path, "/") {
		fail(w, 400, "path must start with /")
		return
	}
	report := a.engine.requestSite(site, req.Method, req.Path, req.FollowRedirects, req.BodyMax)
	ok(w, report)
}

// handleProbe runs the fixed probeSite walk against a site and returns the
// resulting verdict.
func (a *APIServer) handleProbe(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	ok(w, a.engine.probeSite(site))
}
