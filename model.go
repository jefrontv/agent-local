package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AppName is the product name used across binaries, plists, hosts markers.
const AppName = "agent-local"

// Version is the release stamp. The release pipeline overrides it with
// `-ldflags "-X main.Version=…"`; a plain `go build` reports "dev" so a
// hand-built binary is never mistaken for a published one.
var Version = "dev"

// buildCommit and buildDate are stamped by the release pipeline alongside
// Version, so a bug report from a published binary is traceable to a commit.
var (
	buildCommit string
	buildDate   string
)

// SiteState is the lifecycle state of a site row.
type SiteState string

const (
	StateStopped SiteState = "stopped"
	StateRunning SiteState = "running"
	StateError   SiteState = "error"
)

// Site is one managed WordPress install. Worktrees hang off it.
type Site struct {
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	WorkDir    string    `json:"work_dir"`    // git repo root (contains .git + wp/)
	WPDir      string    `json:"wp_dir"`      // wordpress root (worktree path for branches)
	Branch     string    `json:"branch"`      // git branch checked out at WorkDir
	Repo       string    `json:"repo"`        // clone URL, empty = locally created
	PHPVersion string    `json:"php_version"` // e.g. "8.2"
	DBName     string    `json:"db_name"`
	DBUser     string    `json:"db_user"`
	DBPass     string    `json:"db_pass,omitempty"`
	Domain     string    `json:"domain"` // primary host, e.g. mysite.test
	Aliases    []string  `json:"aliases,omitempty"`
	HTTPPort   int       `json:"http_port"`
	HTTPSPort  int       `json:"https_port"`
	CreatedAt  time.Time `json:"created_at"`
	State      SiteState `json:"state"` // persisted last known state
	// Attached marks a site whose files are the user's own: the directory is
	// never removed on delete, and a wp-config.php we did not write is left alone.
	Attached bool `json:"attached,omitempty"`
	// Installed marks files this app put on disk. Delete may clean those up even
	// outside our own tree; anything without it is treated as the user's.
	Installed bool   `json:"installed,omitempty"`
	AdminUser string `json:"admin_user,omitempty"`
	AdminPass string `json:"admin_pass,omitempty"`
}

// SiteID is the stable identity of a site: the slug.
func (s *Site) ID() string { return s.Slug }

// URL returns the canonical http URL for the site.
func (s *Site) URL() string { return fmt.Sprintf("http://%s:%d", s.Domain, s.HTTPPort) }

// SURL returns the canonical https URL.
func (s *Site) SURL() string { return fmt.Sprintf("https://%s:%d", s.Domain, s.HTTPSPort) }

// Runtime is a discovered or installable PHP toolchain.
type Runtime struct {
	Version    string `json:"version"`     // "8.2"
	Bin        string `json:"bin"`         // php cli binary
	FPM        string `json:"fpm"`         // php-fpm binary
	Pear       string `json:"pear"`        // empty if absent
	Source     string `json:"source"`      // "homebrew" | "path"
	InstallCmd string `json:"install_cmd"` // how to (re)install
}

// MySQLRuntime is the embedded database server.
type MySQLRuntime struct {
	Kind    string `json:"kind"` // "mariadb" | "mysql"
	Version string `json:"version"`
	Dir     string `json:"dir"` // engine dir under our root (data dir lives inside)
	Bin     string `json:"bin"`
}

// HTTPRuntime describes the HTTP front. "router" = built-in Go vhost proxy.
type HTTPRuntime struct {
	Kind    string `json:"kind"`    // "router" | "apache"
	Version string `json:"version"` // apache version when kind=apache
	Bin     string `json:"bin"`
}

// Worktree is a git worktree of a site's repo.
type Worktree struct {
	ID     string `json:"id"` // slug@branch sanitized
	Site   string `json:"site"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
	Domain string `json:"domain"`
}

// Inventory is the persisted runtime environment.
type Inventory struct {
	PHPs    []Runtime    `json:"phps"`
	MySQL   MySQLRuntime `json:"mysql"`
	HTTP    HTTPRuntime  `json:"http"`
	Brew    string       `json:"brew"`
	Refresh time.Time    `json:"refresh"`
}

// Runtimes returns installed PHP versions sorted low→high.
func (inv *Inventory) Runtimes() []string {
	out := make([]string, 0, len(inv.PHPs))
	for _, p := range inv.PHPs {
		out = append(out, p.Version)
	}
	return out
}

// FindPHP locates a runtime by version.
func (inv *Inventory) FindPHP(v string) *Runtime {
	for i := range inv.PHPs {
		if inv.PHPs[i].Version == v {
			return &inv.PHPs[i]
		}
	}
	return nil
}

// DefaultPorts are the fallback service ports.
const (
	DefaultHTTPPort  = 1080
	DefaultHTTPSPort = 10443
	DefaultDBPort    = 10360
	DefaultAPIPort   = 10809
)

// TestTLDDomains lists TLDs we use for local hosts.
func TestTLD(domain string) bool {
	return strings.HasSuffix(domain, ".test") || strings.HasSuffix(domain, ".local")
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify makes a filesystem/domain-safe slug.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// BranchSlug sanitizes a git branch for paths/domains.
func BranchSlug(branch string) string {
	b := strings.ReplaceAll(branch, "/", "-")
	return Slugify(b)
}

// HomeDir is $HOME with a fallback.
func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Root is the per-user state directory (~/.agent-local).
func Root() string {
	if r := os.Getenv("AGENT_LOCAL_HOME"); r != "" {
		return r
	}
	return filepath.Join(HomeDir(), "."+AppName)
}

// Paths centralizes every path the app touches.
type Paths struct{ Root string }

// P returns the app paths.
func P() Paths { return Paths{Root: Root()} }

func (p Paths) Sites() string              { return filepath.Join(p.Root, "sites") }
func (p Paths) WP(slug string) string      { return filepath.Join(p.Sites(), slug) }
func (p Paths) Run() string                { return filepath.Join(p.Root, "run") }
func (p Paths) Logs() string               { return filepath.Join(p.Root, "logs") }
func (p Paths) Certs() string              { return filepath.Join(p.Root, "certs") }
func (p Paths) Conf() string               { return filepath.Join(p.Root, "conf") }
func (p Paths) Bin() string                { return filepath.Join(p.Root, "bin") }
func (p Paths) Engines() string            { return filepath.Join(p.Root, "engines") }
func (p Paths) Store() string              { return filepath.Join(p.Root, "sites.json") }
func (p Paths) Inv() string                { return filepath.Join(p.Root, "inventory.json") }
func (p Paths) Token() string              { return filepath.Join(p.Root, "token") }
func (p Paths) RouterPF() string           { return filepath.Join(p.Conf(), "pf-bare.conf") }
func (p Paths) RouterConf() string         { return filepath.Join(p.Conf(), "router.json") }
func (p Paths) ApacheConf() string         { return filepath.Join(p.Conf(), "httpd-agent-local.conf") }
func (p Paths) ApachePid() string          { return filepath.Join(p.Run(), "apache.pid") }
func (p Paths) Log(name string) string     { return filepath.Join(p.Logs(), name+".log") }
func (p Paths) Sock(name string) string    { return filepath.Join(p.Run(), name+".sock") }
func (p Paths) SiteLog(slug string) string { return filepath.Join(p.Logs(), slug) }

// Ensure creates the base directory tree.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Root, p.Sites(), p.Run(), p.Logs(), p.Certs(), p.Conf(), p.Bin(), p.Engines()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
