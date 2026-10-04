// Package dashboard — tier-1 customization mock (BCR-73 verification + Jun 4 retro ask).
//
// Serves a standalone HTML page at /tier that fetches /api/status (same-origin)
// and renders a fleet-compute overview with per-node hardware cards + a theme
// toggle. Two themes demonstrate the Tier-1 "theme knobs" ladder:
//
//   - dark  : the current dashboard palette (default)
//   - retro : CRT/amber monospace, scanlines, aggregate-power scoreboard
//             (Benn's Jun 4 '26 ask: retro-styled status + combined power)
//
// Theme choice persists via localStorage. No backend state — pure client-side.
// This is the "escape hatch" from the customization ladder: a static front-end
// hitting /api/status, no release cycle for theme changes.
package dashboard

import (
	"fmt"
	"html/template"
	"net/http"
	"time"
)

// tierTmpl is the tier-1 mock template.
var tierTmpl = template.Must(template.New("tier").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>gogitops — fleet tier mock</title>
<style>
  /* ── Theme variables (Tier-1 knobs) ────────────────────────────── */
  :root, [data-theme="dark"] {
    --bg: #0f1117;
    --card: #1a1d27;
    --border: #2a2d3a;
    --text: #c9d1d9;
    --text-dim: #8b949e;
    --accent: #4f8ef7;
    --green: #3fb950;
    --yellow: #d29922;
    --red: #f85149;
    --mono: "SF Mono", "Fira Code", ui-monospace, monospace;
    --sans: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    --scanline: none;
    --glow: none;
    --crt-radius: 8px;
  }
  [data-theme="retro"] {
    --bg: #0a0a0a;
    --card: #111;
    --border: #333;
    --text: #e8c04a;
    --text-dim: #997a1e;
    --accent: #e8c04a;
    --green: #4ade80;
    --yellow: #fbbf24;
    --red: #f87171;
    --mono: "VT323", "SF Mono", "Fira Code", monospace;
    --sans: "VT323", "SF Mono", monospace;
    --scanline: repeating-linear-gradient(0deg, rgba(0,0,0,0.15) 0px, rgba(0,0,0,0.15) 1px, transparent 1px, transparent 3px);
    --glow: 0 0 6px rgba(232,192,74,0.3);
    --crt-radius: 2px;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: var(--sans);
    background: var(--bg);
    color: var(--text);
    min-height: 100vh;
  }
  body::before {
    content: "";
    position: fixed; inset: 0;
    pointer-events: none;
    background: var(--scanline);
    z-index: 9999;
  }
  /* Topbar */
  .topbar {
    background: var(--card);
    border-bottom: 1px solid var(--border);
    padding: 14px 28px;
    display: flex; align-items: center; gap: 14px;
  }
  .topbar .brand { font-size: 20px; font-weight: 700; color: var(--accent); }
  [data-theme="retro"] .topbar .brand { text-shadow: var(--glow); }
  .topbar .sub { font-size: 13px; color: var(--text-dim); text-transform: uppercase; letter-spacing: 1px; }
  .topbar .badge {
    font-size: 11px; font-family: var(--mono);
    color: var(--bg); background: var(--accent);
    border-radius: 10px; padding: 2px 9px;
  }
  .topbar .spacer { flex: 1; }
  .theme-toggle {
    display: flex; gap: 0; border: 1px solid var(--border); border-radius: 6px; overflow: hidden;
  }
  .theme-toggle button {
    background: var(--card); border: none; color: var(--text-dim);
    font-family: var(--mono); font-size: 12px; padding: 6px 14px; cursor: pointer;
  }
  .theme-toggle button.active { color: var(--accent); background: rgba(79,142,247,0.1); }
  [data-theme="retro"] .theme-toggle button.active { background: rgba(232,192,74,0.1); text-shadow: var(--glow); }
  .topbar .time { font-size: 12px; color: var(--text-dim); font-family: var(--mono); }
  /* Container */
  .container { max-width: 1100px; margin: 0 auto; padding: 28px 24px; }
  /* Aggregate scoreboard */
  .scoreboard {
    background: var(--card); border: 1px solid var(--border);
    border-radius: var(--crt-radius); padding: 24px 28px; margin-bottom: 28px;
  }
  .scoreboard h2 {
    font-size: 14px; text-transform: uppercase; letter-spacing: 1px;
    color: var(--text-dim); margin-bottom: 18px;
  }
  [data-theme="retro"] .scoreboard h2 { text-shadow: var(--glow); }
  .agg-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 16px;
  }
  .agg-item { text-align: center; }
  .agg-item .agg-val {
    font-size: 32px; font-weight: 700; color: var(--accent); font-family: var(--mono);
    text-shadow: var(--glow);
  }
  [data-theme="dark"] .agg-item .agg-val { text-shadow: none; }
  .agg-item .agg-label {
    font-size: 11px; text-transform: uppercase; letter-spacing: 0.8px;
    color: var(--text-dim); margin-top: 4px;
  }
  /* Node cards */
  .nodes-grid {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 20px;
  }
  .node-card {
    background: var(--card); border: 1px solid var(--border);
    border-radius: var(--crt-radius); overflow: hidden;
  }
  .node-card.offline { opacity: 0.5; }
  .node-header {
    padding: 14px 18px; border-bottom: 1px solid var(--border);
    display: flex; align-items: center; justify-content: space-between;
  }
  .node-header .name { font-size: 16px; font-weight: 600; }
  [data-theme="retro"] .node-header .name { text-shadow: var(--glow); }
  .node-header .status { font-size: 13px; font-family: var(--mono); }
  .node-header .status.healthy { color: var(--green); }
  .node-header .status.degraded { color: var(--yellow); }
  .node-header .status.down { color: var(--red); }
  .node-body { padding: 14px 18px; }
  .spec-line {
    font-family: var(--mono); font-size: 13px; color: var(--text-dim);
    padding: 4px 0; display: flex; gap: 8px; align-items: baseline;
  }
  [data-theme="retro"] .spec-line { text-shadow: var(--glow); }
  .spec-line .k { flex: 0 0 64px; color: var(--accent); font-size: 11px; text-transform: uppercase; }
  .spec-line .v { flex: 1; color: var(--text); word-break: break-word; }
  .node-footer {
    padding: 10px 18px; border-top: 1px solid var(--border);
    font-family: var(--mono); font-size: 11px; color: var(--text-dim);
    display: flex; justify-content: space-between; flex-wrap: wrap; gap: 6px;
  }
  /* Info banner */
  .info {
    background: var(--card); border: 1px solid var(--border); border-left: 3px solid var(--accent);
    border-radius: var(--crt-radius); padding: 14px 18px; margin-bottom: 24px;
    font-size: 13px; color: var(--text-dim); line-height: 1.6;
  }
  .info strong { color: var(--text); }
  .info code { font-family: var(--mono); color: var(--accent); font-size: 12px; }
  /* Loading */
  .loading { text-align: center; padding: 60px; color: var(--text-dim); font-size: 15px; }
  /* Theme note */
  .theme-note {
    margin-top: 20px; text-align: center; font-size: 12px; color: var(--text-dim); font-family: var(--mono);
  }
  @media (max-width: 640px) {
    .agg-grid { grid-template-columns: repeat(2, 1fr); }
    .agg-item .agg-val { font-size: 26px; }
    .nodes-grid { grid-template-columns: 1fr; }
  }
</style>
</head>
<body data-theme="dark">

<div class="topbar">
  <span class="brand">gogitops</span>
  <span class="sub">fleet tier mock</span>
  <span class="badge">TIER 1</span>
  <span class="spacer"></span>
  <div class="theme-toggle">
    <button id="btnDark" class="active" onclick="setTheme('dark')">dark</button>
    <button id="btnRetro" onclick="setTheme('retro')">retro</button>
  </div>
  <span class="time" id="clock">{{.Now}}</span>
</div>

<div class="container">
  <div class="info">
    <strong>Tier-1 mock</strong> — theme knobs via CSS variables + localStorage, aggregate fleet compute, per-node hardware cards.
    This is the <code>/api/status</code> escape hatch: static HTML, no Go release cycle for theme changes.
    Toggle the theme ↑ to see the retro option (Benn's Jun 4 ask: retro-styled status + combined power).
    Benn's pick: <strong>which tier</strong> (Tier 1 theme knobs ≈ a day · Tier 2 per-user layout ≈ a weekend)?
  </div>

  <div class="scoreboard">
    <h2>⚡ Fleet Compute (online nodes only)</h2>
    <div class="agg-grid" id="aggGrid">
      <div class="agg-item"><div class="agg-val" id="aggCores">—</div><div class="agg-label">CPU cores</div></div>
      <div class="agg-item"><div class="agg-val" id="aggRAM">—</div><div class="agg-label">RAM (GB)</div></div>
      <div class="agg-item"><div class="agg-val" id="aggVRAM">—</div><div class="agg-label">VRAM (GB)</div></div>
      <div class="agg-item"><div class="agg-val" id="aggGPUs">—</div><div class="agg-label">GPUs</div></div>
      <div class="agg-item"><div class="agg-val" id="aggDisk">—</div><div class="agg-label">Disk (TB)</div></div>
      <div class="agg-item"><div class="agg-val" id="aggNodes">—</div><div class="agg-label">Nodes online</div></div>
    </div>
  </div>

  <h2 style="font-size:14px;text-transform:uppercase;letter-spacing:1px;color:var(--text-dim);margin-bottom:16px;">Node inventory</h2>
  <div class="nodes-grid" id="nodesGrid">
    <div class="loading">fetching /api/status…</div>
  </div>

  <div class="theme-note" id="themeNote"></div>
</div>

<script>
// ── Theme persistence ─────────────────────────────────────────────
(function() {
  var saved = localStorage.getItem("gogitops-tier-theme") || "dark";
  setTheme(saved);
})();

function setTheme(theme) {
  document.body.setAttribute("data-theme", theme);
  localStorage.setItem("gogitops-tier-theme", theme);
  document.getElementById("btnDark").classList.toggle("active", theme === "dark");
  document.getElementById("btnRetro").classList.toggle("active", theme === "retro");
  var note = document.getElementById("themeNote");
  if (theme === "retro") {
    note.textContent = "► retro theme: CRT amber, scanlines, glow — Benn's Jun 4 'retro-styled status + combined power' ask";
  } else {
    note.textContent = "► dark theme: matches the current dashboard palette";
  }
}

// ── Clock ─────────────────────────────────────────────────────────
function tick() {
  document.getElementById("clock").textContent = new Date().toLocaleTimeString("en-US", {hour12:false});
}
tick(); setInterval(tick, 1000);

// ── Fetch + render ────────────────────────────────────────────────
fetch("/api/status").then(function(r) { return r.json(); }).then(render).catch(function(e) {
  document.querySelector(".loading").textContent = "fetch failed: " + e.message + " (open from the dashboard origin at :7781)";
});

function render(nodes) {
  var online = nodes.filter(function(n) { return n.online; });
  var offline = nodes.filter(function(n) { return !n.online; });

  // Aggregate
  var cores = 0, ram = 0, vram = 0, gpus = 0, diskTB = 0;
  online.forEach(function(n) {
    var h = n.hardware || {};
    cores += h.cpu_cores || 0;
    ram += h.ram_gb || 0;
    (h.gpus || []).forEach(function(g) { vram += Math.round((g.vram_mb || 0) / 1024); gpus++; });
    (h.disks || []).forEach(function(d) { diskTB += (d.size_gb || 0) / 1000; });
  });
  setVal("aggCores", cores || "—");
  setVal("aggRAM", ram || "—");
  setVal("aggVRAM", vram > 0 ? vram : (gpus > 0 ? "0" : "—"));
  setVal("aggGPUs", gpus || "—");
  setVal("aggDisk", diskTB > 0 ? diskTB.toFixed(1) : "—");
  setVal("aggNodes", online.length + "/" + nodes.length);

  // Node cards
  var grid = document.getElementById("nodesGrid");
  grid.innerHTML = "";
  nodes.sort(function(a, b) {
    return (a.online === b.online) ? a.node_name.localeCompare(b.node_name) : (a.online ? -1 : 1);
  }).forEach(function(n) {
    grid.appendChild(card(n));
  });
}

function card(n) {
  var h = n.hardware || {};
  var div = document.createElement("div");
  div.className = "node-card" + (n.online ? "" : " offline");

  // Header
  var hdr = document.createElement("div");
  hdr.className = "node-header";
  var name = document.createElement("span");
  name.className = "name";
  name.textContent = n.node_name;
  var st = document.createElement("span");
  var hs = n.health_status || (n.online ? "healthy" : "down");
  st.className = "status " + hs;
  st.textContent = n.online ? (hs + " · " + n.services_up + "/" + n.services_total + " svc") : "offline";
  hdr.appendChild(name); hdr.appendChild(st);
  div.appendChild(hdr);

  // Body — hardware specs
  var body = document.createElement("div");
  body.className = "node-body";
  if (n.online && h.cpu) {
    body.appendChild(spec("CPU", h.cpu + " · " + (h.cpu_cores || "?") + " cores"));
  }
  if (n.online && h.ram_gb) {
    body.appendChild(spec("RAM", h.ram_gb + " GB" + (h.ram_type ? " " + h.ram_type : "")));
  }
  if (n.online && h.gpus && h.gpus.length > 0) {
    h.gpus.forEach(function(g) {
      var vramStr = g.vram_mb ? " · " + Math.round(g.vram_mb / 1024) + " GB VRAM" : "";
      var drvStr = g.driver ? " · " + g.driver : "";
      body.appendChild(spec("GPU", g.model + vramStr + drvStr));
    });
  } else if (n.online) {
    body.appendChild(spec("GPU", "none detected"));
  }
  if (n.online && h.disks && h.disks.length > 0) {
    var diskStr = h.disks.map(function(d) {
      return (d.model || "disk") + " " + d.size_gb + " GB" + (d.rotational ? " (HDD)" : "");
    }).join(" · ");
    body.appendChild(spec("DISK", diskStr));
  }
  if (n.online && h.motherboard) {
    body.appendChild(spec("BOARD", h.motherboard));
  }
  if (n.online && !h.cpu) {
    body.appendChild(spec("SPECS", "agent not reporting hardware inventory"));
  }
  div.appendChild(body);

  // Footer
  var ft = document.createElement("div");
  ft.className = "node-footer";
  var left = n.online ? (n.version || "") + " · " + n.response_time_ms + "ms" : (n.error ? n.error.substring(0, 80) : "");
  var right = n.online ? "uptime " + n.uptime_24h_pct.toFixed(1) + "%" : "—";
  ft.textContent = left + "  " + right;
  div.appendChild(ft);

  return div;
}

function spec(k, v) {
  var div = document.createElement("div");
  div.className = "spec-line";
  var key = document.createElement("span"); key.className = "k"; key.textContent = k;
  var val = document.createElement("span"); val.className = "v"; val.textContent = v;
  div.appendChild(key); div.appendChild(val);
  return div;
}

function setVal(id, v) { document.getElementById(id).textContent = v; }
</script>
</body>
</html>
`))

// handleTierMock serves the tier-1 customization mock at /tier.
func (h *Handler) handleTierMock(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Version": Version,
		"Now":     time.Now().Format("15:04:05 MST"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tierTmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("tier template error: %v", err), http.StatusInternalServerError)
	}
}
