package main

import (
	"os"
	"path/filepath"
)

// AppKind is what lives in a docroot. The serving layer does not care —
// php-fpm runs whatever index.php is there — but the tooling does: wp-cli,
// wp-config edits, checkpoint scope and the uploads path all assume
// WordPress, and a Joomla site should hear "not a WordPress site" rather than
// a stack trace about wp-config.php.
type AppKind string

const (
	// KindWordPress is the default and what every pre-existing site row is.
	KindWordPress AppKind = "wordpress"
	KindJoomla    AppKind = "joomla"
	KindLaravel   AppKind = "laravel"
	KindDrupal    AppKind = "drupal"
	// KindPHP is a docroot with an index.php and no recognised framework —
	// served as-is, front-controller style, with nothing app-specific.
	KindPHP AppKind = "php"
	// KindEmpty is a directory with no PHP entrypoint at all: attach still
	// registers it (the database is ready when files arrive), and a later
	// re-detect picks up whatever gets installed.
	KindEmpty AppKind = "empty"
)

// DetectKind looks at a docroot and names the application. Markers are the
// files each framework cannot run without; a checkout's repo root is not the
// docroot, so callers pass what DocrootFor returned.
func DetectKind(docroot string) AppKind {
	has := func(rel ...string) bool {
		_, err := os.Stat(filepath.Join(append([]string{docroot}, rel...)...))
		return err == nil
	}
	switch {
	case has("wp-load.php"):
		return KindWordPress
	case has("administrator", "index.php") && (has("configuration.php") || has("installation", "index.php")):
		return KindJoomla
	case has("..", "artisan") || has("artisan"):
		return KindLaravel
	case has("core", "lib", "Drupal.php") || has("..", "core", "lib", "Drupal.php"):
		return KindDrupal
	case has("index.php"):
		return KindPHP
	default:
		return KindEmpty
	}
}

// IsWordPress reports whether WordPress tooling applies. Empty Kind is the
// pre-field default and every such site was created as WordPress.
func (s *Site) IsWordPress() bool {
	return s.Kind == "" || s.Kind == KindWordPress
}

// Label is the kind as a person reads it.
func (k AppKind) Label() string {
	switch k {
	case KindJoomla:
		return "Joomla"
	case KindLaravel:
		return "Laravel"
	case KindDrupal:
		return "Drupal"
	case KindPHP:
		return "PHP"
	case KindEmpty:
		return "empty"
	default:
		return "WordPress"
	}
}

// UploadsPrefix is the conventional URL path under which an app keeps
// user-uploaded media. It is the **kind default** only: the router's media
// fallback calls siteUploadsURLPath, which uses this when the site has no
// content-dir override, and consults the site's own WordPress config (Bedrock's
// CONTENT_DIR, or UPLOADS) otherwise. Frameworks without a conventional one get
// no fallback.
func (k AppKind) UploadsPrefix() string {
	switch k {
	case KindJoomla:
		return "/images/"
	case KindDrupal:
		return "/sites/default/files/"
	case KindLaravel:
		return "/storage/"
	case KindPHP, KindEmpty:
		return ""
	default:
		return "/wp-content/uploads/"
	}
}

// notWordPress is the error every WordPress-only tool returns for another
// kind: names the kind and the tool, so an agent stops retrying.
func notWordPress(site *Site, tool string) error {
	return &kindError{slug: site.Slug, kind: site.Kind, tool: tool}
}

type kindError struct {
	slug string
	kind AppKind
	tool string
}

func (e *kindError) Error() string {
	return e.slug + " is a " + e.kind.Label() + " site; " + e.tool + " only applies to WordPress"
}
