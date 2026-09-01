/* agent-local site: terminal, counters, reveals, copy. Reduced motion gets final frames. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;


/* ---------- the field: a full-hero ocean of characters, moved by slow
   interference of a few sine waves. Abstract on purpose: a curated shape
   would fight the wordmark, and procedural planets read as mud. ---------- */
const field = document.getElementById("wave-field");

const FIELD_RAMP = " ..::-=+*";
const CELL_W = 8.1;  // 13.5px IBM Plex Mono advance width
const CELL_H = 17.5; // 13.5px at line-height 1.3

function fieldFrame(t, cols, rows) {
  const out = [];
  for (let y = 0; y < rows; y++) {
    let row = "";
    for (let x = 0; x < cols; x++) {
      // Three travelling waves and one slow radial swell.
      const v =
        Math.sin(x * 0.09 + t) +
        Math.sin(y * 0.16 - t * 0.62) +
        Math.sin((x * 0.045 + y * 0.075) + t * 0.4) +
        Math.sin(Math.hypot(x - cols / 2, (y - rows / 2) * 2.1) * 0.11 - t * 0.8);
      const n = (v + 4) / 8;
      row += FIELD_RAMP[Math.max(0, Math.min(FIELD_RAMP.length - 1, Math.floor(n * FIELD_RAMP.length)))];
    }
    out.push(row);
  }
  return out.join("\n");
}

if (field) {
  let cols = 0, rows = 0;
  const size = () => {
    const host = field.parentElement;
    cols = Math.ceil(host.clientWidth / CELL_W) + 1;
    rows = Math.ceil(host.clientHeight / CELL_H) + 1;
  };
  size();
  let resizeTimer;
  addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(size, 200); });
  if (reduced) {
    field.textContent = fieldFrame(1.7, cols, rows);
  } else {
    let t = 0;
    let visible = true;
    new IntersectionObserver((es) => { visible = es[0].isIntersecting; }).observe(field);
    setInterval(() => {
      if (!visible) return;
      t += 0.055;
      field.textContent = fieldFrame(t, cols, rows);
    }, 100);
  }
}
/* ---------- terminal: human scene, then agent scene ---------- */
const scenes = [
  [
    ["$ agent-local create demo", "t-cmd", 400, true],
    ["  ◓ installing WordPress into ~/Sites/demo", "", 500],
    ["    files     GET wordpress.org/latest.tar.gz", "", 700],
    ["    database  al_demo created, user granted", "", 600],
    ["    tls       demo.test issued + trusted", "", 600],
    ["  ● Site ready: https://demo.test          15s", "t-ok", 700],
    ["", "", 300],
    ["$ agent-local share demo", "t-cmd", 900, true],
    ["  ● https://gilded-poem-wander.trycloudflare.com", "t-ok", 1600],
    ["    verified serving, expires in 60m", "", 300],
  ],
  [
    ["# the same engine, driven over MCP", "", 500],
    ["→ create_site {\"name\":\"client-qa\"}", "t-cmd", 700],
    ["← {\"ok\":true, \"url\":\"https://client-qa.test\"}", "t-ok", 1500],
    ["→ db_import {\"path\":\"prod.sql.gz\"}", "t-cmd", 800],
    ["← saved auto-import snapshot, 214 tables,", "t-ok", 1500],
    ["  urls prod.client.com → client-qa.test", "t-ok", 300],
    ["→ list_mail {\"slug\":\"client-qa\"}", "t-cmd", 900],
    ["← [{\"subject\":\"Password Reset\", \"to\":\"admin@…\"}]", "t-ok", 1300],
    ["", "", 300],
    ["  59 tools, each one a human command", "", 500],
  ],
];

const screen = document.getElementById("term-screen");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function put(text, cls) {
  const el = document.createElement("span");
  if (cls) el.className = cls;
  el.textContent = text;
  screen.appendChild(el);
  return el;
}

async function typeLine(text, cls) {
  const el = put("", cls);
  const cur = document.createElement("span");
  cur.className = "t-cursor";
  screen.appendChild(cur);
  for (const ch of text) {
    el.textContent += ch;
    await sleep(26 + Math.random() * 38);
  }
  await sleep(160);
  cur.remove();
  el.textContent += "\n";
}

async function play() {
  for (let i = 0; ; i = (i + 1) % scenes.length) {
    screen.textContent = "";
    for (const [text, cls, delay, typed] of scenes[i]) {
      await sleep(delay);
      if (typed) await typeLine(text, cls);
      else put(text + "\n", cls);
    }
    await sleep(4200);
  }
}

if (screen) {
  if (reduced) {
    screen.textContent = "";
    for (const [text, cls] of scenes[0]) put(text + "\n", cls);
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
const rvAuto = document.querySelectorAll(".stat, .statement__copy, .index__row, .compare__table, .install__step, .hero__copy, .bench__metric");
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

/* ---------- copy steps ---------- */
for (const el of document.querySelectorAll("[data-copy]")) {
  el.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(el.dataset.copy);
      el.classList.add("is-done");
      const chip = el.querySelector(".install__copy");
      if (chip) chip.textContent = "copied";
      setTimeout(() => {
        el.classList.remove("is-done");
        if (chip) chip.textContent = "copy";
      }, 1500);
    } catch { /* text is selectable right there */ }
  });
}
