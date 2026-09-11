package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The router's media fallback used to watch a fixed path for every WordPress
// site: /wp-content/uploads/. That is the conventional default, but WordPress
// lets a site move its content directory — Bedrock sets CONTENT_DIR=/app (so
// media live at /app/uploads/), others set UPLOADS or WP_CONTENT_URL. For such
// a site every upload URL is under a path the fallback never watched, so a
// missing file 404'd instead of redirecting to the origin. The path is an
// answer only the site's own config gives, so this reads it.
//
// It is config-derived, not WP-derived: the router serves every request and
// cannot shell out to wp-cli. wp_info (which can) already reports the real
// uploads.baseurl, so an agent that needs the authoritative value has it.

// wpContentDefineRe matches a define()/Config::define() of a content-dir
// marker, capturing the constant name and its literal value. Values are quoted
// strings (or a tidy variable expression we skip); a non-literal value means the
// constant is computed elsewhere and we fall back to the next marker.
var wpContentDefineRe = regexp.MustCompile(`(?m)^\s*(?:Config::)?define\(\s*'((?:CONTENT_DIR|UPLOADS|WP_CONTENT_URL))'\s*,\s*'([^']+)'\s*\)`)

// contentURLPath returns the URL path of a site's content directory ("/app",
// "/wp-content"), or "" when nothing is overridden and the conventional default
// applies. It reads the site's wp-config.php and the Bedrock config that file
// requires (e.g. config/application.php), both of which are where a custom
// content dir gets declared.
func contentURLPath(wpdir string) string {
	cfg := findWPDirConfig(wpdir)
	if cfg == "" {
		return ""
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return ""
	}
	src := string(b)
	for _, match := range wpContentDefineRe.FindAllStringSubmatch(src, -1) {
		if p := contentPathFromMarker(match[1], match[2]); p != "" {
			return p
		}
	}
	// Bedrock: wp-config.php requires ../config/application.php.
	for _, inc := range configIncludes(src, filepath.Dir(cfg)) {
		ib, err := os.ReadFile(inc)
		if err != nil {
			continue
		}
		for _, match := range wpContentDefineRe.FindAllStringSubmatch(string(ib), -1) {
			if p := contentPathFromMarker(match[1], match[2]); p != "" {
				return p
			}
		}
	}
	return ""
}

// contentPathFromMarker turns a detected marker into a URL path. CONTENT_DIR
// and WP_CONTENT_URL are the useful ones; UPLOADS overrides the uploads
// subdirectory itself, so its path already sits under the content dir. A value
// that is the conventional default is not an override and returns "".
func contentPathFromMarker(name, value string) string {
	switch name {
	case "CONTENT_DIR":
		p := slashPath(value)
		if p == "" || p == "/wp-content" {
			return ""
		}
		return p
	case "WP_CONTENT_URL":
		// A literal URL (https://host/app); take its path component, which is
		// what the fallback matches on, not the host.
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ""
		}
		p := slashPath(u.Path)
		if p == "/wp-content" {
			return ""
		}
		return p
	case "UPLOADS":
		return slashPath(value)
	}
	return ""
}

// slashPath normalises a config value to a leading-slash, no-trailing-slash
// URL path. A relative or empty value returns "" (no usable override).
func slashPath(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "://") && !strings.HasPrefix(v, "http") {
		return ""
	}
	p := "/" + strings.Trim(strings.TrimSpace(v), "/")
	if p == "/" {
		return ""
	}
	return p
}

// findWPDirConfig locates a site's wp-config.php: in the docroot, or one level
// above it — WordPress itself accepts both, and Bedrock keeps it in the docroot
// while its config/ lives above.
func findWPDirConfig(wpdir string) string {
	for _, p := range []string{
		filepath.Join(wpdir, "wp-config.php"),
		filepath.Join(filepath.Dir(wpdir), "wp-config.php"),
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// configIncludes resolves the config files a wp-config.php requires, e.g.
// Bedrock's `require_once dirname(__DIR__) . '/config/application.php'`.
// dirname(__DIR__) is the directory above the file's own dir.
func configIncludes(src, dir string) []string {
	var out []string
	re := regexp.MustCompile(`require_once\s+dirname\(__DIR__\)\s*\.\s*'([^']+)'`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		p := filepath.Join(filepath.Dir(dir), m[1])
		if fileExists(p) {
			out = append(out, p)
		}
	}
	return out
}

// contentCache remembers the detected content path per docroot, keyed by the
// config file's modification time, so the router does not re-read and re-parse
// PHP on every uploads miss. Editing the config takes effect on the next miss.
var contentCache sync.Map // wpdir -> contentEntry

type contentEntry struct {
	mod  time.Time
	path string
}

// contentURLPathCached is contentURLPath with the parse skipped while the
// wp-config.php has not changed.
func contentURLPathCached(wpdir string) string {
	cfg := findWPDirConfig(wpdir)
	if cfg == "" {
		return ""
	}
	st, err := os.Stat(cfg)
	if err != nil {
		return ""
	}
	if v, ok := contentCache.Load(wpdir); ok {
		if e := v.(contentEntry); e.mod.Equal(st.ModTime()) {
			return e.path
		}
	}
	p := contentURLPath(wpdir)
	contentCache.Store(wpdir, contentEntry{mod: st.ModTime(), path: p})
	return p
}

// uploadsPrefix is the URL path the media fallback watches for a site: the
// uploads directory under the site's own content dir when one is configured,
// else the kind's conventional prefix.
//
// The kind it consults may be re-detected. Attaching a directory before its
// files arrive records KindEmpty — that is what the placeholder is for — but
// nothing on the serving path used to notice when the app was installed into it
// afterwards. The media fallback is the one caller that reads the kind per
// request, so it alone kept using the placeholder's empty prefix and silently
// stopped redirecting missing uploads, while every WordPress-only tool healed
// itself on the way in.
func (e *Engine) uploadsPrefix(site *Site) string {
	if p := contentURLPathCached(site.WPDir); p != "" {
		return p + "/uploads/"
	}
	kind := site.Kind
	// Scoped so an ordinary request never enters it: only a kind with no
	// conventional prefix can be out of date in a way this fixes, and
	// wp-load.php is the decisive marker DetectKind itself looks for.
	if kind.UploadsPrefix() == "" && fileExists(filepath.Join(site.WPDir, "wp-load.php")) {
		if detected := DetectKind(site.WPDir); detected != kind {
			site.Kind = detected
			e.Store.PutSite(site)
			_ = e.Store.Save()
		}
		kind = site.Kind
	}
	return kind.UploadsPrefix()
}
