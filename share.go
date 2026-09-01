package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Share: a public URL for a running site, through a Cloudflare quick tunnel.
// No account, no token, no config — `cloudflared tunnel --url` hands back a
// random https://<words>.trycloudflare.com address that lives as long as the
// process does, which is exactly the shape of "look at this on your phone".
//
// The tunnel targets the router's HTTP port and keeps the tunnel hostname
// end-to-end: the router resolves it through the share registry, and an
// mu-plugin maps home/siteurl onto it for exactly those requests — so the
// share works without a search-replace, and local browsing is untouched.
// Reserved paths (the database GUI, the mail inbox) answer 404 to tunnel
// traffic; a share exposes the site, not its tooling.
//
// A share is owned by the daemon and dies with it: state lives in memory,
// pid files exist only so a replaced daemon can reap orphaned tunnels.

// shareDefaultMinutes bounds a share nobody remembered to stop. 0 from the
// caller means "until stopped".
const shareDefaultMinutes = 60

// shareMaxWait is how long cloudflared gets to produce a URL before the
// attempt is declared failed.
const shareMaxWait = 45 * time.Second

// shareMUName is the mu-plugin dropped while a site is shared.
const shareMUName = "000-agent-local-share.php"

// tunnelURLRe matches the address cloudflared prints once the quick tunnel
// is up.
var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Share is one active tunnel.
type Share struct {
	Slug      string     `json:"slug"`
	URL       string     `json:"url"`
	Host      string     `json:"host"`
	StartedAt time.Time  `json:"started_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // absent = until stopped

	cmd     *exec.Cmd
	timer   *time.Timer
	muPath  string
	cleanup sync.Once
}

// shutdown tears one share down exactly once, whichever of the three exits
// gets there first: an explicit stop, the expiry timer, or the tunnel
// process dying on its own.
func (s *Share) shutdown() {
	s.cleanup.Do(func() {
		if s.timer != nil {
			s.timer.Stop()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		os.Remove(sharePidFile(s.Slug))
		os.Remove(s.muPath)
		shares.remove(s)
	})
}

// shareRegistry is the daemon's in-memory table of active tunnels —
// in-memory on purpose, because a tunnel is a child of this process and
// persisted state would outlive the thing it describes.
type shareRegistry struct {
	mu     sync.Mutex
	byHost map[string]*Share
	bySlug map[string]*Share
}

var shares = &shareRegistry{byHost: map[string]*Share{}, bySlug: map[string]*Share{}}

func (r *shareRegistry) add(s *Share) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byHost[s.Host] = s
	r.bySlug[s.Slug] = s
}

func (r *shareRegistry) remove(s *Share) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byHost[s.Host] == s {
		delete(r.byHost, s.Host)
	}
	if r.bySlug[s.Slug] == s {
		delete(r.bySlug, s.Slug)
	}
}

// ForHost is the router's lookup: does this Host header belong to a share?
func (r *shareRegistry) ForHost(host string) *Share {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byHost[host]
}

// ForSlug reports a site's active share, nil when it has none.
func (r *shareRegistry) ForSlug(slug string) *Share {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bySlug[slug]
}

// All snapshots the active shares, for shutdown.
func (r *shareRegistry) All() []*Share {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Share, 0, len(r.bySlug))
	for _, s := range r.bySlug {
		out = append(out, s)
	}
	return out
}

func sharePidFile(slug string) string {
	return filepath.Join(P().Run(), "share-"+slug+".pid")
}

// StartShare opens a public tunnel to a site. Idempotent: an active share is
// returned rather than doubled. minutes 0 means the default window; negative
// means until stopped.
func (e *Engine) StartShare(slug string, minutes int, cb func(string, string)) (*Share, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	if sh := shares.ForSlug(slug); sh != nil {
		return sh, nil
	}
	// The router resolves the tunnel hostname per request; apache's vhosts
	// cannot, so a share under that front would serve the wrong site.
	if FrontKind(e.Store) != "router" {
		return nil, fmt.Errorf("share needs the router front (apache vhosts cannot route the tunnel hostname); switch: agent-local front router")
	}
	bin, err := ensureCloudflared(e.Store, cb)
	if err != nil {
		return nil, err
	}
	if err := e.StartSite(slug); err != nil {
		return nil, err
	}
	cb("tunnel", "opening quick tunnel via "+filepath.Base(bin))
	cmd, url, exited, err := spawnQuickTunnel(bin,
		fmt.Sprintf("http://127.0.0.1:%d", DefaultHTTPPort), P().Log("share-"+slug))
	if err != nil {
		return nil, err
	}
	host := strings.TrimPrefix(url, "https://")
	sh := &Share{
		Slug:      slug,
		URL:       url,
		Host:      host,
		StartedAt: time.Now(),
		cmd:       cmd,
		muPath:    filepath.Join(site.WPDir, "wp-content", "mu-plugins", shareMUName),
	}
	if err := writeShareMU(site, host); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("share mu-plugin: %w", err)
	}
	os.WriteFile(sharePidFile(slug), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	if minutes == 0 {
		minutes = shareDefaultMinutes
	}
	if minutes > 0 {
		exp := time.Now().Add(time.Duration(minutes) * time.Minute)
		sh.ExpiresAt = &exp
		sh.timer = time.AfterFunc(time.Until(exp), sh.shutdown)
	}
	shares.add(sh)
	go func() {
		<-exited // tunnel died on its own (or was killed): fold the share
		sh.shutdown()
	}()
	cb("tunnel", url)
	return sh, nil
}

// StopShare closes a site's tunnel. Stopping a site that is not shared is a
// no-op, matching every other stop in the app.
func (e *Engine) StopShare(slug string) bool {
	sh := shares.ForSlug(slug)
	if sh == nil {
		return false
	}
	sh.shutdown()
	return true
}

// SweepShares reaps tunnels a previous daemon left behind and removes the
// share mu-plugin from every site. A share cannot outlive the process that
// owns it, so anything found here is residue.
func SweepShares(store *Store) {
	pids, _ := filepath.Glob(filepath.Join(P().Run(), "share-*.pid"))
	for _, pf := range pids {
		if b, err := os.ReadFile(pf); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 && Alive(pid) {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		os.Remove(pf)
	}
	for _, site := range store.Sites() {
		os.Remove(filepath.Join(site.WPDir, "wp-content", "mu-plugins", shareMUName))
	}
}

// spawnQuickTunnel starts cloudflared and waits for it to print the public
// URL. Its output streams to logPath for the lifetime of the tunnel; exited
// closes when the process ends, after being reaped.
func spawnQuickTunnel(bin, target, logPath string) (*exec.Cmd, string, chan struct{}, error) {
	cmd := exec.Command(bin, "tunnel", "--url", target, "--no-autoupdate")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", nil, err
	}
	cmd.Stdout = cmd.Stderr // cloudflared logs to stderr; catch strays too
	if err := cmd.Start(); err != nil {
		return nil, "", nil, fmt.Errorf("cloudflared: %w", err)
	}
	logf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	urlCh := make(chan string, 1)
	exited := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if logf != nil {
				fmt.Fprintln(logf, line)
			}
			if m := tunnelURLRe.FindString(line); m != "" {
				select {
				case urlCh <- m:
				default:
				}
			}
		}
		if logf != nil {
			logf.Close()
		}
		cmd.Wait()
		close(exited)
	}()
	select {
	case u := <-urlCh:
		return cmd, u, exited, nil
	case <-exited:
		return nil, "", nil, fmt.Errorf("cloudflared exited before producing a URL — its log: agent-local logs %s", filepath.Base(strings.TrimSuffix(logPath, ".log")))
	case <-time.After(shareMaxWait):
		cmd.Process.Kill()
		return nil, "", nil, fmt.Errorf("no tunnel URL within %s (network down, or trycloudflare unreachable) — its log: agent-local logs %s",
			shareMaxWait, filepath.Base(strings.TrimSuffix(logPath, ".log")))
	}
}

// writeShareMU drops an mu-plugin that maps the site onto the tunnel host —
// for tunnel requests only, so local browsing keeps local URLs. Two layers:
// option filters pinned at PHP_INT_MAX (like branch previews, so
// canonical-host security plugins cannot drag visitors back), and an output
// buffer that rewrites the local domain in the finished response — because
// WP_CONTENT_URL is computed before mu-plugins load, and imported sites
// carry their domain baked into post content, where no filter reaches.
func writeShareMU(site *Site, host string) error {
	mu := filepath.Join(site.WPDir, "wp-content", "mu-plugins")
	if err := os.MkdirAll(mu, 0o755); err != nil {
		return err
	}
	domains := append([]string{site.Domain}, site.Aliases...)
	quoted := make([]string, len(domains))
	for i, d := range domains {
		quoted[i] = phpQuote(d)
	}
	body := fmt.Sprintf(`<?php
/**
 * Plugin Name: agent-local share
 * Description: Maps this site onto %s while it is shared. Managed by agent-local; removed when the share stops.
 */
if ( isset( $_SERVER['HTTP_X_FORWARDED_PROTO'] ) && 'https' === $_SERVER['HTTP_X_FORWARDED_PROTO'] ) {
	$_SERVER['HTTPS'] = 'on'; // Cloudflare terminates TLS; tell WordPress
}
if ( isset( $_SERVER['HTTP_HOST'] ) && '%s' === $_SERVER['HTTP_HOST'] ) {
	$al_share_host = '%s';
	$al_share_url  = 'https://' . $al_share_host;
	foreach ( array( 'option_home', 'option_siteurl', 'pre_option_home', 'pre_option_siteurl' ) as $al_f ) {
		add_filter( $al_f, function () use ( $al_share_url ) { return $al_share_url; }, PHP_INT_MAX );
	}
	add_filter( 'redirect_canonical', '__return_false', PHP_INT_MAX );
	// Rewrite what the filters cannot reach: WP_CONTENT_URL (fixed before
	// mu-plugins load) and URLs baked into content. Plain and JSON-escaped.
	ob_start( function ( $al_out ) use ( $al_share_host ) {
		foreach ( array( %s ) as $al_d ) {
			$al_out = str_replace(
				array( 'https://' . $al_d, 'http://' . $al_d, 'https:\/\/' . $al_d, 'http:\/\/' . $al_d ),
				array( 'https://' . $al_share_host, 'https://' . $al_share_host, 'https:\/\/' . $al_share_host, 'https:\/\/' . $al_share_host ),
				$al_out
			);
		}
		return $al_out;
	} );
}
`, host, host, host, strings.Join(quoted, ", "))
	return os.WriteFile(filepath.Join(mu, shareMUName), []byte(body), 0o644)
}

// ensureCloudflared finds cloudflared, installing it through Homebrew when
// missing — the same facilitation as every other dependency.
func ensureCloudflared(s *Store, cb func(string, string)) (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	inv := s.Inventory()
	if inv.Brew != "" {
		if p := filepath.Join(filepath.Dir(inv.Brew), "cloudflared"); fileExists(p) {
			return p, nil
		}
	}
	if inv.Brew == "" {
		return "", fmt.Errorf("cloudflared not found and homebrew missing; install from https://brew.sh and retry")
	}
	cb("install", "brew install cloudflared")
	if err := streamCmd(brewCmd(inv.Brew, "install", "cloudflared"), func(line string) { cb("install", line) }); err != nil {
		return "", fmt.Errorf("brew install cloudflared: %w", err)
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	if p := filepath.Join(filepath.Dir(inv.Brew), "cloudflared"); fileExists(p) {
		return p, nil
	}
	return "", fmt.Errorf("brew install cloudflared finished but the binary is not on PATH")
}
