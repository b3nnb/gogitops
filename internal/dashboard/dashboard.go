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
    max-width: 1100px;
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
  tbody tr:hover { background: rgba(79,142,247,0.04); }
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
  .badge.healthy { color: var(--green); }
  .badge.degraded { color: var(--yellow); }
  .badge.down { color: var(--red); }
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
        <th>Status</th>
        <th>Services</th>
        <th>Last Checkin</th>
        <th>Version</th>
        <th>24h Uptime</th>
      </tr>
    </thead>
    <tbody>
    {{range .Rows}}
      <tr>
        <td class="node-name">{{.NodeName}}</td>
        <td class="ip">{{.DisplayIP}}</td>
        <td><span class="badge {{.Status}}">{{.StatusEmoji}} {{.Status}}</span></td>
        <td class="services">{{.Services}}</td>
        <td class="last-checkin">{{.LastCheckin}}</td>
        <td class="version">{{.Version}}</td>
        <td class="uptime">{{.Uptime24h}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  <p class="refresh-info">Auto-refresh: page reloads on each visit &middot; <a href="/api/status" style="color:var(--accent)">JSON API</a></p>
{{end}}
</div>

</body>
</html>
`))
