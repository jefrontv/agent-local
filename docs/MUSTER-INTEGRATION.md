# Integrating agent-local as a Muster local-stack provider

**Audience:** an agent working in `muster-ui`.
**Goal:** let a Muster site run on `agent-local` instead of LocalWP, selected per site, with LocalWP remaining the default.
**Scope:** agent-local replaces what LocalWP does — *run the local WordPress and own the local database*. It does **not** touch Muster's remote→local import logic (SSH, dump, download, search-replace); that pipeline stays exactly as it is and keeps working unchanged.

Every `path:line` below was read from the current tree. Re-check them before editing; they drift.

---

## 1. Why this is a small change

Two seams already generalize, which is most of the work done for you.

**The stack is already a per-site enum.**

```ts
// src/shared/site-types.ts:34
export type SiteLocalStack = 'plain' | 'mamp' | 'localwp'
```

Persisted per site (`src/main/store/slices/sites.ts` field list around `:17-21`), validated in `src/main/ipc/sites-payload-validation.ts:35`, rendered as a badge in `src/renderer/src/components/sites/SiteRow.tsx:19-22`.

**The local DB layer is already transport-agnostic.**

```ts
// src/main/sites/local-mysql-connection.ts:71-79
const socketPath = target.socketPath?.trim() ?? ''
if (socketPath.length > 0) {
  options.socketPath = socketPath
  return options
}
options.host = LOCAL_MYSQL_HOST
if (target.port) { options.port = target.port }
```

`localMysqlTargetForSite` (`:83-90`) reads `site.dbUser`, `config.dbPassword`, `site.dbSocket`, `site.dbPort`.

agent-local is **the TCP case that already exists** (the MAMP/DBngin shape): one shared MariaDB on `127.0.0.1:10360`, per-site database `al_<slug>` and user `al_<slug>`. Set `dbSocket: ''`, `dbPort: 10360`, `dbUser: 'al_<slug>'` and *nothing in the MySQL layer needs to change*.

**The import pipeline has exactly one local-stack dependency.**

```ts
// src/main/sites/pipeline-import.ts:55-58
ensureLocalWpSiteRunning: (
  sitePath: string,
  onStatus?: (message: string) => void
) => Promise<LocalWpRunningOutcome>
```

Consumed in one place:

```ts
// src/main/sites/pipeline-import.ts:194-208
async function startLocalStack(context, config, deps) {
  const outcome = await deps.ensureLocalWpSiteRunning(config.site.path, context.status)
  if (!outcome.ok) throw new SiteRunStepError(LOCALWP_STEP, outcome.message)
  if (!outcome.socketPath || outcome.socketPath === config.site.dbSocket) return config
  return { ...config, site: { ...config.site, dbSocket: outcome.socketPath } }
}
```

Everything downstream reads `config.site.*`. Generalize that one function and the import path is provider-agnostic.

---

## 2. agent-local in one page

Single Go binary, macOS. Install: `./install.sh` → `~/.local/bin/agent-local`.

| Fact | Value |
|---|---|
| Control API | `http://127.0.0.1:10809`, `Authorization: Bearer <token>` |
| Token | `~/.agent-local/token` (auto-created, 48 hex chars; read the file) |
| Response envelope | `{"ok":true,"data":…}` / `{"ok":false,"error":"…"}` |
| Database | one MariaDB, `127.0.0.1:10360`, per-site db+user `al_<slug>` |
| Serving | shared front on `1080`/`10443`; optional bare `:80`/`:443` via a root daemon on `127.0.0.2` |
| Docroot | created sites: `~/.agent-local/sites/<slug>/wp`; imported sites: served in place at their own docroot |
| Availability probe | binary on PATH **and** `GET /status` answers |

**Daemon lifecycle:** the CLI and the MCP server auto-start the daemon on first use. From Node, do not assume it is up — `GET /status`, and if it refuses, spawn `agent-local daemon --background` and poll `/status` for ~10s. It stays up across HTTP-front switches.

**No password prompts** as long as the user ran `agent-local sudo` once (a scoped `NOPASSWD` allowlist for hosts writes and cert trust). Without it, hosts/cert operations will try to raise a GUI dialog, which a background Electron process cannot satisfy — surface that as a setup step, do not swallow it.

---

## 3. Endpoints you need (all verified)

### Resolve a checkout path → site  *(added for this integration)*

Muster keys sites by `path`; agent-local keys by slug. This bridges them, and accepts any path **inside** the site.

```
GET /resolve?path=<url-encoded absolute path>
```

```jsonc
{"ok":true,"data":{
  "slug":"sulo", "matched":"wp_dir",          // wp_dir | work_dir | worktree
  "url":"http://sulo.pact", "https_url":"https://sulo.pact:10443",
  "domain":"sulo.pact",
  "wp_dir":"/Users/j/Documents/Sites/sulo/app/public",
  "php_version":"8.2", "running":true,
  "db":{"host":"127.0.0.1","port":10360,"socket":"","name":"al_sulo","user":"al_sulo","pass":"…"},
  "cert":{"domain":"sulo.pact","exists":true,"trusted":true,"not_after":"2036-08-05T18:13:17Z"},
  "site":{ /* full record */ }
}}
```

`404 {"ok":false,"error":"no site manages /tmp"}` when unmanaged — that is your "this site is not on agent-local" signal. A worktree path resolves to its parent site with `matched:"worktree"`, and `url`/`domain`/`wp_dir` describe the **preview**.

### Start / stop

```
POST /sites/{slug}/start     → same shape as /resolve minus site/cert/matched
POST /sites/{slug}/stop      → {"ok":true,"data":"stopped"}
POST /sites/{slug}/restart
```

`start` is idempotent and returns live DB details, so it is the direct analogue of `ensureLocalWpSiteRunning` — one call, no follow-up.

### Database

```
POST /sites/{slug}/db                → connection params (starts MariaDB if needed)
POST /sites/{slug}/db/query          {"sql":"…"}          → TSV with header row, site's schema is default
POST /db/query                       {"sql":"…"}          → server-wide (CREATE DATABASE, information_schema)
GET  /sites/{slug}/db/tables                              → name, rows, KB
POST /sites/{slug}/db/import         {"path":"/abs/x.sql.gz","keep_urls":false}
POST /sites/{slug}/db/export         {"path":"/abs/out.sql"}   // path optional
POST /sites/{slug}/db/reset
```

`db/import` streams (gzip auto-detected by magic bytes) and **replaces** the database, rewriting the dump's domains to the site's domain unless `keep_urls` is true.

> Muster may prefer its own `importLocalDatabase` (`src/main/sites/local-database-import.ts`) since it already handles progress and the option file. Both work; agent-local's is a streaming shortcut if you want it.

### Certificates  *(added for this integration)*

```
GET  /certs/{domain}          → {domain,cert_path,key_path,exists,trusted,not_after,issuer}
POST /certs/{domain}/trust    → issues if missing, trusts in the System keychain, returns the new status
```

`trusted` is read from the OS (`security verify-cert -p ssl`), not assumed — a user who revokes trust in Keychain Access is reported accurately. This is the agent-local twin of `localwpCert:{status,trust}` (`src/main/ipc/localwp-cert.ts:16`).

### Lifecycle / platform

```
GET    /status                       // liveness + counts + front + runtimes
GET    /sites                        // all sites
GET    /sites/{slug}                 // detail + worktrees + urls
POST   /sites                        {"name","domain?","php_version?","admin_user?","admin_pass?","admin_email?","title?","repo?","wp_version?"}
POST   /import                       {"source","name?","domain?","php_version?","copy?","sql_dump?","serve_only?","db_host?","db_port?","db_user?","db_pass?","db_name?"}
DELETE /sites/{slug}[?files=keep]
POST   /sites/{slug}/php             {"version":"8.3"}
POST   /sites/{slug}/domain          {"domain":"new.test"}
POST   /sites/{slug}/wp-cli          {"args":["option","get","home"]}
GET    /runtimes                     // installed PHP versions, db, http front
POST   /install                      {"what":"php","version":"8.3"}   // also mariadb|apache|wp-cli|brew
GET    /doctor      POST /doctor/fix
GET    /logs/{name}?lines=N          // mysql|apache|daemon|fpm-<slug>|<slug>
GET|POST /suffix                     // default domain suffix for new sites
GET|POST /front                      // "router" | "apache"
POST|DELETE /hosts                   {"domains":["a.test"]}
```

`POST /import` matters for the switch: `source` is a LocalWP site name **or** any docroot path, and it covers four database modes — live DB read from the docroot's `wp-config.php` (default), explicit `db_*`, `sql_dump`, or `serve_only` (touch no database). Default is serve-in-place; `copy: true` copies files under `~/.agent-local`.

An MCP server (`agent-local mcp`, 40 tools) mirrors all of the above if you would rather drive it that way than over HTTP.

---

## 4. Implementation plan

### 4.1 Widen the enum

```ts
// src/shared/site-types.ts:34
export type SiteLocalStack = 'plain' | 'mamp' | 'localwp' | 'agent-local'
```

- `src/main/ipc/sites-payload-validation.ts:35` — accept the new value.
- `src/renderer/src/components/sites/SiteRow.tsx:19-22` — add a label (`'agent-local': 'agent-local'`).
- Persisted field list in `src/main/store/slices/sites.ts` needs no change (it stores whatever the enum allows).

### 4.2 Add the client

New `src/main/sites/agent-local-host.ts`, mirroring the role of `localwp-host.ts` (injected surface, testable with no daemon):

```ts
export type AgentLocalHost = {
  platform: string
  homeDir: string
  /** Reads ~/.agent-local/token; null when agent-local was never run. */
  readToken: () => Promise<string | null>
  request: (method: string, path: string, body?: unknown) => Promise<AgentLocalResponse>
  spawnDaemon: () => Promise<void>
}
export type AgentLocalResponse = { ok: boolean; data?: unknown; error?: string }
```

Rules: bound every request (5s for reads, longer for `start`/`import`); treat a connection refusal as "daemon down → spawn once → retry", not as an error; never log `db.pass`.

### 4.3 Add site control

New `src/main/sites/agent-local-site-control.ts`, matching the existing contract in `src/main/sites/localwp-site-control.ts:94-180` so the IPC layer needs no new shapes:

| LocalWP function | agent-local equivalent |
|---|---|
| `ensureSiteRunning(sitePath)` → `LocalWpControlOutcome` | `GET /resolve?path=` → `POST /sites/{slug}/start` |
| `stopSite(sitePath)` | resolve → `POST /sites/{slug}/stop` |
| `waitForSocket(sitePath)` | not needed — fixed TCP port; return `''` |
| `detectLocalWpStack(sitePath)` | `GET /resolve?path=` → found ⇒ `'agent-local'` |
| `resolveLocalCli(host)` | not needed |

`LocalWpControlOutcome` carries `socketPath`. Return `''` for agent-local and let the caller keep `dbPort`. **Do not** invent a fake socket path — the TCP branch in `buildLocalMysqlConnectionOptions` is selected precisely by an empty socket.

### 4.4 Generalize the import dependency

In `src/main/sites/pipeline-import.ts`:

1. Rename the dependency `ensureLocalWpSiteRunning` → `ensureLocalSiteRunning` (`:55-58`, `:100`, `:199`) and widen its outcome:

```ts
ensureLocalSiteRunning: (
  site: Pick<Site, 'path' | 'localStack'>,
  onStatus?: (message: string) => void
) => Promise<{
  ok: boolean
  message: string
  socketPath: string          // '' for agent-local
  dbPort?: number | null      // 10360 for agent-local
  dbUser?: string             // al_<slug>
  dbPassword?: string         // fetched live; never persisted
}>
```

2. `createDefaultSiteImportDependencies` (`:98-122`) dispatches on `site.localStack` — LocalWP for `'localwp'`, the new module for `'agent-local'`, existing behaviour otherwise.

3. `startLocalStack` (`:194-208`) adopts whichever transport came back:

```ts
const next = { ...config, site: { ...config.site } }
if (outcome.socketPath) next.site.dbSocket = outcome.socketPath
else {
  next.site.dbSocket = ''
  if (outcome.dbPort) next.site.dbPort = outcome.dbPort
  if (outcome.dbUser) next.site.dbUser = outcome.dbUser
}
if (outcome.dbPassword) next.dbPassword = outcome.dbPassword
return next
```

`SiteRunConfig.dbPassword` already exists (`local-mysql-connection.ts:86`), so the password can ride the run config **without** entering the secret store. Prefer that: agent-local can hand it out any time via `POST /sites/{slug}/db`, so persisting a copy only creates a staleness bug.

Keep the step id `LOCALWP_STEP` or rename to `LOCAL_STACK_STEP` — cosmetic, but the string surfaces in the UI.

### 4.5 Dispatch the IPC channels

`src/main/ipc/site-stacks.ts` currently calls LocalWP functions directly (`:14-36` imports, handlers below). Add a dispatch on `site.localStack` for: `detect`, `start`, `stop`, `resolveSocket`, `previewMigration`, `runMigration`.

For agent-local, `runMigration` (the "make this plain site into a managed site" flow, LocalWP's version at `src/main/sites/localwp-migration.ts`) becomes **one call**:

```
POST /import  {"source": site.path, "name": slug, "domain": request.domain, "php_version": …}
```

No GraphQL (`localwp-site-creation.ts` posts to `http://localhost:10888/graphql`), no `app/public` relocation, no `wp-config` DB rewrite of your own — agent-local does the config rewrite and keeps `wp-config.php.agent-local.bak`. Then persist what the response reports:

```ts
store.updateSite(site.id, {
  localStack: 'agent-local',
  localWpRoot: relative(site.path, data.wp_dir),   // '' when the docroot IS the path
  localDomain: data.domain,
  dbSocket: '',
  dbPort: data.db.port,      // 10360
  dbUser: data.db.user,      // al_<slug>
  phpVersion: data.php_version
})
```

Mirror the progress forwarding already in place (`src/main/ipc/site-stack-progress.ts`) — `POST /import` is a single blocking call, so emit your own stage lines around it rather than expecting a stream.

### 4.6 Docroot path rule

`src/main/sites/localwp-repo-path.ts` remaps a LocalWP site shell to `app/public`, and `src/main/ipc/repos.ts:176-181` and `worktrees.ts:547-549` depend on that. agent-local layouts:

- **imported in place** — the docroot *is* `site.path`; `localWpRoot` is `''`. No remap.
- **created by agent-local** — docroot is `<path>/wp`; `localWpRoot` is `'wp'`.

Do not extend the LocalWP `app/public` remap to agent-local. Read `wp_dir` from `/resolve` and derive `localWpRoot` from it; that is authoritative and survives future layout changes.

### 4.7 Certificates

`src/main/ipc/localwp-cert.ts:16` registers `localwpCert:status` / `localwpCert:trust`. Either add a stack dispatch inside those handlers (keeps renderer channels stable — recommended) or add `siteCert:*` channels and migrate callers. agent-local mapping is direct: `GET /certs/{domain}` and `POST /certs/{domain}/trust`, and the response fields already match what the UI shows (exists / trusted / expiry).

---

## 5. Feature parity: know what you lose

| Capability | LocalWP | agent-local |
|---|---|---|
| Per-site PHP version | yes | yes (`POST /sites/{slug}/php`, live pool restart) |
| Install a new PHP version | via Local UI | `POST /install` (~1 min, verified) |
| Per-site MySQL | per-site mysqld on a unix socket | one shared MariaDB, per-site db+user over TCP |
| HTTPS + trusted cert | yes | yes, per domain, trust queryable |
| Bare domains (no port) | yes (`*:80` wildcard) | yes (`127.0.0.2:80` via root daemon) |
| Site creation / import | GraphQL + Local UI | `POST /sites` / `POST /import` |
| Branch previews on their own domain | no | yes (git worktrees, shared uploads, APFS clones) |
| Mail catcher | mailpit | **absent** |
| DB GUI (Adminer/phpMyAdmin) | yes | **absent** (`db/query`, `db/tables` instead) |
| Xdebug toggle | yes | **absent** |
| Backups / snapshots | yes | **absent** (`db/export` only) |
| Push/event stream of state changes | — | **absent**: poll `GET /status` / `/sites` |
| Per-site php.ini overrides | yes | **absent** (pool config is generated) |
| Multisite | yes | **untested** |

If Muster surfaces mail, Xdebug or backups for LocalWP sites, gate those affordances on `localStack === 'localwp'` rather than letting them fail.

### Port 80 coexistence (matters if both stacks are installed)

The kernel is happy for agent-local's `127.0.0.2:80` listener to coexist with LocalWP's wildcard `*:80` in either order — verified. But **LocalWP pre-checks port 80 and refuses to start when anything is listening**. So when Muster is about to start a LocalWP site and agent-local's bare-URL daemon holds the port, run:

```
agent-local yield 60        # frees :80/:443 for 60s, then re-binds automatically
```

agent-local sites stay reachable on `:1080` throughout, and the specific-address bind is reclaimed afterwards. Worth wiring into the LocalWP start path as a pre-step when `agent-local` is installed; there is no HTTP route for it yet (CLI only) — ask if you want one.

---

## 6. Acceptance criteria

A site with `localStack: 'agent-local'` must:

1. **Resolve** — `GET /resolve?path=<site.path>` returns a slug; a path *inside* the docroot resolves to the same slug.
2. **Start** — `siteStacks:start` reports running, and `GET <url>` returns 200.
3. **Connect** — `checkLocalMysqlConnection` succeeds with `dbSocket: ''`, `dbPort: 10360`, `dbUser: al_<slug>`, using the password from the run config (never from the secret store).
4. **Import** — a full remote import (`exportDatabase` + `exportFiles` + `wpSearchReplace`) completes and the site serves the imported content on its local domain.
5. **Not regress LocalWP** — an unchanged `localStack: 'localwp'` site behaves exactly as before; every LocalWP-specific test still passes.
6. **Degrade honestly** — with the daemon stopped, `detect` reports unavailable with an actionable message instead of throwing; with agent-local not installed at all, the option is hidden or disabled.
7. **Certs** — status shows `trusted: true` after `POST /certs/{domain}/trust`.

Suggested test seams: fake `AgentLocalHost.request` for unit tests (no daemon, no MySQL, mirroring how `LocalWpHost` is faked today); one integration test behind an env guard that talks to a real daemon.

---

## 7. Sequencing

1. Enum + validation + badge, and the client (`agent-local-host.ts`) with unit tests against a fake `request`.
2. Site control module + `pipeline-import.ts` dependency generalization. **Import works end to end at this point** — the smallest useful milestone.
3. IPC dispatch for detect/start/stop, then `runMigration` via `POST /import`.
4. Cert dispatch, docroot rule, renderer affordance to pick the stack.
5. Parity gating for mail/Xdebug/backups.

LocalWP stays the default throughout; nothing changes for existing sites until a user switches one.

---

## 8. Quick manual check before you code

```sh
agent-local doctor                       # stack health
agent-local resolve /path/to/a/site      # path → slug
agent-local cert mysite.test             # exists / trusted / expiry
TOKEN=$(agent-local api-token)
curl -sH "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:10809/resolve?path=$(python3 -c 'import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))' /path/to/a/site)"
```

Questions about agent-local's side — a missing route, a different response shape, an HTTP route for `yield` — are cheap to add. Ask rather than working around them.
