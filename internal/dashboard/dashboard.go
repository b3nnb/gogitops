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
  .refresh-info a { color: var(--accent); text-decoration: none; }
  .refresh-info a:hover { text-decoration: underline; }
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
  <span class="time">{{.Now}}</span>
</div>

<div class="container">
{{if .Empty}}
  <div class="empty">
    <h2>No nodes configured</h2>
    <p>Deploy an agent below — it will register automatically.</p>
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
  <p class="refresh-info">click a row to drill down &middot; <a href="/api/status">JSON API</a></p>

  <!-- Drill-down panel -->
  <div class="drilldown" id="drilldown">
    <button class="close-btn" onclick="closeDrillDown()">&times;</button>
    <h2 id="dd-name"></h2>
    <div class="sub" id="dd-sub"></div>
    <div class="metrics-grid" id="dd-grid"></div>
    <div class="service-list" id="dd-services"></div>
  </div>
{{end}}

  <!-- Deploy Agent panel -->
  <div class="deploy-panel">
    <div class="deploy-header" id="deployToggle" onclick="toggleDeploy()">
      <h2>🚀 Deploy Agent</h2>
      <span class="chevron">▶</span>
    </div>
    <div class="deploy-body" id="deployBody">
      <p>Generate the daemon command for a new node. The agent will register with this dashboard on startup — no manual config needed.</p>

      <div class="deploy-form">
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
</div>

<script>
var DASH_URL = '{{.DashURL}}';

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

function updateDeploy() {
  var host = document.getElementById('deployHostname').value || '<hostname>';
  var bind = document.getElementById('deployBind').value || '0.0.0.0';
  var port = document.getElementById('deployPort').value || '7780';
  var arch = document.getElementById('deployArch').value;

  var cmd = 'gogitops daemon \\\n  -hostname ' + host + ' \\\n  -bind ' + bind + ' \\\n  -port ' + port + ' \\\n  -interval 60 \\\n  -dashboard ' + DASH_URL;
  document.getElementById('cmdCode').textContent = cmd;

  // One-liner install: download from releases + run
  var goos = arch.split('/')[0];
  var goarch = arch.split('/')[1];
  var ext = goos === 'darwin' ? 'darwin-' + goarch : goos + '-' + goarch;
  var installCmd = 'curl -sL ' + DASH_URL + '/api/binary/' + goos + '/' + goarch + ' -o /usr/local/bin/gogitops && \\\nchmod +x /usr/local/bin/gogitops && \\\n' + cmd;
  if (goos === 'darwin') {
    installCmd = '# macOS: download binary, ad-hoc sign, then run\ncurl -sL ' + DASH_URL + '/api/binary/' + goos + '/' + goarch + ' -o /usr/local/bin/gogitops && \\\nchmod +x /usr/local/bin/gogitops && \\\ncodesign --force --sign - /usr/local/bin/gogitops && \\\n' + cmd;
  }
  document.getElementById('installCode').textContent = installCmd;

  // Systemd unit
  var systemd = '[Unit]\nDescription=GoGitOps Agent\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=/usr/local/bin/gogitops daemon \\\n  -hostname ' + host + ' \\\n  -bind ' + bind + ' \\\n  -port ' + port + ' \\\n  -interval 60 \\\n  -dashboard ' + DASH_URL + '\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=multi-user.target';
  document.getElementById('systemdCode').textContent = systemd;

  // LaunchAgent
  var launchd = '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0">\n<dict>\n    <key>Label</key>\n    <string>com.benn.gogitops</string>\n    <key>ProgramArguments</key>\n    <array>\n        <string>/usr/local/bin/gogitops</string>\n        <string>daemon</string>\n        <string>-hostname</string>\n        <string>' + host + '</string>\n        <string>-bind</string>\n        <string>' + bind + '</string>\n        <string>-port</string>\n        <string>' + port + '</string>\n        <string>-interval</string>\n        <string>60</string>\n        <string>-dashboard</string>\n        <string>' + DASH_URL + '</string>\n    </array>\n    <key>RunAtLoad</key>\n    <true/>\n    <key>KeepAlive</key>\n    <true/>\n    <key>StandardOutPath</key>\n    <string>/tmp/gogitops.log</string>\n    <key>StandardErrorPath</key>\n    <string>/tmp/gogitops.err</string>\n</dict>\n</plist>';
  document.getElementById('launchdCode').textContent = launchd;
}

function copyCode(id, btn) {
  var text = document.getElementById(id).textContent;
  navigator.clipboard.writeText(text).then(function() {
    btn.textContent = 'copied!';
    btn.classList.add('copied');
    setTimeout(function() {
      btn.textContent = 'copy';
      btn.classList.remove('copied');
    }, 2000);
  });
}

// Init deploy on load
updateDeploy();
</script>

</body>
</html>
`))
