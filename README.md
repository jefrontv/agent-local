# agent-local

**Local WordPress for humans and agents.** One Go binary that creates, serves and
manages WordPress sites on macOS — per-site PHP versions, an embedded MariaDB,
real domains without port suffixes, and git-branch previews on their own URLs.

Built because existing local-dev apps assume a human is clicking. `agent-local`
exposes the *same* engine three ways, so a coding agent can do everything you can:

| Surface | Use it for |
|---|---|
| **TUI** — `agent-local` | interactive dashboard: sites, worktrees, runtimes, doctor |
| **CLI** — `agent-local <cmd>` | scripting, one command per action |
| **HTTP API + MCP** | agents (Claude, Codex, anything MCP) driving the whole stack |

Everything lives in `~/.agent-local`. Nothing is required up front: whatever is
missing — Homebrew, PHP, MariaDB, Apache, wp-cli — the app detects and installs.

---

## Contents

- [Install](#install) · [Quick start](#quick-start) · [Zero prompts](#zero-prompts-recommended-one-time)
- [Create a site](#create-a-site) · [Import an existing site](#import-an-existing-site) · [Domains](#domains)
- [PHP versions](#php-versions) · [Databases](#databases) · [Branch previews](#branch-previews-git-worktrees)
- [For agents](#for-agents) · [HTTP fronts](#http-fronts) · [CLI reference](#cli-reference)
- [Layout & ports](#layout--ports) · [How it works](#how-it-works) · [Troubleshooting](#troubleshooting) · [Releasing](#releasing) · [Uninstall](#uninstall)

---

## Install

```sh
brew install jefrontv/tap/agent-local
```

No Homebrew:

```sh
curl -fsSL https://raw.githubusercontent.com/jefrontv/agent-local/main/install.sh | bash
```

From a checkout (builds with Go if present, else downloads the latest release):

```sh
git clone git@github.com:jefrontv/agent-local.git && cd agent-local && ./install.sh
```

No prerequisites either way. Then:

```sh
agent-local doctor        # what's present, what's missing
agent-local doctor --fix  # install/repair everything fixable
```

Root is needed for exactly two things — `/etc/hosts` entries and trusting the
per-domain TLS cert — and the app asks (macOS password dialog) at the moment it
needs them. [Zero prompts](#zero-prompts-recommended-one-time) removes even that.

### Updating

```sh
agent-local update            # download + verify + swap the binary
agent-local update --check    # just report what is published
agent-local restart-daemon    # let the new build take over the running daemon
```

`update` verifies the download against the release `checksums.txt` before it
replaces anything, and refuses to touch a Homebrew-managed install — use
`brew upgrade agent-local` there. Releases are cut by GoReleaser from a tag
(`.goreleaser.yaml`, `.github/workflows/release.yml`) as one universal binary
for Apple Silicon and Intel.

## Quick start

```sh
agent-local create mysite                 # WordPress installed and serving, ~15s
agent-local open mysite                   # → http://mysite.test
agent-local list                          # every site, state, URL
```

Admin credentials are printed at the end of `create` and stored in state
(`agent-local db mysite` for DB creds, `get_site` for everything).

## Zero prompts (recommended, one-time)

```sh
agent-local sudo     # scoped NOPASSWD allowlist — exact commands only
agent-local alias    # 127.0.0.2 alias + root front daemon on :80/:443
```

- **`sudo`** writes `/etc/sudoers.d/agent-local` listing only the handful of
  commands the app runs as root (copy a hosts file, add a trusted cert, bring up
  the loopback alias). After this nothing ever prompts again — including agents
  and the daemon, which cannot answer a dialog. Undo: `sudo rm /etc/sudoers.d/agent-local`.
- **`alias`** gives you bare URLs: `http://mysite.test` instead of
  `http://mysite.test:1080`. Hosts entries point at `127.0.0.2`, where a root
  LaunchDaemon binds 80/443 and pipes to the router. A *specific-address* bind
  beats another app's wildcard listener, so agent-local and LocalWP can run side
  by side. Survives reboots; `agent-local alias --off` removes it.

### Sharing port 80 with LocalWP (or MAMP, Valet…)

Both apps can serve at once: they bind different addresses, and the kernel
allows a specific-address listener alongside a wildcard one in either order.
What trips people up is that LocalWP *pre-checks* port 80 and refuses to start
when anything is listening — including us. So hand it the ports for a moment:

```sh
agent-local yield          # frees :80/:443 for 45s, then re-binds automatically
agent-local yield 90       # longer window
```

Start the other app inside that window. agent-local reclaims its specific
address afterwards and both keep working; your sites stay reachable on `:1080`
throughout. The front daemon also stands aside on its own if it sees another
local-dev **router** running with nothing bound on `:80` yet.

Prefer to give up bare URLs entirely? `agent-local alias --off`.

## Create a site

```sh
agent-local create mysite
agent-local create shop --php 8.3 --domain shop.local --title "My Shop"
agent-local create blog --admin-user jake --admin-pass 's3cret' --admin-email me@x.dev
agent-local create theme-dev --repo git@github.com:me/my-wp-tree.git
```

Each site gets its own database, its own php-fpm pool, a hosts entry and a
self-signed cert. `--repo` clones instead of downloading WordPress (the repo
should contain WordPress at `wp/`).

## Import an existing site

Any WordPress directory, or a LocalWP site by name:

```sh
agent-local localwp-sites                    # what's importable
agent-local import orleton-om                # LocalWP site → served locally
agent-local import /path/to/public           # any docroot, served in place
agent-local import /path/to/public --copy     # copy files into ~/.agent-local
```

Where the data comes from — four modes, pick whichever matches reality:

```sh
# 1. Live DB, credentials read from the site's own wp-config.php (default)
agent-local import /path/to/dir

# 2. Explicit source DB (wp-config missing or pointing somewhere stale)
agent-local import /path/to/dir --db-host 127.0.0.1 --db-port 8889 \
  --db-user root --db-pass secret --db-name mydb

# 3. From a dump instead of a live server
agent-local import /path/to/dir --sql ~/Downloads/site.sql

# 4. Leave the database alone entirely and just serve the files
agent-local import /path/to/dir --serve-only
```

The import pipeline:

1. locate the source database (LocalWP registry socket/creds, `wp-config.php`, or your flags)
2. stream `dump → collation fixer → load` into the embedded MariaDB — flat memory
   for multi-GB dumps, and MySQL-8 `utf8mb4_0900_ai_ci` is rewritten to a MariaDB
   collation on the way through
3. rewrite `wp-config.php` `DB_*` (original kept as `wp-config.php.agent-local.bak`)
   plus any theme URL-override constant (e.g. `EFRONT_URL_OVERRIDE`)
4. `search-replace` every domain the database actually stores — including staging
   subdomains — to the new local domain, then flush and serve

In-place is the default, so a 7 GB checkout costs zero copy time. Deleting an
imported site never touches files outside `~/.agent-local`; it detaches the
config and restores the backup.

## Domains

Any domain works — `.test` is only the default:

```sh
agent-local create mysite --domain whatever.you.want
agent-local domain mysite newname.local      # move an existing site
agent-local suffix                           # show the default suffix
agent-local suffix .localhost                # change it for new sites/previews
```

`.test` and `.localhost` are reserved by RFC 6761 and safest. `.local` works but
collides with mDNS/Bonjour. Multi-level suffixes (`.mysite.local`) are fine.

## PHP versions

```sh
agent-local install php 8.1      # also: mariadb | apache | wp-cli | brew
agent-local php mysite 8.1       # switch a site; its pool restarts live
agent-local doctor               # every discovered runtime
```

Installing a version takes about a minute (Homebrew keg + re-discovery) and needs
no shell work. Each site runs its own php-fpm pool, so versions never conflict —
one site on 7.4 next to one on 8.4 is normal.

## Databases

One embedded MariaDB on port 10360 backs every site. Each site gets its own
database (`al_<slug>`) and user; queries run as root, so there are no
restrictions.

```sh
agent-local db mysite                             # host/port/db/user/pass
agent-local db mysite tables                      # tables + row counts + KB
agent-local db mysite "SELECT * FROM wp_users"     # SQL, this site's schema
agent-local db mysite import dump.sql.gz          # replace contents (gzip auto-detected)
agent-local db mysite import dump.sql --keep-urls # …without rewriting domains
agent-local db mysite export [out.sql]            # default ~/.agent-local/dumps/
agent-local db mysite reset                       # empty it (grants kept)
```

- **Unrestricted SQL.** SELECT/INSERT/UPDATE/DELETE, DDL, and multi-statement
  scripts. Scoped to a site the schema is selected for you; unscoped you get the
  whole server (`CREATE DATABASE`, cross-database joins, `information_schema`).
  Results come back as TSV with a header row.
- **Imports stream.** Same collation fixer as the live importer, so dump size
  doesn't matter. An import *replaces* the database (drop + recreate).
- **URLs are rewritten by default.** The dump's `siteurl`/`home` hosts are
  search-replaced to the target site's domain, serialized data included, so a
  production dump serves locally instead of redirecting to production.
  `--keep-urls` opts out.
- **A dump carries no files.** Import a database whose active theme isn't in the
  target's `wp-content/themes` and WordPress renders blank — the import result
  says so. For a whole site, use `import` (files + database together).

## Branch previews (git worktrees)

Serve a branch of a site's repo on its own domain, next to the running site:

```sh
agent-local branches sulo                      # what's available
agent-local worktree sulo no-footer            # → http://no-footer.sulo.pact
agent-local worktrees sulo                     # list previews
agent-local worktree sulo no-footer --remove    # tear down (branch is kept)
```

The preview domain is derived from the site's own domain, so custom TLDs carry
over. The repo is found automatically: a site's work dir, or the docroot itself
(LocalWP-style checkouts keep `.git` in `app/public`).

What a preview is:

- **branch files win** — anything the branch tracks is the checkout's copy, so
  editing it changes only the preview
- **everything else is filled in from the base site** — WordPress core, plugins,
  gitignored build output (`assets/dist`, `vendor/`), untracked sibling themes.
  A theme-only repo still boots a complete site.
- **code is clone-copied, not symlinked.** PHP resolves `__FILE__` through
  symlinks, so a symlinked `wp-load.php` would set `ABSPATH` to the base install
  and silently run the base site's `wp-config.php`. APFS copy-on-write keeps it
  cheap: a 555 MiB preview costs ~60 MiB of real disk and ~10s to create.
- **uploads are shared** by symlink — no duplicated media
- **its own page cache**, so previews never serve or poison base cache files
- **its own `wp-config.php`** — same database, URLs pinned to the preview domain,
  plus an mu-plugin that re-pins `option_home`/`option_siteurl` at `PHP_INT_MAX`
  and disables canonical redirects. Without it, security plugins that force the
  canonical host (iThemes/Solid Security's SSL module) drag previews back to the
  base domain.

Same database both sides — the difference between them is code.

## For agents

### MCP (Claude, Codex, any MCP client)

```json
{
  "mcpServers": {
    "agent-local": { "command": "agent-local", "args": ["mcp"] }
  }
}
```

41 tools — everything the CLI can do, no shell required:

| Area | Tools |
|---|---|
| discovery | `status`, `list_sites`, `get_site`, `localwp_sites`, `list_runtimes`, `list_branches` |
| lifecycle | `create_site`, `import_site`, `start_site`, `stop_site`, `restart_site`, `delete_site` |
| runtime | `switch_php`, `install_runtime`, `get_http_front`, `set_http_front` |
| domains | `set_domain`, `get_domain_suffix`, `set_domain_suffix`, `add_hosts_entries`, `remove_hosts_entries` |
| database | `db_creds`, `db_query`, `db_tables`, `db_import`, `db_export`, `db_reset` |
| wordpress | `wp_cli`, `worktree_wp_cli` |
| previews | `add_worktree`, `list_worktrees`, `start_worktree`, `stop_worktree`, `remove_worktree` |
| ops | `get_logs`, `doctor`, `doctor_fix` |
| integration | `resolve_path`, `cert_status`, `cert_trust`, `yield_ports` |

Design notes that matter when driving this from an agent:

- **The daemon auto-starts** on the first tool call and stays up under either HTTP
  front, so switching fronts never costs you control of the API.
- **Everything is idempotent-friendly.** Starting a running site is a no-op;
  create/import block until the site actually answers.
- **No prompts, ever** — after `agent-local sudo`, root steps go through the
  allowlist. Not exposed as tools: `sudo` and `alias`, the two one-time installs
  that genuinely need a password.
- **Every response is `{"ok":true,"data":…}` or `{"ok":false,"error":…}`.**

### HTTP API

```sh
TOKEN=$(agent-local api-token)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:10809/status
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"demo","php_version":"8.2"}' http://127.0.0.1:10809/sites
```

| Group | Endpoints |
|---|---|
| sites | `GET /status`, `GET\|POST /sites`, `GET /sites/{slug}`, `DELETE /sites/{slug}[?files=keep&db=keep]`, `POST /sites/{slug}/{start,stop,restart,php,domain,wp-cli}` |
| import | `POST /import` |
| database | `POST /sites/{slug}/db`, `POST /sites/{slug}/db/query`, `POST /sites/{slug}/db/{import,export,reset}`, `GET /sites/{slug}/db/tables`, `POST /db/query` |
| previews | `GET /sites/{slug}/branches`, `GET\|POST /sites/{slug}/worktrees`, `POST /sites/{slug}/worktrees/{id}/{start,stop,wp-cli}`, `DELETE /sites/{slug}/worktrees/{id}` |
| platform | `GET /runtimes`, `POST /install`, `GET\|POST /front`, `GET\|POST /suffix`, `GET /doctor`, `POST /doctor/fix`, `GET /logs/{name}?lines=N`, `POST\|DELETE /hosts` |
| integration | `GET /resolve?path=…`, `GET /certs/{domain}`, `POST /certs/{domain}/trust`, `POST /yield` |

### Embedding agent-local in another app

`GET /resolve?path=…` maps a checkout directory (or any file inside it) to a
slug plus everything needed to talk to the site — URL, docroot, PHP version,
live DB connection details, cert state. That is the entry point for a host app
that keys sites by path rather than by slug. `POST /sites/{slug}/start` returns
the same runtime block, so starting a site and learning how to reach its
database is a single call.

[docs/MUSTER-INTEGRATION.md](docs/MUSTER-INTEGRATION.md) is a worked example:
adding agent-local as a selectable local-stack provider alongside LocalWP in an
Electron app, with the endpoint reference, the provider contract, parity gaps
and acceptance criteria.

## HTTP fronts

Both fronts serve every site and preview on the same ports; switch any time with
`agent-local front router|apache` (or `set_http_front`).

- **router** (default) — the daemon's built-in Go vhost proxy: Host-header
  routing, static files straight from disk, FastCGI streaming to per-site pools,
  per-domain TLS via SNI, pretty-permalink rewrites, and duplicate-`Set-Cookie`
  collapsing for plugins that emit hundreds of them. Zero config.
- **apache** — renders `~/.agent-local/conf/httpd-agent-local.conf`
  (`mod_proxy_fcgi` vhosts per site and preview, HTTP + HTTPS with each domain's
  cert, `AllowOverride All` with the 2.2-compat modules loaded so production
  `.htaccess` files behave) and runs Homebrew's httpd. Needs
  `agent-local install apache`.

## CLI reference

```
agent-local                            TUI
agent-local create NAME [--domain d] [--php v] [--repo url]
                       [--admin-user u] [--admin-pass p] [--admin-email e] [--title t]
agent-local list | start SLUG | stop SLUG | restart SLUG | open SLUG
agent-local delete SLUG [--yes] [--keep-files] [--keep-db]
agent-local import SOURCE [--name n] [--domain d] [--php v] [--copy]
                         [--sql file] [--serve-only]
                         [--db-host h] [--db-port p] [--db-user u] [--db-pass p] [--db-name n]
agent-local localwp-sites              list importable LocalWP sites
agent-local db SLUG [sql | tables | import FILE [--keep-urls] | export [FILE] | reset]
agent-local php SLUG VERSION           switch PHP (live)
agent-local domain SLUG NAME           change a site's domain
agent-local suffix [.test]             show/set the default domain suffix
agent-local branches SLUG              git branches of the site's repo
agent-local worktree SLUG BRANCH [--remove]
agent-local worktrees SLUG
agent-local wp SLUG -- core version    wp-cli through the site's PHP
agent-local install brew|php V|mariadb|apache|wp-cli
agent-local front [router|apache]      show / switch HTTP front
agent-local sudo                       passwordless root allowlist (one-time)
agent-local alias [--off]              bare URLs on 127.0.0.2:80/443 (one-time)
agent-local yield [secs]               free :80/:443 briefly, then re-bind
agent-local resolve [PATH]             which site owns a path (default: cwd)
agent-local cert DOMAIN [--trust]      TLS state for a domain
agent-local doctor [--fix]
agent-local logs NAME [lines]          mysql | apache | daemon | fpm-<slug> | <slug>
agent-local daemon [--background]      router + agent API
agent-local mcp                        MCP server over stdio
agent-local api-token | version
```

## Layout & ports

| What | Where |
|---|---|
| Sites created by agent-local | `~/.agent-local/sites/<slug>/wp/` |
| Branch previews | `<repo>/@/<branch>/` |
| MariaDB data | `~/.agent-local/engines/mysql/data/` |
| Generated config (fpm pools, apache, pf) | `~/.agent-local/conf/` |
| Logs | `~/.agent-local/logs/` |
| Dumps from `db export` | `~/.agent-local/dumps/` |
| State + API token | `~/.agent-local/sites.json`, `~/.agent-local/token` |

| Port | Service |
|---|---|
| 80 / 443 | bare-URL front daemon on `127.0.0.2` (after `agent-local alias`) |
| 1080 / 10443 | shared HTTP / HTTPS front for all sites and previews |
| 10360 | embedded MariaDB |
| 10809 | agent HTTP API (bearer token in `~/.agent-local/token`) |

## How it works

**`create`** provisions the database and user, downloads WordPress (or clones
`--repo`), writes `wp-config.php` with random salts, starts the site's php-fpm
pool, POSTs the WordPress installer, then adds the hosts entry and issues +
trusts a self-signed cert.

**Serving** is one shared front for every site and preview, routed by Host
header to a per-site php-fpm pool over a unix socket. Static files are served
straight from disk; PHP is streamed over FastCGI.

**State** is a single JSON file (`sites.json`) written atomically under an
exclusive flock, and re-read whenever it changes on disk — so the CLI, the TUI,
the daemon and multiple agents can all act at once without clobbering each other.

**Processes** (MariaDB, php-fpm pools, apache, the daemon) are supervised with
pid files, reaped on exit, and de-duplicated on start, so repeated restarts can't
leave orphans holding sockets.

## Troubleshooting

```sh
agent-local doctor          # platform, brew, PHP runtimes, DB, front, hosts, certs, per-site liveness
agent-local doctor --fix    # apply every fixable finding
agent-local logs daemon 50  # or: mysql | apache | fpm-<slug>
```

Common cases:

- **Site loads on `:1080` but not the bare domain** → run `agent-local alias`.
- **A password dialog appeared** → run `agent-local sudo` once; it won't come back.
- **Another local-dev app is running** → fine. agent-local binds specific
  addresses and its own ports, so both coexist.
- **Imported site redirects somewhere else** → its database still holds the old
  domain; `agent-local db <slug> tables` then re-import, or
  `agent-local wp <slug> -- search-replace old.host <slug>.test --all-tables`.

## Releasing

```sh
git tag -a v0.2.0 -m v0.2.0 && git push origin v0.2.0
```

That is the whole process. `.github/workflows/release.yml` runs GoReleaser on a
macOS runner (needed: `lipo` and `codesign`), which builds one universal binary,
ad-hoc signs it, archives it with the README and docs, publishes checksums, and
writes release notes grouped from the commit subjects.

Dry-run any change to the pipeline without tagging or uploading:

```sh
goreleaser check                        # config valid?
goreleaser release --snapshot --clean   # full build into dist/
```

**Homebrew cask** publishing is opt-in: set a repo secret `HOMEBREW_TAP_TOKEN`
to a token with write access to [`jefrontv/homebrew-tap`](https://github.com/jefrontv/homebrew-tap)
and every release also updates `Casks/agent-local.rb`. Without the secret the
release still succeeds and only the cask step is skipped.

```sh
gh secret set HOMEBREW_TAP_TOKEN --repo jefrontv/agent-local
```

## Uninstall

```sh
brew uninstall --zap agent-local        # Homebrew installs: also removes ~/.agent-local
```

Manual installs:

```sh
agent-local alias --off                 # remove the root front daemon + alias
sudo rm -f /etc/sudoers.d/agent-local   # remove the NOPASSWD allowlist
rm -rf ~/.agent-local                   # state, sites, database, logs
rm -f ~/.local/bin/agent-local
```

Imported sites that were served in place keep their files; only agent-local's own
copies live under `~/.agent-local`.
