#!/usr/bin/env node
// Static export of the marketing site.
//
// The page is the theme's shipped defaults rendered by WordPress; nothing on
// it needs PHP once rendered. So the public host gets a static copy: the
// rendered HTML with WordPress's own boilerplate removed, beside the theme's
// dist/ and assets/. This script writes public/ (index.html, robots.txt,
// sitemap.xml), which is committed; the Muster deploy step on the server
// pulls the repo and assembles the docroot from public/ + dist/ + assets/.
//
//   npm run export          # render from the local site into public/
//   git commit && git push  # then deploy from Muster (remote step)
//
// Environment (defaults suit this checkout):
//   SOURCE      http://agentlocal.test/
//   PUBLIC_URL  https://al.tools.efront.dev/

import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const SOURCE = process.env.SOURCE ?? "http://agentlocal.test/";
const PUBLIC_URL = (process.env.PUBLIC_URL ?? "https://al.tools.efront.dev/").replace(/\/?$/, "/");
const out = join(here, "public");

const origin = new URL(SOURCE).origin;

// Every page renders the same way: the front page, then /docs/ and each of
// its sub-pages from docs-content.php. The slug list is read from the theme
// so a new docs page is exported without editing this script.
async function render(path) {
  const res = await fetch(origin + path);
  if (!res.ok) throw new Error(`${origin}${path} answered ${res.status}`);
  let html = await res.text();

  // WordPress boilerplate the static page has no use for. Each pattern is one
  // self-contained tag or block; anything not listed is left exactly as rendered.
  const strip = [
    /<link rel='dns-prefetch'[^>]*>\s*/g,
    /<link rel="alternate" title="oEmbed[^>]*>\s*/g,
    /<link rel="alternate" title="JSON"[^>]*>\s*/g,
    /<link rel="https:\/\/api\.w\.org\/"[^>]*>\s*/g,
    /<link rel="EditURI"[^>]*>\s*/g,
    /<link rel='shortlink'[^>]*>\s*/g,
    /<meta name="generator"[^>]*>\s*/g,
    /<script id="wp-emoji-settings"[\s\S]*?<\/script>\s*/g,
    /<script type="speculationrules">[\s\S]*?<\/script>\s*/g,
    /<script type="module">[\s\S]*?<\/script>\s*/g,
  ];
  for (const re of strip) html = html.replace(re, "");

  // Theme assets become root-relative; every other reference to the local host
  // becomes the public one (canonical, og:url, the wordmark's home link).
  html = html.replace(themePrefix, "/");
  html = html.split(origin + "/").join(PUBLIC_URL);
  html = html.split(origin).join(PUBLIC_URL.replace(/\/$/, ""));
  // Social scrapers want the share image absolute; everything else stays relative.
  html = html.replace(/(property="og:image" content=")\/assets\//, `$1${PUBLIC_URL}assets/`);
  return html;
}

const themePrefix = new RegExp(`${origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/wp-content/themes/agent-local/`, "g");
const html = await render("/");

await mkdir(out, { recursive: true });
await writeFile(join(out, "index.html"), html);
await writeFile(join(out, "robots.txt"), `User-agent: *\nAllow: /\nSitemap: ${PUBLIC_URL}sitemap.xml\n`);

// Docs pages: the docs index's own sidebar nav lists every slug; export each
// to docs/<slug>/index.html, then the index itself.
const docsIndex = await render("/docs/");
const slugs = [...new Set([...docsIndex.matchAll(/\/docs\/([a-z0-9-]+)\//g)].map((m) => m[1]))];
await mkdir(join(out, "docs"), { recursive: true });
await writeFile(join(out, "docs", "index.html"), docsIndex);
let docs = 1;
for (const slug of slugs) {
  const dir = join(out, "docs", slug);
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, "index.html"), await render(`/docs/${slug}/`));
  docs++;
}

const entries = [`<url><loc>${PUBLIC_URL}</loc><changefreq>weekly</changefreq></url>`].concat(
  [...new Set(slugs)].filter((s) => s !== "index").map((s) => `<url><loc>${PUBLIC_URL}docs/${s}/</loc><changefreq>weekly</changefreq></url>`),
);
await writeFile(join(out, "sitemap.xml"),
  `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${entries.join("")}</urlset>\n`);

console.log(`public/index.html  ${Math.round(Buffer.byteLength(html) / 1024)} KB, rendered from ${SOURCE}`);
console.log(`public/docs/       ${docs} pages`);
console.log(`commit site/public, push, then deploy from Muster to publish at ${PUBLIC_URL}`);
