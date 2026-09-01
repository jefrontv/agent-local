/* agent-local site: terminal, counters, reveals, copy. Reduced motion gets final frames. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;


/* ---------- ascii planet: a shaded, rotating world with a twinkling
   atmosphere, in the register of ghostty.org's hero. Two layers so the
   body and the aura carry different colors without per-cell spans. */
const planetBody = document.getElementById("planet-body");
const planetAura = document.getElementById("planet-aura");

const RAMP = " .,:;!i1tfLCG08%@";

// Deterministic hash noise: cheap, seamless when sampled on sphere coords.
function hash3(x, y, z) {
  let n = Math.sin(x * 127.1 + y * 311.7 + z * 74.7) * 43758.5453;
  return n - Math.floor(n);
}
function vnoise(x, y, z) {
  const xi = Math.floor(x), yi = Math.floor(y), zi = Math.floor(z);
  const xf = x - xi, yf = y - yi, zf = z - zi;
  const s = (v) => v * v * (3 - 2 * v);
  const u = s(xf), v = s(yf), w = s(zf);
  let acc = 0;
  for (let dz = 0; dz <= 1; dz++)
    for (let dy = 0; dy <= 1; dy++)
      for (let dx = 0; dx <= 1; dx++)
        acc += hash3(xi + dx, yi + dy, zi + dz) *
          (dx ? u : 1 - u) * (dy ? v : 1 - v) * (dz ? w : 1 - w);
  return acc;
}
function fbm(x, y, z) {
  return 0.65 * vnoise(x, y, z) + 0.35 * vnoise(x * 2.3 + 5, y * 2.3 + 5, z * 2.3 + 5);
}

function planetFrame(t, w = 76, h = 34) {
  const body = [], aura = [];
  const cx = w / 2 - 0.5, cy = h / 2 - 0.5;
  const R = h / 2 - 2.5;
  const aspect = 2.0;
  const tick = Math.floor(t * 2.2);
  for (let y = 0; y < h; y++) {
    let brow = "", arow = "";
    for (let x = 0; x < w; x++) {
      const dx = (x - cx) / (R * aspect);
      const dy = (y - cy) / R;
      const rr = dx * dx + dy * dy;
      if (rr <= 1) {
        const dz = Math.sqrt(1 - rr);
        const lon = Math.atan2(dx, dz) + t;
        const lat = Math.asin(dy);
        // Texture rides rotated sphere coordinates, so it wraps seamlessly.
        const tex = fbm(Math.cos(lon) * 1.7, lat * 2.4, Math.sin(lon) * 1.7);
        // Key light from the upper left, limb darkening from dz.
        const light = Math.max(0, -0.5 * dx - 0.38 * dy + 0.78 * dz);
        const v = Math.min(0.999, light * (0.32 + 0.78 * tex) * 1.35);
        brow += RAMP[Math.floor(v * RAMP.length)];
        arow += " ";
      } else if (rr <= 1.55) {
        // Twinkling atmosphere: sparse, re-seeded a couple of times a second.
        const p = hash3(x * 3.1, y * 7.7, tick);
        const edge = rr <= 1.16;
        arow += p > (edge ? 0.72 : 0.955) ? (p > 0.985 ? "+" : edge ? "=" : ".") : " ";
        brow += " ";
      } else {
        brow += " ";
        arow += " ";
      }
    }
    body.push(brow);
    aura.push(arow);
  }
  return [body.join("\n"), aura.join("\n")];
}

if (planetBody && planetAura) {
  const draw = (t) => {
    const [b, a] = planetFrame(t);
    planetBody.textContent = b;
    planetAura.textContent = a;
  };
  if (reduced) {
    draw(0.8);
  } else {
    let t = 0;
    let spinning = true;
    new IntersectionObserver((es) => { spinning = es[0].isIntersecting; }).observe(planetBody);
    setInterval(() => {
      if (!spinning) return;
      t += 0.035;
      draw(t);
    }, 95);
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
