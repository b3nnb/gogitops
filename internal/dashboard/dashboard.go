// Package dashboard provides the HTML template for the GoGitOps status dashboard.
package dashboard

import "html/template"

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
  tbody td {
    padding: 14px 18px;
    font-size: 14px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  tbody tr:last-child td { border-bottom: none; }
  tbody tr:hover { background: rgba(79,142,247,0.06); cursor: pointer; }
  .node-name { font-weight: 600; }
  .ip { color: var(--text-dim); font-family: "SF Mono", "Fira Code", monospace; font-size: 13px; }
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
  /* Drill-down panel */
  .drilldown {
    display: none;
    margin-top: 24px;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 24px;
    position: relative;
  }
  .drilldown.active { display: block; }
  .drilldown .close-btn {
    position: absolute;
    top: 12px;
    right: 16px;
    background: none;
    border: none;
    color: var(--text-dim);
    font-size: 18px;
    cursor: pointer;
  }
  .drilldown .close-btn:hover { color: var(--text); }
  .drilldown h2 {
    font-size: 18px;
    font-weight: 600;
    margin-bottom: 4px;
  }
  .drilldown .sub {
    font-size: 13px;
    color: var(--text-dim);
    margin-bottom: 20px;
  }
  .drilldown .metrics-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
  @media (max-width: 640px) {
    .drilldown .metrics-grid { grid-template-columns: 1fr; }
  }
  .metric-card {
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
  }
  .metric-card .value.mono {
    font-family: "SF Mono", "Fira Code", monospace;
    font-size: 14px;
  }
  .service-list {
    margin-top: 16px;
  }
  .service-list .svc-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    font-size: 13px;
  }
  .service-list .svc-row:last-child { border-bottom: none; }
  .service-list .svc-name { font-weight: 500; flex: 1; }
  .service-list .svc-status { font-family: "SF Mono", monospace; font-size: 12px; }
  .service-list .svc-status.running { color: var(--green); }
  .service-list .svc-status.down { color: var(--red); }
  .service-list .svc-status.error { color: var(--red); }
  .loading { color: var(--text-dim); font-style: italic; }
</style>
</head>
<body>

<div class="topbar">
  <span class="brand">gogitops</span>
  <span class="subtitle">status dashboard</span>
  <span class="time">{{.Now}}</span>
</div>

<div class="container">
{{if .Empty}}
  <div class="empty">
    <h2>No nodes configured</h2>
    <p>Start the dashboard with <code>--nodes name=ip,name=ip</code> to monitor your fleet.</p>
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
        <th>Last Checkin</th>
        <th>Version</th>
        <th>24h Uptime</th>
      </tr>
    </thead>
    <tbody>
    {{range .Rows}}
      <tr onclick="drillDown('{{.NodeName}}')">
        <td class="node-name">{{.NodeName}}</td>
        <td class="ip">{{.DisplayIP}}</td>
        <td><span class="badge {{if .Online}}online{{else}}offline{{end}}">{{.OnlineEmoji}} {{if .Online}}online{{else}}offline{{end}}</span></td>
        <td><span class="badge {{.HealthStatus}}">{{.HealthEmoji}} {{.HealthStatus}}</span></td>
        <td class="services">{{.Services}}</td>
        <td class="last-checkin">{{.LastCheckin}}</td>
        <td class="version">{{.Version}}</td>
        <td class="uptime">{{.Uptime24h}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  <p class="refresh-info">Auto-refresh: page reloads on each visit &middot; <a href="/api/status" style="color:var(--accent)">JSON API</a> &middot; click a row to drill down</p>

  <!-- Drill-down panel -->
  <div class="drilldown" id="drilldown">
    <button class="close-btn" onclick="closeDrillDown()">&times;</button>
    <h2 id="dd-name"></h2>
    <div class="sub" id="dd-sub"></div>
    <div class="metrics-grid" id="dd-grid"></div>
    <div class="service-list" id="dd-services"></div>
  </div>
{{end}}
</div>

<script>
function drillDown(name) {
  var panel = document.getElementById('drilldown');
  var grid = document.getElementById('dd-grid');
  var svcList = document.getElementById('dd-services');

  document.getElementById('dd-name').textContent = name;
  document.getElementById('dd-sub').textContent = 'Loading...';
  grid.innerHTML = '';
  svcList.innerHTML = '<div class="loading">Fetching metrics...</div>';
  panel.classList.add('active');

  fetch('/api/node/' + name)
    .then(function(r) { return r.json(); })
    .then(function(d) {
      if (d.error && !d.hostname) {
        document.getElementById('dd-sub').textContent = 'Offline — ' + d.error;
        grid.innerHTML = '';
        svcList.innerHTML = '';
        return;
      }

      var upSec = d.uptime_seconds || 0;
      var upStr = upSec < 60 ? upSec + 's'
                : upSec < 3600 ? Math.floor(upSec/60) + 'm ' + (upSec%60) + 's'
                : upSec < 86400 ? Math.floor(upSec/3600) + 'h ' + Math.floor((upSec%3600)/60) + 'm'
                : Math.floor(upSec/86400) + 'd ' + Math.floor((upSec%86400)/3600) + 'h';

      var sys = d.system || {};
      var peersUp = (d.peers_reachable || []).length;
      var peersDown = (d.peers_unreachable || []).length;
      var svcUp = 0, svcDown = 0;
      for (var k in d.services) {
        if (d.services[k] === 'running') svcUp++; else svcDown++;
      }

      document.getElementById('dd-sub').textContent =
        (d.agent_version || 'dev') + ' · uptime ' + upStr +
        (d.nebula_running ? ' · nebula ✅' : ' · nebula ❌');

      grid.innerHTML =
        metricCard('Services', svcUp + '/' + (svcUp+svcDown) + ' running', 'mono') +
        metricCard('Uptime', upStr, 'mono') +
        metricCard('Peers', peersUp + ' up / ' + peersDown + ' down', 'mono') +
        metricCard('Nebula', d.nebula_running ? '✅ running' : '❌ down', '') +
        metricCard('OS', sys.os + '/' + sys.arch, 'mono') +
        metricCard('Host ID', sys.host_id || '—', 'mono') +
        metricCard('Outbound IP', sys.ip || '—', 'mono') +
        metricCard('Labels', (d.labels || []).join(', '), '');

      var svcHtml = '<div class="label" style="margin-bottom:8px">SERVICES</div>';
      for (var k in d.services) {
        var v = d.services[k];
        var cls = v === 'running' ? 'running' : 'down';
        var icon = v === 'running' ? '✅' : '❌';
        svcHtml += '<div class="svc-row"><span>' + icon + '</span><span class="svc-name">' + k + '</span><span class="svc-status ' + cls + '">' + v + '</span></div>';
      }
      if (d.disk_warns && d.disk_warns.length > 0) {
        svcHtml += '<div class="label" style="margin-top:12px;margin-bottom:8px">DISK WARNINGS</div>';
        for (var i = 0; i < d.disk_warns.length; i++) {
          svcHtml += '<div class="svc-row"><span>⚠️</span><span class="svc-name">' + d.disk_warns[i] + '</span></div>';
        }
      }
      if (d.peers_reachable && d.peers_reachable.length > 0) {
        svcHtml += '<div class="label" style="margin-top:12px;margin-bottom:8px">REACHABLE PEERS</div>';
        for (var i = 0; i < d.peers_reachable.length; i++) {
          svcHtml += '<div class="svc-row"><span>✅</span><span class="svc-name">' + d.peers_reachable[i] + '</span></div>';
        }
      }
      if (d.peers_unreachable && d.peers_unreachable.length > 0) {
        svcHtml += '<div class="label" style="margin-top:12px;margin-bottom:8px">UNREACHABLE PEERS</div>';
        for (var i = 0; i < d.peers_unreachable.length; i++) {
          svcHtml += '<div class="svc-row"><span>❌</span><span class="svc-name">' + d.peers_unreachable[i] + '</span></div>';
        }
      }
      svcList.innerHTML = svcHtml;
    })
    .catch(function(e) {
      document.getElementById('dd-sub').textContent = 'Error: ' + e.message;
      grid.innerHTML = '';
      svcList.innerHTML = '';
    });
}

function metricCard(label, value, cls) {
  return '<div class="metric-card"><div class="label">' + label + '</div><div class="value ' + cls + '">' + value + '</div></div>';
}

function closeDrillDown() {
  document.getElementById('drilldown').classList.remove('active');
}
</script>

</body>
</html>
`))
