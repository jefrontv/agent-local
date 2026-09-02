import Lenis from "lenis";
import gsap from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";

/* agent-local site: field, transcript, counters, reveals, copy. Reduced motion gets final frames. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;

/* ---------- motion system: lenis owns the wheel, gsap owns entrances and
   the scrubbed parallax, and scroll velocity feeds the ascii field. ---------- */
let fieldEnergy = 0;

if (!reduced) {
  gsap.registerPlugin(ScrollTrigger);
  const lenis = new Lenis({ lerp: 0.16, wheelMultiplier: 1.15 });
  lenis.on("scroll", (e) => {
    ScrollTrigger.update();
    fieldEnergy = Math.min(3, Math.abs(e.velocity) / 45);
  });
  gsap.ticker.add((time) => lenis.raf(time * 1000));
  gsap.ticker.lagSmoothing(0);

  for (const a of document.querySelectorAll('a[href^="#"]')) {
    a.addEventListener("click", (e) => {
      const target = document.querySelector(a.getAttribute("href"));
      if (!target) return;
      e.preventDefault();
      lenis.scrollTo(target, { offset: -8, duration: 0.9 });
    });
  }

  // Statements arrive like the products of a keystroke: fast out of the
  // gate, settling long. GSAP owns these, so the IO reveal must not.
  for (const el of document.querySelectorAll(".statement__display")) {
    el.classList.remove("rv-scale");
    gsap.fromTo(el,
      { y: 90, scale: 0.97, opacity: 0 },
      { y: 0, scale: 1, opacity: 1, duration: 1.1, ease: "expo.out",
        scrollTrigger: { trigger: el, start: "top 84%" } });
  }
  // The wordmarks ride the scroll: the hero's sinks away, the footer's rises.
  gsap.to(".hero__mark", { yPercent: 26, ease: "none",
    scrollTrigger: { trigger: ".hero", start: "bottom 99%", end: "bottom top", scrub: true } });
  gsap.from(".footer__mark", { yPercent: 34, ease: "none",
    scrollTrigger: { trigger: ".footer", start: "top bottom", end: "bottom bottom", scrub: true } });
}

/* ---------- the field: an ocean of characters, moved by slow interference
   of a few sine waves. Abstract on purpose: a curated shape would fight the
   wordmark, and procedural planets read as mud. The pointer is a stone
   dropped in: rings spread from it while it moves and fade when it leaves.
   Any `.wave-field` <pre> gets one; the hero and the footer each have theirs. ---------- */
const FIELD_RAMP = " ..::-=+*";
const CELL_W = 8.1;  // 13.5px IBM Plex Mono advance width
const CELL_H = 17.5; // 13.5px at line-height 1.3

function fieldFrame(t, cols, rows, pointer) {
  const out = [];
  const pe = pointer.energy;
  for (let y = 0; y < rows; y++) {
    let row = "";
    const dy = (y - pointer.y) * 2.1;
    for (let x = 0; x < cols; x++) {
      // Three travelling waves and one slow radial swell.
      let v =
        Math.sin(x * 0.09 + t) +
        Math.sin(y * 0.16 - t * 0.62) +
        Math.sin((x * 0.045 + y * 0.075) + t * 0.4) +
        Math.sin(Math.hypot(x - cols / 2, (y - rows / 2) * 2.1) * 0.11 - t * 0.8);
      if (pe > 0.01) {
        const d = Math.hypot(x - pointer.x, dy);
        v += Math.sin(d * 0.36 - t * 9) * Math.exp(-d / 24) * pe * 5;
      }
      const n = (v + 4) / 8;
      row += FIELD_RAMP[Math.max(0, Math.min(FIELD_RAMP.length - 1, Math.floor(n * FIELD_RAMP.length)))];
    }
    out.push(row);
  }
  return out.join("\n");
}

// One field: its <pre>, the box it fills, the section whose pointer stirs it.
// pointer.x/y is where the rings come from; tx/ty is where the cursor is. The
// origin drifts toward the cursor a little each frame, so a fast move leaves
// the disturbance trailing behind like something carried in water.
function mountField(pre, index) {
  const host = pre.parentElement;
  const f = {
    pre, host, cols: 0, rows: 0, visible: true,
    phase: index * 3.1, // siblings never show the same frame
    pointer: { x: 0, y: 0, tx: 0, ty: 0, energy: 0 },
  };
  f.size = () => {
    f.cols = Math.ceil(host.clientWidth / CELL_W) + 1;
    f.rows = Math.ceil(host.clientHeight / CELL_H) + 1;
  };
  f.size();
  return f;
}

const fields = [...document.querySelectorAll(".wave-field")].map(mountField);
if (fields.length) {
  let resizeTimer;
  addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => fields.forEach((f) => f.size()), 200); });
  if (reduced) {
    for (const f of fields) f.pre.textContent = fieldFrame(1.7 + f.phase, f.cols, f.rows, f.pointer);
  } else {
    for (const f of fields) {
      const zone = f.host.parentElement;
      const p = f.pointer;
      zone.addEventListener("pointermove", (e) => {
        const r = f.host.getBoundingClientRect();
        p.tx = (e.clientX - r.left) / CELL_W;
        p.ty = (e.clientY - r.top) / CELL_H;
        // First contact starts the rings under the cursor, not on a journey
        // from wherever they last died.
        if (p.energy < 0.02) { p.x = p.tx; p.y = p.ty; }
        p.energy = Math.min(1, p.energy + 0.12);
      });
      zone.addEventListener("pointerleave", () => { p.energy *= 0.5; });
      new IntersectionObserver((es) => { f.visible = es[0].isIntersecting; }).observe(f.pre);
    }

    // Phase accumulates per frame so scroll velocity can pour energy in:
    // the ocean stirs while you move and settles when you stop. ~30 fps,
    // since a text reflow at 60 buys nothing visible. Off-screen fields
    // still advance their phase, so they never freeze mid-wave on return.
    let last = 0;
    const loop = (now) => {
      const dt = now - last;
      if (dt > 32) {
        last = now;
        const step = dt * 0.0006 * (1 + fieldEnergy * 2.2);
        fieldEnergy *= 0.92;
        for (const f of fields) {
          f.phase += step;
          const p = f.pointer;
          p.energy *= 0.965;
          p.x += (p.tx - p.x) * 0.085;
          p.y += (p.ty - p.y) * 0.085;
          if (f.visible) f.pre.textContent = fieldFrame(f.phase, f.cols, f.rows, p);
        }
      }
      requestAnimationFrame(loop);
    };
    requestAnimationFrame(loop);
  }
}

/* ---------- the agent transcript: looping sessions in the register of a
   coding-agent chat. Prefixes come from CSS ::before so each line kind
   carries two colors without per-character spans. Each scene is one job the
   tool set can do end to end, in its own working directory. Project names
   are invented; any resemblance to a real checkout is unlucky. ---------- */
const scenes = [
  {
    cwd: "~/Sites/ferncreek",
    lines: [
      ["qa copy of fern creek, and prove the contact form emails work", "t-user", 700, true],
      ["create_site(name: \"ferncreek-qa\")", "t-tool", 900],
      ["https://ferncreek-qa.test · serving · 19s", "t-res", 1500],
      ["db_import(path: \"prod.sql.gz\")", "t-tool", 900],
      ["saved auto-import snapshot · 214 tables · urls rewritten", "t-res", 1700],
      ["browser(submit \"/contact\" with a test enquiry)", "t-tool", 900],
      ["302 → /contact?sent=1", "t-res", 1200],
      ["get_mail(slug: \"ferncreek-qa\")", "t-tool", 900],
      ["\"New enquiry\" → hello@ferncreekstudio.com · body matches", "t-res", 1400],
      ["The QA copy is live and its contact form delivers.", "t-assist", 900],
      ["If anything drifts, db_restore puts the snapshot back.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/oldmill",
    lines: [
      ["serve the old mill checkout in ~/Sites, load the prod dump, and stop the broken images", "t-user", 700, true],
      ["attach_site(dir: \"~/Sites/oldmill\", php_version: \"8.4\")", "t-tool", 900],
      ["https://oldmill.test · serving · existing wp-config kept", "t-res", 1500],
      ["db_import(slug: \"oldmill\", path: \"~/Downloads/oldmill-prod.sql.gz\")", "t-tool", 900],
      ["snapshot saved first · 1.2 GB streamed · urls rewritten to oldmill.test", "t-res", 1700],
      ["set_media_fallback(slug: \"oldmill\", url: \"auto\")", "t-tool", 900],
      ["adopted the .htaccess rule → https://oldmillhouse.co.uk", "t-res", 1300],
      ["browser(open \"/\", count failed images)", "t-tool", 900],
      ["38 images · 38 served · 0 local files, all redirected to production", "t-res", 1300],
      ["The checkout is live on PHP 8.4 with the production database behind it.", "t-assist", 900],
      ["Uploads aren't copied; anything missing locally falls through to the live site.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/harborline",
    lines: [
      ["is the redesign branch safe to merge? check it against the live copy", "t-user", 700, true],
      ["list_branches(slug: \"harborline\")", "t-tool", 900],
      ["main · redesign · fix/footer · no previews yet", "t-res", 1300],
      ["add_worktree(slug: \"harborline\", branch: \"redesign\")", "t-tool", 900],
      ["https://redesign.harborline.test · same database · 9s", "t-res", 1600],
      ["browser(diff \"/\", \"/about\", \"/contact\" against https://harborline.test)", "t-tool", 900],
      ["3 pages · 2 layout changes · 0 console errors", "t-res", 1400],
      ["remove_worktree(id: \"harborline--redesign\")", "t-tool", 900],
      ["preview gone · branch kept", "t-res", 1100],
      ["The redesign renders cleanly on the live data.", "t-assist", 900],
      ["Nothing in it touches the database, so the merge is code only.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/quarrylane",
    lines: [
      ["the client's staging is on PHP 8.1 and white-screens. find out why", "t-user", 700, true],
      ["switch_php(slug: \"quarrylane\", version: \"8.1\")", "t-tool", 900],
      ["php@8.1 installed · pool restarted · https://quarrylane.test → 500", "t-res", 1600],
      ["set_wp_debug(slug: \"quarrylane\", on: true)", "t-tool", 900],
      ["WP_DEBUG on · log → ~/.agent-local/logs/wp-quarrylane.log", "t-res", 1300],
      ["get_logs(name: \"wp-quarrylane\", lines: 40)", "t-tool", 900],
      ["Fatal: undefined function json_validate() · inc/forms.php:48", "t-res", 1500],
      ["switch_php(slug: \"quarrylane\", version: \"8.4\")", "t-tool", 900],
      ["pool restarted · 200", "t-res", 1100],
      ["One call in inc/forms.php:48 needs PHP 8.3 or newer.", "t-assist", 900],
      ["Swap it for json_decode with a null check, or move staging to 8.3.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/meadowbank",
    lines: [
      ["move the old horticulture site out of ddev. it's been halted since march", "t-user", 700, true],
      ["ddev_projects()", "t-tool", 900],
      ["2 projects · meadowbank · stopped · PHP 8.1", "t-res", 1300],
      ["import_site(source: \"meadowbank\", php_version: \"8.1\")", "t-tool", 900],
      ["asked DDEV to start it · dumped 1.9 GB from its published port", "t-res", 1700],
      ["urls rewritten · wp-config added the defines it kept in wp-config-ddev.php", "t-res", 1500],
      ["meadowbank removed from DDEV · its snapshot kept in .ddev/db_snapshots", "t-res", 1500],
      ["It serves from the same folder, on its own PHP 8.1 pool.", "t-assist", 900],
      ["You can quit Docker. This one comes back by itself after a reboot.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/bramblecourt",
    lines: [
      ["the client wants to see the homepage on her phone before the call. ten minutes", "t-user", 700, true],
      ["share_local_site(slug: \"bramblecourt\", minutes: 30)", "t-tool", 900],
      ["https://quiet-fern-a1b2.trycloudflare.com · verified 200 · stops in 30 min", "t-res", 1600],
      ["browser(open the share at a phone width, check every image)", "t-tool", 900],
      ["41 images · all 200 · production fallback still applies", "t-res", 1400],
      ["Here's a link she can open anywhere. It closes itself in thirty minutes.", "t-assist", 900],
      ["Nothing on the site was renamed: the tunnel host applies to tunnel requests only.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/pedlars",
    lines: [
      ["orders from last week are missing on the client's shop. use the local copy, not prod", "t-user", 700, true],
      ["db_snapshot(slug: \"pedlars\", name: \"before-looking\")", "t-tool", 900],
      ["pedlars-20260902-1702-before-looking.sql.gz · 212 MB", "t-res", 1300],
      ["db_query(slug: \"pedlars\", sql: \"SELECT status, COUNT(*) FROM wp_wc_orders WHERE date_created_gmt >= '2026-08-24' GROUP BY status\")", "t-tool", 900],
      ["wc-completed 31 · wc-processing 4 · trash 47", "t-res", 1500],
      ["db_query(slug: \"pedlars\", sql: \"SELECT MIN(date_updated_gmt), MAX(date_updated_gmt) FROM wp_wc_orders WHERE status = 'trash'\")", "t-tool", 900],
      ["2026-08-29 02:14:07 → 2026-08-29 02:14:09", "t-res", 1400],
      ["open_adminer(slug: \"pedlars\")", "t-tool", 900],
      ["https://pedlars.test/.agent-local/adminer", "t-res", 1100],
      ["47 orders were trashed within two seconds early on the 29th: a bulk action, not customers.", "t-assist", 900],
      ["Nothing is deleted. They're in the trash, and Adminer is open if you want to see them.", "t-assist", 400],
    ],
  },
  {
    cwd: "~/Sites/harborline",
    lines: [
      ["the site stopped loading after i ran brew upgrade this morning", "t-user", 700, true],
      ["doctor()", "t-tool", 900],
      ["11 checks · 1 finding · php@8.4 keg unlinked after brew upgrade · fix: install_runtime php 8.4", "t-res", 1600],
      ["doctor_fix()", "t-tool", 900],
      ["php@8.4 relinked · pool restarted · https://harborline.test → 200", "t-res", 1500],
      ["Homebrew replaced PHP under the running pool and left the keg unlinked.", "t-assist", 900],
      ["Doctor relinked it and restarted the pool. Nothing else changed.", "t-assist", 400],
    ],
  },
];

const screen = document.getElementById("term-screen");
const viewport = screen?.parentElement;
const promptBox = document.getElementById("term-input");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Lines are blocks, not newline-joined text: spacing between entries is the
// stylesheet's job, and the fixed viewport tails like a terminal, so the
// card never changes the page's height. Tool calls take Claude Code's MCP
// shape, "server - tool (MCP)(args)", with the name in bold.
function put(text, cls) {
  const el = document.createElement("span");
  el.className = "t-line" + (cls ? " " + cls : "");
  const txt = document.createElement("span");
  const paren = cls === "t-tool" ? text.indexOf("(") : -1;
  if (paren > 0) {
    const name = text.slice(0, paren);
    const b = document.createElement("b");
    b.textContent = name === "browser" ? name : `agent-local - ${name} (MCP)`;
    txt.append(b, text.slice(paren));
  } else {
    txt.textContent = text;
  }
  el.appendChild(txt);
  screen.appendChild(el);
  viewport.scrollTop = viewport.scrollHeight;
  return el;
}

// The terminal chrome: the title bar and the welcome box both carry the
// scene's working directory, so every scenario reads as its own session.
const termTitle = document.querySelector(".term__title");

function welcome(cwd) {
  const el = document.createElement("span");
  el.className = "t-line t-welcome";
  el.innerHTML = "<i>\u273B</i> Welcome to Claude Code!<small>/help for help, /status for your current setup</small>";
  const c = document.createElement("small");
  c.textContent = "cwd: " + cwd;
  el.appendChild(c);
  screen.appendChild(el);
}

// Claude Code's working indicator: a turning asterisk, a verb, the clock.
const SPIN = "\u00B7\u2722\u2733\u2736\u273B\u273D";
const VERBS = ["Thinking", "Pondering", "Provisioning", "Cogitating", "Brewing", "Checking", "Noodling", "Herding"];
function spinner() {
  const el = put("", "t-spin");
  const glyph = document.createElement("i");
  const label = document.createElement("span");
  el.firstChild.replaceWith(glyph, label);
  const verb = VERBS[Math.floor(Math.random() * VERBS.length)];
  const t0 = Date.now();
  let i = 0;
  const tick = () => {
    glyph.textContent = SPIN[i++ % SPIN.length];
    label.textContent = ` ${verb}\u2026 (${Math.max(1, Math.round((Date.now() - t0) / 1000))}s)`;
  };
  tick();
  const id = setInterval(tick, 140);
  return () => { clearInterval(id); el.remove(); };
}

// The prompt is typed where a person types it, then submitted into the log.
async function typePrompt(text) {
  for (const ch of text) {
    promptBox.textContent += ch;
    await sleep(26 + Math.random() * 38);
  }
  await sleep(260);
  promptBox.textContent = "";
  put(text, "t-user");
}

async function play() {
  for (let i = 0; ; i = (i + 1) % scenes.length) {
    screen.textContent = "";
    const scene = scenes[i];
    welcome(scene.cwd);
    if (termTitle) termTitle.textContent = "claude — " + scene.cwd;
    let stop = null;
    for (let n = 0; n < scene.lines.length; n++) {
      const [text, cls, delay] = scene.lines[n];
      await sleep(delay);
      if (stop) stop();
      if (cls === "t-user") await typePrompt(text);
      else put(text, cls);
      // The spinner sits under the log while the model or a tool is at work;
      // streamed prose has no gap to fill, and a finished session goes quiet.
      const next = scene.lines[n + 1];
      stop = next && !(cls === "t-assist" && next[1] === "t-assist") ? spinner() : null;
    }
    await sleep(4600);
  }
}

if (screen) {
  screen.textContent = "";
  if (reduced) {
    welcome(scenes[0].cwd);
    for (const [text, cls] of scenes[0].lines) put(text, cls);
  } else play();
}

/* ---------- stat counters ---------- */
function runCounter(el) {
  const target = parseFloat(el.dataset.count);
  if (isNaN(target) || reduced) return;
  const decimals = (el.dataset.count.split(".")[1] || "").length;
  const t0 = performance.now();
  const dur = 1300;
  function tick(t) {
    const p = Math.min(1, (t - t0) / dur);
    const eased = 1 - Math.pow(1 - p, 3);
    el.textContent = (target * eased).toFixed(decimals);
    if (p < 1) requestAnimationFrame(tick);
  }
  requestAnimationFrame(tick);
}

/* ---------- reveals ---------- */
const rvAuto = document.querySelectorAll(".stat, .statement__copy, .index__row, .compare__table, .install__step, .bench__metric, .how__node, .how__copy");
for (const el of rvAuto) el.classList.add("rv");
const io = new IntersectionObserver((entries) => {
  for (const e of entries) {
    if (!e.isIntersecting) continue;
    e.target.classList.add("is-in");
    const counter = e.target.querySelector?.("[data-count]");
    if (counter) runCounter(counter);
    io.unobserve(e.target);
  }
}, { threshold: 0.15 });
for (const el of [...rvAuto, ...document.querySelectorAll(".rv-scale")]) io.observe(el);

/* ---------- nav: the link for the section under the reader is lit. A band
   across the upper third decides, so short sections still get their turn. */
const navLinks = new Map();
for (const a of document.querySelectorAll('.nav__links a[href^="#"]')) {
  const target = document.querySelector(a.getAttribute("href"));
  if (target) navLinks.set(target, a);
}
if (navLinks.size) {
  const current = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      for (const a of navLinks.values()) a.removeAttribute("aria-current");
      navLinks.get(e.target).setAttribute("aria-current", "true");
    }
  }, { rootMargin: "-20% 0px -65% 0px" });
  for (const section of navLinks.keys()) current.observe(section);
}

/* ---------- copy steps. navigator.clipboard exists only on secure origins,
   and a local dev domain is plain http, so fall back to a selection copy. */
function copyText(text) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.cssText = "position:fixed;opacity:0";
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand("copy");
  ta.remove();
  return ok ? Promise.resolve() : Promise.reject(new Error("copy refused"));
}

const live = document.getElementById("copy-live");
for (const el of document.querySelectorAll("[data-copy]")) {
  el.addEventListener("click", async () => {
    try {
      await copyText(el.dataset.copy);
      el.classList.add("is-done");
      const chip = el.querySelector(".install__copy");
      if (chip) chip.textContent = "copied";
      if (live) live.textContent = "Copied: " + el.dataset.copy;
      setTimeout(() => {
        el.classList.remove("is-done");
        if (chip) chip.textContent = "copy";
      }, 1500);
    } catch { /* text is selectable right there */ }
  });
}
