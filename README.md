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
- [PHP versions](#php-versions) · [Databases](#databases) · [Snapshots](#snapshots) · [Branch previews](#branch-previews-git-worktrees)
- [Outgoing mail](#outgoing-mail) · [WP_DEBUG](#wp_debug-without-the-ritual) · [Sharing publicly](#sharing-a-site-publicly)
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
agent-local autoupdate on     # let the daemon install releases itself (off by default)
```

The daemon watches its own binary. However a new build lands — `update`,
`brew upgrade`, `install.sh` — it finishes any running job, steps down, and
launchd starts the new one; nobody runs `restart-daemon`. Once a day it asks
GitHub what is published and remembers the answer, so the dashboard header,
`doctor`, `version` and the `status` API can say a release is out without any
of them touching the network. With `autoupdate on` that daily check also
installs what it finds.

One macOS wrinkle. The system asks before a *new build* may read `~/Documents`,
`~/Desktop` or `~/Downloads`, and a freshly installed binary counts as new. Sites
kept in those folders mean a permission dialog after each update; the daemon
boots anyway and PHP serves, but their static files 403 until you click Allow.
Sites anywhere else — `~/Sites`, or the default `agent-local sites-dir` — never
ask.

`update` verifies the download against the release `checksums.txt` before it
replaces anything, and refuses to touch a Homebrew-managed install — use
`brew upgrade agent-local` there (the daemon still hands over by itself).
Releases are cut by GoReleaser from a tag (`.goreleaser.yaml`,
`.github/workflows/release.yml`) as one universal binary for Apple Silicon and
Intel.

## Quick start

```sh
agent-local create mysite                 # WordPress installed and serving, ~15s
agent-local open mysite                   # → http://mysite.test
agent-local list                          # every site, state, URL
```

Admin credentials are printed at the end of `create` and stored in state
(`agent-local db mysite` for DB creds, `get_site` for everything).

### Importing a halted site

`agent-local import <localwp-site>` no longer needs the site to be running. If its
database is unreachable, agent-local asks Local to start it through the app's own
API, waits for mysqld, and dumps through the per-site socket that only exists once
the site is up:

```
[source] seo-website-auditor is not running — asking LocalWP to start it
[source] seo-website-auditor is up
[files]  copying docroot → ~/.agent-local/sites/lwtest/wp
[database] dumping local from …/Local/run/a9HNIy28u/mysql/mysqld.sock
```

Local has to be open — it owns those services. When it is not, or the site fails
to come up, the error says which of the two it was and what to do by hand rather
than surfacing a bare "can't connect to MySQL".

## Speed

Commands are cheap because the toolchain scan is cached, not repeated. Discovery
runs `php -v` per installed keg (~90ms each), which every command used to pay:

| | before | now |
|---|---|---|
| `agent-local list` | 889ms | **28ms** |
| `agent-local media <slug>` | 871ms | **20ms** |
| TUI first frame | 601ms | **36ms** |
| `doctor` (6 sites) | ~9s of serial probes | **1.7s** |

The scan is stored with a timestamp and re-run when it stops being true — any
recorded binary missing, or 24h old — so `brew install php@8.4` is picked up
without a flag. `agent-local doctor --refresh` forces it. Per-site probes and keg
probes both run concurrently.

Serving itself is not the bottleneck: **2.9ms** for a static file, **4.6ms** for a
cached PHP page through FastCGI.

### Long actions do not block the UI

Installing WordPress, renaming a domain (a search-replace across every table),
switching PHP, `brew install` — all of it runs in the background with the screen
still painting, showing what the engine is doing as it does it:

```
◓ installing WordPress into ~/Sites/mysite   16s
    files GET https://wordpress.org/latest.tar.gz
```

Keys are held while an action runs, since acting on a half-changed site turns one
problem into two — `ctrl+c` still quits.

### `.local` domains work too

`.local` is reserved for mDNS (RFC 6762), so macOS resolves those names through
Bonjour rather than trusting `/etc/hosts`. An entry with only an A record still
sends the AAAA question to mDNS, where nothing answers and the resolver waits
**five seconds — per lookup, uncached**:

```
mdnsprobe.local  (IPv4 hosts line only)   5004ms  5005ms  5006ms
orleton-om.test  (IPv4 hosts line only)      3ms     2ms     2ms
```

So agent-local serves both families. `lo0` carries `127.0.0.2` and `fd00:a10c::2`,
the front daemon binds `:80`/`:443` on each, and every domain gets two hosts lines.
Both questions are then answered from the file and mDNS is never consulted:

```
transportaustralia.local   before 5.006s   after 0.003s      (~1500x)
```

A ULA, not `::1`: another local-dev router answers on `::1` when one is running, and
your own sites would land on it. Existing sites gain the second line the next time
their hosts entry is written (`agent-local alias` does the lot); `doctor` reports
which state each `.local` domain is in. Without the IPv6 half everything still
works — `.local` is just slow again — so it never blocks setup.

## After a reboot

Everything comes back by itself. Three things have to happen, and each is now
someone's job rather than a manual step:

| | who does it |
|---|---|
| `127.0.0.2` back on `lo0` | the root front daemon, at startup and whenever the address disappears — macOS does not persist `ifconfig` aliases |
| the router/API daemon running | a per-user LaunchAgent (`agent-local autostart`, on by default) |
| sites that were running, running | the daemon restores every site and preview whose state says it was up, concurrently |

The alias is the one that used to bite: without it the front daemon has nothing to
bind, so bare URLs vanish and every address falls back to `:1080`. It is now
re-added within about two seconds of going missing.

```sh
agent-local autostart          # on (default)
agent-local autostart --off    # only run the daemon when something asks for it
```

The login job stands by while another daemon holds the ports rather than exiting,
so if that one dies the standby takes over in about two seconds — verified by
killing it: `api=200 site=200` on the next probe.

## Sharing a machine with LocalWP

Bare URLs need `:80`/`:443`, and so does every other local-dev tool. agent-local
binds them on a loopback alias (`127.0.0.2`) rather than the wildcard, so both can
run — but the order matters, because of how BSD sockets work:

| Started first | Then | Result |
|---|---|---|
| LocalWP (`*:80`) | agent-local (`127.0.0.2:80`) | both work |
| agent-local (`127.0.0.2:80`) | LocalWP (`*:80`) | LocalWP's nginx cannot bind |

A wildcard bind fails while any specific address holds that port, so agent-local
getting there first stops nginx from starting at all — and Local will still report
its sites as "running" while nothing serves them. Step aside for a moment:

```sh
agent-local yield 60      # frees :80/:443, then takes them back automatically
```

Start the site in Local inside that window. Once nginx holds the wildcard,
agent-local re-binds its alias alongside it and both keep working. `doctor` reports
which of those three states you are in, so a dark site is never a mystery.
Throughout a yield, agent-local sites stay reachable on `:1080`/`:10443`.

The boot race resolves itself. When the front daemon starts and finds LocalWP open
with nothing holding `:80` yet, it waits up to 20 seconds for nginx to bind before
taking the alias — the case where both start at login. The wait is bounded, so a
rival that never appears cannot keep the ports, and it never happens when LocalWP
is closed or already serving. `AGENT_LOCAL_NO_GRACE=1` disables it.

## Missing uploads

A local database usually references thousands of images the local disk does not
have. On production that is solved with an Apache rewrite in `.htaccess`:

```apache
RewriteCond %{REQUEST_URI} ^/wp-content/uploads/
RewriteCond %{REQUEST_FILENAME} !-f
RewriteRule ^(.*)$ https://example.org/$1 [QSA,L]
```

The built-in router does not run `.htaccess` — it is an Apache feature — but it does
read that one rule, because a rule sitting in your own repo should not need a second
command to take effect. Any site whose docroot carries an uploads rewrite has it
honoured automatically, re-read when the file changes. To pin, override or refuse it:

```sh
agent-local media SLUG               # show it, and what .htaccess implies
agent-local media SLUG --auto        # adopt the origin from .htaccess
agent-local media SLUG https://x.org # set it explicitly
agent-local media SLUG --off         # back to 404s
```

In the TUI: `m` on the Sites tab, where `⇥` fills in the `.htaccess` origin.
`doctor` shows where each site's missing uploads go, and whether that came from the
file or was set explicitly. Precedence: a value set here, then an explicit `--off`,
then the site's `.htaccess`.

A `GET` under `/wp-content/uploads/` with no local file gets a 302 to the origin —
a redirect, not a proxy, so behaviour matches the `.htaccess` exactly and nothing
is cached locally. Local files always win, and paths outside `uploads/` still 404
so genuine mistakes stay visible.

## Outgoing mail

Every email a site sends is captured into a per-site inbox instead of being
handed to macOS's usually-dead postfix, where it used to vanish silently —
password resets and form notifications on local sites simply did not work.
Capture is one line of generated pool config (`sendmail_path` points back at
`agent-local sendmail`), so nothing inside the site changes and every plugin
that sends mail the normal way is caught. Existing sites gain it on their
next restart.

```sh
agent-local mail mysite            # list captured messages
agent-local mail mysite <id>       # read one: headers, text body, attachments
agent-local mail mysite --open     # browser inbox
agent-local mail mysite --clear
```

The browser inbox lives at `https://<domain>/.agent-local/mail` on every
site — the same reserved path idea as the database GUI, HTML rendered by the
router itself (the apache front proxies it), auto-refreshing so a form
submission shows up as you alt-tab. HTML bodies render in a sandboxed iframe;
the raw `.eml` is one click away.

For agents this closes a loop: drive the site with a browser, submit the
form, then `list_mail` / `get_mail` and assert the email that came out —
recipient, subject, decoded body — no human mailbox involved. Every message
also comes back with `url` (its page in the inbox) and, when it has an HTML
part, `html_url` (that part alone, sandboxed), so an agent with a browser tool
can look at the email the way the recipient would. Inboxes keep the newest
200 messages (branch previews get their own, keyed by worktree id) and are
removed with the site.

Plugins that bypass `mail()` for a real SMTP/ESP connection (WP Mail SMTP,
FluentSMTP) are not intercepted — they are configured to deliver for real,
and that stays your call.

## WP_DEBUG without the ritual

```sh
agent-local wpdebug mysite on      # WP_DEBUG, log → ~/.agent-local/logs/wp-mysite.log
agent-local logs wp-mysite 50      # read it
agent-local wpdebug mysite off
```

`on` rewrites the constants in wp-config.php (inserted above the "stop
editing" line when absent) and keeps `WP_DEBUG_DISPLAY` off, so notices land
in a tailable file rather than the middle of a rendered page. The white-screen
loop becomes: turn it on, reproduce, `logs wp-<slug>` — or for an agent,
`set_wp_debug` then `get_logs`.

## Sharing a site publicly

```sh
agent-local share mysite              # → https://<random>.trycloudflare.com
agent-local share mysite --minutes 480
agent-local share mysite --off
```

A Cloudflare quick tunnel — no account, no token, no config. Anyone with the
random URL sees the site while the tunnel is up: a client on their phone, a
form-testing service, a colleague. `cloudflared` is installed through
Homebrew on first use like every other dependency.

What makes it work without a search-replace: the tunnel keeps its own
hostname end-to-end, the router resolves it to the shared site, and an
mu-plugin maps `home`/`siteurl` onto the tunnel host **for tunnel requests
only** — pinned at `PHP_INT_MAX` like branch previews, so canonical-host
security plugins cannot drag visitors back to a domain their machine cannot
resolve. Local browsing keeps local URLs throughout, and the plugin is
removed when the share stops.

The boundaries:

- **Auto-stops after 60 minutes** by default (`--minutes N` to change,
  `--forever` to keep it until `--off`). A tunnel nobody remembers is still
  a public URL to your machine.
- **`/.agent-local/*` answers 404 to tunnel traffic** — the share exposes
  the site, never the database GUI or the mail inbox.
- **Shares belong to the daemon** and die with it; a replaced daemon reaps
  any tunnel its predecessor left. `agent-local logs share-<slug>` has
  cloudflared's own output.
- **Router front only** — apache's vhosts cannot route a hostname that did
  not exist when the config was rendered.
- Quick tunnels are Cloudflare's best-effort service for exactly this use;
  for a stable subdomain you would bring your own tunnel token (not built in).

For agents: `share_local_site {"slug":"mysite"}` returns the URL (idempotent
while active), `unshare_local_site` closes it.

## The dashboard

`agent-local` with no arguments opens the panel:

```
  agent-local  0.4.1                                  ● ready   router :1080   6 sites
  Sites   Worktrees   Runtimes   Doctor
─━━━━━━━────────────────────────────────────────────────────────────────────────────────

    SITE                 PHP   DOMAIN                         PREVIEWS
▌ ● freshdemo            8.2   freshdemo.test
  ● muster-demo          8.2   muster-demo.test
  ● sulo                 8.2   sulo.pact                             1

╭──────────────────────────────────────────────────────────────────╮
│    open  https://freshdemo.test                                  │
│      db  al_freshdemo  as al_freshdemo  127.0.0.1:10360          │
│   files  ~/.agent-local/sites/freshdemo/wp   137M                │
╰──────────────────────────────────────────────────────────────────╯
  / search  n new  i import  s start  …  g db               ? help  ⇥ tab  q quit
```

Press `n` and it asks one question:

```
new site in ~/Sites? › 
                       ~/Sites — holds 4 directories
                       enter or y → keep them together · n → a path for this one · d → change this directory
```

Answer it with `enter`, give the site a name, take the offered domain, and that is
the whole flow: the site is installed at `~/Sites/<name>/wp` and served. Where that
shared directory points is a setting, changed from this prompt with `d` or from
anywhere else:

```sh
agent-local sites-dir ~/Sites        # new sites go here from now on
agent-local sites-dir                # show it
agent-local sites-dir --default      # back to ~/.agent-local/sites
```

Existing sites never move. `create` on any surface honours the setting, so an agent
calling `create_site` with no `dir` puts the checkout where you would have.

Answer `n` instead and you pick the path yourself, completing as you go with `⇥`:

```
where should the site live? › ~/Documents/client/their-site
                             ~/Documents/client/their-site does not exist yet
                             enter or y → installs into …/their-site/wp
                             n → serve the folder as-is with an empty database
```

An empty or missing directory is offered a fresh install. A directory that already
has files skips that question — nothing is ever installed on top of your work — and
is attached instead: served as it is, with an empty database of its own. The prompt
names the directory that will be served (`app/public`, `wp`, `public`, `web`,
`htdocs` and the path itself are all understood) before you commit to anything.

Attaching never touches your files. An existing `wp-config.php` is kept as it is;
one is written only when WordPress core is sitting there with no config at all, and
deleting the site removes that config again. Both paths exist on every surface:

```sh
agent-local create mysite                        # into the shared sites directory
agent-local create mysite --dir ~/one-off/here   # fresh install, exact path
agent-local attach ~/Sites/existing-thing        # serve what is there + empty DB
```

One dot per row, green when that thing is serving and grey when it is parked —
the same lamp on Runtimes for which PHP is actually in use, and on Doctor for
each check. The header keeps one word for the whole stack: `● ready` when
everything answers, or the parts that don't (`● db, api down`) in red. Parked rows dim so a long list
reads at a glance. Keys are listed per tab at the bottom; `⇥` cycles tabs.

Rendering one frame without opening the UI (for design work, or to check a layout
in CI):

```sh
agent-local tui --frame 120 --tab doctor
```

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

A LocalWP site by name, a DDEV project by name, or any WordPress directory:

```sh
agent-local localwp-sites                    # LocalWP sites available
agent-local ddev-projects                    # DDEV projects available
agent-local import orleton-om                # LocalWP site → served locally
agent-local import ddevwp                    # DDEV project → moved out of DDEV
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

1. locate the source database (LocalWP registry socket/creds, DDEV's published
   port — starting the project first if it is stopped — `wp-config.php`, or your flags)
2. stream `dump → collation fixer → load` into the embedded MariaDB — flat memory
   for multi-GB dumps, and MySQL-8 `utf8mb4_0900_ai_ci` is rewritten to a MariaDB
   collation on the way through
3. rewrite `wp-config.php` `DB_*` — adding the defines and `$table_prefix` a
   DDEV config keeps in `wp-config-ddev.php` (original kept as
   `wp-config.php.agent-local.bak`) — plus any theme URL-override constant
   (e.g. `EFRONT_URL_OVERRIDE`)
4. `search-replace` every domain the database actually stores — including staging
   subdomains — to the new local domain, then flush and serve

In-place is the default, so a 7 GB checkout costs zero copy time. Deleting an
imported site never touches files outside `~/.agent-local`; it detaches the
config and restores the backup.

A DDEV source is moved out of DDEV once it serves here: `ddev delete` removes the
containers and the database volume, DDEV's own snapshot (kept under
`.ddev/db_snapshots/`) is the way back, and your files plus `.ddev/` are never
touched. `--keep-ddev` leaves the project registered instead — its wp-config now
points at agent-local, so restore the `.bak` (or `ddev snapshot restore`) to serve
it from DDEV again. `ddev-projects` works with Docker down too: names and roots
come from DDEV's registry, with the status saying why nothing else is shown.

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
agent-local install php 8.1        # also: mariadb | apache | wp-cli | brew
agent-local install php 7.4 --tap  # 7.4 and 8.0: see "end-of-life versions"
agent-local php mysite 8.1         # switch a site; its pool restarts live, installing 8.1 first if needed
agent-local php mysite 7.4 --tap   # a version Homebrew dropped: allow the versioned tap
agent-local php                    # installed, broken, installable
agent-local doctor                 # every discovered runtime
```

Installing a version takes about a minute (Homebrew keg + re-discovery) and needs
no shell work. Each site runs its own php-fpm pool, so versions never conflict —
one site on 7.4 next to one on 8.4 is normal. Versions are matched loosely, so
`8.2`, `8.2.28` and `php@8.2` all mean the same series.

**End-of-life versions.** homebrew-core deletes a PHP formula once the release is
end of life: 7.4 and 8.0 are already gone, and the rest follow in time. They live
on in the third-party `shivammathur/php` tap, which Homebrew will not load until
you trust it — trusting a tap lets its formulae run code on this machine, so that
stays your call. `--tap` (CLI) or `tap: true` (MCP) taps and trusts it for you;
without the flag you get the exact brew commands to run yourself.

**Broken kegs.** `brew autoremove` will happily take a library php still links
against, leaving a keg that is installed but dies on a missing dylib. Discovery
lists those separately instead of hiding them, and `agent-local install php 7.4`
repairs one by reinstalling the dependency that went missing — seconds, rather
than a rebuild. `agent-local doctor --fix` does the same unprompted.

## Databases

One embedded MariaDB on port 10360 backs every site, bound to `127.0.0.1` only.
Each site gets its own database (`al_<slug>`) and user. `root` is
password-protected — the password is generated on first use and kept in
`~/.agent-local/db-root-pass` (`0600`), so another process running as you cannot
read or drop your data just by connecting to the port. Commands below run as
root on your behalf, so there are no restrictions on what you can do.

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

### Snapshots

Save-points for the one database that exists nowhere else — taken
automatically before anything destructive, or by hand before you try
something:

```sh
agent-local db mysite snapshot pre-migration   # save (label optional)
agent-local db mysite snapshots                # list, newest first
agent-local db mysite restore                  # put the newest back
agent-local db mysite restore 20260901-105748-pre-migration
```

- **Destructive operations snapshot first.** `db import`, `db reset`,
  `restore` and `delete` each save the current contents before touching
  anything (`--no-snapshot` / `no_snapshot` opts out — and a snapshot that
  fails stops the operation, because a safety net that silently misses is
  worse than none). Automatic save-points are pruned to the newest five per
  site; named ones are kept until you delete the files.
- **A snapshot is a plain `.sql.gz`** under `~/.agent-local/snapshots/<slug>/`,
  loadable anywhere. Restore streams it back through the same collation-fixing
  loader as imports, so size does not matter.
- **The directory survives deleting the site.** The snapshot taken before
  `delete` is the way back: recreate the site and `db <slug> restore`, or
  `db <new-slug> import` the file into a different one.
- **Restores never rewrite URLs** — the dump came from this same site.

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

### Connect a coding-agent harness

```sh
agent-local connect                   # checklist: checked = registered after apply; uncheck to remove
agent-local connect --list            # status only: not installed / installed / configured / stale
agent-local connect --all             # register in every harness found installed
agent-local connect codex cursor      # register only the named ones
agent-local connect --remove codex    # unregister (also --remove --all)
agent-local connect --json            # machine-readable status
```

Detects Claude Code, Claude Desktop, Codex CLI, Gemini CLI, Oh My Pi, OpenCode,
pi (through `pi-mcp-adapter`), Cursor, Windsurf, VS Code, Qwen Code and Zed by
their config location or CLI binary, writes the `agent-local` MCP server entry
with the absolute path of this binary, and leaves anything already pointing at
the current binary untouched. A harness pointing at a different agent-local
binary shows as "stale" so a rebuild or reinstall doesn't leave two entries
fighting each other. Removal takes out only the `agent-local` entry; everything
else in the file stays as it was. Running harnesses need a restart to pick up
either change.

For a client `connect` doesn't know about, or to see the raw block:

```sh
agent-local mcp --config     # prints this, with the absolute path filled in
```

```json
{
  "mcpServers": {
    "agent-local": { "command": "agent-local", "args": ["mcp"] }
  }
}
```

### MCP (Claude, Codex, any MCP client)

`agent-local mcp` is a stdio server: a client launches it and speaks JSON-RPC over
its stdin and stdout. Run it by hand and it will sit there waiting for input —
correctly, but silently — so on a terminal it prints a note saying so rather than
looking hung. To exercise it without a client:

```sh
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | agent-local mcp
```

60 tools — everything the CLI can do, no shell required:

| Area | Tools |
|---|---|
| discovery | `status`, `list_sites`, `get_site`, `localwp_sites`, `ddev_projects`, `resolve_path`, `list_runtimes` |
| lifecycle | `create_site`, `attach_site`, `import_site`, `start_site`, `stop_site`, `restart_site`, `delete_site` |
| runtime | `switch_php`, `install_runtime`, `doctor`, `doctor_fix`, `get_http_front`, `set_http_front` |
| domains | `set_domain`, `get_domain_suffix`, `set_domain_suffix`, `add_hosts_entries`, `remove_hosts_entries`, `cert_status`, `cert_trust` |
| database | `db_creds`, `db_query`, `db_tables`, `db_import`, `db_export`, `db_reset`, `db_snapshot`, `db_snapshots`, `db_restore` |
| files & media | `get_media_fallback`, `set_media_fallback`, `get_sites_dir`, `set_sites_dir`, `yield_ports` |
| wordpress | `wp_cli`, `worktree_wp_cli`, `get_wp_debug`, `set_wp_debug`, `open_adminer` |
| mail | `list_mail`, `get_mail`, `clear_mail` |
| previews | `list_branches`, `add_worktree`, `list_worktrees`, `start_worktree`, `stop_worktree`, `remove_worktree` |
| jobs & sharing | `list_jobs`, `get_job`, `share_local_site`, `unshare_local_site`, `get_logs` |

Design notes that matter when driving this from an agent:

- **The daemon auto-starts** on the first tool call and stays up under either HTTP
  front, so switching fronts never costs you control of the API.
- **Everything is idempotent-friendly.** Starting a running site is a no-op;
  create/import block until the site actually answers.
- **No prompts, ever** — after `agent-local sudo`, root steps go through the
  allowlist. Not exposed as tools: `sudo` and `alias`, the two one-time installs
  that genuinely need a password.
- **Every response is `{"ok":true,"data":…}` or `{"ok":false,"error":…}`.**
- **`switch_php` gets the runtime it needs.** A version that is missing is
  installed, and a keg that is installed but broken is repaired, before the
  switch. That runs brew, so it comes back as a job: wait on the call, or pass
  `async` and poll `get_job` with the id. `install: false` refuses instead, and
  the error names the versions that are installed. PHP 7.4 and 8.0 need
  `tap: true` — see [PHP versions](#php-versions).
- **A failed call says what to do next.** Errors carry the fixing call, in both
  CLI and MCP form, rather than only the fact that something was missing.

### HTTP API

```sh
TOKEN=$(agent-local api-token)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:10809/status
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"demo","php_version":"8.2"}' http://127.0.0.1:10809/sites
```

| Group | Endpoints |
|---|---|
| sites | `GET /status`, `GET\|POST /sites`, `POST /attach`, `GET /sites/{slug}`, `DELETE /sites/{slug}[?files=keep&db=keep&snapshot=off]`, `POST /sites/{slug}/{start,stop,restart,php,domain,wp-cli}`, `GET\|POST /sites/{slug}/wp-debug`, `GET\|POST /sites/{slug}/media`, `GET /sites/{slug}/adminer` |
| import | `POST /import` |
| database | `POST /sites/{slug}/db`, `POST /sites/{slug}/db/query`, `POST /sites/{slug}/db/{import,export,reset,snapshot,restore}`, `GET /sites/{slug}/db/{tables,snapshots}`, `POST /db/query` |
| mail | `GET /sites/{slug}/mail`, `GET /sites/{slug}/mail/{id}`, `DELETE /sites/{slug}/mail` |
| share | `POST /sites/{slug}/share`, `GET /sites/{slug}/share`, `DELETE /sites/{slug}/share` |
| previews | `GET /sites/{slug}/branches`, `GET\|POST /sites/{slug}/worktrees`, `POST /sites/{slug}/worktrees/{id}/{start,stop,wp-cli}`, `DELETE /sites/{slug}/worktrees/{id}` |
| jobs | `GET /jobs`, `GET /jobs/{id}` — long-running calls (`?async=1` or `Prefer: respond-async`) return one of these |
| platform | `GET /runtimes`, `POST /install`, `GET\|POST /front`, `GET\|POST /suffix`, `GET\|POST /sites-dir`, `GET /doctor`, `POST /doctor/fix`, `GET /logs/{name}?lines=N`, `POST\|DELETE /hosts` |
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
agent-local                            open the dashboard
agent-local tui [--frame W] [--tab T]  print one frame (design/debug)
agent-local create NAME [--domain d] [--php v] [--repo url]
                       [--admin-user u] [--admin-pass p] [--admin-email e] [--title t]
agent-local attach DIR [--name n] [--domain d] [--php v]   serve a directory you already have
agent-local list | start SLUG | stop SLUG | restart SLUG | open SLUG [--db]
agent-local delete SLUG [--yes] [--keep-files] [--keep-db] [--no-snapshot]
agent-local import SOURCE [--name n] [--domain d] [--php v] [--copy]
                         [--sql file] [--serve-only] [--keep-ddev]
                         [--db-host h] [--db-port p] [--db-user u] [--db-pass p] [--db-name n]
agent-local localwp-sites              list importable LocalWP sites
agent-local ddev-projects              list importable DDEV projects
agent-local db SLUG [sql | tables | import FILE [--keep-urls] | export [FILE] | reset | gui]
agent-local db SLUG snapshot [NAME] | snapshots | restore [NAME] [--no-snapshot]
agent-local mail SLUG [ID] [--open] [--clear]  captured outgoing email
agent-local media SLUG [URL | --auto | --off]  send missing uploads to a production origin
agent-local wpdebug SLUG [on|off]      WP_DEBUG with the log in ~/.agent-local/logs
agent-local share SLUG [--minutes N | --forever] [--off]   public quick-tunnel URL
agent-local jobs | job ID              long-running create/import status
agent-local php SLUG VERSION [--tap]   switch PHP (live), installing it if needed
agent-local domain SLUG NAME           change a site's domain
agent-local suffix [.test]             show/set the default domain suffix
agent-local sites-dir [PATH]           where new sites are created
agent-local branches SLUG              git branches of the site's repo
agent-local worktree SLUG BRANCH [--remove]
agent-local worktrees SLUG
agent-local wp SLUG -- core version    wp-cli through the site's PHP
agent-local install brew|php V|mariadb|apache|wp-cli
agent-local front [router|apache]      show / switch HTTP front
agent-local sudo                       passwordless root allowlist (one-time)
agent-local alias [--off]              bare URLs on 127.0.0.2:80/443 (one-time)
agent-local yield [secs]               free :80/:443 briefly, then re-bind
agent-local autostart [--off]          start the daemon at login (on by default)
agent-local resolve [PATH]             which site owns a path (default: cwd)
agent-local cert DOMAIN [--trust]      TLS state for a domain
agent-local doctor [--fix]
agent-local logs NAME [lines]          mysql | apache | daemon | fpm-<slug> | wp-<slug>
agent-local daemon [--background]      router + agent API
agent-local restart-daemon             hand over to a freshly installed binary
agent-local update [--check]           install the latest release
agent-local autoupdate [on|off]        let the daemon install releases itself (off by default)
agent-local mcp [--config]             MCP server over stdio; --config prints the client block
agent-local connect [--list|--all|--remove|--json|--yes] [harness...]  register (or remove) the MCP server in a client
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
| Captured mail | `~/.agent-local/mail/<slug or worktree id>/` |
| DB snapshots (survive delete) | `~/.agent-local/snapshots/<slug>/` |
| State, API token, DB root password | `~/.agent-local/sites.json`, `~/.agent-local/token`, `~/.agent-local/db-root-pass` (all `0600` where secret) |

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
- **White screen or misbehaving plugin** → `agent-local wpdebug <slug> on`,
  reproduce, `agent-local logs wp-<slug>`.
- **"Where did the confirmation email go?"** → `agent-local mail <slug>` — every
  email the site sends is captured there, nothing is delivered.
- **Site redirects to its production or staging domain** → `agent-local doctor`
  names the cause as `url:<slug>`: a `WP_HOME`/`WP_SITEURL` pinned in
  `wp-config.php` (a production config that came in with the files beats the
  database), or stored URLs an import's search-replace never reached.
  `agent-local doctor --fix` repoints both at the site's domain.

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
