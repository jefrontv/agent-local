/* agent-local site — hero terminal, tab thumb, reveals, copy chips.
   Everything degrades: reduced motion gets final frames, no-JS gets static text. */

const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;

/* ---------- hero terminal: two scenes on a loop ---------- */
/* Each line: [text, cssClass, msBeforeLine]. Typed per-character for commands,
   printed whole for output — the rhythm of a real session. */

const S = {
  cmd: null, // typed
  out: "",
  dim: "t-dim",
  ok: "t-ok",
  warn: "t-warn",
};

const scenes = [
  [
    ["$ agent-local create demo", S.cmd, 400],
    ["  ◓ installing WordPress into ~/Sites/demo", S.dim, 500],
    ["    files     GET wordpress.org/latest.tar.gz", S.dim, 700],
    ["    database  al_demo created, user granted", S.dim, 600],
    ["    installer POST /wp-admin/install.php", S.dim, 700],
    ["    tls       demo.test issued + trusted", S.dim, 500],
    ["  ● Site ready: https://demo.test              15s", S.ok, 600],
    ["", S.out, 200],
    ["$ agent-local share demo", S.cmd, 900],
    ["    tunnel    waiting for the edge to route …", S.dim, 700],
    ["  ● https://gilded-poem-wander.trycloudflare.com", S.ok, 1400],
    ["    verified serving — expires in 60m", S.dim, 300],
  ],
  [
    ["# an agent, same engine — MCP over stdio", S.dim, 500],
    ["→ create_site {\"name\":\"client-qa\"}", S.warn, 700],
    ["← {\"ok\":true, \"url\":\"https://client-qa.test\"}", S.ok, 1600],
    ["→ db_import {\"path\":\"prod.sql.gz\"}", S.warn, 800],
    ["← saved 20260901-auto-import, imported (214 tables),", S.ok, 1500],
    ["  urls prod.client.com → client-qa.test", S.ok, 300],
    ["→ list_mail {\"slug\":\"client-qa\"}", S.warn, 900],
    ["← [{\"subject\":\"Password Reset\", \"to\":\"admin@…\"}]", S.ok, 1200],
    ["", S.out, 200],
    ["  every tool call a human command — 59 of them", S.dim, 500],
  ],
];

const screen = document.getElementById("term-screen");

function lineEl(text, cls) {
  const el = document.createElement("span");
  if (cls) el.className = cls;
  el.textContent = text;
  return el;
}

function renderStatic(scene) {
  screen.textContent = "";
  for (const [text, cls] of scene) {
    screen.appendChild(lineEl(text + "\n", cls === S.cmd ? "" : cls));
  }
}

async function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function typeLine(text) {
  const el = lineEl("", "");
  const cursor = document.createElement("span");
  cursor.className = "t-cursor";
  screen.appendChild(el);
  screen.appendChild(cursor);
  for (const ch of text) {
    el.textContent += ch;
    await sleep(28 + Math.random() * 40);
  }
  await sleep(180);
  cursor.remove();
  el.textContent += "\n";
}

async function playScene(scene) {
  screen.textContent = "";
  for (const [text, cls, delay] of scene) {
    await sleep(delay);
    if (cls === S.cmd) await typeLine(text);
    else screen.appendChild(lineEl(text + "\n", cls));
  }
  await sleep(3800);
}

async function loop() {
  for (let i = 0; ; i = (i + 1) % scenes.length) {
    await playScene(scenes[i]);
  }
}

if (screen) {
  if (reduced) renderStatic(scenes[0]);
  else loop();
}

/* ---------- the 59 tools, grouped ---------- */
const toolGroups = {
  "discovery": ["status", "list_sites", "get_site", "localwp_sites", "list_runtimes", "list_branches"],
  "lifecycle": ["create_site", "attach_site", "import_site", "start_site", "stop_site", "restart_site", "delete_site"],
  "runtime": ["switch_php", "install_runtime", "get_http_front", "set_http_front"],
  "domains": ["set_domain", "get_domain_suffix", "set_domain_suffix", "add_hosts_entries", "remove_hosts_entries"],
  "database": ["db_creds", "db_query", "db_tables", "db_import", "db_export", "db_reset", "db_snapshot*", "db_snapshots*", "db_restore*"],
  "wordpress": ["wp_cli", "worktree_wp_cli", "get_wp_debug*", "set_wp_debug*"],
  "mail": ["list_mail*", "get_mail*", "clear_mail*"],
  "share": ["share_local_site*", "unshare_local_site*"],
  "previews": ["add_worktree", "list_worktrees", "start_worktree", "stop_worktree", "remove_worktree"],
  "media & layout": ["get_media_fallback", "set_media_fallback", "get_sites_dir", "set_sites_dir"],
  "ops": ["doctor", "doctor_fix", "get_logs", "list_jobs", "get_job"],
  "integration": ["resolve_path", "cert_status", "cert_trust", "yield_ports", "open_adminer"],
};

const grid = document.getElementById("tool-grid");
if (grid) {
  for (const [cat, tools] of Object.entries(toolGroups)) {
    const h = document.createElement("span");
    h.className = "tools__cat";
    h.textContent = cat;
    grid.appendChild(h);
    for (const t of tools) {
      const el = document.createElement("span");
      const fresh = t.endsWith("*");
      el.textContent = fresh ? t.slice(0, -1) : t;
      if (fresh) { el.className = "is-new"; el.title = "new in 0.18"; }
      grid.appendChild(el);
    }
  }
}

/* ---------- scroll reveals ---------- */
const revealables = document.querySelectorAll(".section h2, .section .section__lede, .card, .point, .bench, .spec, .steps, .how__diagram");
for (const el of revealables) el.classList.add("rv");
const io = new IntersectionObserver((entries) => {
  for (const e of entries) if (e.isIntersecting) { e.target.classList.add("is-in"); io.unobserve(e.target); }
}, { threshold: 0.12 });
for (const el of revealables) io.observe(el);

/* ---------- tab thumb follows the active section ---------- */
const links = [...document.querySelectorAll(".tabs__links a")];
const thumb = document.querySelector(".tabs__thumb");
const rule = document.querySelector(".tabs__rule");
function moveThumb(link) {
  if (!link || !thumb) return;
  const r = link.getBoundingClientRect();
  const rr = rule.getBoundingClientRect();
  thumb.style.left = (r.left - rr.left) + "px";
  thumb.style.width = r.width + "px";
  links.forEach(a => a.classList.toggle("is-active", a === link));
}
const sectionFor = new Map(links.map(a => [document.querySelector(a.getAttribute("href")), a]));
const secIO = new IntersectionObserver((entries) => {
  for (const e of entries) if (e.isIntersecting) moveThumb(sectionFor.get(e.target));
}, { rootMargin: "-30% 0px -60% 0px" });
for (const s of sectionFor.keys()) if (s) secIO.observe(s);
addEventListener("resize", () => moveThumb(links.find(a => a.classList.contains("is-active"))));

/* ---------- copy chips ---------- */
for (const el of document.querySelectorAll("[data-copy]")) {
  const btn = el.querySelector("button") || el;
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(el.dataset.copy);
      (el.querySelector("button") || el).classList.add("is-done");
      el.classList.add("is-done");
      if (btn.tagName === "BUTTON") { const t = btn.textContent; btn.textContent = "copied"; setTimeout(() => { btn.textContent = t; btn.classList.remove("is-done"); el.classList.remove("is-done"); }, 1600); }
      else setTimeout(() => el.classList.remove("is-done"), 1600);
    } catch { /* clipboard unavailable: the text is right there to select */ }
  });
}
