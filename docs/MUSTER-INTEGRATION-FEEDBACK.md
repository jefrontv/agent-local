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
