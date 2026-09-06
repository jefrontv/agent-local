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
		'intro'  => 'agent-local is one Go binary that creates, serves and manages local PHP sites on macOS — WordPress end to end, and any other PHP app (Joomla, Laravel, Drupal, plain files) served with its own domain, certificate and database. Native processes on real files, no Docker, no prerequisites, and the same engine is exposed to you and to coding agents over MCP and HTTP.',
		'sections' => array(
			array( 'h', 'Quick start' ),
			array( 'pre', "# install (Homebrew)\nbrew install jefrontv/agent-local/agent-local\n\n# one-time root steps: hosts, certs, the bare-URL front daemon\nagent-local sudo\n\n# your first site, serving in about twenty seconds\nagent-local create mysite\nagent-local open mysite" ),
			array( 'p', 'A site gets its own database, its own php-fpm pool, a hosts entry and a TLS certificate the moment it is created. The admin credentials print at the end of `create` — no wizard, no email.' ),
			array( 'h', 'What it runs' ),
			array( 'ul', array(
				'**A Go router + php-fpm** — one pool per site, each pinned to the PHP version the site asks for (7.4 – 8.5). Static files stream from disk; PHP goes over FastCGI. Apache is available as an alternative front when a site needs its `.htaccess` honoured.',
				'**An embedded MariaDB** — one instance, one database per site, snapshots as plain `.sql.gz`.',
				'**A front daemon** — holds `127.0.0.2:80/443`, so sites answer on `https://name.test` with no port suffix and no warning page.',
				'**A dashboard, a CLI and an agent API** — the TUI is `agent-local` with no arguments; every site-facing command is also an MCP tool.',
				'**A tools page on every site** — `https://name.test/.agent-local` links to that site\'s database GUI and captured-mail inbox. Local only; a share link never exposes it.',
			) ),
			array( 'h', 'Requirements' ),
			array( 'ul', array(
				'macOS on Apple Silicon or Intel. That is the whole list.',
				'Homebrew, for installing PHP, MariaDB and Apache on first use — agent-local installs them for you.',
				'Docker is never required. Imports *remove* the need for it: LocalWP sites and DDEV projects move in.',
			) ),
			array( 'h', 'Where things live' ),
			array( 'table', array( 'Path', 'What' ), array(
				array( '`~/.agent-local/sites/SLUG/wp`', 'docroots of copied sites; imports and attached directories stay where they are' ),
				array( '`~/.agent-local/snapshots/SLUG/`', 'database restore points, gzipped SQL' ),
				array( '`~/.agent-local/checkpoints/SLUG/`', 'checkpoints: a database snapshot plus a copy-on-write clone of the files' ),
				array( '`~/.agent-local/dumps/`', '`db export` output' ),
				array( '`~/.agent-local/logs/`', 'daemon, Apache, php-fpm and WordPress debug logs' ),
				array( '`~/.agent-local/engines/`', 'the installed PHP runtimes, MariaDB and Apache' ),
				array( '`/etc/hosts`', 'one line per domain, pointing at `127.0.0.2`' ),
			) ),
			array( 'note', 'Everything survives a reboot: the daemon starts at login through launchd, brings the database up, and restores every site and preview that was running.' ),
		),
	),

	'sites' => array(
		'title'  => 'Sites',
		'kicker' => 'Docs',
		'intro'  => 'Create a WordPress site from nothing, attach any PHP directory you already have, or import a WordPress site from LocalWP or DDEV. Every site is a slug, a domain, a database and a PHP pool.',
		'sections' => array(
			array( 'h', 'Create' ),
			array( 'pre', "agent-local create NAME [--domain d] [--php v] [--repo url]\n\n# a WordPress 8.4 site on a custom domain\nagent-local create mysite --domain mysite.example --php 8.4\n\n# create from a fresh git repo, ready for a theme checkout\nagent-local create mysite --repo git@github.com:me/mytheme.git" ),
			array( 'p', 'Downloads WordPress, provisions the database, writes `wp-config.php`, registers the hosts entry, issues and trusts the certificate, installs wp-cli caches. Prints the URL and admin credentials.' ),
			array( 'h', 'Attach' ),
			array( 'pre', "agent-local attach ~/Sites/existing --name existing\n\n# a Laravel checkout: public/ is found and served\nagent-local attach ~/Sites/shop --domain shop.test" ),
			array( 'p', 'Serves a directory that already exists. Your files are never touched, and the site gets its own domain, certificate and empty database. The app is detected from what is in the docroot and shown in the site record as its kind:' ),
			array( 'table', array( 'Kind', 'Detected by', 'What attach does' ), array(
				array( 'WordPress', '`wp-load.php`', 'keeps an existing `wp-config.php`; writes one only when core is there with none' ),
				array( 'Joomla', '`administrator/` + `configuration.php`', 'prints the database credentials to paste into `configuration.php`' ),
				array( 'Laravel', '`artisan`', 'serves `public/`; prints credentials for `.env`' ),
				array( 'Drupal', '`core/lib/Drupal.php`', 'serves the docroot; prints credentials for `settings.php`' ),
				array( 'PHP', 'an `index.php`, nothing else recognised', 'serves it front-controller style: real files first, then `index.php`' ),
				array( 'empty', 'nothing yet', 'registers the site; re-detected the moment files arrive' ),
			) ),
			array( 'p', 'The serving layer is the same for every kind. What differs is the toolkit: `wp`, `wpdebug`, `wpconst`, `login`, `wp_info` and the URL-pin checks in `doctor` are WordPress-only and say so for another kind rather than failing on a missing `wp-config.php`. `checkpoint` on a non-WordPress site saves the whole docroot rather than `wp-content`. The media fallback watches the app\'s own uploads path — `/images/` for Joomla, `/sites/default/files/` for Drupal.' ),
			array( 'note', '`create` and `import` are WordPress-only. For another app, attach the directory and load its database with `agent-local db SLUG import dump.sql` — the WordPress URL rewrite is skipped for other kinds, so the dump lands as-is.' ),
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
			array( 'p', 'A certificate is issued the moment a domain is created or renamed, and trusted in the keychain automatically after the one-time `agent-local sudo`. That trust is scoped: the allowlist permits trusting one fixed, root-owned staging path and nothing else, so no other process can use it to trust an arbitrary certificate. Check on one with `agent-local cert DOMAIN [--trust]`; `--trust` shows the password dialog when the allowlist is not installed.' ),
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
			array( 'pre', "agent-local db SLUG                            # connection details\nagent-local db SLUG \"SELECT * FROM wp_users\"   # run a statement\nagent-local db SLUG tables                     # list tables\nagent-local db SLUG gui                        # open Adminer\nagent-local db SLUG search efront.dev          # where a string still lives: table, column, count\nagent-local db SLUG search-replace old new --apply   # dry run without --apply\nagent-local db SLUG import dump.sql.gz         # replace contents; snapshot first\nagent-local db SLUG export [FILE]              # dump to a file\nagent-local db SLUG reset                      # empty it (grants kept)" ),
			array( 'p', 'Imports and restores stream through the same collation-fixing loader as site imports, so dump size does not matter. On a WordPress site URLs are rewritten on import unless `--keep-urls` says otherwise; other kinds load as-is. Adminer is one click away at `https://SLUG.test/.agent-local` — the tools page every site carries, local only.' ),
			array( 'h', 'Snapshots' ),
			array( 'pre', "agent-local db SLUG snapshot [NAME]    # save a restore point\nagent-local db SLUG snapshots          # list them\nagent-local db SLUG restore [NAME]     # default: the newest" ),
			array( 'ul', array(
				'**Automatic before every destructive operation** — `db import`, `db reset`, `restore` and `delete` each save the current contents first. A snapshot that fails stops the operation.',
				'**Written atomically** — a snapshot is streamed to a temp file and renamed into place, so a crash mid-write never leaves a half-file the list would call healthy.',
				'**A failed restore rolls itself back** — if the dump being restored will not load, the snapshot taken moments before is put back automatically. A bad file cannot leave you with an empty database.',
				'**Plain `.sql.gz`** — loadable anywhere, not a private format.',
				'**The pre-delete snapshot survives the site** — recreate and restore, or import it into a different site.',
			) ),
			array( 'h', 'Checkpoints' ),
			array( 'pre', "agent-local checkpoint SLUG before-updates     # database snapshot + copy-on-write clone of wp-content\nagent-local checkpoint SLUG --scope all         # …or the whole docroot\nagent-local checkpoint SLUG --list\nagent-local rollback SLUG 20260904-183000-before-updates" ),
			array( 'p', 'A checkpoint is files and database together under one name — the save-point to take before a risky plugin update or migration. On APFS the file copy is a clone: seconds and near-zero space regardless of size. A rollback restores both and restarts the pool; what was there is kept beside the checkpoint as `pre-rollback-<time>`, so a rollback is itself undoable. Non-WordPress sites checkpoint the whole docroot.' ),
		),
	),

	'mail' => array(
		'title'  => 'Captured mail',
		'kicker' => 'Docs',
		'intro'  => 'Every email a site sends lands in a per-site local inbox instead of vanishing: password resets, form notifications, WooCommerce receipts. No SMTP configuration, nothing leaves the machine.',
		'sections' => array(
			array( 'h', 'Reading the inbox' ),
			array( 'pre', "agent-local mail SLUG              # newest first\nagent-local mail SLUG ID           # one message, full text and headers\nagent-local mail SLUG --open       # the inbox in your browser\nagent-local mail SLUG --clear" ),
			array( 'p', 'The web inbox lives at `https://SLUG.test/.agent-local/mail`, one click from the site\'s tools page at `/.agent-local`. Bodies render as sent — HTML in a sandboxed frame, plain text as text — with attachments and headers one click away. The newest 200 messages are kept; branch previews have their own inboxes.' ),
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
		'intro'  => 'One call that says what is wrong with a site, WP_DEBUG with the ritual removed, every log in one place, and a doctor that names the exact command that fixes what it finds.',
		'sections' => array(
			array( 'h', 'When a site "doesn\'t work"' ),
			array( 'pre', "agent-local probe SLUG         # requests /, wp-login, wp-admin, wp-json and an asset through the real stack\nagent-local errors SLUG --since 1h   # deduplicated PHP errors: level, message, file:line, count\nagent-local wpinfo SLUG        # version, URLs vs served domain, plugins, theme, debug state\nagent-local login SLUG         # a one-time URL straight into wp-admin, no password" ),
			array( 'p', '`probe` returns each status, redirect target, timing, body size and the PHP errors logged during that request, then gives one verdict: down, fatal, redirecting off-site, blank, slow, or healthy. It is the first thing to run — and the first thing an agent runs, as `probe_site`.' ),
			array( 'h', 'WP_DEBUG' ),
			array( 'pre', "agent-local wpdebug SLUG on\n# log → ~/.agent-local/logs/wp-SLUG.log, display kept off\nagent-local logs wp-SLUG 40\nagent-local wpconst SLUG SCRIPT_DEBUG true   # any wp-config constant; --remove drops one" ),
			array( 'h', 'Logs' ),
			array( 'pre', "agent-local logs mysql | apache | daemon | fpm-SLUG | wp-SLUG [LINES]" ),
			array( 'h', 'Doctor' ),
			array( 'p', '`agent-local doctor` checks the whole stack: runtimes, database, HTTP front, the loopback alias, the sudo allowlist, DNS, certificates, orphans, and every site — stale drop-ins, URLs pinned to another host, recent fatals. Every finding reports the exact command that repairs it, and `doctor --fix` applies every fix — safe ones directly, privileged ones through the sudo allowlist.' ),
			array( 'table', array( 'Symptom', 'Usual cause and fix' ), array(
				array( 'setup stops with "needs root"', 'the sudo allowlist is missing, or predates this release — `agent-local sudo` once, one password dialog' ),
				array( 'after an update, static files 403 or plugins fail to load', 'the daemon was started without macOS folder access — `agent-local restart-daemon` hands it to launchd, which has it (fixed for good in 0.22.3)' ),
				array( '`db down` after a reboot', 'the database was not restored at boot — fixed in 0.27.1; `agent-local restart-daemon` on older builds' ),
				array( 'site white-screens after `brew upgrade`', 'Homebrew unlinked the PHP keg under a running pool — `doctor --fix` relinks and restarts' ),
				array( 'imported site redirects somewhere else', 'a `WP_HOME` pin or stored URL still names the old host — `doctor --fix` repoints both' ),
				array( 'imported site renders blank, nothing in any log', 'a drop-in (`advanced-cache.php`, `object-cache.php`) embeds the origin server\'s path — `doctor` names the plugin; `--fix` regenerates WP Rocket\'s' ),
				array( 'bare URLs dead, `:1080` in every printed URL', 'the front daemon or its alias is missing — `doctor` names it, `--fix` repairs' ),
				array( 'LocalWP will not start', 'it pre-checks port 80 — `agent-local yield 60`, then start it' ),
			) ),
			array( 'note', 'Every daemon restart is a hand-off: the new build takes the ports only once the old one has fully exited, and a daemon that cannot bind or answer its own API exits so launchd can try again rather than sitting there looking healthy.' ),
		),
	),

	'agents' => array(
		'title'  => 'Agents',
		'kicker' => 'Docs',
		'intro'  => 'The whole engine is an API. Seventy-three MCP tools over stdio, the same surface over HTTP, no prompts ever — the same calls you type are the calls an agent makes.',
		'sections' => array(
			array( 'h', 'Connect a harness' ),
			array( 'pre', "agent-local connect                  # Claude Code, Codex, Cursor, Gemini CLI, …\nagent-local connect --list           # what is registered\nagent-local connect --remove codex\nagent-local mcp --config             # the config block, for any other client" ),
			array( 'p', 'Registration is one JSON edit in the client\'s config: the absolute path to this binary, run with `mcp`. Long-running work (create, import, db import) returns a job id; `jobs` and `job ID` follow progress. Tool calls run concurrently, so a slow import never blocks the `get_job` polls that watch it; quick reads time out in 20 seconds rather than hanging when the daemon is wedged.' ),
			array( 'h', 'Tool groups' ),
			array( 'table', array( 'Area', 'Tools' ), array(
				array( 'discovery', '`status`, `list_sites`, `get_site`, `localwp_sites`, `ddev_projects`, `resolve_path`, `list_runtimes`' ),
				array( 'lifecycle', '`create_site`, `attach_site`, `import_site`, `start_site`, `stop_site`, `restart_site`, `delete_site`' ),
				array( 'diagnose', '`probe_site`, `http_request`, `get_errors`, `wp_info`, `get_logs`, `doctor`, `doctor_fix`' ),
				array( 'fix & undo', '`checkpoint`, `list_checkpoints`, `rollback`, `delete_checkpoint`, `db_search`, `search_replace`, `magic_login`' ),
				array( 'runtime', '`switch_php`, `install_runtime`, `get_http_front`, `set_http_front`' ),
				array( 'domains', '`set_domain`, `get_domain_suffix`, `set_domain_suffix`, `add_hosts_entries`, `remove_hosts_entries`, `cert_status`, `cert_trust`' ),
				array( 'database', '`db_creds`, `db_query`, `db_tables`, `db_import`, `db_export`, `db_reset`, `db_snapshot`, `db_snapshots`, `db_restore`, `open_adminer`' ),
				array( 'files & media', '`get_media_fallback`, `set_media_fallback`, `get_sites_dir`, `set_sites_dir`, `yield_ports`' ),
				array( 'wordpress', '`wp_cli`, `worktree_wp_cli`, `get_wp_debug`, `set_wp_debug`, `get_wp_constants`, `set_wp_constant` — answer `409` for a site that is not WordPress, naming its kind' ),
				array( 'mail', '`list_mail`, `get_mail`, `clear_mail`' ),
				array( 'previews', '`list_branches`, `add_worktree`, `list_worktrees`, `start_worktree`, `stop_worktree`, `remove_worktree`' ),
				array( 'jobs & sharing', '`list_jobs`, `get_job`, `share_local_site`, `unshare_local_site`' ),
			) ),
			array( 'h', 'HTTP API' ),
			array( 'p', 'Every tool is also an endpoint on `127.0.0.1:10809`, bearer-token authenticated (`agent-local api-token`). The shape mirrors the CLI: `GET /status`, `GET|POST /sites`, `POST /import`, `POST /sites/{slug}/probe`, `POST /sites/{slug}/db/query`, `GET /sites/{slug}/mail` and so on. Long-running calls return a job with `?async=1`; `GET /jobs/{id}` follows it. A missing required argument comes back as a protocol error naming it, never a confusing 404.' ),
			array( 'note', 'Root steps (hosts, certificates, ports) go through a scoped passwordless sudo allowlist after the one-time `agent-local sudo` — exact commands, and cert trust on one fixed path. Not exposed as tools: `sudo`, `alias`, `connect`, `update` and the daemon controls — one-time installs and binary lifecycle that only make sense run by hand.' ),
		),
	),

	'cli' => array(
		'title'  => 'CLI reference',
		'kicker' => 'Docs',
		'intro'  => 'Every command, as `agent-local help` prints it. Every site-facing command is also an MCP tool with the same name; the machine-level ones (`sudo`, `alias`, `connect`, `update`, the daemon) are run by hand.',
		'sections' => array(
			array( 'h', 'Sites' ),
			array( 'pre', "agent-local                                         open the dashboard\ncreate NAME [--domain d] [--php v] [--repo url]     create and install a WordPress site\nattach DIR [--name n] [--domain d] [--php v]        serve a directory you already have, with an empty database\nimport SOURCE [--copy] [--sql FILE] [--serve-only] [--keep-ddev]\n                                                    import a LocalWP site, DDEV project or docroot\nlocalwp-sites                                       LocalWP sites available to import\nddev-projects                                       DDEV projects available to import\nlist                                                every site, its state and URL\nstart | stop | restart SLUG                         control one site\ndelete SLUG [--yes] [--keep-files] [--keep-db]      remove a site; a snapshot is saved first\nopen SLUG                                           open the site in your browser\ndomain SLUG NAME                                    change a site's domain; hosts entry and cert follow\nphp SLUG VERSION [--tap]                            switch PHP version, installing it if needed\nresolve [PATH]                                      which site owns a path (default: cwd)" ),
			array( 'h', 'Database' ),
			array( 'pre', "db SLUG                                             connection details\ndb SLUG \"SQL\"                                       run a statement\ndb SLUG import FILE.sql[.gz] [--keep-urls]          load a dump; URLs rewritten (WordPress), snapshot saved first\ndb SLUG export [FILE]                               dump to a file\ndb SLUG reset | tables | gui                        empty it, list tables, or open Adminer\ndb SLUG search NEEDLE                               where a string still lives: table, column, count\ndb SLUG search-replace OLD NEW [--apply]            replace across every table; a dry run unless --apply\ndb SLUG snapshot [NAME]                             save a restore point\ndb SLUG snapshots                                   list restore points\ndb SLUG restore [NAME]                              restore one (default: newest)" ),
			array( 'h', 'Develop' ),
			array( 'pre', "worktree SLUG BRANCH [--remove]                     serve a git branch on its own URL\nworktrees SLUG                                      list branch previews\nbranches SLUG                                       branches of the site's repo\nwp SLUG -- ARGS                                     run wp-cli against the site\nwpdebug SLUG [on|off]                               WP_DEBUG, logged to ~/.agent-local/logs/wp-SLUG.log\nwpconst SLUG [NAME VALUE | NAME --remove]           read, set or remove wp-config constants\nlogs NAME [LINES]                                   tail a log: mysql, apache, daemon, fpm-SLUG, wp-SLUG\nmail SLUG [ID] [--open] [--clear]                   emails the site has sent\nmedia SLUG [URL | --auto | --off]                   send missing uploads to a production origin\nshare SLUG [--minutes N] [--off]                    public URL through a Cloudflare tunnel\ncert DOMAIN [--trust]                               TLS state for a domain; --trust issues and trusts it\nprobe SLUG [PATH] [--follow]                        request the site like a browser would; PHP errors and a verdict\nerrors SLUG [--since 1h]                            PHP errors from the pool and debug logs, deduplicated\nwpinfo SLUG                                         the WordPress install in one JSON document\ncheckpoint SLUG [LABEL] [--scope all] [--list]      save a restore point: database + wp-content\nrollback SLUG CHECKPOINT                            put the site back; what was there is kept aside\nlogin SLUG [USER] [--open]                          one-time URL straight into wp-admin" ),
			array( 'h', 'Agents' ),
			array( 'pre', "connect [--list | --all | --remove] [HARNESS...]    register the MCP server in Claude Code, Codex, Cursor and friends\nmcp                                                 the MCP server itself (stdio); clients launch this\nmcp --config                                        the client config block, for a client connect doesn't know\napi-token                                           bearer token for the HTTP API\njobs                                                recent long-running jobs\njob ID                                              one job's progress" ),
			array( 'h', 'Machine' ),
			array( 'pre', "doctor [--fix]                                      health checks; --fix applies every repair\ninstall brew | php VERSION | mariadb | apache       install a dependency (wp-cli too)\nfront [router | apache]                             show or switch the HTTP front\nyield [SECONDS]                                     free :80/:443 briefly so another app can start\nalias [--off]                                       bare URLs: the 127.0.0.2 alias and its root front daemon\nsudo                                                one-time allowlist so root steps never prompt again\nautostart [--off]                                   start the daemon at login (on by default)\nsites-dir [PATH]                                    where new sites are created\nsuffix [.test]                                      default domain suffix\ndaemon [--background]                               run the daemon by hand\nrestart-daemon                                      hand over to a freshly installed binary\nupdate [--check]                                    install the latest release\nversion                                             what build this is" ),
		),
	),
);
