# agent-local Improvement Plan

**Date:** 2026-11-23  
**Baseline:** `main` at `a5f7e68` (`v0.36.1`), Go `1.26.8`, 55 source files (26,166 LOC), 57 test files (9,150 LOC). `go vet ./...` is clean, `go test ./...` passes in 18.2s at 36.3% statement coverage, and `go test -race ./...` passes in 63.2s.

**Live machine snapshot (`2026-11-23`):**
- 84 managed sites (9 `running`, 75 `sleeping`), 1 worktree, `front: router`, `auto_update: true`.
- 194 leaf `.crt` files in `~/.agent-local/certs` (30 of 30 sampled certs are self-signed and span more than 398 days; the local CA now exists).
- `fpm-madebytait.log` is 46 MB; `daemon-agent.log` has 50,028 lines, 99.7% of which (49,902 lines) are loopback TLS handshake `EOF` errors.
- `agent-local doctor` reports 5 `dns-shadow` hosts, and `sites.json` still holds 1 greedy `work_dir` (`avalon-new` at `/Users/jakevarrese/Sites`).

**Relation to [2026-09-25-review-findings.md](file:///Users/jakevarrese/Sites/agent-local/docs/specs/2026-09-25-review-findings.md):** Release 1 (`v0.34.2` data safety: F1–F6, F13, F14, F16, F20, F32, F39) and the local-CA UX half of F9 (`v0.36.1`) shipped. Releases 2–5 remain open and are consolidated here with new findings from `v0.35`–`v0.36` and live runtime inspection.

---

## 1. Contract Invariants (Must Not Break)

Every change below must preserve all 12 Muster integration contracts from `docs/specs/2026-09-25-review-findings.md` §1:

1. Response envelope `{ok, data, error}`, where `error` remains a string.
2. `/resolve` and `/sites` keys (`slug`, `wp_dir`, `work_dir`, `domain`, `php_version`, `state`, `running`), with `data` as an array on `/sites`.
3. `db.{host,port,socket,name,user,pass}` on `/resolve`, `/sites/{slug}/start`, and `/sites/{slug}/db` (`db.socket` stays `""`).
4. Job fields: `status` (`ok` / `error`), `steps[].{stage,detail}`, `result`, `error`.
5. `/status.version` stays a semver-shaped string; `installed` and `update.{latest,available}` stay intact.
6. Probe verdict words: `healthy`, `fatal`, `redirects_offsite`, `blank`, `down`, `error`, `asset_404`, `slow`.
7. Error substrings Muster matches (`already exists`; `copy database` or `dump` with `can't connect|cannot connect|connection refused|unknown database|access denied|2002|2003`; last CLI line starting with `authorization failed` from `agent-local cert <d> --trust`) remain unchanged for at least two minor releases after Muster adopts `code`.
8. `agent-local restart-daemon` exits 0 once the daemon answers.
9. `POST /yield {"seconds":N}` continues working regardless of which process holds `127.0.0.2:80`.
10. Control API listens on `127.0.0.1:10809` with the bearer token from `~/.agent-local/token`.
11. `ValidDomain` keeps accepting `<first-label>.local` with `_` in labels (for example `christopher_boots.local`).
12. `curl … | bash -s -- --setup` continues working end to end.

---

## 2. Phase 1 — `v0.36.2` Runtime & Correctness Quick Wins (~1 day)

No breaking API or sudoers changes. Targets bugs active in `v0.35.0`–`v0.36.1` and live daemon noise.

### 1.1 Stop `doctor` probes from resetting idle sleep
- **Files:** `netutil.go:63-83` (`httpProbeHostTimed`), `router.go:151` (`ServeHTTP`), `doctor.go:257` (`Doctor`).
- **Problem:** `doctor` probes every running site via `httpProbeHostTimed`, hitting `router.go:151` and calling `r.engine.act.stamp(fpmID)`. On this machine, 8 of 9 running `php-fpm` pools have been alive 3–7 days despite 12h idle sleep and `last_active` timestamps up to 13 days old.
- **Change:** Send an internal probe header (`X-Agent-Local-Probe: 1`) in `httpProbeHostTimed`. In `Router.ServeHTTP`, skip `r.engine.act.stamp(fpmID)` when that header is `"1"` and `clientIP(req)` is loopback (`127.0.0.1` or `::1`). Keep `act.enter` / `act.leave` active so a pool cannot park mid-probe.

### 1.2 Fix idle-sleep data race and parking TOCTOU
- **Files:** `sleep.go:153-197` (`SleepIdlePools`), `sleep.go:273-297` (`PersistActivity`), `store.go` (`SetSiteState`, `SetWorktreeState`), `router.go:107-108`.
- **Problem:** `SleepIdlePools` and `PersistActivity` mutate `site.LastActive` and `w.LastActive` outside `Store.mu` on shared pointers, and `park()` checks `inflight == 0` without preventing a new request from calling `act.enter(id)` before `StopFPM` and `act.forget(id)`.
- **Change:** Add locked store helpers (`SetSiteSleeping`, `SetSiteLastActive`, and worktree equivalents) so all `LastActive` writes hold `Store.mu`. Coordinate `park()` with `act` under a per-pool lock (or `tryPark(id)` check-and-mark) so an incoming request waits for teardown/restart instead of getting SIGKILLed or having its fresh `inflight`/`last` wiped by `act.forget(id)`.

### 1.3 Fix leaf cert >398-day span, atomic writes, auto-renewal & CA error hint
- **Files:** `certs.go:28-69` (`EnsureCert`), `certs.go:134` (`trustAnchor`), `ca.go:76-106` (`loadCA`), `doctor.go:224-236`.
- **Problem:** `EnsureCert` sets `NotBefore = Now - 1h` and `NotAfter = Now + 398d` (398.04 days total, exceeding Apple's 398-day TLS cap; 30/30 sampled certs on disk exceed 398 days). Cert writes are non-atomic (`os.WriteFile`), expired/expiring leaf certs are never renewed (F27), `caCache` ignores bundle mtime changes, and `certs.go:134` formats a failed non-interactive CA trust as `agent-local cert agent-local-ca --trust` (not a valid domain).
- **Change:** Set leaf `NotAfter` to `time.Now().Add(396 * 24 * time.Hour)` so total span stays under 397 days. Write `.crt` and `.key` via temp+rename under a per-domain mutex. Renew leaf certs in `EnsureCert` when expired, missing SAN, or within 30 days of expiry (F27). Track `caBundleMtime` in `loadCA`. In `trustAnchor`, when `anchorPath == checkPath` (local CA), suggest `agent-local setup` or `agent-local doctor --fix`. Add cert expiry/untrusted checks and a `legacy-certs` finding (Q3) in `doctor.go`.

### 1.4 Silence loopback TLS `EOF` noise in `daemon-agent.log`
- **Files:** `router.go:38` (`ListenAndServe`), `netutil.go:25-32` (`portOpen`).
- **Problem:** `portOpen(DefaultHTTPSPort)` opens and closes TCP `:10443` without a TLS handshake, generating `http: TLS handshake error from 127.0.0.1:...: EOF` (49,902 of 50,028 lines in `daemon-agent.log`).
- **Change:** Attach an `ErrorLog` wrapper to `httpsSrv` that suppresses `TLS handshake error` lines from `127.0.0.1:` / `[::1]:` ending in `: EOF`.

### 1.5 Tail-seek logs, cap `logDelta`, and rotate FPM logs
- **Files:** `errlog.go:138-151` (`collectErrors`), `dropins.go:142-158` (`recentFatals`), `daemon.go:1811-1837` (`handleLogs`, F22), `probe.go:178-216` (`logDelta`), `engine.go:386-427` (`StartFPM`).
- **Problem:** `fpm-madebytait.log` is 46 MB. `collectErrors` and `recentFatals` read from byte 0 with `bufio.Scanner` (aborting on any line >1 MB); `handleLogs` reads the full file into memory before slicing to 1 MB; `logDelta` runs an unbounded `io.ReadAll`.
- **Change:** Seek to `max(0, size - 2MB)` in `collectErrors`/`recentFatals` and iterate via `bufio.Reader` (`ReadString('\n')`) so single oversized lines do not stop scanning. Seek to `max(0, size - 1MB)` in `handleLogs`. Cap `logDelta` at 64 KB per request. Rotate `fpm-<id>.log` to `.1` on `StartFPM` when larger than 10 MB.

### 1.6 Fix `doctor` speed, output noise, and broken `--fix` cases
- **Files:** `doctor.go:61-95, 161-208, 300-321, 444-469, 547-666`.
- **Problem:** `Doctor` reads `/etc/hosts` ~120 times per run across loops (`hostsHas` / `hostsHasIP`). With 84 sites, 35 green `dns:<slug>` lines bury real warnings. In `DoctorFix`, `strings.HasPrefix(f.Check, "dns")` re-runs `EnsureHosts` once per site and misses `!Alias6Active()`, while `homebrew`, `php`, and `database` set `AutoFix: true` with no `DoctorFix` handler (F28).
- **Change:** Read `/etc/hosts` once into memory per `Doctor` run. Collapse passing `.local` `dns:<slug>` checks into one summary finding while keeping per-site lines for warnings/failures. Deduplicate `dns` auto-fix in `DoctorFix`, trigger `EnsureLoopAlias` when IPv6 alias is missing, and either implement or clear `AutoFix` on `homebrew`/`php`/`database`.

### 1.7 Fix `front-daemon` TCP half-close and dial timeout
- **Files:** `front.go:348-359` (`pipeConn`).
- **Problem:** `pipeConn` uses `net.Dial` with no timeout and returns on the first `io.Copy` completion, `defer`-closing both sockets and cutting off responses when the client half-closes stdin/request first.
- **Change:** Use `net.DialTimeout(..., 5*time.Second)`, call `CloseWrite()` on `*net.TCPConn` when each `io.Copy` finishes, and wait for both copy directions before closing.

**Phase 1 verification:**
- `go vet ./...`
- `go test -race -count=1 ./...`
- New unit tests covering: probe header skipping `act.stamp`, concurrent request+park race in `sleep_test.go`, `EnsureCert` `<= 397d` span + renewal, `errlog` tail seek + >1 MB lines, `doctor` single `/etc/hosts` read + collapsed `.local` summary + `DoctorFix` deduplication, `pipeConn` half-close.

---

## 3. Phase 2 — `v0.37.0` Privilege Hardening & Supply Chain Security (~2 days)

Requires a one-time `agent-local sudo` refresh after upgrading. Addresses P0 security findings F7–F12 and P1 supply-chain findings F23, F24, F26, F29–F31, F33.

### 2.1 Narrow `/etc/sudoers.d/agent-local` (F7, F9 allowlist cleanup, F10)
- **Files:** `main.go:2084-2124` (`installSudoAllowlist`), `priv.go:22-72` (`RunPrivileged`, `allowlistCurrent`), `certs.go:76-144`, `loopalias.go:102-123, 163-188`.
- **Change:**
  - Remove passwordless `tee`, `chown`, `chmod`, `launchctl load/unload`, and `rm` rules for `/Library/LaunchDaemons/local.agent-local.front.plist` (F7). Installing/reinstalling the front daemon always prompts; `watchFront` logs the manual command instead of running silently.
  - Remove `tee /var/db/agent-local-trust.crt` and `security add-trusted-cert` sudoers entries (F9: the single local CA is trusted once interactively via `agent-local setup`).
  - Remove `/etc/pf.conf` `tee` and `pfctl -d` sudoers entries (F10).
  - Replace passwordless `tee /etc/hosts` with a root-owned helper binary (`/Library/PrivilegedHelperTools/agent-local-hosts` or combined helper) that accepts only validated add/remove domain arguments, or restrict root writes to validated markers.
  - Replace `os.Getenv("USER")` with `os/user.Current()` when generating sudoers and configs; pass absolute binary paths in `RunPrivileged`.
  - Update `allowlistCurrent` in `priv.go` to match the new allowlist signature.

### 2.2 Stop running a user-writable binary as root (F8, Q1 settled decision)
- **Files:** `loopalias.go:203-235` (`installFrontDaemon`), `front.go:25-44` (`RunFrontDaemon`).
- **Change:** Move `front-daemon` to run unprivileged under launchd (`UserName` set to the installing user), binding `127.0.0.2:80` and `:443` directly (supported on macOS 10.14+; verify on target runner/machine, with fallback to installing a root-owned `/Library/PrivilegedHelperTools/agent-local-front` binary writing logs to `/Library/Logs`). Split `ifconfig lo0 alias` into a minimal root one-shot boot plist that executes no user-writable paths and never runs `watchBinary`.
- **Acceptance:** `POST /yield {"seconds":N}` followed by LocalWP startup succeeds cleanly (Invariant 9).

### 2.3 Block DNS rebinding on API & Apache + pin Adminer (F11, F12, F24)
- **Files:** `daemon.go:437-462` (`auth`), `mailui.go:76`, `httpfront.go:302-370` (`renderApacheConf`), `adminer.go:25-70, 113-196`.
- **Change:**
  - Add Host-header middleware on `:10809` rejecting any `Host` other than `127.0.0.1:10809`, `localhost:10809`, or `[::1]:10809` with HTTP `421` (F12).
  - Enforce `sameOrigin(req)` on `/mail-ui/*/clear` `POST` in `mailui.go:76`.
  - Emit a first default `<VirtualHost>` per port in `renderApacheConf` returning `421` for unknown `Host` headers (F11).
  - Validate `$_SERVER['HTTP_HOST']` against the site's domain, aliases, and worktree domains inside `writeAdminerBoot`.
  - Write `writeAdminerBoot` via temp+rename only when content changes, and verify the downloaded Adminer `6.0.1` script against a pinned SHA-256 (F24).

### 2.4 Sign releases, harden updater, CI & `install.sh` (F29, F30, F31, F33, Q2)
- **Files:** `update.go:440-612` (`SelfUpdate`, `verifyChecksum`), `main.go`, `.goreleaser.yaml`, `install.sh`, `.github/workflows/release.yml`, `.github/workflows/ci.yml`.
- **Change:**
  - Sign `checksums.txt` in GoReleaser (minisign or cosign) and embed the public key in `update.go`; fail closed in `SelfUpdate` if verification fails (F29).
  - Add `agent-local verify-release` subcommand so `install.sh` verifies upgrades through the already-installed binary (TOFU on first install per Q2).
  - Update `SelfUpdate` (F31) to target `installedBinaryPath()`, stage with `os.CreateTemp` in the destination dir, enforce semver upgrade (`verLess`, no accidental downgrades), and retain a `.old` rollback copy.
  - Wrap `install.sh` in `main "$@"` (F30), guard `REPO_ROOT` with `[[ -f "${BASH_SOURCE[0]:-}" ]]` so `curl | bash` never grabs a stale `./agent-local` from cwd, update `.goreleaser.yaml` footer/caveats to `agent-local setup` + `https://al.tools.efront.dev/install.sh`, add `local.agent-local.daemon` to `uninstall.launchctl`, and replace `go mod tidy` in `before.hooks` with `go mod verify`.
  - Run `go test -race ./...` in `release.yml`, add `-race` to `ci.yml`, and pin GitHub Action versions to commit SHAs (F33).

### 2.5 Safe PID signaling & `StartShare` singleflight (F23, F26)
- **Files:** `proc.go:160-179` (`Stop`), `share.go:145-165, 307-320` (`StartShare`, `SweepShares`), `httpfront.go:225-236`.
- **Change:** Verify process identity (`Marker` / `ps -o comm=`) before `SweepShares` `SIGKILL` and Apache `SIGUSR1`; signal `-pid` in `Proc.Stop` only when `syscall.Getpgid(pid) == pid`; add per-slug singleflight in `StartShare` so concurrent calls cannot leak duplicate `cloudflared` tunnels.

**Phase 2 verification:**
- `goreleaser check` and `go test -race -count=1 ./...`.
- Verify `sudo -n -l` contains zero plist `tee`/`launchctl` rules and zero `add-trusted-cert` rules.
- Verify `curl -i -H "Host: evil.com:10809" http://127.0.0.1:10809/healthz` returns `421`.
- Verify `POST /yield {"seconds":30}` with LocalWP start.
- Verify `curl -fsSL ... | bash -s -- --setup` inside an isolated test path or VM (Invariant 12).

---

## 4. Phase 3 — `v0.38.0` Data Model, Import/DB Safety & API Consistency (~2 days)

Addresses remaining P1/P2 model, DB/import, router, and API contract items (F2 follow-up, F9 API block, F15, F17–F19, F21, F25, F34–F38, F48).

### 3.1 Strict domain validation & greedy `work_dir` migration (F19, F21)
- **Files:** `store.go:730-735` (`ValidDomain`), `import.go:274, 318` (`ImportSite`), `sites.go`.
- **Change:**
  - Tighten `ValidDomain` to `^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?$` per dot-separated label (preserving `_` for Invariant 11), `>= 2` labels, total length `<= 253`. Apply on create, import, attach, domain rename, and alias updates without breaking existing records on load.
  - In `ImportSite`, set `WorkDir = filepath.Dir(targetWPDir)` only when docroot basename/suffix matches a known subdir (`wp`, `public`, `web`, `public_html`, `app/public`, `app/web`); otherwise set `WorkDir = targetWPDir`. Add a one-time store migration normalizing existing greedy `work_dir` entries (fixing `avalon-new`).

### 3.2 DB ownership, safe SQL import, URL rewrite, search-replace & `MYSQL_PWD` (F2 follow-up, F15, F17, F18, F48)
- **Files:** `import.go:633-720, 1020-1042, 1186-1210` (`loadSQLFile`, `ImportSQL`, `ResetDB`, `rewriteImportedURLs`), `snapshot.go:184-265` (`RestoreSnapshot`), `dbsearch.go:175-210`, `engine.go:195-224`.
- **Change:**
  - Guard `ResetDB` and `RestoreSnapshot` with `ownsDB(site)` so serve-only imports pointing at external schemas cannot be wiped (F2 follow-up).
  - Strip `USE` / `CREATE DATABASE` lines in the `loadSQLFile` stream filter and fail if `countTables` returns 0 tables afterwards (F18).
  - Wrap the SQL import stream in a counting reader that emits `cb("loading", ...)` progress every 10s with bytes read and total size (F48 / Muster M-4).
  - Switch `rewriteImportedURLs` to `wp search-replace --regex` with boundary lookahead so `https://example.com` does not rewrite `https://example.com.au` (F17).
  - Reject leading `-` in `dbsearch.go` search/replace terms with `400` and take an automatic pre-write snapshot on non-dry `SearchReplace` unless `no_snapshot: true` (F15).
  - Pass the root MySQL password via `MYSQL_PWD` in `cmd.Env` in `engine.go:210` and `import.go:1008` rather than `--password=` CLI args.

### 3.3 Consistent HTTP statuses, error codes, warnings & FastCGI chunked bodies (F9 API, F25, F34–F38)
- **Files:** `daemon.go`, `certs.go:187-224` (`InspectCert`), `engine.go`, `sites.go`, `router.go:522-525` (`proxyFCGIScript`), `model.go:18`.
- **Change:**
  - Map typed errors to `400`/`404`/`409`, return JSON bodies for unmatched routes (`404`) and wrong methods (`405`), and verify `wt.Site == slug` on worktree routes (F35).
  - Add optional `code` on error responses (`site_exists`, `source_db_unreachable`, `not_found`, `route_not_found`, `invalid_input`, `authorization_failed`, `not_wordpress`) while preserving all Invariant 7 substrings (F36).
  - Surface non-fatal stderr warnings (`SetDomain`, `AddWorktree`, `DeleteSite`) in an envelope `warnings: [...]` field on `apiResp` (F37).
  - Move `healKind`, `requireWordPress`, and `RestartSite` into `Engine` so CLI, TUI, and HTTP share identical validation and failure semantics (F38).
  - Add `ca` (`CAStatus`) to `GET /certs/{domain}` responses (F9 API / Muster M-1).
  - Redact `db_pass` and `admin_pass` from `GET /jobs` list snapshots while keeping them on `GET /jobs/{id}` (F25).
  - Handle chunked FastCGI request bodies when `req.ContentLength < 0` in `router.go:522-525` instead of sending `CONTENT_LENGTH=0`.
  - Stamp source builds via `-ldflags` or `runtime/debug` / `git describe --tags` (`<tag>-<N>-g<sha>`) so Muster's semver parser reads source builds cleanly (F34).

**Phase 3 verification:**
- `go test -race -count=1 ./...` with table tests for `ValidDomain`, `WorkDir` migration, `ownsDB` on `ResetDB`/`RestoreSnapshot`, `USE` stripping + 10s heartbeat, `--regex` boundary rewrite, leading-`-` rejection in `SearchReplace`, JSON 404/405 + typed error `code`, `GET /certs/{domain}` `ca` block, and chunked FastCGI `POST` body delivery.

---

## 5. Phase 4 — `v0.38.x` TUI, CLI, Test Coverage, Docs & Repo Cleanup (~2 days)

Addresses remaining UX, test gap, docs drift, and workspace hygiene items (F40–F47, section 8).

### 4.1 TUI responsiveness & crash guards (F40–F43)
- **Files:** `tui.go:212-296, 479-505, 620-633, 1409-1412, 1909`, `sites.go:1724` (`SiteDirSize`).
- **Change:**
  - Block or confirm `ctrl+c` during `modeBusy` so in-flight moves, imports, or deletes cannot be severed mid-write (F40).
  - Fix `SiteDirSize` to measure `site.WorkDir` instead of `P().Sites()/slug`, and compute sizes asynchronously in a `tea.Cmd` rather than during `View` (F41).
  - Add a nil-store guard after `OpenStore()` and `recover()` inside `runActionWith` so panics do not leave the terminal in raw mode (F42).
  - Move `EnsureDB`, SQL checks, `StopSite`, port dials, and `ps` scans out of UI-thread handlers into `tea.Cmd`s (F43).

### 4.2 CLI exit codes, `flag.FlagSet`, and MCP URL escaping (F45–F47)
- **Files:** `main.go:186-203, 911-935, 1945-1964`, `tui.go:2315`, `mcp.go:552-846`.
- **Change:**
  - Exit non-zero when `doctor` reports `fail` findings or `probe` reports an unhealthy site; write `runTUI` errors to stderr with exit code 1; add `--json` to inspection commands (`doctor`, `probe`, `list`, `wpinfo`) printing the daemon payload (F45).
  - Replace `positional()`'s hardcoded flag list with per-command `flag.FlagSet` parsing (F46).
  - Apply `url.PathEscape` to every interpolated `slug`, `id`, `name`, and `domain` segment in `mcp.go` (F47).

### 4.3 Test coverage expansion (F44)
- Raise package coverage from 36.3% to >=55%: add tests for `CreateSite`, `ImportSite`, `ImportSQL`, `SetDomain`, `SelfUpdate`, `handleDelete`, `handleImport`, `dbauth.go`, and a table test verifying every tool in `mcpTools()` maps to a valid handler in `APIServer.routes()`.

### 4.4 Docs, website & repository hygiene
- **Files:** `docs/MUSTER-INTEGRATION.md`, `site/docs-content.php`, `site/public/`, repo root.
- **Change:**
  - Update `docs/MUSTER-INTEGRATION.md` §2, §3, §5 to reflect the actual 76 MCP tools and 81 HTTP routes (including `?async=1`, `/jobs/{id}`, mail, Adminer, snapshots, checkpoints), add the §1 contract invariants, and update the version-floors table through `v0.38.0`.
  - Update `site/docs-content.php` to document `/.al`, the local CA, and current MCP tool counts, then rebuild/export `site/public/`.
  - Remove stale untracked files at repo root (`./agent-local` stale `dev` binary, obsolete `plan.md` from `v0.32.3`, empty `untitled.md`), delete merged branch `fix/data-safety`, and align or drop the `bitbucket` remote (currently behind at `v0.34.0`).

**Phase 4 verification:**
- `go test -race -cover ./...` (confirm `>=55%` coverage, zero data races).
- `go vet ./...` and `gofmt -l .` clean.
- Smoke test `agent-local tui --frame 120 --height 24 --tab doctor` and `agent-local doctor` exit codes.

---

## 6. Sequencing & Open Decisions

| Phase | Target | Focus | Est. Effort | Muster Impact |
|---|---|---|---|---|
| Phase 1 | `v0.36.2` | Idle sleep probe/race fixes, cert 397d + hint, log tailing/rotation, TLS noise filter, `doctor`, `front` TCP half-close | ~1 day | none |
| Phase 2 | `v0.37.0` | Sudoers removal, unprivileged front proxy, API/Apache DNS rebinding 421, Adminer pin, signed releases, `install.sh` + CI | ~2 days | safe (test `/yield` + `--setup`) |
| Phase 3 | `v0.38.0` | `ValidDomain`, `WorkDir` migration, `ownsDB` reset/restore, DB import heartbeat, typed HTTP statuses/codes, `GET /certs` `ca` | ~2 days | M-1 (`ca` block), M-2 (`code`/warnings), M-4 (heartbeat) |
| Phase 4 | `v0.38.x` | TUI `ctrl+c`/async size/recover, CLI exit codes + `FlagSet`, MCP path escaping, >=55% tests, docs + repo cleanup | ~2 days | docs sync |

### Decisions needed before Phase 2 / Phase 4
1. **Unprivileged `127.0.0.2:80/:443` bind (Phase 2 / F8):** Verify on this macOS version whether launchd with `UserName` binds `127.0.0.2:80/:443` without root, or commit straight to the root-owned `/Library/PrivilegedHelperTools/agent-local-front` helper.
   **Resolved 2026-11-23:** on macOS 26 (Darwin 25.6) the `UserName` job fails with `bind: permission denied`, because an unprivileged process may bind ports below 1024 only on the wildcard address. The front runs as root from the root-owned helper, logging to `/Library/Logs/agent-local-front.log`. A legacy root job keeps serving after an update without following binary changes, and `doctor` sends it to `agent-local alias`, which needs one password prompt.
2. **Release signing tool (Phase 2 / F29):** Choose `minisign` (tiny embedded verifier, single CI secret key) vs `cosign` (keyless/OIDC or keypair) for `checksums.txt` signing.
3. **`bitbucket` remote (Phase 4 hygiene):** Push `origin/main` to `bitbucket` (`git@ssh.bitbucket.org:efront_au/agent-local.git`) or remove the stale remote.

### Immediate 2-minute live-machine cleanup (independent of code changes)
- Run `agent-local doctor --fix` to comment out the 5 active `dns-shadow` lines in `/etc/hosts`.
- Remove stale root artifacts (`rm -f ./agent-local ./plan.md ./untitled.md`) so `./install.sh` from a checkout never installs an old `dev` binary.
