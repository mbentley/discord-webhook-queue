package server

import "fmt"

// renderUI returns the self-contained HTML page for the root endpoint.
// authHeader is injected so the JS uses the correct token header name.
func renderUI(authHeader string) string {
	return fmt.Sprintf(uiTemplate, authHeader, authHeader)
}

// uiTemplate is a self-contained HTML page with inline CSS and JS.
// fmt.Sprintf substitutions: %s = auth header label, %q = auth header JS string.
const uiTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>discord-webhook-queue</title>
<style>
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: system-ui, -apple-system, sans-serif; background: #f5f5f5; color: #222; padding: 2rem; max-width: 800px; }
h1 { font-size: 1.4rem; margin-bottom: 0.25rem; }
.subtitle { color: #666; font-size: 0.875rem; margin-bottom: 1.75rem; }
.auth-bar { background: #fff; border: 1px solid #ddd; border-radius: 6px; padding: 1rem; margin-bottom: 1.5rem; display: flex; align-items: center; gap: 0.75rem; }
.auth-bar label { font-size: 0.8rem; color: #555; white-space: nowrap; font-family: monospace; }
.auth-bar input { flex: 1; padding: 0.375rem 0.6rem; border: 1px solid #ccc; border-radius: 4px; font-size: 0.875rem; font-family: monospace; min-width: 0; }
.card { background: #fff; border: 1px solid #ddd; border-radius: 6px; padding: 1.25rem; margin-bottom: 1rem; }
.card-header { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.5rem; }
.method { font-size: 0.7rem; font-weight: 700; padding: 0.2rem 0.45rem; border-radius: 3px; letter-spacing: 0.06em; }
.get    { background: #dbeafe; color: #1d4ed8; }
.post   { background: #dcfce7; color: #166534; }
.delete { background: #fee2e2; color: #991b1b; }
.path { font-family: monospace; font-size: 0.95rem; font-weight: 600; }
.desc { font-size: 0.825rem; color: #555; margin-bottom: 0.75rem; }
.controls { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
input[type=number] { padding: 0.375rem 0.6rem; border: 1px solid #ccc; border-radius: 4px; font-size: 0.875rem; width: 10rem; }
button { padding: 0.375rem 0.9rem; border: none; border-radius: 4px; font-size: 0.825rem; cursor: pointer; background: #3b82f6; color: #fff; }
button:hover { background: #2563eb; }
button.danger { background: #ef4444; }
button.danger:hover { background: #dc2626; }
a.subtle { font-size: 0.8rem; color: #3b82f6; }
.badge { font-size: 0.775rem; font-weight: 600; }
.ok  { color: #16a34a; }
.err { color: #dc2626; }
.response { margin-top: 0.75rem; display: none; }
.response pre { background: #1e1e1e; color: #d4d4d4; padding: 0.75rem 1rem; border-radius: 4px; font-size: 0.775rem; overflow-x: auto; white-space: pre-wrap; word-break: break-all; max-height: 280px; overflow-y: auto; }
</style>
</head>
<body>
<h1>discord-webhook-queue</h1>
<p class="subtitle">Discord webhook proxy &amp; queue daemon</p>

<div class="auth-bar">
  <label>%s:</label>
  <input type="password" id="auth-token" placeholder="leave blank if auth is not configured">
</div>

<div class="card">
  <div class="card-header"><span class="method post">POST</span><span class="path">/webhooks/{id}/{token}</span></div>
  <p class="desc">Enqueue a Discord webhook message. Accepts <code>application/json</code> and <code>multipart/form-data</code>. Returns 204 on success.</p>
  <div class="controls">
    <input type="text" id="webhook-id" placeholder="webhook ID">
    <input type="text" id="webhook-token" placeholder="webhook token">
  </div>
  <div class="controls" style="margin-top:0.5rem;">
    <textarea id="webhook-payload" placeholder='{"content":"hello"}' rows="3" style="flex:1;padding:0.375rem 0.6rem;border:1px solid #ccc;border-radius:4px;font-family:monospace;font-size:0.825rem;resize:vertical;"></textarea>
  </div>
  <div class="controls" style="margin-top:0.5rem;">
    <button onclick="sendWebhook()">Enqueue</button>
    <span id="r-ingest-badge" class="badge"></span>
  </div>
  <div class="response" id="r-ingest"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method get">GET</span><span class="path">/status</span></div>
  <p class="desc">Current daemon state, queue depth, and last failure time.</p>
  <div class="controls">
    <button onclick="call('GET','/status','r-status')">Fetch</button>
    <span id="r-status-badge" class="badge"></span>
  </div>
  <div class="response" id="r-status"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method get">GET</span><span class="path">/queue</span></div>
  <p class="desc">List all queued messages. Webhook tokens and payloads are not included.</p>
  <div class="controls">
    <button onclick="call('GET','/queue','r-queue')">Fetch</button>
    <span id="r-queue-badge" class="badge"></span>
  </div>
  <div class="response" id="r-queue"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method get">GET</span><span class="path">/metrics</span></div>
  <p class="desc">Prometheus metrics.</p>
  <div class="controls">
    <button onclick="call('GET','/metrics','r-metrics')">Fetch</button>
    <a class="subtle" href="/metrics" target="_blank">open in new tab ↗</a>
    <span id="r-metrics-badge" class="badge"></span>
  </div>
  <div class="response" id="r-metrics"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method post">POST</span><span class="path">/alert/test</span></div>
  <p class="desc">Send a test alert email to verify SMTP configuration.</p>
  <div class="controls">
    <button onclick="call('POST','/alert/test','r-alert')">Send Test Alert</button>
    <span id="r-alert-badge" class="badge"></span>
  </div>
  <div class="response" id="r-alert"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method delete">DELETE</span><span class="path">/queue/{id}</span></div>
  <p class="desc">Remove a specific queued message by ID. Message IDs appear in the logs.</p>
  <div class="controls">
    <input type="number" id="delete-id" placeholder="message ID" min="1">
    <button class="danger" onclick="deleteOne()">Delete</button>
    <span id="r-delete-badge" class="badge"></span>
  </div>
  <div class="response" id="r-delete"><pre></pre></div>
</div>

<div class="card">
  <div class="card-header"><span class="method delete">DELETE</span><span class="path">/queue</span></div>
  <p class="desc">Remove all queued messages that are not currently being sent to Discord.</p>
  <div class="controls">
    <button class="danger" onclick="clearAll()">Clear Queue</button>
    <span id="r-clear-badge" class="badge"></span>
  </div>
  <div class="response" id="r-clear"><pre></pre></div>
</div>

<script>
const AUTH_HEADER = %q;

function hdrs() {
  const t = document.getElementById('auth-token').value.trim();
  return t ? { [AUTH_HEADER]: t } : {};
}

async function call(method, path, rid) {
  const wrap = document.getElementById(rid);
  const pre  = wrap.querySelector('pre');
  const badge = document.getElementById(rid + '-badge');
  pre.textContent = 'loading\u2026';
  wrap.style.display = 'block';
  try {
    const r = await fetch(path, { method, headers: hdrs() });
    const text = await r.text();
    let body = text;
    try { body = JSON.stringify(JSON.parse(text), null, 2); } catch (_) {}
    pre.textContent = body || '(empty)';
    badge.textContent = 'HTTP ' + r.status;
    badge.className = 'badge ' + (r.ok ? 'ok' : 'err');
  } catch (e) {
    pre.textContent = e.message;
    badge.textContent = 'network error';
    badge.className = 'badge err';
  }
}

async function sendWebhook() {
  const id    = document.getElementById('webhook-id').value.trim();
  const token = document.getElementById('webhook-token').value.trim();
  if (!id || !token) { alert('Enter both webhook ID and token'); return; }
  const payload = document.getElementById('webhook-payload').value.trim();
  const wrap  = document.getElementById('r-ingest');
  const pre   = wrap.querySelector('pre');
  const badge = document.getElementById('r-ingest-badge');
  pre.textContent = 'loading\u2026';
  wrap.style.display = 'block';
  try {
    const r = await fetch('/webhooks/' + encodeURIComponent(id) + '/' + encodeURIComponent(token), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: payload || '{}',
    });
    pre.textContent = r.status === 204 ? '(no content)' : await r.text();
    badge.textContent = 'HTTP ' + r.status;
    badge.className = 'badge ' + (r.ok ? 'ok' : 'err');
  } catch (e) {
    pre.textContent = e.message;
    badge.textContent = 'network error';
    badge.className = 'badge err';
  }
}

async function deleteOne() {
  const id = document.getElementById('delete-id').value.trim();
  if (!id) { alert('Enter a message ID'); return; }
  await call('DELETE', '/queue/' + encodeURIComponent(id), 'r-delete');
}

async function clearAll() {
  if (!confirm('Delete all queued messages (excluding any currently in_flight)?')) return;
  await call('DELETE', '/queue', 'r-clear');
}
</script>
</body>
</html>
`
