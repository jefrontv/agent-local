<?php
/**
 * Documentation content for the /docs section. Plain data: each page is
 * rendered by docs.php. Inline `code` and **strong** are the only markup.
 *
 * Kept in step with README.md; the CLI page mirrors `agent-local help`.
 */

return array(

	'index' => array(
		'title'  => 'Overview',
		'kicker' => 'Docs',
		'intro'  => 'agent-local is one Go binary that creates, serves and manages WordPress sites on macOS. Native processes on real files — no Docker, no prerequisites — and the same engine is exposed to you and to coding agents over MCP and HTTP.',
		'sections' => array(
			array( 'h', 'Quick start' ),
			array( 'pre', "# install (Homebrew)\nbrew install jefrontv/agent-local/agent-local\n\n# one-time root steps: hosts, certs, the bare-URL front daemon\nagent-local sudo\n\n# your first site, serving in about twenty seconds\nagent-local create mysite\nagent-local open mysite" ),
			array( 'p', 'A site gets its own database, its own php-fpm pool, a hosts entry and a TLS certificate the moment it is created. The admin credentials print at the end of `create` — no wizard, no email.' ),
			array( 'h', 'What it runs' ),
			array( 'ul', array(
				'**Apache + php-fpm** — one pool per site, each pinned to the PHP version the site asks for (7.4 – 8.5).',
				'**An embedded MariaDB** — one instance, one database per site, snapshots as plain `.sql.gz`.',
				'**A front daemon** — holds `127.0.0.2:80/443`, so sites answer on `https://name.test` with no port suffix and no warning page.',
				'**A dashboard, a CLI and an agent API** — the TUI is `agent-local` with no arguments; every CLI command is also an MCP tool.',
			) ),
			array( 'h', 'Requirements' ),
			array( 'ul', array(
				'macOS on Apple Silicon or Intel. That is the whole list.',
				'Homebrew, for installing PHP, MariaDB and Apache on first use — agent-local installs them for you.',
				'Docker is never required. Imports *remove* the need for it: LocalWP sites and DDEV projects move in.',
			) ),
			array( 'h', 'Where things live' ),
			array( 'table', array( 'Path', 'What' ), array(
				array( '`~/.agent-local/sites/SLUG/wp`', 'docroots of copied sites; imports served in place stay where they are' ),
				array( '`~/.agent-local/snapshots/SLUG/`', 'database restore points, gzipped SQL' ),
				array( '`~/.agent-local/dumps/`', '`db export` output' ),
				array( '`~/.agent-local/logs/`', 'daemon, Apache, php-fpm and WordPress debug logs' ),
				array( '`~/.agent-local/engines/`', 'the installed PHP runtimes, MariaDB and Apache' ),
				array( '`/etc/hosts`', 'one line per domain, pointing at `127.0.0.2`' ),
			) ),
			array( 'note', 'Everything survives a reboot: the daemon starts at login, and sites that were running come back on their own.' ),
		),
	),

	'sites' => array(
		'title'  => 'Sites',
		'kicker' => 'Docs',
		'intro'  => 'Create a site from nothing, attach a directory you already have, or import one from LocalWP or DDEV. Every site is a slug, a domain, a database and a PHP pool.',
		'sections' => array(
			array( 'h', 'Create' ),
			array( 'pre', "agent-local create NAME [--domain d] [--php v] [--repo url]\n\n# a WordPress 8.4 site on a custom domain\nagent-local create mysite --domain mysite.example --php 8.4\n\n# create from a fresh git repo, ready for a theme checkout\nagent-local create mysite --repo git@github.com:me/mytheme.git" ),
			array( 'p', 'Downloads WordPress, provisions the database, writes `wp-config.php`, registers the hosts entry, issues and trusts the certificate, installs wp-cli caches. Prints the URL and admin credentials.' ),
			array( 'h', 'Attach' ),
			array( 'pre', "agent-local attach ~/Sites/existing --name existing" ),
			array( 'p', 'Serves a directory that is already a WordPress checkout. Your files are never touched: the existing `wp-config.php` is kept, and the site gets its own empty database pointed at by a small drop-in.' ),
			array( 'h', 'Lifecycle' ),
			array( 'table', array( 'Command', 'What it does' ), array(
				array( '`agent-local list`', 'every site, its PHP version, state and URL' ),
				array( '`agent-local open SLUG`', 'the site (or `open admin SLUG` — wp-admin)' ),
				array( '`agent-local start · stop · restart SLUG`', 'control one site; starting a running site is a no-op' ),
				array( '`agent-local delete SLUG --yes`', 'remove it — a database snapshot is saved first; `--keep-files` / `--keep-db` opt out of either' ),
				array( '`agent-local resolve [PATH]`', 'which site owns a path (default: the working directory)' ),
			) ),
			array( 'h', 'PHP versions' ),
			array( 'pre', "agent-local php SLUG 8.3        # live switch, pool restarted\nagent-local install php 8.2     # or install a runtime by hand" ),
			array( 'p', 'Each site runs its own pool, so versions are per-site and switching is live. Installing a version agent-local does not have yet reaches Homebrew for it — including releases Homebrew has since dropped, via a versioned tap.' ),
			array( 'note', 'Deleting an imported site never touches files outside `~/.agent-local`: in-place imports are detached and the original `wp-config.php` restored from the `.bak`.' ),
		),
	),

	'import' => array(
		'title'  => 'Importing',
		'kicker' => 'Docs',
		'intro'  => 'Bring a site in from anywhere: a LocalWP site by name, a DDEV project by name, or any WordPress directory. The database streams in, every stored domain is rewritten, and the site serves from where its files already are.',
		'sections' => array(
			array( 'h', 'What can be imported' ),
			array( 'pre', "agent-local localwp-sites      # LocalWP sites available\nagent-local ddev-projects      # DDEV projects available\nagent-local import SOURCE      # name or docroot path" ),
			array( 'p', 'A stopped LocalWP site or DDEV project is started first so its database can be read — LocalWP through its own control API, DDEV through `ddev start`. With Docker down, `ddev-projects` still lists names and roots from DDEV\'s registry.' ),
			array( 'h', 'Where the data comes from' ),
			array( 'pre', "# 1. Live DB, credentials read from the site's own wp-config.php (default)\nagent-local import /path/to/dir\n\n# 2. Explicit source DB\nagent-local import /path/to/dir --db-host 127.0.0.1 --db-port 8889 \\\n  --db-user root --db-pass secret --db-name mydb\n\n# 3. From a dump instead of a live server\nagent-local import /path/to/dir --sql ~/Downloads/site.sql\n\n# 4. Leave the database alone entirely and just serve the files\nagent-local import /path/to/dir --serve-only" ),
			array( 'h', 'The pipeline' ),
			array( 'ol', array(
				'locate the source database (registry socket, DDEV\'s published port, `wp-config.php`, or your flags)',
				'stream `dump → collation fixer → load` — flat memory for multi-GB dumps; MySQL-8 collations are rewritten to MariaDB ones on the way through',
				'point `wp-config.php` at the new database, adding any defines it was missing (original kept as `wp-config.php.agent-local.bak`)',
				'`search-replace` every domain the database actually stores — staging subdomains included — then flush and serve',
			) ),
			array( 'h', 'DDEV projects' ),
			array( 'p', 'A DDEV source is **moved out of DDEV** once it serves here: `ddev delete` removes the containers and the database volume, DDEV\'s own snapshot (kept in `.ddev/db_snapshots/`) is the way back, and your files plus `.ddev/` are never touched.' ),
			array( 'pre', "agent-local import ddevsite              # move it out of DDEV (default)\nagent-local import ddevsite --keep-ddev  # leave it registered in DDEV" ),
			array( 'note', '`--keep-ddev` leaves the project in place, but one docroot can point at only one database: its wp-config now points here, so restore the `.bak` (or `ddev snapshot restore`) to serve it from DDEV again.' ),
			array( 'h', 'Media fallback' ),
			array( 'p', 'Imports do not copy uploads. Point missing uploads at the production origin and any missing file redirects there: `agent-local media SLUG --auto` adopts the rule already in the site\'s `.htaccess`.' ),
		),
	),

	'domains' => array(
		'title'  => 'Domains & HTTPS',
		'kicker' => 'Docs',
		'intro'  => 'Any domain works — `.test` is only the default. Every domain gets a hosts entry, a locally issued TLS certificate trusted in your keychain, and an answer on bare `https://name.test` with no port suffix.',
		'sections' => array(
			array( 'h', 'Domains' ),
			array( 'pre', "agent-local domain SLUG shop.example   # hosts entry and cert follow\nagent-local suffix [.test]             # show or set the default suffix" ),
			array( 'h', 'Certificates' ),
			array( 'p', 'A certificate is issued the moment a domain is created or renamed, and trusted in the keychain automatically after the one-time `agent-local sudo`. Check on one with `agent-local cert DOMAIN [--trust]`.' ),
			array( 'h', 'Bare URLs and port 80' ),
			array( 'p', 'Sites answer on `:80`/`:443` through a front daemon bound to the loopback alias `127.0.0.2` — a specific address, which the kernel prefers over another app\'s wildcard bind. If something else must have the ports, hand them over briefly:' ),
			array( 'pre', "agent-local yield 60    # free :80/:443 for a minute; sites stay reachable on :1080" ),
			array( 'note', 'Sharing the machine with LocalWP: both can serve at once, because they bind different addresses. The one trap is LocalWP pre-checking port 80 and refusing to start — `yield`, start the site in Local, and both run side by side.' ),
		),
	),

	'databases' => array(
		'title'  => 'Databases & snapshots',
		'kicker' => 'Docs',
		'intro'  => 'One embedded MariaDB, one database per site. SQL from the command line, dumps in and out, and a restore point saved before anything destructive.',
		'sections' => array(
			array( 'h', 'The db command' ),
			array( 'pre', "agent-local db SLUG                            # connection details\nagent-local db SLUG \"SELECT * FROM wp_users\"   # run a statement\nagent-local db SLUG tables                     # list tables\nagent-local db SLUG gui                        # open Adminer\nagent-local db SLUG import dump.sql.gz         # replace contents; snapshot first\nagent-local db SLUG export [FILE]              # dump to a file\nagent-local db SLUG reset                      # empty it (grants kept)" ),
			array( 'p', 'Imports and restores stream through the same collation-fixing loader as site imports, so dump size does not matter. URLs are rewritten on import unless `--keep-urls` says otherwise.' ),
			array( 'h', 'Snapshots' ),
			array( 'pre', "agent-local db SLUG snapshot [NAME]    # save a restore point\nagent-local db SLUG snapshots          # list them\nagent-local db SLUG restore [NAME]     # default: the newest" ),
			array( 'ul', array(
				'**Automatic before every destructive operation** — `db import`, `db reset`, `restore` and `delete` each save the current contents first. A snapshot that fails stops the operation.',
				'**Plain `.sql.gz`** — loadable anywhere, not a private format.',
				'**The pre-delete snapshot survives the site** — recreate and restore, or import it into a different site.',
			) ),
		),
	),

	'mail' => array(
		'title'  => 'Captured mail',
		'kicker' => 'Docs',
		'intro'  => 'Every email a site sends lands in a per-site local inbox instead of vanishing: password resets, form notifications, WooCommerce receipts. No SMTP configuration, nothing leaves the machine.',
		'sections' => array(
			array( 'h', 'Reading the inbox' ),
			array( 'pre', "agent-local mail SLUG              # newest first\nagent-local mail SLUG ID           # one message, full text and headers\nagent-local mail SLUG --open       # the inbox in your browser\nagent-local mail SLUG --clear" ),
			array( 'p', 'The web inbox lives at `https://SLUG.test/_mail`. Bodies render as sent — HTML in a sandboxed frame, plain text as text — with attachments and headers one click away. The newest 275 messages are kept; branch previews have their own inboxes.' ),
			array( 'h', 'For agents' ),
			array( 'p', '`list_mail`, `get_mail` and `clear_mail` are MCP tools. A complete end-to-end check with no human mailbox involved: drive the site with a browser tool, submit the form, then assert on the mail that came out.' ),
			array( 'note', 'Mail is captured by routing PHP\'s `mail()` into the inbox — plugins that bypass `mail()` for a real SMTP connection are not intercepted, and deliver as configured.' ),
		),
	),

	'previews' => array(
		'title'  => 'Branch previews',
		'kicker' => 'Docs',
		'intro'  => 'Any git branch of a site\'s repo can serve on its own URL — a full worktree beside the site, same database, without touching the checkout you are working in.',
		'sections' => array(
			array( 'h', 'Using previews' ),
			array( 'pre', "agent-local branches SLUG            # branches of the site's repo\nagent-local worktree SLUG BRANCH     # serve one → https://BRANCH.SLUG.test\nagent-local worktrees SLUG           # list previews\nagent-local worktree SLUG BRANCH --remove" ),
			array( 'p', 'The repo is found automatically: the site\'s work dir, or the docroot itself (LocalWP-style checkouts keep `.git` in `app/public`). Nothing is copied — the preview is a real git worktree, so it is cheap.' ),
			array( 'note', 'A preview shares the site\'s database. A branch with migrations runs them against real data; remove the preview when you are done and the branch itself is untouched.' ),
		),
	),

	'debugging' => array(
		'title'  => 'Debugging & health',
		'kicker' => 'Docs',
		'intro'  => 'WP_DEBUG with the ritual removed, every log in one place, and a doctor that names the exact command that fixes what it finds.',
		'sections' => array(
			array( 'h', 'WP_DEBUG' ),
			array( 'pre', "agent-local wpdebug SLUG on\n# log → ~/.agent-local/logs/wp-SLUG.log, display kept off\nagent-local logs wp-SLUG 40" ),
			array( 'h', 'Logs' ),
			array( 'pre', "agent-local logs mysql | apache | daemon | fpm-SLUG | wp-SLUG [LINES]" ),
			array( 'h', 'Doctor' ),
			array( 'p', '`agent-local doctor` checks the whole stack: runtimes, database, HTTP front, the loopback alias, DNS, certificates, orphans. Every finding reports the exact command that repairs it, and `doctor --fix` applies every fix — safe ones directly, privileged ones through the sudo allowlist.' ),
			array( 'table', array( 'Symptom', 'Usual cause and fix' ), array(
				array( 'site white-screens after `brew upgrade`', 'Homebrew unlinked the PHP keg under a running pool — `doctor --fix` relinks and restarts' ),
				array( 'imported site redirects somewhere else', 'its database still holds the old domain — `wp search-replace old.host SLUG.test --all-tables`' ),
				array( 'bare URLs dead, `:1080` in every printed URL', 'the front daemon or its alias is missing — `doctor` names it, `--fix` repairs' ),
				array( 'LocalWP will not start', 'it pre-checks port 80 — `agent-local yield 60`, then start it' ),
			) ),
		),
	),

	'agents' => array(
		'title'  => 'Agents',
		'kicker' => 'Docs',
		'intro'  => 'The whole engine is an API. Sixty MCP tools over stdio, the same surface over HTTP, no prompts ever — the same calls you type are the calls an agent makes.',
		'sections' => array(
			array( 'h', 'Connect a harness' ),
			array( 'pre', "agent-local connect                  # Claude Code, Codex, Cursor, Gemini CLI, …\nagent-local connect --list           # what is registered\nagent-local connect --remove codex\nagent-local mcp --config             # the config block, for any other client" ),
			array( 'p', 'Registration is one JSON edit in the client\'s config: the absolute path to this binary, run with `mcp`. Long-running work (create, import, db import) returns a job id; `jobs` and `job ID` follow progress.' ),
			array( 'h', 'Tool groups' ),
			array( 'table', array( 'Area', 'Tools' ), array(
				array( 'discovery', '`status`, `list_sites`, `get_site`, `localwp_sites`, `ddev_projects`, `resolve_path`, `list_runtimes`' ),
				array( 'lifecycle', '`create_site`, `attach_site`, `import_site`, `start_site`, `stop_site`, `restart_site`, `delete_site`' ),
				array( 'runtime', '`switch_php`, `install_runtime`, `doctor`, `doctor_fix`, `get_http_front`, `set_http_front`' ),
				array( 'domains', '`set_domain`, `get_domain_suffix`, `set_domain_suffix`, `add_hosts_entries`, `remove_hosts_entries`, `cert_status`, `cert_trust`' ),
				array( 'database', '`db_creds`, `db_query`, `db_tables`, `db_import`, `db_export`, `db_reset`, `db_snapshot`, `db_snapshots`, `db_restore`' ),
				array( 'files & media', '`get_media_fallback`, `set_media_fallback`, `get_sites_dir`, `set_sites_dir`, `yield_ports`' ),
				array( 'wordpress', '`wp_cli`, `worktree_wp_cli`, `get_wp_debug`, `set_wp_debug`, `open_adminer`' ),
				array( 'mail', '`list_mail`, `get_mail`, `clear_mail`' ),
				array( 'previews', '`list_branches`, `add_worktree`, `list_worktrees`, `start_worktree`, `stop_worktree`, `remove_worktree`' ),
				array( 'jobs & sharing', '`list_jobs`, `get_job`, `share_local_site`, `unshare_local_site`, `get_logs`' ),
			) ),
			array( 'h', 'HTTP API' ),
			array( 'p', 'Every tool is also an endpoint on `127.0.0.1:8090`, bearer-token authenticated (`agent-local api-token`). The shape mirrors the CLI: `GET /status`, `GET|POST /sites`, `POST /import`, `POST /sites/{slug}/db/query`, `GET /sites/{slug}/mail` and so on.' ),
			array( 'note', 'Root steps (hosts, certificates, ports) go through a passwordless sudo allowlist after the one-time `agent-local sudo`. Not exposed as tools: `sudo` and `alias` — the two one-time installs that genuinely need a password.' ),
		),
	),

	'cli' => array(
		'title'  => 'CLI reference',
		'kicker' => 'Docs',
		'intro'  => 'Every command, as `agent-local help` prints it. Each command is also an MCP tool with the same name.',
		'sections' => array(
			array( 'h', 'Sites' ),
			array( 'pre', "agent-local                                         open the dashboard\ncreate NAME [--domain d] [--php v] [--repo url]     create and install a WordPress site\nattach DIR [--name n] [--domain d] [--php v]        serve a directory you already have, with an empty database\nimport SOURCE [--copy] [--sql FILE] [--serve-only] [--keep-ddev]\n                                                    import a LocalWP site, DDEV project or docroot\nlocalwp-sites                                       LocalWP sites available to import\nddev-projects                                       DDEV projects available to import\nlist                                                every site, its state and URL\nstart | stop | restart SLUG                         control one site\ndelete SLUG [--yes] [--keep-files] [--keep-db]      remove a site; a snapshot is saved first\nopen SLUG                                           open the site in your browser\ndomain SLUG NAME                                    change a site's domain; hosts entry and cert follow\nphp SLUG VERSION [--tap]                            switch PHP version, installing it if needed\nresolve [PATH]                                      which site owns a path (default: cwd)" ),
			array( 'h', 'Database' ),
			array( 'pre', "db SLUG                                             connection details\ndb SLUG \"SQL\"                                       run a statement\ndb SLUG import FILE.sql[.gz] [--keep-urls]          load a dump; URLs rewritten, snapshot saved first\ndb SLUG export [FILE]                               dump to a file\ndb SLUG reset | tables | gui                        empty it, list tables, or open Adminer\ndb SLUG snapshot [NAME]                             save a restore point\ndb SLUG snapshots                                   list restore points\ndb SLUG restore [NAME]                              restore one (default: newest)" ),
			array( 'h', 'Develop' ),
			array( 'pre', "worktree SLUG BRANCH [--remove]                     serve a git branch on its own URL\nworktrees SLUG                                      list branch previews\nbranches SLUG                                       branches of the site's repo\nwp SLUG -- ARGS                                     run wp-cli against the site\nwpdebug SLUG [on|off]                               WP_DEBUG, logged to ~/.agent-local/logs/wp-SLUG.log\nlogs NAME [LINES]                                   tail a log: mysql, apache, daemon, fpm-SLUG, wp-SLUG\nmail SLUG [ID] [--open] [--clear]                   emails the site has sent\nmedia SLUG [URL | --auto | --off]                   send missing uploads to a production origin\nshare SLUG [--minutes N] [--off]                    public URL through a Cloudflare tunnel\ncert DOMAIN [--trust]                               TLS state for a domain; --trust issues and trusts it" ),
			array( 'h', 'Agents' ),
			array( 'pre', "connect [--list | --all | --remove] [HARNESS...]    register the MCP server in Claude Code, Codex, Cursor and friends\nmcp                                                 the MCP server itself (stdio); clients launch this\nmcp --config                                        the client config block, for a client connect doesn't know\napi-token                                           bearer token for the HTTP API\njobs                                                recent long-running jobs\njob ID                                              one job's progress" ),
			array( 'h', 'Machine' ),
			array( 'pre', "doctor [--fix]                                      health checks; --fix applies every repair\ninstall brew | php VERSION | mariadb | apache       install a dependency (wp-cli too)\nfront [router | apache]                             show or switch the HTTP front\nyield [SECONDS]                                     free :80/:443 briefly so another app can start\nautostart [--off]                                   start the daemon at login (on by default)\nsites-dir [PATH]                                    where new sites are created\nsuffix [.test]                                      default domain suffix\ndaemon [--background]                               run the daemon by hand\nrestart-daemon                                      hand over to a freshly installed binary\nupdate [--check]                                    install the latest release\nversion                                             what build this is" ),
		),
	),
);
