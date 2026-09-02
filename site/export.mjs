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

const res = await fetch(SOURCE);
if (!res.ok) throw new Error(`${SOURCE} answered ${res.status}`);
let html = await res.text();

const origin = new URL(SOURCE).origin;
const themePrefix = new RegExp(`${origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/wp-content/themes/agent-local/`, "g");

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
  /<style id="(wp-[^"]*|classic-theme-styles-inline-css|global-styles-inline-css)">[\s\S]*?<\/style>\s*/g,
  /<script id="wp-emoji-settings"[\s\S]*?<\/script>\s*/g,
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

await mkdir(out, { recursive: true });
await writeFile(join(out, "index.html"), html);
await writeFile(join(out, "robots.txt"), `User-agent: *\nAllow: /\nSitemap: ${PUBLIC_URL}sitemap.xml\n`);
await writeFile(join(out, "sitemap.xml"),
  `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>${PUBLIC_URL}</loc><changefreq>weekly</changefreq></url></urlset>\n`);

console.log(`public/index.html  ${Math.round(Buffer.byteLength(html) / 1024)} KB, rendered from ${SOURCE}`);
console.log(`commit site/public, push, then deploy from Muster to publish at ${PUBLIC_URL}`);
