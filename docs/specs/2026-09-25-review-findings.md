# agent-local review: flaws, fixes and Muster impact

**Date:** 25 Sep 2026. **Tree:** `main` at `419a927` (v0.34.1 + 5 commits), clean against `origin/main`.
**Baseline:** `go vet` clean, 353 tests pass, `go test -race` finds no races, coverage 34.4%.
**Muster checked against:** `muster-ui` `main`, fast-forwarded 232 commits to `origin/muster` on 25 Sep 2026.

Every finding below was read in code. Items marked *verified* were also reproduced in a scratch copy or against the live site list. Items marked *unverified* are plausible but unproven.

The Muster column says what each fix does to `muster-ui`:

- **none**: Muster does not touch the changed surface.
- **safe**: the surface changes, but Muster's current code handles the new behaviour.
- **M-n**: Muster needs item *n* of `muster-ui/docs/specs/2026-09-25-agent-local-compat.md`.

---

## 1. Invariants every fix must keep

These are the parts of the contract Muster reads today (full inventory in the compat spec, section 1). A fix that changes one of them is not allowed without a version gate.

1. The envelope `{ok, data, error}`. `error` stays a string.
2. `/resolve` and `/sites` field names: `slug, wp_dir, work_dir, domain, php_version, state, running`, with `data` an array on `/sites`.
3. `db.{host,port,socket,name,user,pass}` on `/resolve`, `/sites/{slug}/start`, `/sites/{slug}/db`.
4. Job `status` values `ok` / `error`, `steps[].{stage,detail}`, `result`, `error`.
5. `/status.version` stays a semver-shaped string. `installed` and `update.{latest,available}` stay.
6. Probe verdict words: `healthy, fatal, redirects_offsite, blank, down, error, asset_404, slow`.
7. Error substrings Muster matches: `already exists`; `copy database` or `dump` together with one of `can't connect|cannot connect|connection refused|unknown database|access denied|2002|2003`; and a last CLI line that starts with `authorization failed` from `agent-local cert <d> --trust`.
8. `agent-local restart-daemon` exits 0 once the daemon answers.
9. `POST /yield {"seconds":N}` keeps working whatever process owns `127.0.0.2:80`.
10. The API answers on `127.0.0.1:10809` with the bearer token read from `~/.agent-local/token`.
11. `ValidDomain` keeps accepting what Muster generates: `<first-label>.local`, where the label is a lowercased folder name that can contain `_` (`christopher_boots.local` is a live example).
12. `install.sh` piped as `curl … | bash -s -- --setup` keeps working.

---

## 2. P0: data loss on this machine today

### F1. Deleting an attached or in-place site removes the user's whole repo (high, verified)

`sites.go:640` checks `managedDir(site.WorkDir)` before it looks at `Installed` or `Attached`. `managedDir` (`sites.go:378`) is true for anything under the configured sites directory. On this machine `sites_dir` is `/Users/jakevarrese/Sites`, so for about 35 attached sites (`alchemy`, `altamira`, `pact`, …) and every in-place import under `~/Sites` (`sulo`: `work_dir=~/Sites/sulo/app`), `delete` with the default `files` behaviour runs `os.RemoveAll` on the client repo. The MCP `delete_site` description promises "an imported external checkout is only detached", which is the opposite of what the code does.

**Fix:** allow `RemoveAll` only for `site.Installed`, or for a copy-mode import whose `WorkDir` is under `P().Sites()`. Attached and in-place imports always take the "restore wp-config only" branch. Add a table test covering attached, in-place, installed and copy under both a default and a custom sites dir.
**Surface:** no route or schema change. Delete stops removing those directories. **Muster:** none (Muster never calls `DELETE`).

### F2. Delete of a serve-only import drops a database and user agent-local never created (high)

A serve-only import copies `DB_NAME`/`DB_USER` from the folder's own `wp-config.php` (`import.go:378`). `DropSiteDB` (`engine.go:259`) later drops them with no identifier check. A config with `DB_USER 'root'` locks agent-local out of its own MariaDB. A folder pointing at `al_other` drops another site's schema. A backtick in `DB_NAME` is SQL injection.

**Fix:** derive ownership instead of storing it. Every provisioning path names both schema and user `al_<slug>`, so `ownsDB(site)` is `db_name == db_user == "al_" + slug`. `DropSiteDB` refuses anything else, delete skips the drop and the pre-delete snapshot with a warning, and `CreateSiteDB`/`DropSiteDB` run `requireSQLIdent`. Reset and restore on a non-owned schema are a follow-up.
**Surface:** none (no new field, no migration). **Muster:** none.
**Status:** done on `fix/data-safety` for delete and drop.

### F3. Toggling WP_DEBUG or a constant overwrites the pre-import wp-config backup (high)

`writeWPConfigSrc` (`wpdebug.go:113`) rewrites `wp-config.php.agent-local.bak` on every call. That file holds the user's original config, and delete restores it. After `wpdebug on`, delete "restores" agent-local's config and the original credentials are lost.

**Fix:** keep `.agent-local.bak` write-once. Use `.agent-local.prev` for the rolling toggle backup.
**Surface:** one new file name in the docroot. **Muster:** none (Muster reads neither file).

### F4. Failure paths drop a database the caller asked to keep (high)

`CreateSiteDB` uses `CREATE DATABASE IF NOT EXISTS` and so silently adopts a leftover `al_<slug>` (the documented `?db=keep` re-adopt flow). Every `fail()` path in create and import then calls `DropSiteDB` (`sites.go:137`, `import.go:365`, `import.go:371`).

**Fix:** check `information_schema.SCHEMATA` before provisioning. Record whether the schema already existed and drop only what this call created.
**Surface:** none. **Muster:** none.

### F5. Store never loads another process's changes after its own save (high, verified)

`Save` (`store.go:188`) writes the merged document but keeps the pre-merge `s.Data`, and moves `loadedAt` to its own write. A site the CLI created is on disk and invisible to the daemon until some later write. `StartSite`, `StopSite` and `StartWorktree` also mutate `*Site` without the lock or `PutSite`.

**Fix:** unmarshal the merged bytes back into `s.Data`. Route field mutations through locked setters. Add an `flock` on the state file for check-then-insert (create, attach, import).
**Surface:** none. **Muster:** safe (Muster's `/resolve` becomes correct sooner).

### F6. Unlocked reads of `Store.Data.Worktrees` can kill the daemon (med-high)

`daemon.go:582, 600, 763, 1477`, `httpfront.go:366` and several in `engine.go`/`sites.go` read the map without `s.mu`. A worktree add at the same moment is a fatal "concurrent map read and map write" that takes every site down.

**Fix:** `Store.Worktree(id)` and `Store.WorktreesFor(slug)` accessors under `RLock`. Route all readers through them.
**Surface:** none. **Muster:** safe (a crash today looks like "daemon down" and triggers `restart-daemon`).

---

## 3. P0: promptless user-to-root

### F7. The sudoers allowlist permits writing and loading an arbitrary LaunchDaemon (high, verified)

`main.go:1976-1990` allows `tee`, `chown`, `chmod` and `launchctl load` on `/Library/LaunchDaemons/local.agent-local.front.plist`. `tee` takes any stdin, so any process running as the user (an npm postinstall, a WordPress plugin under FPM, an agent) can install `ProgramArguments=/bin/sh -c …` and load it as root with no prompt.

**Fix:** remove the plist `tee/chown/chmod/load/unload/rm` lines. Installing or reinstalling the front daemon always prompts. `watchFront` logs the command instead of running it.
**Surface:** `/etc/sudoers.d/agent-local` content. **Muster:** none.

### F8. The root front daemon executes a user-writable binary (high)

The plist's `ProgramArguments` is `~/.local/bin/agent-local` (`loopalias.go:207-222`, `autostart.go:122-128`), KeepAlive is on, and `watchBinary` runs `exec.Command(path, "version")` as root after each change. Replacing that file gives root within about 10 s.

**Fix, preferred:** run the proxy under launchd with `UserName` set to the user (macOS 10.14+ lets unprivileged processes bind below 1024 on a specific address; confirm on the lowest supported macOS). A separate root one-shot job does only `ifconfig lo0 alias`. **Fallback:** copy the binary to a root-owned path such as `/Library/PrivilegedHelperTools/agent-local-front` at install and move the logs to `/Library/Logs`.
**Surface:** LaunchDaemon plist keys, possibly a second plist, possibly a root-owned helper path. **Muster:** safe, provided invariant 9 (`/yield`) holds. Acceptance must include `POST /yield` then a LocalWP start.

### F9. `tee /var/db/agent-local-trust.crt` plus `add-trusted-cert` trusts any root CA silently (high)

`main.go:1990-1991` and `certs.go:107-109`. The staging path accepts any stdin, and the next allowed command trusts it as a root. Together with passwordless `tee /etc/hosts` this is a silent MITM of any domain. *Unverified:* whether macOS 11+ shows an authorization dialog anyway.

**Fix:** switch to one local CA (the mkcert model). Trust the CA once, interactively. Sign leaf certs with it. Drop both trust lines from sudoers. `GET /certs/{d}` keeps `exists/trusted/not_after/cert_path` with the same meaning (`trusted` = the leaf verifies against the System keychain) and adds `ca: {trusted, not_after}`. `POST /certs/{d}/trust` issues the leaf and, if the CA is not yet trusted, returns an error whose message starts with `authorization failed` when no interactive session is available. The CLI `cert <d> --trust` prompts once for the CA.
**Surface:** new CA files in the certs dir, leaf certs reissued, additive `ca` block, sudoers content. **Muster:** M-1.

### F10. Passwordless writes to `/etc/hosts` and `/etc/pf.conf` (med)

`main.go:1977-1978`. Any user process can redirect any hostname. `pf.conf` is only written to strip a legacy block, and `tee` truncates in place.

**Fix:** a root-owned helper that accepts only a validated domain for add/remove, installed with the front helper from F8. Drop the `pf.conf` line.
**Surface:** sudoers content, new helper binary. **Muster:** none.

### F11. Apache front answers unknown Host headers with the first site and its auto-login Adminer (high, apache front only)

`httpfront.go:302-304` has no default vhost, so Apache serves the first vhost for any Host. That vhost carries the Adminer alias, and the wrapper auto-logs in (`adminer.go:168`). A DNS-rebinding page gets SQL on that site and then PHP execution through WP admin.

**Fix:** emit a first `<VirtualHost>` per port that returns 421. Have the Adminer wrapper refuse any `HTTP_HOST` that is not the site's domain, an alias, or a worktree domain.
**Surface:** unknown Host gets 421 instead of a site. **Muster:** none.

### F12. The API accepts any Host header; `/mail-ui/*` and `/hub-ui/*` skip the token (med-high)

`daemon.go:394`. A rebinding page on `attacker.com:10809` can read password-reset mail, request logs with reset keys, and clear inboxes.

**Fix:** middleware that accepts only Host `127.0.0.1:10809`, `localhost:10809` and `[::1]:10809`, and returns 421 otherwise. The Apache `ProxyPass` lines already send `127.0.0.1:10809`. Add `sameOrigin` to mail clear (`mailui.go:76`).
**Surface:** requests with another Host get 421. **Muster:** safe. Muster's origin is hard-coded to `http://127.0.0.1:10809` (`agent-local-host.ts:17`) and Node's `fetch` sends a matching Host. M-6 pins this with a test.

---

## 4. P1: correctness and robustness

| # | Sev | Where | Problem | Fix | Muster |
|---|---|---|---|---|---|
| F13 | med | `import.go:535-541` | Import hangs forever when the loader exits early: `dumpOut` is never closed and mysqldump blocks. | Close or kill the dump on `streamErr`; add a context deadline. | safe; M-4 adds a poll deadline on Muster's side |
| F14 | med, verified | `wpdebug.go:145-147`, `import.go` `setWPConst` | The new value is used as a regexp replacement template (`$name` expands) and `[^)]+?` stops at the first `)`. `'http://' . $_SERVER['HTTP_HOST']` becomes a parse error. | `ReplaceAllStringFunc`, a small tokenizer for the value, temp file plus rename. | none |
| F15 | med | `dbsearch.go:175,199` | Search-replace needles reach wp-cli as positional args; `--exec=<php>` runs PHP. A real write takes no pre-change snapshot. | 400 on a leading `-`. Snapshot before a non-dry run unless `no_snapshot: true` (same name as `db/import`). | M-2 (pass `no_snapshot: true`, or Muster's 20 min budget absorbs a redundant snapshot) |
| F16 | med | `snapshot.go:271`, `import.go:790` | `tableCount` returns 0 on error, which `autoSnapshot` reads as "empty, skip". Delete/reset/import then run with no save point. | Return `(int, error)`; fail closed. | none |
| F17 | med | `import.go:1168` | URL rewrite is a prefix match: `https://example.com` also rewrites `https://example.com.au/…`. | wp-cli `--regex` with a boundary lookahead. | safe (`total`/`hits` counts change, shape does not) |
| F18 | med | `import.go:639` | Dumps load as root, so a `USE prod` in the file loads into another schema and the import reports success. | Strip `USE`/`CREATE DATABASE` in the stream filter; error when the target has 0 tables afterwards. | safe (a bad dump now fails the job with `status: error`) |
| F19 | med | `import.go:319` | In-place import sets `WorkDir = filepath.Dir(docroot)`. `avalon-new` has `work_dir=/Users/jakevarrese/Sites`, so `SiteForPath` resolves every unmanaged folder under `~/Sites` to it. | Take the parent only when the docroot basename is a known subdir (`wp, public, web, public_html, app/public, app/web`); else the docroot itself. Migrate existing records by the same rule. | safe; Muster already distrusts a greedy `work_dir` (`site-resolve.ts:89-101`). `/resolve` now answers 404 where it wrongly matched. M-5 lets Muster drop the workaround later |
| F20 | med | `sites.go:620,1499` | `delete --keep-files` still runs `git worktree remove --force` and loses uncommitted preview work; the `.bak` is never restored with keep-files. | Under keep-files only unregister worktrees; restore `.bak` outside the `withFiles` branch. | none |
| F21 | med | `import.go:274`, `store.go:595` | `ImportSite` never calls `ValidDomain`; `ValidDomain` accepts `\n`, `\r`, NUL and `..`. Values reach `/etc/hosts` and vhosts. | Per label `^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?$`, at least two labels, total ≤ 253, applied on create, import, attach, domain and alias. Underscore stays legal (invariant 11). Existing records are not re-validated. | safe, with the underscore rule. A strict RFC 1123 rule would break Muster (`christopher_boots.local`) |
| F22 | med | `daemon.go:1733` | `GET /logs/{name}` reads the whole file before trimming. `fpm-traders-in-purple.log` is 1.6 GB here. | Seek to `size-1MB`; rotate FPM logs on pool start. | none |
| F23 | med | `share.go:150` | Two concurrent `StartShare` calls open two tunnels; the first can never be stopped and the mu-plugins fight. | Per-slug singleflight. | none |
| F24 | med | `adminer.go:65,196`, `router.go:465` | Adminer boot file rewritten on every request (truncate then write), and the Adminer download has no checksum. | Write only on change via rename; pin the 6.0.1 SHA-256. | none |
| F25 | low-med | `daemon.go:692` | `GET /jobs` returns retained create/import results with `db_pass` and `admin_pass`. | Redact in the list; keep secrets on `GET /jobs/{id}` only. | safe (Muster reads `/jobs/{id}` only) |
| F26 | low-med | `share.go:312`, `httpfront.go:225-236`, `proc.go:166-176` | Stale pid files: `SweepShares` SIGKILLs and Apache reload SIGUSR1s whatever reused the pid; `Stop` signals the whole process group. | Check process identity (`Marker`, `ps -o comm=`); signal `-pid` only when `Getpgid(pid)==pid`. | none |
| F27 | med | `certs.go:28`, `doctor.go:210-222` | Leaf certs are never renewed (398 days) and deleted sites' trust entries are never removed. Doctor checks only that the file exists. | Reissue under 30 days left; remove trust on delete/rename; doctor warns on expiry and untrusted. Mostly absorbed by F9. | safe (a renewed leaf under a trusted CA stays `trusted: true`) |
| F28 | med | `doctor.go:62-94` vs `:540-644` | `homebrew`, `php` and `database` findings say `auto_fix: true`, but `DoctorFix` has no case for them. | Add the cases or set `auto_fix: false`. | none |

---

## 5. P1: supply chain and release

| # | Sev | Where | Problem | Fix | Muster |
|---|---|---|---|---|---|
| F29 | med | `update.go:461-488`, `install.sh:82-92` | Update checks integrity, not authenticity: `checksums.txt` comes from the same release as the archive. With F8 unfixed that is root on every auto-updating machine. | Sign `checksums.txt` in goreleaser (minisign or cosign), embed the public key, verify in `SelfUpdate`. For `install.sh`, open question Q2. | none (new release asset) |
| F30 | med | `install.sh:40,56,102-105` | Piped from `curl`, `$0` is `bash`, so `REPO_ROOT` becomes the cwd and an executable `./agent-local` there gets installed. The script is not wrapped in a function, so a truncated download runs partially. | Use `REPO_ROOT` only when `[[ -f "${BASH_SOURCE[0]}" ]]`; wrap in `main "$@"`. | safe; invariant 12 is the acceptance test |
| F31 | low-med | `update.go:444-450,492,513` | `SelfUpdate` replaces `os.Executable()`, not the installed path the daemons run. The `.new` staging name races daemon vs CLI. `!=` allows downgrades. | Target `installedBinaryPath()`, `os.CreateTemp` in the same dir, `verLess`, keep `.old`. | none (Muster runs `agent-local update` from PATH, which is the installed path) |
| F32 | low-med | `update.go:597-603` | `want[:12]` panics on a short hash inside the update goroutine and kills the daemon. | Require 64 hex chars. | none |
| F33 | low-med | `.github/workflows/release.yml:27-35`, `ci.yml:31`, `.goreleaser.yaml:16` | The release job runs no tests and is not gated on CI; no `-race`; actions pinned to tags; `go mod tidy` runs at release. | `go test -race ./...` in release; pin action SHAs; replace tidy with `go mod verify` and `git diff --exit-code`. | none |
| F34 | low | `model.go:18`, `.goreleaser.yaml` | A source build reports `version: "dev"`. Muster parses that as 0.0.0 and turns off every daemon import route (`agent-local-import-api.ts:54-71`), while the integration doc says "treat dev as newest". | Stamp source builds with `git describe --tags` (`0.34.1-5-g419a927`). Muster's parser reads the leading `0.34.1`. | safe; M-3 aligns Muster with the doc for a bare `dev` |

---

## 6. P2: API consistency

### F35. Status codes and error bodies (low)

Caller errors return 500: invalid domain (`handleDomain`), unknown checkpoint, unknown slug on `POST /sites/{slug}/worktrees`. Unmatched routes and wrong methods return Go's plain-text 404/405, so MCP and Muster see a JSON decode error. Worktree routes do not check that `wt.Site` matches `{slug}`.

**Fix:** typed errors mapped to 400/404/409; a JSON catch-all and a 405 wrapper; a slug check on worktree routes. Keep `error` a string.
**Muster:** safe. Muster branches only on 404 from `/resolve` and `/sites` (`agent-local-site-control.ts:115,152,201`), which already return 404.

### F36. Machine-readable error codes (improvement)

Muster matches three error-message substrings (invariant 7). Any rewording in agent-local breaks Muster's `/import` to `/attach` fallback, its "already exists" adoption, and cert-trust detection.

**Fix:** add an optional `code` to the error envelope: `{"ok":false,"error":"…","code":"site_exists"}`. Initial codes: `site_exists`, `source_db_unreachable`, `not_found`, `route_not_found`, `invalid_input`, `authorization_failed`, `not_wordpress`. Keep every message substring from invariant 7 unchanged for at least two minor releases after Muster adopts codes.
**Muster:** M-2.

### F37. Warnings swallowed on stderr (med)

`SetDomain`, `AddWorktree` and `DeleteSite` print hosts, cert and URL-rewrite failures to the daemon's stderr (`sites.go:636,850-889,1104`). `POST /sites/{slug}/domain` returns `{"ok":true,"data":"<domain>"}` even when the hosts write failed.

**Fix:** return `[]string` warnings. Put them on the envelope, not inside `data`: `{"ok":true,"data":"x.test","warnings":["…"]}`, so `data` keeps its shape.
**Muster:** M-2 (optional: surface them).

### F38. Behaviour differs by caller (med)

Rules live in HTTP handlers, and the CLI and TUI call `Engine` directly. `healKind` runs only in HTTP `requireSite` (`daemon.go:1087`), the `requireWordPress` 409 exists only over HTTP, and restart has three different failure rules (`daemon.go:1156`, `tui.go:1193`, `main.go:552`).

**Fix:** move kind healing, the WordPress guard and `RestartSite` into `Engine`.
**Surface:** CLI exit codes on non-WordPress sites. **Muster:** none.

---

## 7. P2: TUI, CLI and tests

| # | Sev | Where | Problem | Fix |
|---|---|---|---|---|
| F39 | high (dev machines) | `daemon_lifetime_test.go:22`, `autostart.go:35-70` | When the ports are free (daemon stopped, or CI on macOS), the test calls `EnsureDaemonAutostart`, which boots out the real `local.agent-local.daemon` and bootstraps a plist pointing at the `go test` binary. `AGENT_LOCAL_HOME` is never unset in tests. | `TestMain` that unsets `AGENT_LOCAL_HOME` and sets `AGENT_LOCAL_LAUNCHD=1`; stop the daemon at the end of the test. |
| F40 | high | `tui.go:620-633`, `main.go:19-20` | ctrl+c during a busy action quits the process and kills a move, import or delete halfway. | Wait for the in-flight action ("finishing…"), or confirm before quitting. |
| F41 | med-high, verified | `tui.go:286-295,1699-1702,1905` | The size cache is set on a value-receiver copy, so `du -sh` runs synchronously on every frame and keypress; it also measures `P().Sites()/slug`, not `WorkDir`. | Compute in a `tea.Cmd`, key by `WorkDir`, store through a pointer. |
| F42 | med, verified | `tui.go:212-219,487` | Nil store after a corrupt `OpenStore` panics; action goroutines have no `recover`, which leaves the terminal in raw mode. | Nil guard; `recover` in `runActionWith`. |
| F43 | med | `tui.go:1056-1060,1181,260-279` | `EnsureDB`, SQL, `StopSite`, four port dials and a `ps` scan run on the UI goroutine. | Move into `runAction` or a Cmd. |
| F44 | med | coverage | 0% on `ImportSite`, `ImportSQL`, `CreateSite`, `DeleteSite`, `SetDomain`, worktrees, hosts, `dbauth.go`, `SelfUpdate`, `handleDelete`, `handleImport`, `mcpCall`. | Tests first for F1-F5 and F29-F32; an MCP-tool-to-route table test against `routes()`. |
| F45 | low-med | `main.go:809,1843`, `tui.go:2312` | `doctor` and `probe` exit 0 on failures; `runTUI` errors go to stdout with exit 0; only `connect` has `--json`. | Non-zero exits for unhealthy results; `--json` that prints the API payload. |
| F46 | low | `main.go:185` | `positional()` has a hard-coded list of value flags missing `--scope`, `--since`, `--minutes`, `--dir`, `--site`. | Per-command `flag.FlagSet`. |
| F47 | low | `mcp.go:552-846` | MCP pastes `slug`, `id`, `name`, `domain` into URL paths unescaped. | `url.PathEscape` every segment. |

Lower-priority items, kept for completeness: passwords on the command line via `--password=` (use `MYSQL_PWD` in the child env); no fsync before the store rename; `RemovePFWiring` runs `pfctl -d` system-wide; no timeouts on `wpCLI`, `downloadWP` and git clone; git gets no `--` before the repo argument; `errlog.go:145` stops at the first line over 1 MB; checkpoint pre-rollback trees live inside the checkpoint dir; auto-snapshot pruning can delete a deleted site's `auto-delete` save point after slug reuse; `SiteDirSize` uses `P().Sites()/slug`; `handleDBTables` interpolates `DBName`; chunked FastCGI bodies send `CONTENT_LENGTH=0`; `logDelta` does an unbounded `ReadAll`; `ValidDomain` is also bypassed by aliases; `hostsMu` is per process, so CLI and daemon can lose each other's `/etc/hosts` edits; `RunPrivileged` calls relative `security` and `sh`; the sudoers user comes from `$USER`; `setup` and `doctor --fix` self-update with auto-update off; `pgrep -f "Local.app/Contents"` matches any command line; connect's `atomicWrite` replaces a symlinked config with a file.

---

## 8. Docs drift

`docs/MUSTER-INTEGRATION.md` is the document a Muster agent reads first, and parts of it are wrong now.

| Doc claim | Code |
|---|---|
| §3: "40 tools" | 76 tools (`mcp.go:228-453`). The site pages say 75. |
| §5: mail catcher, DB GUI, backups "absent" | Present: `mail.go`/`mailui.go`, Adminer, DB snapshots and checkpoints. |
| §2: "treat `dev` as newest" | Muster treats it as 0.0.0 (F34, M-3). |
| §2: start the daemon with `daemon --background` | Muster uses `restart-daemon` (launchd), deliberately. |
| §2: feature floors 0.1.1 to 0.3.0 | Muster's real floor is 0.32.2 for the import routes; not documented. |
| §3 endpoint list | 24 of 81 routes undocumented, including `?async=1`, `/jobs/{id}`, `/status.installed`, `update.*`, `/media.pinned/off`, `/probe.reason`, `/errors.entries`. |
| §2: created sites at `~/.agent-local/sites/<slug>/wp` | `<sites-dir>/<slug>/wp` (`sites.go:59`). |
| §4.4/4.5 line refs | Stale; Muster now has `ensureLocalSiteRunning` and a `LocalStackProvider` registry. |

**Fix:** rewrite MUSTER-INTEGRATION.md §2, §3 and §5 from the code, add a "contract invariants" section copied from section 1 here, and add a "version floors" table that includes every feature in this spec.

---

## 9. Sequencing

Each release is a tag; patch bumps for fixes, minor for surface changes (existing convention).

1. **v0.34.2, data safety, no surface change:** F1, F2 (delete and drop), F3, F4, F5, F6, F13, F14, F16, F20 (worktree part), F32, F39, plus tests. Ship first; F1 affects about 35 sites on this machine. **Implemented on branch `fix/data-safety`, uncommitted.** Not covered by a test: F13 (needs real dump processes). F20's `.bak` restore under `--keep-files` was dropped on purpose: keeping the files is the documented re-adopt flow, which needs the wp-config to keep pointing at agent-local; the `DeleteOpts` comment now says so.
2. **v0.35.0, privilege hardening:** F7, F8, F10, F11, F12, F26, F29, F30, F31, F33. Needs `agent-local sudo` re-run, which rewrites the allowlist. Acceptance includes invariants 9, 10 and 12.
3. **v0.36.0, certs and data model:** F2 follow-up (reset and restore guard), F9 (local CA), F19 (`work_dir` migration), F21, F27.
4. **v0.37.0, API consistency:** F15, F17, F18, F25, F35, F36, F37, F34 (version stamping), F38.
5. **v0.37.x, TUI/CLI and docs:** F22-F24, F28, F40-F47, section 8 rewrite.

Muster work is independent of steps 1 and 2. M-1 must land before or with step 3. M-2 depends on step 4.

---

## 10. Decisions (review round 1, 25 Sep 2026)

- **Q1, F8 macOS floor:** 10.14+ is acceptable. F8 takes the unprivileged-bind approach.
- **Q2, F29 install.sh verification:** `SelfUpdate` verifies the signature with the embedded key and fails hard; that covers auto-update, the path that matters most. A downloaded binary verifying itself proves nothing, so `install.sh` never asks the new binary to check itself. When an `agent-local` is already installed, `install.sh` runs the *installed* binary's `verify-release` against the new archive (trust on first use). A fresh install stays at HTTPS plus checksum, as today, and prints that it did so. The `openssl` route is dropped: LibreSSL Ed25519 support is unverified and would add a second verifier to maintain.
- **Q3, F9 legacy trusted certs:** no automatic removal during upgrade, because an unannounced admin prompt mid-update is the wrong surprise. `doctor` reports a `legacy-certs` finding with the count, and `doctor --fix` (or `agent-local cert --remove-legacy`) removes them with one interactive authorization.
- **Q4, F1 delete default:** the default stays as documented: remove what agent-local created, detach everything else. F1 makes the code honour that. Requiring `?files=keep` for attached sites would push the decision onto every caller and change the MCP and CLI surface for no extra safety once F1 is fixed. A regression test pins the rule for each site type.
