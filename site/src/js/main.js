/* agent-local site: terminal, counters, reveals, copy. Reduced motion gets final frames. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;


/* ---------- ascii globe: a small rotating wireframe, the hero's only motion ---------- */
const globe = document.getElementById("ascii-globe");

function globeFrame(t, w = 30, h = 14) {
  const grid = Array.from({ length: h }, () => Array(w).fill(" "));
  const R = (h - 1) / 2;
  const aspect = 2.05; // mono cells are taller than wide
  const put = (x3, y3, z3, ch) => {
    const px = Math.round(w / 2 + x3 * R * aspect * 0.98);
    const py = Math.round(h / 2 + y3 * R * 0.98);
    if (px >= 0 && px < w && py >= 0 && py < h) grid[py][px] = ch;
  };
  const shade = (z) => (z > 0.55 ? "o" : z > 0.15 ? ":" : ".");
  // latitude and longitude grid, rotated around the vertical axis
  for (let la = -75; la <= 75; la += 15) {
    const lar = (la * Math.PI) / 180;
    for (let lo = 0; lo < 360; lo += 10) {
      const lor = (lo * Math.PI) / 180 + t;
      const x = Math.cos(lar) * Math.cos(lor);
      const z = Math.cos(lar) * Math.sin(lor);
      if (z > -0.1 && lo % 30 === 0) put(x, -Math.sin(lar), z, shade(z));
    }
  }
  // equator drawn denser, so the rotation reads
  for (let lo = 0; lo < 360; lo += 6) {
    const lor = (lo * Math.PI) / 180 + t;
    const z = Math.sin(lor);
    if (z > -0.1) put(Math.cos(lor), 0, z, shade(z));
  }
  // silhouette ring
  for (let a = 0; a < 360; a += 5) {
    const ar = (a * Math.PI) / 180;
    put(Math.cos(ar), Math.sin(ar), 0.12, ".");
  }
  return grid.map((r) => r.join("")).join("\n");
}

if (globe) {
  if (reduced) {
    globe.textContent = globeFrame(0.6);
  } else {
    let t = 0;
    let spinning = true;
    new IntersectionObserver((es) => { spinning = es[0].isIntersecting; }).observe(globe);
    setInterval(() => {
      if (!spinning) return;
      t += 0.045;
      globe.textContent = globeFrame(t);
    }, 90);
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
