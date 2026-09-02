package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The command reference as data, rendered in the TUI's palette so
// `agent-local help` reads like the rest of the tool. Grouped by what a
// person is trying to do, one line per command, descriptions in plain words.

type helpEntry struct {
	cmd  string // subcommand, "" for the bare binary
	args string // placeholders and flags, as typed
	desc string
}

type helpGroup struct {
	title   string
	entries []helpEntry
}

var helpGroups = []helpGroup{
	{"Sites", []helpEntry{
		{"", "", "open the dashboard"},
		{"create", "NAME [--domain d] [--php v] [--repo url]", "create and install a WordPress site"},
		{"attach", "DIR [--name n] [--domain d] [--php v]", "serve a directory you already have, with an empty database"},
		{"import", "SOURCE [--copy] [--sql FILE] [--serve-only]", "import a LocalWP site or any docroot"},
		{"localwp-sites", "", "LocalWP sites available to import"},
		{"list", "", "every site, its state and URL"},
		{"start | stop | restart", "SLUG", "control one site"},
		{"delete", "SLUG [--yes] [--keep-files] [--keep-db]", "remove a site; a snapshot is saved first"},
		{"open", "SLUG", "open the site in your browser"},
		{"domain", "SLUG NAME", "change a site's domain; hosts entry and cert follow"},
		{"php", "SLUG VERSION [--tap]", "switch PHP version, installing it if needed"},
		{"resolve", "[PATH]", "which site owns a path (default: cwd)"},
	}},
	{"Database", []helpEntry{
		{"db", "SLUG", "connection details"},
		{"db", "SLUG \"SQL\"", "run a statement"},
		{"db", "SLUG import FILE.sql[.gz] [--keep-urls]", "load a dump; URLs rewritten, snapshot saved first"},
		{"db", "SLUG export [FILE]", "dump to a file"},
		{"db", "SLUG reset | tables | gui", "empty it, list tables, or open Adminer"},
		{"db", "SLUG snapshot [NAME]", "save a restore point"},
		{"db", "SLUG snapshots", "list restore points"},
		{"db", "SLUG restore [NAME]", "restore one (default: newest)"},
	}},
	{"Develop", []helpEntry{
		{"worktree", "SLUG BRANCH [--remove]", "serve a git branch on its own URL"},
		{"worktrees", "SLUG", "list branch previews"},
		{"branches", "SLUG", "branches of the site's repo"},
		{"wp", "SLUG -- ARGS", "run wp-cli against the site"},
		{"wpdebug", "SLUG [on|off]", "WP_DEBUG, logged to ~/.agent-local/logs/wp-SLUG.log"},
		{"logs", "NAME [LINES]", "tail a log: mysql, apache, daemon, fpm-SLUG, wp-SLUG"},
		{"mail", "SLUG [ID] [--open] [--clear]", "emails the site has sent"},
		{"media", "SLUG [URL | --auto | --off]", "send missing uploads to a production origin"},
		{"share", "SLUG [--minutes N] [--off]", "public URL through a Cloudflare tunnel"},
		{"cert", "DOMAIN [--trust]", "TLS state for a domain; --trust issues and trusts it"},
	}},
	{"Agents", []helpEntry{
		{"connect", "[--list | --all | --remove] [HARNESS...]", "register the MCP server in Claude Code, Codex, Cursor and friends"},
		{"mcp", "", "the MCP server itself (stdio); clients launch this"},
		{"mcp", "--config", "the client config block, for a client connect doesn't know"},
		{"api-token", "", "bearer token for the HTTP API"},
		{"jobs", "", "recent long-running jobs"},
		{"job", "ID", "one job's progress"},
	}},
	{"Machine", []helpEntry{
		{"doctor", "[--fix]", "health checks; --fix applies every repair"},
		{"install", "brew | php VERSION | mariadb | apache", "install a dependency (wp-cli too)"},
		{"front", "[router | apache]", "show or switch the HTTP front"},
		{"yield", "[SECONDS]", "free :80/:443 briefly so another app can start"},
		{"autostart", "[--off]", "start the daemon at login (on by default)"},
		{"sites-dir", "[PATH]", "where new sites are created"},
		{"suffix", "[.test]", "default domain suffix"},
		{"daemon", "[--background]", "run the daemon by hand"},
		{"restart-daemon", "", "hand over to a freshly installed binary"},
		{"update", "[--check]", "install the latest release"},
		{"version", "", "what build this is"},
	}},
}

// helpColumn is where descriptions start. Anything longer wraps its
// description onto the next line rather than pushing the column around.
const helpColumn = 54

func renderHelp() string {
	var b strings.Builder
	b.WriteString(stName.Render(AppName) + "  " + stDim.Render("local WordPress for humans and agents") + "  " + stVersion.Render(Version) + "\n")
	b.WriteString(stDim.Render("usage: "+AppName+" <command> [arguments]") + "\n")

	stArgs := lipgloss.NewStyle().Foreground(cInk)
	for _, g := range helpGroups {
		b.WriteString("\n" + stKey.Render(strings.ToUpper(g.title)) + "\n")
		for _, e := range g.entries {
			left := e.cmd
			if e.args != "" {
				left += " " + e.args
			}
			rendered := "  " + stName.Render(e.cmd)
			if e.args != "" {
				rendered += " " + stArgs.Render(e.args)
			}
			if e.cmd == "" {
				rendered = "  " + stName.Render(AppName)
				left = AppName
			}
			if len(left)+2 < helpColumn {
				b.WriteString(rendered + strings.Repeat(" ", helpColumn-len(left)-2) + stDim.Render(e.desc) + "\n")
			} else {
				b.WriteString(rendered + "\n" + strings.Repeat(" ", helpColumn) + stDim.Render(e.desc) + "\n")
			}
		}
	}
	b.WriteString("\n" + stDim.Render("Every command here is also an MCP tool for the agents you connect.") + "\n")
	return b.String()
}

// printHelp is the help command and the fallback for an unknown one.
func printHelp() {
	fmt.Print(renderHelp())
}
