# agent-local site

The marketing site for agent-local: a WordPress theme served, naturally, by agent-local.

## Serve it

```sh
agent-local create agentlocal-site --domain agentlocal.test
ln -s "$(pwd)" ~/Sites/agentlocal-site/wp/wp-content/themes/agent-local
agent-local wp agentlocal-site -- theme activate agent-local
agent-local wp agentlocal-site -- post create --post_type=page --post_title=Home --post_status=publish --porcelain
# set the returned page id as the static front page:
agent-local wp agentlocal-site -- option update show_on_front page
agent-local wp agentlocal-site -- option update page_on_front <id>
```

Activating the theme sets a permalink structure (WordPress defaults to "plain", under
which `/docs/` cannot route) and flushes the rewrite rules. Activated some other way,
or already on plain permalinks? `agent-local wp agentlocal-site -- rewrite structure '/%postname%/'`
then `-- rewrite flush` does the same by hand.

## Build the assets

Compiled CSS/JS live in `dist/` and are committed, so the theme works without Node.
To change styles or scripts:

```sh
npm install
npm run build     # sass + esbuild, once
npm run watch     # rebuild on save
```

Sources are `src/scss/` and `src/js/`; `style.css` is only the theme header.

## Publishing

The public copy at https://al.tools.efront.dev/ is static: the page is the theme's
shipped defaults rendered once, with WordPress's own boilerplate stripped, beside
`dist/` and `assets/`. No WordPress on the host, nothing to patch.

```sh
npm run export        # builds, renders http://agentlocal.test/ into public/
git commit -am "..." && git push
```

Then run the Muster deploy for the `agent-local` site: its "Publish static site" step
(`.muster/steps/deploy-static-site.sh`) pulls this repository on the server and mirrors
`site/public` + `dist` + `assets` into the docroot, leaving the host's `.htaccess` and
`.well-known` alone. Muster's built-in pre-check currently expects a WordPress root on
the server; until that allows plain docroots, the same script runs over Muster's SSH
session with `run_ssh_command` (see the commit that added it).

## Editing copy

Every string on the page ships as a default in `front-page.php`, so the theme renders
complete with no plugins. With ACF PRO installed, the "Front Page" field group
(`acf-json/`) overrides any of it from the page editor: stats, statements, benchmark
rows, features, the comparison table, and install steps. The "How it works" map, the
harness marquee and the Claude Code transcript scenes (`src/js/main.js`) are code, not copy.

The `/docs` section is not ACF content: its pages live in `docs-content.php`, rendered by
`docs.php` through `docs/…` rewrite rules (the site needs anything but plain permalinks).
`npm run export` renders each docs page beside the front page, so the static host serves
them with no WordPress. Edit the docs in `docs-content.php`; the CLI page mirrors
`agent-local help` and the tool table mirrors `mcp.go` — keep them in step.

The headline stats repeat the benchmark section's figures on purpose. Change them
together, or the page contradicts itself under a label that says "measured".

## Icons

`assets/favicon.svg` is the hero field in miniature: three swells and the lamp above
them. `favicon-32.png` and `apple-touch-icon.png` are rasters of it; regenerate both
if the SVG changes.

## Benchmark provenance

The numbers in the benchmarks section were measured 2026-09-01 on an Apple M3
(macOS 15): one HTTP client for every provider, interleaved samples, medians of 30
for latency and 3 for lifecycle. DDEV 1.24.x on Colima (4 CPU / 6 GB); LocalWP 9.
Ties are disclosed on the page. Re-measure before changing the values.
