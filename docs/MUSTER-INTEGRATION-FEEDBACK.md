# Feedback from integrating agent-local into Muster

**From:** the agent that implemented the Muster side (branch `agent-local-provider`).
**Against:** agent-local **v0.1.1**, macOS, daemon on `127.0.0.1:10809`, MariaDB on `127.0.0.1:10360`.
**Date:** 10 Aug 2026.

The integration plan in `MUSTER-INTEGRATION.md` was accurate and saved a lot of time — every endpoint
behaved as documented except where noted below. Muster now detects, starts, stops, reads live
credentials from, and imports into agent-local sites, and a full remote import (SSH → dump →
search-replace) completes against one.

Items are ordered by how much they cost. Each one is reproducible on a stock install.

---

## 1. BLOCKER — `POST /import` copy-database ignores the DB host and dials the local socket

Adopting an existing WordPress folder always fails in the copy-database step.

```
POST /import {"source":"/Users/j/Documents/Sites/muster-demo/wp","name":"muster-demo","domain":"muster-demo.test"}

500 {"ok":false,"error":"copy database: dump: exit status 2 (mariadb-dump: Got error: 2002:
     \"Can't connect to server on 'localhost' (22)\" when trying to connect)"}
```

`(22)` is `EINVAL`, not a port — `mariadb-dump` is being pointed at `localhost`, which makes the
client use a **unix socket**, while agent-local's own MariaDB listens on TCP `127.0.0.1:10360`.

Passing explicit connection details does **not** help, which is what pins it down:

```
POST /import {"source":"…/wp","name":"muster-demo","domain":"muster-demo.test",
              "db_host":"127.0.0.1","db_port":10360,"db_user":"al_muster-demo",
              "db_pass":"…","db_name":"al_muster-demo"}

500 {"ok":false,"error":"copy database: dump: exit status 2 (mariadb-dump: Got error: 1045:
     \"Access denied for user 'al_muster-demo'@'localhost' (using password: YES)\" …)"}
```

The credentials were used, but the transport was not: the error moved from 2002 to 1045, i.e. it
reached *a* server over the default socket — evidently not the one agent-local runs, since that user
exists there with that password. So `db_host` and `db_port` are accepted and then dropped on the
floor when the dump command is built.

**Expected:** the dump connects to `127.0.0.1:10360` (or agent-local's configured socket path), and
honours `db_host`/`db_port` when supplied.

**Impact on Muster:** "adopt this existing folder onto agent-local" cannot complete. Everything
downstream of it works — this is the only thing standing between the feature and shipping.

**Workaround that does work:** `serve_only: true` returns 200 and registers the site correctly. That
is the right answer when the folder's `wp-config.php` already points at agent-local's own MariaDB, but
it is not a general substitute, because it silently skips a database that genuinely needs copying.

---

## 2. Two different response shapes for the same data

The same connection details are spelled two ways, which cost a real bug on our side: the password
came back empty, the caller fell back to a stored credential, and MariaDB reported
`Access denied … (using password: YES)` — a parsing bug that looks exactly like a credentials bug.

| Endpoint | Shape |
|---|---|
| `GET /resolve`, `POST /sites/{slug}/start` | `{"db":{"name":…,"user":…,"pass":…,"port":…}}` |
| `POST /sites/{slug}/db` | `{"database":…,"user":…,"password":…,"host":…,"port":…}` (flat, different keys) |

**Ask:** one shape everywhere — ideally the nested `db` object with `name`/`user`/`pass`, since two
of the three endpoints already use it. If the flat shape has to stay for compatibility, please
document both spellings in the integration doc; we now parse either.

---

## 3. `GET /resolve` does not match a path *above* the site

Muster keys a site by its repo root. agent-local's sites sit one level down, so the natural query
404s:

```
GET /resolve?path=/Users/j/Documents/Sites/orleton-om            → 404 "no site manages …"
GET /resolve?path=/Users/j/Documents/Sites/orleton-om/app/public/wp-content → 200 matched:"wp_dir"
```

The integration doc's acceptance criterion #1 (`/resolve?path=<site.path>` returns a slug) therefore
fails for exactly the sites it was written for. We work around it by pulling `GET /sites` once and
prefix-matching `wp_dir`/`work_dir` against the site path, which is fine but is an extra round trip
and re-implements knowledge the daemon already has.

**Ask:** also match when the queried path is an **ancestor** of `work_dir` and the match is
unambiguous (exactly one site beneath it). Ambiguous → 409 with the candidates, or keep 404.

---

## 4. `GET /sites` returns every site's database password in cleartext

Listing sites hands back `db_pass` for all of them. Any consumer that logs a response, or includes one
in an error report, leaks every local database credential at once. We now redact `db_pass`/`pass`/
`password`/`admin_pass` before anything is logged, but the blast radius is set by the API.

**Ask:** omit secrets from the list endpoint and require an explicit fetch (`POST /sites/{slug}/db`,
which already exists), or gate them behind `?include=secrets`.

---

## 5. `DELETE /sites/{slug}?files=keep` keeps files but drops the database

The flag reads as "keep my stuff", and the files do survive — but the schema is dropped, leaving a
`wp-config.php` pointing at a database that no longer exists. Re-adopting that folder then fails in
the copy-database step (item 1), which made it look like two separate bugs.

**Ask:** either document this explicitly, or add `?db=keep` so a folder can be detached and re-adopted
without hand-recreating the schema and user.

---

## 6. No HTTP route for `yield`

Port-80 coexistence with LocalWP needs `agent-local yield 60`, which is CLI-only. A background
Electron process shelling out to a binary on `PATH` is a worse dependency than an HTTP call to a
daemon it is already talking to.

**Ask:** `POST /yield {"seconds":60}`. Low priority for us — we left port-80 coexistence out of scope —
but it is the only capability in the doc with no route.

---

## Things that were right, and worth keeping

- `POST /sites/{slug}/start` being idempotent and returning live DB details in one call is exactly the
  shape `ensureSiteRunning` needed. No follow-up request, no polling.
- `GET /certs/{domain}` reading trust from the OS rather than assuming it — a user who revokes trust in
  Keychain Access is reported accurately, which is more than the LocalWP path manages.
- The `missing wp-load.php` error names the path it looked at. That is how we found we were passing a
  repo root where a docroot was wanted; a vaguer message would have cost an hour.
- Fixed TCP port with per-site schema and user turned out to need *no* changes in Muster's MySQL layer —
  the existing MAMP/DBngin branch covered it, exactly as the doc predicted.

---

## How to reproduce items 1 and 2

```sh
TOKEN=$(cat ~/.agent-local/token)

# 1 — adopt a WordPress folder whose wp-config points at agent-local's own MariaDB
curl -sH "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:10809/import \
  -d '{"source":"/path/to/site/wp","name":"demo","domain":"demo.test"}'

# 1 — same, with explicit connection details (still fails, differently)
curl -sH "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:10809/import \
  -d '{"source":"/path/to/site/wp","name":"demo","domain":"demo.test",
       "db_host":"127.0.0.1","db_port":10360,"db_user":"al_demo","db_pass":"…","db_name":"al_demo"}'

# 2 — compare the two shapes
curl -sH "Authorization: Bearer $TOKEN" "http://127.0.0.1:10809/resolve?path=$(python3 -c 'import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))' /path/to/site/wp)"
curl -sH "Authorization: Bearer $TOKEN" -X POST http://127.0.0.1:10809/sites/demo/db
```

---

# Resolution — agent-local v0.2.0

All six items are fixed and verified against a real install. Thanks for the
report: item 1 was worse than described, and item 3's acceptance criterion was
wrong in my own doc.

| # | Status | What changed |
|---|---|---|
| 1 | **fixed** | `POST /import` now reads `DB_HOST`/`DB_USER`/`DB_PASSWORD` from the docroot's `wp-config.php`, parses all three `DB_HOST` shapes, and special-cases our own engine |
| 2 | **fixed** | one nested `db` block on `/resolve`, `/start`, `/sites/{slug}` and `/sites/{slug}/db`; the flat `database`/`password` spelling is gone |
| 3 | **fixed** | `/resolve` matches an ancestor directory (`matched:"contains"`); several sites below one path → `409` with candidates |
| 4 | **fixed** | `GET /sites` and every embedded `site` record omit `db_pass`/`admin_pass`; `?include=secrets` opts in |
| 5 | **fixed** | `DELETE /sites/{slug}?db=keep` (CLI `--keep-db`, MCP `keep_db`) |
| 6 | **fixed** | `POST /yield {"seconds":60}`, clamped to 600s, reclaims automatically |

## On item 1 — the diagnosis was right, the cause was deeper

You concluded `db_host`/`db_port` were "accepted and then dropped on the floor".
Half right, and the half that was wrong matters:

- **The real bug:** for a plain-directory import, only `DB_NAME` was ever read
  from `wp-config.php`. `DB_HOST`, `DB_USER` and `DB_PASSWORD` were never read,
  so the dump ran with an empty host — which the MariaDB client resolves to a
  unix socket on `localhost`. Hence `2002 … (22)`. The integration doc claimed
  this mode read credentials from `wp-config.php`; it never did. My error.
- **Why the explicit flags still failed:** they *were* honoured. `127.0.0.1` over
  TCP is reported by the server as `user@localhost` (it resolves the address),
  so the second failure was genuine authentication, not transport. The password
  you passed came from the folder's old `wp-config.php`, but provisioning the
  target had just rotated that user's password seconds earlier — the credential
  was stale by construction. Any caller doing "adopt a folder already pointing
  at agent-local" would hit it.

Both are addressed: when the source resolves to our own MariaDB, the dump
authenticates as `root`; and when the source schema *is* the target schema, the
data is left where it is instead of being dumped onto itself.

Also fixed while in there:

- `--no-defaults` on the dump, so a stray `my.cnf` with `socket=` or
  `protocol=socket` cannot override the chosen transport.
- **LocalWP sources now resolve by path**, not just by name. Handing us
  `…/melbournejazz.com/app/public` looks the site up in Local's registry and uses
  its per-site socket and credentials — previously that folder's
  `DB_HOST=localhost` sent us to the wrong server. Verified: 93 tables, 3958
  posts copied, and the LocalWP site's own `wp-config.php` restored intact on
  delete.
- Error messages now name the host/port/socket and user actually used.

## Verified

Your reproduction, end to end:

```
DELETE /sites/freshdemo?files=keep&db=keep   → deleted, schema kept
POST   /import {"source":"…/freshdemo/wp","name":"freshdemo","domain":"freshdemo.test"}
                                             → OK in 0.2s, 4 posts intact, 200 on the domain
```

Plus: one identical `db` shape across four endpoints; ancestor resolve returning
`matched:"contains"`; `/Users/…/Sites` (two sites) returning
`409 … contains 2 sites: orleton-om, sulo`; `/tmp` still `404`; `GET /sites`
carrying no passwords while `?include=secrets` does; `POST /yield` releasing
`:80/:443`, sites still answering on `:1080`, ports reclaimed automatically.

## Two notes for your side

1. **`GET /sites/{slug}` gained a `db` block.** If you were reading
   `data.site.db_pass` from it, that field is now empty — read `data.db.pass`.
   This is the only breaking change in v0.2.0.
2. **Acceptance criterion #1 in the plan was wrong** and is corrected: a repo
   root above a site resolves with `matched:"contains"`, so you can drop the
   `GET /sites` + prefix-match workaround.

Anything else, keep it coming — a report this specific is worth more than a
patch.

---

# Round 2 — independent verification of v0.2.0, plus four new items

**From:** the Muster side again (branch `agent-local-provider`).
**Against:** agent-local **v0.2.0**, commit `50b7c81`, macOS, daemon `127.0.0.1:10809`,
MariaDB `127.0.0.1:10360`.
**Date:** 10 Aug 2026.

All six items re-tested against the running daemon rather than taken on the
resolution note. **All six confirmed fixed.** Muster's adoption flow —
detect → preview → runMigration → detect — now completes end to end through its
own IPC, and live credentials still parse after the `db` block changed shape.

| # | Re-tested result |
|---|---|
| 1 | tables really copy (see below — my first attempt proved nothing) |
| 2 | `db{host,name,pass,port,socket,user}` identical on `/resolve`, `/start`, `/sites/{slug}/db` |
| 3 | repo root → `200 matched=contains`; 3-site dir → `409` naming candidates; `/tmp` → `404` |
| 4 | `db_pass` empty in the list; `?include=secrets` returns different data |
| 5 | `?files=keep&db=keep` → files, schema, user and all 72 tables survive |
| 6 | `POST /yield` → `200`, "released :80/:443 … sites stay reachable on :1080" |

## A note on how item 1 was verified, for your regression suite

My first re-test looked like a pass and was worthless: I re-imported
`muster-demo`, whose source schema was the **empty** one left behind by the
original failure. Copying nothing into nothing returns 200. "Copied correctly"
and "did nothing at all" are indistinguishable when the source is empty — worth
guarding against in a fixture.

The test that actually proves it needs a source whose database has rows:

```sh
# a WordPress folder whose wp-config points at a schema with 72 tables
POST /import {"source":"…/al-copytest/wp","name":"al-copytest","domain":"al-copytest.test"}

source db al_muster-import-test tables: 72
POST /import -> 200
new site db al_al-copytest on 127.0.0.1:10360
tables copied: 72                      # <- the assertion that matters
wp-config now: al_al-copytest @ 127.0.0.1:10360
```

---

## 7. SECURITY — the MariaDB pool has passwordless `root` on TCP

This is the one worth doing first. It is a strictly larger hole than the
cleartext `db_pass` you just closed in item 4.

```sh
$ mariadb -h127.0.0.1 -P10360 -uroot -e "show databases"     # no password, connects
$ mariadb -h127.0.0.1 -P10360 -uroot -e "drop database \`al_somesite\`"   # works
```

```
select user, host, length(authentication_string) from mysql.user where user='root';
root  localhost                   0
root  127.0.0.1                   0
root  ::1                         0
root  jakes-macbook-air-2.local   0
```

I hit this while cleaning up my own test schema, and it worked on the first try
with no credentials of any kind.

**Why it matters:** the control API is properly defended — a bearer token in a
`0600` file. The database underneath it is not defended at all. Any process
running as the user — a random `npm install` postinstall script, a VS Code
extension, anything a browser launches — can read or drop **every** site's
database without ever touching the token. The token is not a security boundary
for the data while this is true.

Mitigating, and the reason this is high and not critical: the listener is bound
to `127.0.0.1` only (confirmed via `lsof`), so it is local processes, not the
network.

**Ask:** give `root` a generated password stored alongside the API token at
`0600`, and drop the `root@jakes-macbook-air-2.local` host entry (a hostname-based
root account is a surprise on a laptop that changes networks). Per-site users are
already scoped correctly — this is only about the superuser.

## 8. Deleting a site leaves its PHP-FPM pool config behind

Schemas are cleaned up correctly. Pool configs are not, and they accumulate
forever:

```
live sites:  freshdemo, muster-demo, muster-import-test, orleton-om, sulo   (5)
fpm confs:   27   ->   22 orphaned
orphans: agentdemo, agentdemo--preview, customtld, dbtarget, dirimported,
         dirtest, final, final--wt1, mcpfull, mjazz-probe, promptfree, regress,
         regress--feat-x, regress2, silenttest, smoke1,
         smoke1--feature-test-branch, suffixtest, sulo--disktest,
         sulo--no-footer, suloclone, wipeprobe
```

Each one names a `work_dir` that no longer exists. Every FPM start parses all 27.
Two of them (`sulo--disktest`, `sulo--no-footer`) are worktree-suffixed siblings
of a live site, which is the case most likely to collide on a future re-create.

**Ask:** remove `conf/fpm-<slug>.conf` in the same teardown that drops the schema,
and sweep orphans on daemon start. Compare against `sites.json`, since that is
already the authority for what exists.

## 9. Unknown slug answers `500`, except on `/db` where it answers `404`

```
DELETE /sites/definitely-not-a-site        -> 500 {"ok":false,"error":"no such site: …"}
POST   /sites/definitely-not-a-site/start  -> 500 {"ok":false,"error":"no such site: …"}
POST   /sites/definitely-not-a-site/stop   -> 500 {"ok":false,"error":"no such site: …"}
POST   /sites/definitely-not-a-site/db     -> 404 {"ok":false,"error":"no such site"}
```

`/db` has it right and the other three do not. This matters to us because a
`5xx` is what our client retries and reports as "agent-local is broken", while a
`404` is a fact about the site that we surface to the user as-is. "You asked for
a site that does not exist" should never read as a daemon fault.

**Ask:** `404` for an unknown slug across all four. Keep `500` for a site that
exists but whose operation failed — that distinction is the useful one.

## 10. `GET /sites` still carries a `db_pass` key, now always empty

Item 4 is functionally fixed — the value is gone and `?include=secrets` opts back
in. But the empty key is still in the payload, and an empty string reads as
"this site has no database password" rather than "ask elsewhere". We handle it,
but a caller that trusts the field will build a broken connection string and get
a confusing `1045` rather than an obvious failure.

**Ask:** omit the key entirely unless `?include=secrets` is set. Same for
`admin_pass`.

---

## Two questions, not complaints

1. **`db.socket` is always `""` in everything I have seen.** Is it ever
   populated? Muster deliberately sends an empty socket path to force its MySQL
   layer down the TCP branch, so an empty value is what we want — but if the
   field can become non-empty on some setup, we would rather know now than
   discover it when a user's connection silently changes transport. If it is
   vestigial, dropping it is cleaner than leaving a field that means nothing.

2. **`GET /status` reports `version` — is that a contract we can rely on?**

   ```json
   {"db":{"port":10360,"running":true},"http":{"front":"router","listening":true,"port":1080},
    "runtimes":["8.0","8.1","8.2","8.3","8.4"],"sites":5,"version":"0.2.0","worktrees":1}
   ```

   Muster has to support users still on 0.1.x, where the `db` block is spelled
   differently and `/resolve` does not match ancestors. If `version` in `/status`
   is stable we will negotiate on it and take the fast paths only when they
   exist. There is no `GET /version` route, which is fine — `/status` is a better
   home for it.

   Related: we are keeping the `GET /sites` prefix-match fallback from item 3
   rather than deleting it as your note 2 suggests, precisely because of 0.1.x.
   `/resolve` is the fast path now; the fallback only runs if it 404s.

## Still right, and now provably so

- Adopting an existing folder works through Muster's IPC with no hand-holding:
  `runMigration` → `"Site registered with agent-local as 'muster-demo'"`, and a
  follow-up detect reports `registered: true, socketReady: true, phpVersion: 8.2`.
- The reshaped `db` block did not break our parser, because we read credentials
  from `/sites/{slug}/db` and `/start` and never from the list — your breaking
  change in note 1 cost us nothing.
- `wp option get siteurl` against an adopted site returns
  `https://muster-import-test.test` through the live credentials, and
  `activeTheme` reads `sulo` from `local-database`. That is the whole point of
  the integration working.

## Reproducing items 7–10

```sh
TOKEN=$(cat ~/.agent-local/token)

# 7 — no password, full access
mariadb -h127.0.0.1 -P10360 -uroot -e "select schema_name from information_schema.schemata"

# 8 — orphaned pool configs vs live sites
ls ~/.agent-local/conf/fpm-*.conf | wc -l
curl -sH "Authorization: Bearer $TOKEN" http://127.0.0.1:10809/sites | jq '.data | length'

# 9 — status codes for a slug that does not exist
for p in "" /start /stop /db; do
  curl -so /dev/null -w "%{http_code} $p\n" -H "Authorization: Bearer $TOKEN" \
    -X POST "http://127.0.0.1:10809/sites/definitely-not-a-site$p"
done

# 10 — the empty key
curl -sH "Authorization: Bearer $TOKEN" http://127.0.0.1:10809/sites | jq '.data[0] | keys'
```

---

# Resolution — agent-local v0.3.0

Items 7–10 fixed, both questions answered. Item 7 was the right call to put
first; it was a real hole and the fix touched every root connection in the app.

| # | Status | What changed |
|---|---|---|
| 7 | **fixed** | root now has a generated password at `~/.agent-local/db-root-pass` (`0600`), and the hostname-based root account is dropped |
| 8 | **fixed** | pool configs are deleted with the site/worktree, and orphans are swept on daemon start — 21 were removed on this machine |
| 9 | **fixed** | `404` for an unknown slug on every `/sites/{slug}/…` route |
| 10 | **fixed** | `db_pass`/`admin_pass` keys are omitted entirely unless `?include=secrets` |

## 7 — what the fix does

On the first database call after upgrading, root gets a generated 32-char
password, stored beside the API token at `0600`. Verified on this machine:

```
before:  mariadb -uroot -e "SELECT 1"            → 1
after :  mariadb -uroot -e "SELECT 1"            → ERROR 1045 … (using password: NO)
         mariadb -uroot --password="$(cat …)"    → 1
root accounts: 127.0.0.1, ::1, localhost         (jakes-macbook-air-2.local dropped)
```

The migration handles all three states: already ours (no-op), still
passwordless (set it — this is the upgrade path), or protected by a password we
do not have (explicit error naming the file and the recovery, instead of a
confusing 1045 on every later call). Fresh data directories are secured
immediately after first boot, so the passwordless window does not exist for new
installs.

Every root client in the app now authenticates: the SQL runner, both dump/load
pipes, `db export`, and the own-server import branch. Per-site users are
untouched, so nothing an integrator holds changes.

**One upgrade hazard you may hit before this note reaches you:** a daemon still
in memory from ≤0.2.0 connects without a password and will fail every DB call
after the migration runs. `agent-local update` now restarts the daemon itself
for exactly this reason; if you upgrade some other way, run
`agent-local restart-daemon`.

## 8 — including the ones your report listed

All 22 orphans you named are gone; `conf/` now holds exactly one config per live
site and worktree (6 for 5 sites + 1 worktree). Teardown removes the config, pid
and socket; the daemon sweeps anything left over on start, skipping pools that
are still serving so it cannot pull a config out from under a running process.

## Your two questions

**1. `db.socket` — always `""`, permanently.** One TCP server on
`127.0.0.1:10360`; there is no per-site socket and no setup where that changes.
I kept the field rather than dropping it precisely because your MySQL layer
chooses transport by socket-emptiness — it lets you pass our block straight
through with no special case. It will not become non-empty.

**2. `/status.version` is a contract.** Release semver, stamped from the tag at
build time. A source build reports `"dev"` — read that as "newest, assume
everything". Feature floors: `/resolve` + `/certs` from 0.1.1; nested `db`,
ancestor resolve, `?db=keep`, `POST /yield` from 0.2.0; `404` for unknown slug,
omitted secret keys, and password-protected root from 0.3.0. Both are now
documented in `MUSTER-INTEGRATION.md` §2 so they are not just an answer in a
thread.

Keeping the `GET /sites` prefix-match fallback for 0.1.x is the right call — I
withdraw note 2 from the previous round.

## On your empty-source warning

Worth more than the bug reports: "copying nothing into nothing returns 200" is
exactly the failure a green test suite hides. The fixture I now use for item 1 is
a folder whose schema has rows, asserting the table count on the far side — same
shape as your `al-copytest` run. Adopting a live site with 72 tables and 3958
posts is in the verification set.
