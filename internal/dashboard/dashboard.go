// Package dashboard provides the HTML template for the GoGitOps status dashboard.
package dashboard

import "html/template"

// Version is the dashboard binary's build version — set from cmd/gogitops at
// startup so the topbar pill shows the real release instead of a stale "dev".
var Version = "dev"

// dashboardTmpl is the embedded HTML template for the status dashboard.
// Uses a dark theme matching NetEnv's dark UI (#0f1117 bg, #1a1d27 cards, #4f8ef7 blue accent).
var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>gogitops — status dashboard</title>
<style>
  :root {
    --bg: #0f1117;
    --card: #1a1d27;
    --border: #2a2d3a;
    --text: #c9d1d9;
    --text-dim: #8b949e;
    --accent: #4f8ef7;
    --green: #3fb950;
    --yellow: #d29922;
    --red: #f85149;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    background: var(--bg);
    color: var(--text);
    min-height: 100vh;
  }
  /* Top bar */
  .topbar {
    background: var(--card);
    border-bottom: 1px solid var(--border);
    padding: 16px 32px;
    display: flex;
    align-items: baseline;
    gap: 12px;
  }
  .topbar .brand {
    font-size: 22px;
    font-weight: 700;
    color: var(--accent);
    letter-spacing: -0.5px;
  }
  .topbar .subtitle {
    font-size: 14px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 1px;
  }
  .topbar .ver {
    font-size: 12px;
    font-family: "SF Mono", "Fira Code", monospace;
    color: var(--bg);
    background: var(--accent);
    border-radius: 10px;
    padding: 2px 10px;
  }
  .topbar .time {
    margin-left: auto;
    font-size: 13px;
    color: var(--text-dim);
  }
  /* Main content */
  .container {
    max-width: 1200px;
    margin: 32px auto;
    padding: 0 24px;
  }
  /* Table */
  table {
    width: 100%;
    table-layout: fixed;
    border-collapse: collapse;
    background: var(--card);
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid var(--border);
  }
  thead th {
    padding: 14px 18px;
    text-align: left;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--text-dim);
    background: rgba(255,255,255,0.02);
    border-bottom: 1px solid var(--border);
  }
  /* fixed-layout column plan: Node 11 · IP 22 · Online 6 · Health 9 ·
     Services 9 · Selftests 11 · Checkin 10 · Version 10 · Uptime 12 = 100% */
  thead th:nth-child(1) { width: 11%; }
  thead th:nth-child(2) { width: 22%; }
  thead th:nth-child(3) { width: 6%; }
  thead th:nth-child(4) { width: 9%; }
  thead th:nth-child(5) { width: 9%; }
  thead th:nth-child(6) { width: 11%; }
  thead th:nth-child(7) { width: 10%; }
  thead th:nth-child(8) { width: 10%; }
  thead th:nth-child(9) { width: 12%; }
  tbody td {
    padding: 14px 18px;
    font-size: 14px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  tbody tr:last-child td { border-bottom: none; }
  tbody tr:hover { background: rgba(79,142,247,0.06); cursor: pointer; }
  .node-name { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .ip { color: var(--text-dim); font-family: "SF Mono", "Fira Code", monospace; font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* Status badges */
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 500;
  }
  .badge.online { color: var(--green); }
  .badge.offline { color: var(--red); }
  .badge.healthy { color: var(--green); }
  .badge.degraded { color: var(--yellow); }
  .badge.down { color: var(--text-dim); }
  .services { font-family: "SF Mono", "Fira Code", monospace; font-size: 13px; }
  .uptime { font-family: "SF Mono", "Fira Code", monospace; font-size: 13px; }
  .version { color: var(--text-dim); font-size: 12px; }
  .tests-ok { color: var(--text-dim); font-size: 12px; }
  .tests-bad { color: var(--red); font-weight: 500; }
  .response { color: var(--text-dim); font-size: 11px; }
  .last-checkin { color: var(--text-dim); font-size: 13px; }
  /* Empty state */
  .empty {
    text-align: center;
    padding: 80px 20px;
    color: var(--text-dim);
  }
  .empty h2 { font-size: 20px; margin-bottom: 8px; color: var(--text); }
  .empty p { font-size: 14px; }
  /* Refresh */
  .refresh-info {
    margin-top: 16px;
    text-align: center;
    font-size: 12px;
    color: var(--text-dim);
  }
  .refresh-info a { color: var(--accent); text-decoration: none; }
  .refresh-info a:hover { text-decoration: underline; }
  /* Inline drill-down (expands under the tapped row — mobile-first) */
  .detail-row { display: none; }
  .detail-row.open { display: table-row; }
  .detail-row > td {
    padding: 0 0 18px 0 !important;
    border-bottom: 1px solid var(--border) !important;
  }
  .dd {
    background: rgba(255,255,255,0.02);
    border-top: 2px solid var(--accent);
    padding: 18px 18px 0 18px;
  }
  .dd .dd-sub {
    font-size: 13px;
    color: var(--text-dim);
    margin-bottom: 14px;
  }
  .dd h3 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--text-dim);
    margin: 16px 0 6px 0;
  }
  .dd .metrics-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
  @media (max-width: 640px) {
    .dd .metrics-grid { grid-template-columns: 1fr 1fr; }
    th:nth-child(6), td:nth-child(6),
    th:nth-child(7), td:nth-child(7),
    th:nth-child(8), td:nth-child(8),
    th:nth-child(9), td:nth-child(9) { display: none; }  /* tests, checkin, version, uptime — detail view has them */
  }
  .metric-card {
    min-width: 0;
    background: rgba(255,255,255,0.02);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px 16px;
  }
  .metric-card .label {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--text-dim);
    margin-bottom: 6px;
  }
  .metric-card .value {
    font-size: 15px;
    font-weight: 500;
    word-break: break-word;
  }
  .metric-card .value.mono {
    font-family: "SF Mono", "Fira Code", monospace;
    font-size: 14px;
  }
  .svc-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    font-size: 13px;
  }
  .svc-row:last-child { border-bottom: none; }
  .svc-name { font-weight: 500; flex: 1; min-width: 0; }
  .svc-status { font-family: "SF Mono", monospace; font-size: 12px; }
  .svc-status.running { color: var(--green); }
  .svc-status.down { color: var(--red); }
  .svc-status.error { color: var(--red); }
  .svc-status.skipped { color: var(--text-dim); }
  .st-toggle {
    background: none;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--accent);
    font-size: 11px;
    padding: 3px 10px;
    cursor: pointer;
  }
  .st-toggle:hover { border-color: var(--accent); }
  .st-summary .svc-name { color: var(--text-dim); }
  .st-list.st-hidden { display: none; }
  /* Services + Attributes share one row: services left, attrs right */
  .dd .two-col { display: flex; gap: 24px; margin-top: 16px; }
  .dd .two-col .col { flex: 1; min-width: 0; }
  .dd .two-col .col > h3:first-child,
  .dd .two-col .col > .dd-head:first-child { margin-top: 0; }
  /* Attributes section */
  .dd-head { display: flex; align-items: baseline; justify-content: space-between; margin: 16px 0 6px 0; }
  .dd-head h3 { margin: 0; }
  .attr-count { color: var(--text-dim); font-size: 11px; text-transform: none; letter-spacing: 0; margin-left: 6px; }
  .attr-row { display: flex; gap: 10px; padding: 4px 0; font-size: 12.5px; border-bottom: 1px solid rgba(255,255,255,0.03); }
  .attr-row:last-child { border-bottom: none; }
  .attr-key { flex: 0 0 190px; font-family: "SF Mono", "Fira Code", monospace; color: var(--accent); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .attr-val { flex: 1; min-width: 0; font-family: "SF Mono", "Fira Code", monospace; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* mobile: half-width attr column — stack key over value so values stay
     readable instead of clipping to nothing at 150px */
  @media (max-width: 640px) {
    .attr-row { flex-direction: column; gap: 1px; padding: 5px 0; }
    .attr-key { flex-basis: auto; }
    .attr-val { white-space: normal; word-break: break-all; }
  }
  .loading { color: var(--text-dim); font-style: italic; }
  /* Disk usage bars */
  .disk-row { display: flex; align-items: center; gap: 10px; padding: 5px 0; font-size: 13px; }
  .disk-row .disk-mount { flex: 0 0 170px; font-family: "SF Mono", "Fira Code", monospace; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .disk-row .disk-track { flex: 1; height: 8px; background: rgba(255,255,255,0.06); border-radius: 4px; overflow: hidden; }
  .disk-row .disk-fill { height: 100%; border-radius: 4px; background: var(--green); }
  .disk-row .disk-fill.warn { background: var(--yellow); }
  .disk-row .disk-fill.crit { background: var(--red); }
  .disk-row .disk-pct { flex: 0 0 44px; text-align: right; font-family: "SF Mono", "Fira Code", monospace; color: var(--text-dim); }
  /* Activity log lines */
  .log-line { display: flex; gap: 10px; padding: 4px 0; font-size: 12.5px; border-bottom: 1px solid rgba(255,255,255,0.03); font-family: "SF Mono", "Fira Code", monospace; }
  .log-line:last-child { border-bottom: none; }
  .log-line .log-ts { flex: 0 0 42px; color: var(--text-dim); }
  .log-line .log-cat { flex: 0 0 62px; color: var(--accent); }
  .log-line .log-msg { flex: 1; min-width: 0; color: var(--text); word-break: break-word; }
  .dd .dd-fail { color: var(--red); font-size: 13px; padding: 8px 0; }

  /* Deploy panel */
  .deploy-panel {
    margin-top: 24px;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }
  .deploy-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 20px;
    cursor: pointer;
    user-select: none;
  }
  .deploy-header:hover { background: rgba(79,142,247,0.04); }
  .deploy-header h2 {
    font-size: 16px;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .deploy-header .chevron {
    font-size: 12px;
    color: var(--text-dim);
    transition: transform 0.2s;
  }
  .deploy-header.open .chevron { transform: rotate(90deg); }
  .deploy-body {
    display: none;
    padding: 0 20px 20px 20px;
  }
  .deploy-body.open { display: block; }
  .deploy-body p {
    font-size: 13px;
    color: var(--text-dim);
    margin-bottom: 12px;
    line-height: 1.5;
  }
  .deploy-form {
    display: flex;
    gap: 12px;
    margin-bottom: 20px;
    flex-wrap: wrap;
  }
  .deploy-form label {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--text-dim);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .deploy-form input, .deploy-form select {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 8px 12px;
    color: var(--text);
    font-size: 14px;
    font-family: "SF Mono", "Fira Code", monospace;
    outline: none;
  }
  .deploy-form input:focus, .deploy-form select:focus {
    border-color: var(--accent);
  }
  .deploy-form select { cursor: pointer; }
  .code-block {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px 16px;
    margin-bottom: 12px;
    position: relative;
  }
  .code-block .code-label {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: var(--text-dim);
    margin-bottom: 8px;
  }
  .code-block pre {
    font-family: "SF Mono", "Fira Code", monospace;
    font-size: 13px;
    line-height: 1.6;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
    margin: 0;
  }
  .copy-btn {
    position: absolute;
    top: 10px;
    right: 10px;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text-dim);
    font-size: 11px;
    padding: 4px 10px;
    cursor: pointer;
  }
  .copy-btn:hover { color: var(--text); border-color: var(--accent); }
  .copy-btn.copied { color: var(--green); border-color: var(--green); }
  .tab-bar {
    display: flex;
    gap: 0;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--border);
  }
  .tab-bar button {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-dim);
    font-size: 13px;
    padding: 8px 16px;
    cursor: pointer;
  }
  .tab-bar button:hover { color: var(--text); }
  .tab-bar button.active { color: var(--accent); border-bottom-color: var(--accent); }
  .tab-content { display: none; }
  .tab-content.active { display: block; }
</style>
</head>
<body>

<div class="topbar">
  <span class="brand">gogitops</span>
  <span class="subtitle">status dashboard</span>
  <span class="ver">{{if .Version}}{{.Version}}{{else}}dev{{end}}</span>
  <span class="time">{{.Now}}</span>
</div>

<div class="container">
  <!-- Deploy Agent panel -->
  <div class="deploy-panel">
    <div class="deploy-header" id="deployToggle" onclick="toggleDeploy()">
      <h2>🚀 Deploy Agent</h2>
      <span class="chevron">▶</span>
    </div>
    <div class="deploy-body" id="deployBody">
      <p>Generate the daemon command for a new node. The agent will register with this dashboard on startup — no manual config needed.</p>

      <div class="deploy-form">
        <label>Beacon URL <small>(saved)</small>
          <input type="text" id="deployDash" placeholder="http://10.2.0.102:7781" oninput="deploySettingChanged()">
        </label>
        <label>Config Repo URL <small>(saved)</small>
          <input type="text" id="deployRepo" placeholder="git@github.com:b3nnb/gogitops.git" oninput="deploySettingChanged()">
        </label>
        <label>Hostname
          <input type="text" id="deployHostname" placeholder="e.g. nas" oninput="updateDeploy()">
        </label>
        <label>Bind
          <input type="text" id="deployBind" value="0.0.0.0" oninput="updateDeploy()">
        </label>
        <label>Port
          <input type="text" id="deployPort" value="7780" oninput="updateDeploy()">
        </label>
        <label>OS/Arch
          <select id="deployArch" onchange="updateDeploy()">
            <option value="linux/amd64">Linux amd64</option>
            <option value="linux/arm64">Linux arm64</option>
            <option value="darwin/arm64">macOS arm64</option>
            <option value="darwin/amd64">macOS amd64</option>
          </select>
        </label>
      </div>

      <div class="tab-bar">
        <button class="active" onclick="switchTab('cmd', this)">Daemon Command</button>
        <button onclick="switchTab('install', this)">One-Liner Install</button>
        <button onclick="switchTab('systemd', this)">Systemd Unit</button>
        <button onclick="switchTab('launchd', this)">LaunchAgent (macOS)</button>
      </div>

      <div class="tab-content active" id="tab-cmd">
        <div class="code-block">
          <div class="code-label">Run on the target node</div>
          <button class="copy-btn" onclick="copyCode('cmdCode', this)">copy</button>
          <pre id="cmdCode"></pre>
        </div>
      </div>

      <div class="tab-content" id="tab-install">
        <div class="code-block">
          <div class="code-label">Download + run in one shot</div>
          <button class="copy-btn" onclick="copyCode('installCode', this)">copy</button>
          <pre id="installCode"></pre>
        </div>
      </div>

      <div class="tab-content" id="tab-systemd">
        <div class="code-block">
          <div class="code-label">/etc/systemd/system/gogitops.service</div>
          <button class="copy-btn" onclick="copyCode('systemdCode', this)">copy</button>
          <pre id="systemdCode"></pre>
        </div>
      </div>

      <div class="tab-content" id="tab-launchd">
        <div class="code-block">
          <div class="code-label">~/Library/LaunchAgents/com.benn.gogitops.plist</div>
          <button class="copy-btn" onclick="copyCode('launchdCode', this)">copy</button>
          <pre id="launchdCode"></pre>
        </div>
      </div>
    </div>
  </div>

{{if .Empty}}
  <div class="empty">
    <h2>No nodes configured</h2>
    <p>Use the Deploy Agent panel above — it will register automatically.</p>
  </div>
{{else}}
  <table>
    <thead>
      <tr>
        <th>Node</th>
        <th>IP</th>
        <th>Online</th>
        <th>Health</th>
        <th>Services</th>
        <th>Selftests</th>
        <th>Last Checkin</th>
        <th>Version</th>
        <th>Uptime</th>
      </tr>
    </thead>
    <tbody>
    {{range .Rows}}
      <tr onclick="toggleDetail('{{.NodeName}}')" class="node-row">
        <td class="node-name" title="{{.NodeName}}">{{.NodeName}}</td>
        <td class="ip" title="{{.DisplayIP}}">{{.DisplayIP}}</td>
        <td><span class="badge {{if .Online}}online{{else}}offline{{end}}" title="{{if .Online}}online{{else}}offline{{end}}">{{.OnlineEmoji}}</span></td>
        <td><span class="badge {{.HealthStatus}}">{{.HealthStatus}}</span></td>
        <td class="services">{{.Services}}</td>
        <td class="{{.TestsClass}}" title="{{.TestsFailing}}">{{.Tests}}</td>
        <td class="last-checkin">{{.LastCheckin}}</td>
        <td class="version">{{.Version}}</td>
        <td class="uptime" title="agent uptime &middot; 24h availability">{{.Uptime}}</td>
      </tr>
      <tr class="detail-row" id="detail-{{.NodeName}}">
        <td colspan="9"><div class="dd" id="dd-{{.NodeName}}"></div></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  <p class="refresh-info">tap a row to expand full details &middot; <a href="/api/status">JSON API</a></p>
{{end}}

</div>

<script>
var DASH_URL = '{{.DashURL}}';

function esc(s) {
  return String(s).replace(/[&<>"']/g, function(ch) {
    return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch];
  });
}

var openNode = null;
var refreshTimer = null;
var selftestsOpen = {};  // per-node: SELFTESTS full list expanded
var attrsOpen = {};        // per-node: ATTRIBUTES section showing the yaml view

function toggleDetail(name) {
  var row = document.getElementById('detail-' + name);
  if (!row) return;
  var isOpen = row.classList.contains('open');
  closeAllDetails();
  if (!isOpen) {
    row.classList.add('open');
    openNode = name;
    loadDetail(name);
    refreshTimer = setInterval(function() { if (openNode) loadDetail(openNode, true); }, 30000);
  }
}

function closeAllDetails() {
  document.querySelectorAll('.detail-row.open').forEach(function(el) { el.classList.remove('open'); });
  openNode = null;
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
}

function toggleSelftests(name) {
  selftestsOpen[name] = !selftestsOpen[name];
  var list = document.getElementById('st-list-' + name);
  if (list) list.classList.toggle('st-hidden', !selftestsOpen[name]);
  var box = document.getElementById('dd-' + name);
  var btn = box ? box.querySelector('.st-toggle') : null;
  if (btn) btn.textContent = selftestsOpen[name] ? 'hide passing' : 'show all';
}

function toggleAttrs(name) {
  attrsOpen[name] = !attrsOpen[name];
  loadDetail(name, true); // re-render with the new view state
}

// yamlScalar quotes a single-line string only when YAML needs it.
// JSON double-quoted strings are valid YAML double-quoted scalars.
function yamlScalar(s) {
  if (s === '' || !isNaN(Number(s)) || s === 'true' || s === 'false' || s === 'null' || s === '~' ||
      /^[\s#\-?:\[\]{},&*!|>'"%@\u0060]/.test(s) || /:\s/.test(s) || /\s#/.test(s) || /\s$/.test(s)) {
    return JSON.stringify(s);
  }
  return s;
}

// yamlOf renders the attr map as a YAML document — the reference for
// attr.x substitution and when_attr conditions in recipes.
function yamlOf(obj) {
  var keys = Object.keys(obj).sort();
  var out = '';
  for (var i = 0; i < keys.length; i++) {
    var v = String(obj[keys[i]]);
    if (v.indexOf('\n') >= 0) {
      out += keys[i] + ': |-\n';
      var lines = v.split('\n');
      for (var j = 0; j < lines.length; j++) out += '  ' + lines[j] + '\n';
    } else {
      out += keys[i] + ': ' + yamlScalar(v) + '\n';
    }
  }
  return out;
}

function loadDetail(name, quiet) {
  var box = document.getElementById('dd-' + name);
  if (!box) return;
  if (!quiet) box.innerHTML = '<div class="loading">Fetching details…</div>';
  Promise.all([
    fetch('/api/node/' + name).then(function(r) { return r.json(); }).catch(function() { return null; }),
    fetch('/api/node/' + name + '/logs?limit=10').then(function(r) { return r.json(); }).catch(function() { return null; }),
    fetch('/api/node/' + name + '/tests').then(function(r) { return r.json(); }).catch(function() { return null; }),
    fetch('/api/node/' + name + '/attrs').then(function(r) { return r.json(); }).catch(function() { return null; })
  ]).then(function(res) {
    renderDetail(name, res[0], Array.isArray(res[1]) ? {logs: res[1]} : res[1], res[2], res[3]);
  });
}

function renderDetail(name, d, logs, tests, attrs) {
  var box = document.getElementById('dd-' + name);
  if (!box) return;
  if (!d || (!d.hostname && d.error)) {
    box.innerHTML = '<div class="dd-fail">offline — ' + esc((d && d.error) || 'unreachable') + '</div>' + renderLogs(logs);
    return;
  }

  var upSec = d.uptime_seconds || 0;
  var upStr = upSec < 60 ? upSec + 's'
            : upSec < 3600 ? Math.floor(upSec/60) + 'm'
            : upSec < 86400 ? Math.floor(upSec/3600) + 'h ' + Math.floor((upSec%3600)/60) + 'm'
            : Math.floor(upSec/86400) + 'd ' + Math.floor((upSec%86400)/3600) + 'h';
  var sys = d.system || {};
  var peersUp = (d.peers_reachable || []).length;
  var peersDown = (d.peers_unreachable || []).length;
  var svcUp = 0, svcDown = 0;
  for (var k in d.services) { if (d.services[k] === 'running') svcUp++; else svcDown++; }

  var html = '<div class="dd-sub">' + esc(d.agent_version || 'dev') + ' &middot; uptime ' + upStr +
             (d.nebula_running ? ' &middot; nebula ' + esc(d.nebula_ip || '✅') : ' &middot; nebula ❌') + '</div>';

  html += '<div class="metrics-grid">';
  html += metricCard('Services', svcUp + '/' + (svcUp+svcDown) + ' running');
  html += metricCard('Peers', peersUp + ' up / ' + peersDown + ' down');
  html += metricCard('OS', (sys.os || '—') + '/' + (sys.arch || ''));
  html += metricCard('Host ID', sys.host_id || '—');
  html += metricCard('Outbound IP', sys.ip || '—');
  html += metricCard('Labels', (d.labels || []).join(', ') || '—');
  html += '</div>';

  // Disk usage bars (per-mount, always shown)
  if (d.disk && d.disk.length > 0) {
    html += '<h3>DISK</h3>';
    for (var i = 0; i < d.disk.length; i++) {
      var m = d.disk[i];
      var cls = m.used_pct >= m.crit_pct ? 'crit' : (m.used_pct >= m.warn_pct ? 'warn' : '');
      html += '<div class="disk-row"><span class="disk-mount">' + esc(m.mount) + '</span>' +
              '<span class="disk-track"><span class="disk-fill ' + cls + '" style="width:' + Math.min(100, m.used_pct) + '%"></span></span>' +
              '<span class="disk-pct">' + m.used_pct + '%</span></div>';
    }
  }
  if (d.disk_warns && d.disk_warns.length > 0) {
    html += '<div class="dd-fail">⚠ ' + esc(d.disk_warns.join(' · ')) + '</div>';
  }

  // Services + Attributes share a row — services left, attrs right
  html += '<div class="two-col"><div class="col">';
  html += '<h3>SERVICES</h3>';
  var names = Object.keys(d.services).sort(function(a, b) {
    return (d.services[a] === 'running') - (d.services[b] === 'running');
  });
  for (var j = 0; j < names.length; j++) {
    var k2 = names[j], v = d.services[k2];
    var icon = v === 'running' ? '✅' : '❌';
    var cls2 = v === 'running' ? 'running' : 'down';
    html += '<div class="svc-row"><span>' + icon + '</span><span class="svc-name">' + esc(k2) + '</span><span class="svc-status ' + cls2 + '">' + esc(v) + '</span></div>';
  }

  html += '</div><div class="col">';
  // Attributes — the node's live device attr store; list view, plus a YAML
  // view to copy for attr.x / when_attr recipe reference
  if (attrs && Object.keys(attrs).length > 0) {
    var akeys = Object.keys(attrs).sort();
    html += '<div class="dd-head"><h3>ATTRIBUTES <span class="attr-count">' + akeys.length + ' keys</span></h3>' +
            '<button class="st-toggle" onclick="toggleAttrs(\'' + esc(name) + '\')">' + (attrsOpen[name] ? 'hide yaml' : 'view yaml') + '</button></div>';
    if (attrsOpen[name]) {
      html += '<div class="code-block"><div class="code-label">device attr store &mdash; yaml (copy for recipe reference)</div>' +
              '<button class="copy-btn" onclick="copyCode(\'attrsYaml\', this)">copy</button>' +
              '<pre id="attrsYaml">' + esc(yamlOf(attrs)) + '</pre></div>';
    } else {
      for (var a = 0; a < akeys.length; a++) {
        var av = String(attrs[akeys[a]]);
        html += '<div class="attr-row"><span class="attr-key" title="' + esc(akeys[a]) + '">' + esc(akeys[a]) + '</span>' +
                '<span class="attr-val" title="' + esc(av).replace(/\n/g, ' &middot; ') + '">' + esc(av) + '</span></div>';
      }
    }
  }  html += '</div></div>';

  // Peers — unreachable first (failures first)
  if (peersDown > 0 || peersUp > 0) {
    html += '<h3>PEERS</h3>';
    if (d.peers_unreachable && d.peers_unreachable.length > 0) {
      for (var u = 0; u < d.peers_unreachable.length; u++) {
        html += '<div class="svc-row"><span>❌</span><span class="svc-name">' + esc(d.peers_unreachable[u]) + '</span><span class="svc-status down">unreachable</span></div>';
      }
    }
    for (var r2 = 0; r2 < (d.peers_reachable || []).length; r2++) {
      html += '<div class="svc-row"><span>✅</span><span class="svc-name">' + esc(d.peers_reachable[r2]) + '</span><span class="svc-status running">reachable</span></div>';
    }
  }

  // Selftests — failures always visible; passing/skipped behind a toggle.
  if (tests) {
    var tp = tests.pass || 0, tf = tests.fail || 0, tsk = tests.skip || 0;
    if (tp + tf + tsk > 0) {
      var named = [];
      var results = tests.results || [];
      for (var ri = 0; ri < results.length; ri++) {
        if (results[ri] && results[ri].name) named.push(results[ri]);
      }
      var failing = [], passing = [], skipped = [];
      for (var si = 0; si < named.length; si++) {
        if (named[si].status === 'fail') failing.push(named[si]);
        else if (named[si].status === 'skip') skipped.push(named[si]);
        else passing.push(named[si]);
      }
      // pre-v0.7.15 agents serialize results as empty objects — fall back
      // to the attrs.tests.failing summary so failures still show
      if (failing.length === 0 && tf > 0 && tests.attrs && tests.attrs['tests.failing']) {
        var fb = tests.attrs['tests.failing'].split(',');
        for (var fi = 0; fi < fb.length; fi++) {
          var fname = fb[fi].trim();
          if (fname) failing.push({name: fname, module: ''});
        }
      }
      var ran = (tests.when || '').split('T').pop().split(':').slice(0, 2).join(':');
      html += '<h3>SELFTESTS</h3>';
      for (var f = 0; f < failing.length; f++) {
        html += '<div class="svc-row"><span>❌</span><span class="svc-name">' + esc((failing[f].module ? failing[f].module + '/' : '') + failing[f].name) + '</span><span class="svc-status down">failing</span></div>';
      }
      if (tf === 0) {
        html += '<div class="svc-row"><span>✅</span><span class="svc-name">all ' + tp + ' selftests passing</span><span class="svc-status running">✓</span></div>';
      }
      if (named.length > 0) {
        html += '<div class="svc-row st-summary"><span></span><span class="svc-name">' + tp + ' passing &middot; ' + tsk + ' skipped &middot; ' + tf + ' failing' + (ran ? ' &middot; ran ' + ran : '') + '</span><span class="svc-status ' + (tf > 0 ? 'down' : 'running') + '">' + (tp + tf + tsk) + '</span></div>';
        html += '<div class="svc-row"><span></span><span class="svc-name"><button class="st-toggle" onclick="toggleSelftests(\'' + esc(name) + '\')">' + (selftestsOpen[name] ? 'hide passing' : 'show all ' + (tp + tf + tsk)) + '</button></span><span></span></div>';
        html += '<div class="st-list' + (selftestsOpen[name] ? '' : ' st-hidden') + '" id="st-list-' + esc(name) + '">';
        for (var p = 0; p < passing.length; p++) {
          html += '<div class="svc-row"><span>✅</span><span class="svc-name">' + esc((passing[p].module ? passing[p].module + '/' : '') + passing[p].name) + '</span><span class="svc-status running">pass</span></div>';
        }
        for (var k = 0; k < skipped.length; k++) {
          html += '<div class="svc-row"><span>⏭</span><span class="svc-name">' + esc((skipped[k].module ? skipped[k].module + '/' : '') + skipped[k].name) + '</span><span class="svc-status skipped">skip</span></div>';
        }
        html += '</div>';
      } else {
        html += '<div class="svc-row st-summary"><span></span><span class="svc-name">agent pre-v0.7.15 — full list available after it self-updates</span><span></span></div>';
      }
    }
  }

  html += renderLogs(logs);
  box.innerHTML = html;
}

function renderLogs(logs) {
  if (!logs) return '';
  var entries = Array.isArray(logs) ? logs : (logs.logs || []);
  if (entries.length === 0) return '';
  var out = '<h3>RECENT ACTIVITY</h3>';
  entries = entries.slice(0, 10);
  for (var i = 0; i < entries.length; i++) {
    var e = entries[i];
    var ts = (e.ts || '').replace(/^\d{4}-\d{2}-\d{2}T/, '').replace(/\+.*/, '');
    out += '<div class="log-line"><span class="log-ts">' + esc(ts) + '</span><span class="log-cat">' + esc(e.cat || e.level || '') + '</span><span class="log-msg">' + esc(e.msg || e.message || '') + '</span></div>';
  }
  return out;
}

function metricCard(label, value) {
  return '<div class="metric-card"><div class="label">' + label + '</div><div class="value mono">' + esc(value) + '</div></div>';
}

/* Deploy Agent */
function toggleDeploy() {
  var header = document.getElementById('deployToggle');
  var body = document.getElementById('deployBody');
  var isOpen = body.classList.contains('open');
  if (isOpen) {
    body.classList.remove('open');
    header.classList.remove('open');
  } else {
    body.classList.add('open');
    header.classList.add('open');
    updateDeploy();
  }
}

function switchTab(name, btn) {
  document.querySelectorAll('.tab-content').forEach(function(el) { el.classList.remove('active'); });
  document.querySelectorAll('.tab-bar button').forEach(function(el) { el.classList.remove('active'); });
  document.getElementById('tab-' + name).classList.add('active');
  btn.classList.add('active');
}

var deploySaveTimer = null;
function deploySettingChanged() {
  updateDeploy();
  clearTimeout(deploySaveTimer);
  deploySaveTimer = setTimeout(saveDeploySettings, 600);
}

function saveDeploySettings() {
  var payload = {
    dashboard_url: document.getElementById('deployDash').value.trim(),
    repo_url: document.getElementById('deployRepo').value.trim()
  };
  if (!payload.dashboard_url && !payload.repo_url) return;
  fetch('/api/settings', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(payload) })
    .catch(function(e) { console.log('settings save failed', e); });
}

function loadDeploySettings() {
  fetch('/api/settings').then(function(r) { return r.json(); }).then(function(s) {
    if (s.dashboard_url && !document.getElementById('deployDash').value) {
      document.getElementById('deployDash').value = s.dashboard_url;
    }
    if (s.repo_url && !document.getElementById('deployRepo').value) {
      document.getElementById('deployRepo').value = s.repo_url;
    }
    updateDeploy();
  }).catch(function(e) { console.log('settings load failed', e); });
}

function updateDeploy() {
  // Escape-proof: build shell text from char codes — no source backslash
  // counting, ever. (The old hand-escaped version had \'\' string
  // collisions + doubled backslashes that killed the whole script block.)
  var BS = String.fromCharCode(92);   // backslash
  var LF = String.fromCharCode(10);   // newline
  var NL = BS + LF;                   // shell line continuation

  var host = document.getElementById('deployHostname').value || '<hostname>';
  var bind = document.getElementById('deployBind').value || '0.0.0.0';
  var port = document.getElementById('deployPort').value || '7780';
  var arch = document.getElementById('deployArch').value;
  var dash = document.getElementById('deployDash').value.trim() || DASH_URL;
  var repo = document.getElementById('deployRepo').value.trim();

  var cmd = 'gogitops daemon ' + NL +
    '  -hostname ' + host + ' ' + NL +
    '  -bind ' + bind + ' ' + NL +
    '  -port ' + port + ' ' + NL +
    '  -interval 60 ' + NL +
    '  -dashboard ' + dash;
  document.getElementById('cmdCode').textContent = cmd;

  // One-liner install: download from releases + run
  var goos = arch.split('/')[0];
  var goarch = arch.split('/')[1];
  var installCmd = 'curl -sL ' + dash + '/api/binary/' + goos + '/' + goarch + ' -o /usr/local/bin/gogitops && ' + NL +
    'chmod +x /usr/local/bin/gogitops && ' + NL + cmd;
  if (goos === 'darwin') {
    installCmd = '# macOS: download binary, ad-hoc sign, then run' + LF +
      'curl -sL ' + dash + '/api/binary/' + goos + '/' + goarch + ' -o /usr/local/bin/gogitops && ' + NL +
      'chmod +x /usr/local/bin/gogitops && ' + NL +
      'codesign --force --sign - /usr/local/bin/gogitops && ' + NL + cmd;
  }
  // Bake installer config: agent.env + config repo clone
  if (goos === 'linux' && (dash || repo)) {
    var envLines = 'GOGITOPS_HOSTNAME=' + host + LF +
      'GOGITOPS_BIND=' + bind + LF +
      'GOGITOPS_PORT=' + port + LF +
      'GOGITOPS_DASHBOARD_URL=' + dash + LF;
    if (repo) { envLines += 'GOGITOPS_REPO_URL=' + repo + LF; }
    installCmd = 'sudo mkdir -p /etc/gogitops && ' + NL +
      "printf '%s' '" + envLines + "' | sudo tee /etc/gogitops/agent.env > /dev/null && " + NL + installCmd;
  }
  if (repo) {
    installCmd = installCmd.replace('gogitops daemon', 'git clone ' + repo + ' ~/.config/gogitops && ' + NL + 'gogitops daemon');
  }
  document.getElementById('installCode').textContent = installCmd;

  // Systemd unit
  var systemd = '[Unit]' + LF + 'Description=GoGitOps Agent' + LF + 'After=network.target' + LF + LF +
    '[Service]' + LF + 'Type=simple' + LF +
    'ExecStart=/usr/local/bin/gogitops daemon ' + NL +
    '  -hostname ' + host + ' ' + NL +
    '  -bind ' + bind + ' ' + NL +
    '  -port ' + port + ' ' + NL +
    '  -interval 60 ' + NL +
    '  -dashboard ' + dash + LF +
    'Restart=always' + LF + 'RestartSec=5' + LF + LF +
    '[Install]' + LF + 'WantedBy=multi-user.target';
  document.getElementById('systemdCode').textContent = systemd;

  // LaunchAgent
  var P = function(s) { return '<string>' + s + '</string>'; };
  var launchd = '<?xml version="1.0" encoding="UTF-8"?>' + LF +
    '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' + LF +
    '<plist version="1.0">' + LF + '<dict>' + LF +
    '    <key>Label</key>' + LF + '    ' + P('com.benn.gogitops') + LF +
    '    <key>ProgramArguments</key>' + LF + '    <array>' + LF +
    '        ' + P('/usr/local/bin/gogitops') + LF +
    '        ' + P('daemon') + LF +
    '        ' + P('-hostname') + LF + '        ' + P(host) + LF +
    '        ' + P('-bind') + LF + '        ' + P(bind) + LF +
    '        ' + P('-port') + LF + '        ' + P(port) + LF +
    '        ' + P('-interval') + LF + '        ' + P('60') + LF +
    '        ' + P('-dashboard') + LF + '        ' + P(dash) + LF +
    '    </array>' + LF +
    '    <key>RunAtLoad</key>' + LF + '    <true/>' + LF +
    '    <key>KeepAlive</key>' + LF + '    <true/>' + LF +
    '    <key>StandardOutPath</key>' + LF + '    ' + P('/tmp/gogitops.log') + LF +
    '    <key>StandardErrorPath</key>' + LF + '    ' + P('/tmp/gogitops.err') + LF +
    '  </dict>' + LF + '</plist>';
  if (repo || dash) {
    var envDict = '';
    if (dash) { envDict += '    <key>GOGITOPS_DASHBOARD_URL</key>' + LF + '    ' + P(dash) + LF; }
    if (repo) { envDict += '    <key>GOGITOPS_REPO_URL</key>' + LF + '    ' + P(repo) + LF; }
    launchd = launchd.replace('  </dict>' + LF + '</plist>',
      '    <key>EnvironmentVariables</key>' + LF + '    <dict>' + LF + envDict + '    </dict>' + LF + '  </dict>' + LF + '</plist>');
  }
  document.getElementById('launchdCode').textContent = launchd;
}

// Init deploy on load
loadDeploySettings();
updateDeploy();
</script>

</body>
</html>
`))
