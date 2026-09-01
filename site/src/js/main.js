import Lenis from "lenis";

/* agent-local site: terminal, counters, reveals, copy. Reduced motion gets final frames. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;



/* ---------- soft scrolling: lenis drives the wheel, and in-page anchors
   route through it so nav clicks glide instead of jumping. ---------- */
if (!matchMedia("(prefers-reduced-motion: reduce)").matches) {
  const lenis = new Lenis({ autoRaf: true, lerp: 0.085, wheelMultiplier: 0.95 });
  for (const a of document.querySelectorAll('a[href^="#"]')) {
    a.addEventListener("click", (e) => {
      const target = document.querySelector(a.getAttribute("href"));
      if (!target) return;
      e.preventDefault();
      lenis.scrollTo(target, { offset: -8, duration: 1.4 });
    });
  }
}
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
		// Time-based phase off requestAnimationFrame: the wave moves at the
		// same speed everywhere and never steps, throttled to ~30 fps since
		// a text reflow at 60 buys nothing visible.
		let visible = true;
		let last = 0;
		new IntersectionObserver((es) => { visible = es[0].isIntersecting; }).observe(field);
		const loop = (now) => {
			if (visible && now - last > 32) {
				last = now;
				field.textContent = fieldFrame(now / 1600, cols, rows);
			}
			requestAnimationFrame(loop);
		};
		requestAnimationFrame(loop);
	}
}
/* ---------- the agent transcript: one looping session in the register of
   a coding-agent chat. Prefixes come from CSS ::before so each line kind
   carries two colors without per-character spans. ---------- */
const scenes = [
  [
    ["qa copy of sulo, and prove the contact form emails work", "t-user", 700, true],
    ["", "", 500],
    ["create_site(name: \"sulo-qa\")", "t-tool", 700],
    ["https://sulo-qa.test · serving · 15s", "t-res", 1500],
    ["db_import(path: \"prod.sql.gz\")", "t-tool", 900],
    ["saved auto-import snapshot · 214 tables · urls rewritten", "t-res", 1700],
    ["browser · submit /contact with a test enquiry", "t-tool", 900],
    ["302 → /contact?sent=1", "t-res", 1200],
    ["get_mail(slug: \"sulo-qa\")", "t-tool", 900],
    ["\"New enquiry\" → studio@client.com · body matches", "t-res", 1400],
    ["", "", 500],
    ["The QA copy is live and its contact form delivers.", "t-assist", 800],
    ["If anything drifts, db_restore puts the snapshot back.", "t-assist", 400],
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

for (const el of document.querySelectorAll("[data-copy]")) {
  el.addEventListener("click", async () => {
    try {
      await copyText(el.dataset.copy);
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
