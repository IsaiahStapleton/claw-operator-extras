/*
Agent Console — read-only observability UI for OpenClaw agents.

Zero build step, zero dependencies: the server embeds these files and serves
them directly. Routing is hash-based so the whole app is one static document,
and every filter lives in the query string so any view can be shared as a URL.

Honesty rules carried over from the data layer: a failed scan renders a danger
banner and nothing else, and unparseable lines are surfaced in the masthead
rather than quietly dropped.
*/
'use strict';

/* ------------------------------------------------------------------ utils */

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

// Timestamps arrive as ISO strings; normalize to millis at the boundary so
// every comparison downstream is numeric.
function ms(ts) {
  if (ts == null || ts === '') return 0;
  if (typeof ts === 'number') return ts;
  const t = Date.parse(ts);
  return Number.isNaN(t) ? 0 : t;
}

function rel(ts, now) {
  const t = ms(ts);
  if (!t) return '—';
  const d = Math.max(0, (now || Date.now()) - t);
  if (d < 60e3) return Math.max(1, Math.floor(d / 1e3)) + 's ago';
  if (d < 36e5) return Math.floor(d / 6e4) + 'm ago';
  if (d < 864e5) return Math.floor(d / 36e5) + 'h ago';
  return Math.floor(d / 864e5) + 'd ago';
}

function exact(ts) {
  const t = ms(ts);
  return t ? new Date(t).toLocaleString('en-US', {
    month: 'short', day: 'numeric', year: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }) : '';
}

const hms = (ts) => ms(ts) ? new Date(ms(ts)).toLocaleTimeString('en-GB', { hour12: false }) : '';

function tok(n) {
  n = n || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
  return String(n);
}

const OUTCOMES = {
  ok: { label: 'OK', color: 'var(--ok)', bg: 'var(--ok-bg)', dot: 'var(--ok)' },
  error: { label: 'Error', color: 'var(--err)', bg: 'var(--err-bg)', dot: 'var(--err)' },
  running: { label: 'Running', color: 'var(--info)', bg: 'var(--info-bg)', dot: 'var(--info)', spin: true },
  stale: { label: 'Stale', color: 'var(--warn)', bg: 'var(--warn-bg)', dot: 'var(--gold)' },
};
const outMeta = (o) => OUTCOMES[o] || { label: o || '—', color: 'var(--sub)', bg: 'var(--surface2)', dot: 'var(--sub)' };

const STATUSES = {
  active: { label: 'Active', color: 'var(--ok)', bg: 'var(--ok-bg)', dot: 'var(--ok)', pulse: true },
  idle: { label: 'Idle', color: 'var(--sub)', bg: 'var(--surface2)', dot: 'var(--sub)' },
  stale: { label: 'Stale', color: 'var(--warn)', bg: 'var(--warn-bg)', dot: 'var(--gold)' },
};
const stMeta = (s) => STATUSES[s] || STATUSES.idle;

const EVENT_TYPES = {
  'session.started': ['var(--sub)', 'var(--surface2)'],
  'session.ended': ['var(--sub)', 'var(--surface2)'],
  'prompt.submitted': ['var(--purple)', 'var(--purple-bg)'],
  'tool.call': ['var(--info)', 'var(--info-bg)'],
  'tool.result': ['var(--teal)', 'var(--teal-bg)'],
  'model.completed': ['var(--ok)', 'var(--ok-bg)'],
  message: ['var(--purple)', 'var(--purple-bg)'],
};
const typeMeta = (t) => {
  const m = EVENT_TYPES[t] || ['var(--sub)', 'var(--surface2)'];
  return { label: t, color: m[0], bg: m[1] };
};

const DAY = 864e5;

/* ------------------------------------------------------------------ state */

const state = {
  hash: location.hash || '#/',
  theme: 'light',
  loaded: false,
  now: Date.now(),
  refreshedAt: 0,
  backendDown: false,
  integrityOpen: false,
  runLimit: 25,
  sortKey: 'time',
  sortDir: 'desc',
  expandedEv: {},
  expandedMem: {},
  follow: true,
  showResume: false,
  topoRange: '7d',
  selEdge: null,
  // data
  agents: [],
  runs: [],
  memory: [],
  observed: [],
  handoffs: [],
  meta: { ok: true, error: '', badLines: 0, scannedFiles: 0, unreadableFiles: 0 },
  gateway: { status: 'disabled' },
  hostname: '',
  dataDir: '/data/agents',
  // replay-scoped
  session: null,
  sessionKey: '',
  // multi-tenant scope
  local: false,
  user: '',
  claws: [],          // [{namespace, name, ready}]
  scopeLoaded: false,
  scopeError: '',
  namespace: '',
  claw: '',
  // memory wiki
  wiki: null,          // {pages, sources, edges, counts}
  wikiExpanded: {},    // page id -> sources revealed
  wikiPage: null,      // {page, content} being read
  wikiLoading: false,
};

// Every data call is scoped to one Claw; the server rejects a request without
// it, so the query is built in one place.
function scopeQuery(extra) {
  const p = new URLSearchParams(extra || {});
  if (!state.local) {
    p.set('namespace', state.namespace);
    p.set('claw', state.claw);
  }
  const q = p.toString();
  return q ? '?' + q : '';
}

/* ------------------------------------------------------------------ router */

function parseHash(h) {
  const raw = (h || state.hash || '#/').replace(/^#/, '');
  const [p, qs] = raw.split('?');
  const q = {};
  (qs || '').split('&').forEach((kv) => {
    if (!kv) return;
    const i = kv.indexOf('=');
    const k = i < 0 ? kv : kv.slice(0, i);
    q[k] = decodeURIComponent(i < 0 ? '' : kv.slice(i + 1));
  });
  return { seg: p.split('/').filter(Boolean), q };
}

function setQ(patch) {
  const { seg, q } = parseHash();
  const next = { ...q, ...patch };
  const qs = Object.keys(next)
    .filter((k) => next[k] !== '' && next[k] != null)
    .map((k) => k + '=' + encodeURIComponent(next[k]))
    .join('&');
  location.hash = '#/' + seg.join('/') + (qs ? '?' + qs : '');
}

async function loadWiki() {
  if (state.wiki || state.wikiLoading) return;
  state.wikiLoading = true;
  try {
    state.wiki = await getJSON('api/wiki' + scopeQuery());
  } catch (err) {
    state.wiki = { pages: [], sources: {}, edges: [], counts: {}, error: String(err.message || err) };
  }
  state.wikiLoading = false;
  render();
}

async function openWikiPage(path) {
  try {
    state.wikiPage = await getJSON('api/wiki/page' + scopeQuery({ path }));
  } catch (err) {
    state.wikiPage = { page: { path, title: path }, content: '', error: String(err.message || err) };
  }
  render();
}

function route() {
  const { seg, q } = parseHash();
  const isReplay = seg[0] === 'agents' && seg[2] === 'sessions' && !!seg[3];
  const isAgent = seg[0] === 'agents' && !isReplay && !!seg[1];
  return {
    seg, q, isReplay, isAgent,
    isTopology: seg[0] === 'topology',
    isMemory: seg[0] === 'memory',
    isWiki: seg[0] === 'wiki',
    isOverview: !isReplay && !isAgent && !['topology', 'memory', 'wiki'].includes(seg[0]),
  };
}

/* -------------------------------------------------------------- data layer */

async function getJSON(url) {
  const res = await fetch(url, { headers: { accept: 'application/json' } });
  if (!res.ok) throw new Error('HTTP ' + res.status);
  return res.json();
}

// loadScope discovers which Claws this user may read. It runs once; the
// picker then drives everything else.
async function loadScope() {
  try {
    const d = await getJSON('api/scope');
    state.local = !!d.local;
    state.user = d.user || '';
    state.claws = d.claws || [];
    state.scopeError = '';
    const { q } = route();
    const wanted = state.claws.find((c) => c.namespace === q.ns && c.name === q.claw);
    const pick = wanted || state.claws[0];
    if (pick) {
      state.namespace = pick.namespace;
      state.claw = pick.name;
    }
  } catch (err) {
    state.scopeError = String(err.message || err);
  }
  state.scopeLoaded = true;
}

async function refresh() {
  if (!state.scopeLoaded) await loadScope();
  if (!state.claws.length) {
    state.loaded = true;
    return; // nothing this user can read; the UI says so
  }
  try {
    const [agents, runs, memory, handoffs, health] = await Promise.all([
      getJSON('api/agents' + scopeQuery()),
      getJSON('api/runs' + scopeQuery({ limit: 500 })),
      getJSON('api/memory' + scopeQuery({ limit: 200 })),
      getJSON('api/handoffs' + scopeQuery()),
      getJSON('api/health' + scopeQuery()),
    ]);
    Object.assign(state, {
      agents: agents.agents || [],
      runs: runs.runs || [],
      memory: memory.writes || [],
      observed: memory.observed || [],
      handoffs: handoffs.handoffs || [],
      meta: health.data || state.meta,
      gateway: health.gateway || { status: 'disabled' },
      hostname: health.hostname || '',
      backendDown: false, loaded: true, refreshedAt: Date.now(),
    });
  } catch (err) {
    state.backendDown = true;
    state.loaded = true;
  }
}

// Replay data is fetched on demand (an explicit click), then tailed while the
// session is still running.
async function loadSession(agent, sessionId) {
  const key = agent + '/' + sessionId;
  try {
    const d = await getJSON(`api/runs/${encodeURIComponent(agent)}/${encodeURIComponent(sessionId)}` +
      scopeQuery({ limit: 1000 }));
    state.session = d;
    state.sessionKey = key;
  } catch {
    state.session = null;
    state.sessionKey = key;
  }
}

async function tailSession() {
  const s = state.session;
  if (!s || !isRunningSession(s)) return false;
  try {
    const d = await getJSON(
      `api/runs/${encodeURIComponent(s.agent)}/${encodeURIComponent(s.sessionId)}/tail` +
      scopeQuery({ after: s.events.length }));
    if (d.events && d.events.length) {
      s.events = s.events.concat(d.events);
      s.total = d.total;
      return true;
    }
  } catch { /* transient; the next tick retries */ }
  return false;
}

function isRunningSession(s) {
  if (!s) return false;
  if (s.run && s.run.outcome) return s.run.outcome === 'running';
  const r = state.runs.find((x) => x.sessionId === s.sessionId && x.agent === s.agent);
  return !!r && r.outcome === 'running';
}

/* ------------------------------------------------------------- derivations */

const agentByName = (name) => state.agents.find((a) => a.name === name);

function agentMeta(name) {
  const a = agentByName(name);
  return {
    emoji: (a && a.emoji) || '🤖',
    title: (a && a.title) || name || '—',
    desc: (a && a.desc) || '',
  };
}

function rangeCut(v) {
  const n = state.now;
  if (v === '24h') return n - DAY;
  if (v === '7d') return n - 7 * DAY;
  if (v === '30d') return n - 30 * DAY;
  return 0;
}

// Runs matching the current toolbar filters, sorted per the active column.
function filteredRuns(ctxAgent) {
  const { q } = route();
  const outcomes = (q.outcome || '').split(',').filter(Boolean);
  const cut = rangeCut(q.range || '7d');
  const search = (q.q || '').toLowerCase();
  const agent = ctxAgent || q.agent || '';

  let rows = state.runs.filter((r) =>
    (!agent || r.agent === agent) &&
    (!outcomes.length || outcomes.includes(r.outcome)) &&
    (!cut || ms(r.startedAt) >= cut) &&
    (!search || (r.prompt || '').toLowerCase().includes(search)));

  const key = state.sortKey === 'tokens'
    ? (x) => (x.tokens && x.tokens.total) || 0
    : (x) => ms(x.startedAt);
  rows = rows.slice().sort((a, b) => (state.sortDir === 'desc' ? key(b) - key(a) : key(a) - key(b)));
  return rows;
}

function dayBuckets(runs, days) {
  const start = new Date(state.now);
  start.setHours(0, 0, 0, 0);
  const d0 = start.getTime();
  const out = [];
  for (let i = days - 1; i >= 0; i--) {
    const t0 = d0 - i * DAY;
    const inDay = runs.filter((r) => ms(r.startedAt) >= t0 && ms(r.startedAt) < t0 + DAY);
    out.push({
      t0,
      runs: inDay.length,
      errs: inDay.filter((r) => r.outcome === 'error').length,
      tokens: inDay.reduce((t, r) => t + ((r.tokens && r.tokens.total) || 0), 0),
    });
  }
  return out;
}

const dayLabel = (t) => new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });

/* ------------------------------------------------------------- components */

function pill(label, m, spinning) {
  const inner = spinning
    ? `<span class="spinner"></span>`
    : `<span class="dot sm" style="background:${m.dot}"></span>`;
  return `<span class="pill" style="background:${m.bg};color:${m.color}">${inner}${esc(label)}</span>`;
}

function statusPill(status) {
  const m = stMeta(status);
  return `<span class="pill" style="background:${m.bg};color:${m.color}">
    <span class="dot sm${m.pulse ? ' pulse' : ''}" style="background:${m.dot}"></span>${m.label}</span>`;
}

function outcomePill(outcome) {
  const m = outMeta(outcome);
  return pill(m.label, m, !!m.spin);
}

function agentLink(name) {
  const m = agentMeta(name);
  return `${m.emoji} <a href="#/agents/${encodeURIComponent(name)}">${esc(m.title)}</a>`;
}

function sparkline(counts) {
  const max = Math.max(1, ...counts);
  const pts = counts.map((c, i) =>
    `${(i * (110 / Math.max(1, counts.length - 1))).toFixed(1)},${(24 - (c / max) * 20).toFixed(1)}`).join(' ');
  return `<svg width="110" height="26" viewBox="0 0 110 26" style="overflow:visible" aria-hidden="true">
    <polyline points="${pts}" fill="none" stroke="var(--chart)" stroke-width="1.5"></polyline></svg>`;
}

/* ------------------------------------------------------------------ views */

function renderMasthead() {
  const integ = (state.meta.badLines || 0) + (state.meta.unreadableFiles || 0) + (state.meta.truncatedEvents || 0);
  const showInteg = state.loaded && integ > 0 && state.meta.ok !== false;
  const gw = state.gateway.status || 'disabled';
  const gwDot = gw === 'up' ? '#3d7317' : gw === 'down' ? '#f0561d' : '#6a6e73';
  const gwLabel = gw === 'up' ? 'Gateway up' : gw === 'down' ? 'Gateway down' : 'Gateway disabled';
  const gwTip = gw === 'disabled' ? 'GATEWAY_URL unset — health checks disabled' : 'OpenClaw gateway health';

  const refreshed = state.backendDown
    ? `<span class="masthead-item hide-sm" title="The console cannot reach its backend"><span class="dot sm" style="background:#f0561d"></span>backend unreachable</span>`
    : `<span class="masthead-item dim hide-sm" title="Polling every 5 s"><span class="dot sm pulse" style="background:#3d7317"></span>refreshed ${rel(state.refreshedAt, state.now)}</span>`;

  return `<header class="masthead">
    <a class="brand" href="#/">
      <svg width="28" height="28" viewBox="0 0 28 28" aria-hidden="true">
        <rect width="28" height="28" rx="6" fill="var(--brand-red)"></rect>
        <path d="M8 20 Q10 9 14 7 Q12 13 12 20 Z" fill="#fff"></path>
        <path d="M13.5 20 Q15.5 10 19 8.5 Q17 14 16.5 20 Z" fill="#fff" opacity="0.85"></path>
        <path d="M18.5 20 Q20.5 13 23 12 Q21 16 20.5 20 Z" fill="#fff" opacity="0.7"></path>
      </svg>
      <span class="brand-text">
        <span class="brand-title">Agent Console</span>
        <span class="brand-sub">OpenClaw fleet</span>
      </span>
    </a>
    ${renderScopePicker()}
    <div class="spacer"></div>
    ${showInteg ? `<button class="integrity-btn" data-act="integrity" title="Data integrity">
      <svg width="13" height="13" viewBox="0 0 16 16" aria-hidden="true">
        <path d="M8 1 L15 14 H1 Z" fill="#f0c000"></path>
        <rect x="7.3" y="6" width="1.4" height="4" fill="#151515"></rect>
        <rect x="7.3" y="11" width="1.4" height="1.4" fill="#151515"></rect>
      </svg>${integ}</button>` : ''}
    <span class="masthead-item hide-sm" title="${esc(gwTip)}"><span class="dot sm" style="background:${gwDot}"></span>${gwLabel}</span>
    ${refreshed}
    <button class="icon-btn" data-act="theme" title="Toggle light/dark">${state.theme === 'light' ? '☾' : '☀'}</button>
    <span class="masthead-host">${esc(state.hostname)}</span>
    ${state.integrityOpen ? `<div class="popover">
      <h3 class="display">Data integrity</h3>
      <div class="popover-grid">
        <span style="color:var(--sub)">Files scanned</span><span class="mono">${state.meta.scannedFiles || 0}</span>
        <span style="color:var(--sub)">Unparseable lines</span><span class="mono" style="color:var(--warn)">${state.meta.badLines || 0}</span>
        <span style="color:var(--sub)">Unreadable files</span><span class="mono" style="color:var(--warn)">${state.meta.unreadableFiles || 0}</span>
        <span style="color:var(--sub)">Truncated events</span><span class="mono" style="color:var(--warn)">${state.meta.truncatedEvents || 0}</span>
      </div>
      <div class="popover-note">Counted, never hidden. Unparseable lines are excluded from every figure here.
        Truncated events are ones OpenClaw wrote with their payload dropped for exceeding its size limit —
        their prompts and token counts are missing at the source, not lost by this console.</div>
    </div>` : ''}
  </header>`;
}

// The scope picker is the multi-tenant entry point: one console, and this
// chooses which Claw it is reporting on. The selection is mirrored into the
// URL so a link keeps pointing at the same Claw.
function renderScopePicker() {
  if (state.local || state.claws.length === 0) return '';
  const opts = state.claws.map((c) => {
    const v = c.namespace + '/' + c.name;
    const sel = c.namespace === state.namespace && c.name === state.claw;
    return `<option value="${esc(v)}"${sel ? ' selected' : ''}>${esc(c.namespace)} / ${esc(c.name)}${c.ready ? '' : ' (not ready)'}</option>`;
  }).join('');
  return `<label class="scope" title="Claws you have access to">
    <span class="scope-label">Claw</span>
    <select class="scope-select" data-act="scope">${opts}</select>
  </label>`;
}

function renderSidebar(r) {
  const nav = [
    ['Overview', '#/', r.isOverview],
    ['Topology', '#/topology', r.isTopology],
    ['Memory', '#/memory', r.isMemory],
    ['Wiki', '#/wiki', r.isWiki],
  ].map(([label, href, active]) =>
    `<a class="nav-item${active ? ' active' : ''}" href="${href}">${label}</a>`).join('');

  const agents = state.agents.map((a) => {
    const active = (r.isAgent || r.isReplay) && r.seg[1] === a.name;
    const m = stMeta(a.status);
    return `<a class="nav-item nav-agent${active ? ' active' : ''}" href="#/agents/${encodeURIComponent(a.name)}">
      <span>${a.emoji || '🤖'}</span>
      <span class="nav-agent-name">${esc(a.title || a.name)}</span>
      <span class="dot sm${m.pulse ? ' pulse' : ''}" style="background:${m.dot}"></span>
    </a>`;
  }).join('');

  return `<nav class="sidebar">
    ${nav}
    <div class="nav-section">AGENTS</div>
    ${agents}
    <div class="nav-foot">Read-only monitoring surface.<br>Data: <span class="mono">${esc(state.dataDir)}</span></div>
  </nav>`;
}

function renderToolbar(lockedAgent) {
  const { q } = route();
  const outcomes = (q.outcome || '').split(',').filter(Boolean);
  const range = q.range || '7d';

  const agentSelect = lockedAgent
    ? `<span class="locked-filter">Agent: <b>${esc(agentMeta(lockedAgent).title)}</b> 🔒</span>`
    : `<select class="field" data-act="filter-agent">
        <option value=""${!q.agent ? ' selected' : ''}>All agents</option>
        ${state.agents.map((a) => `<option value="${esc(a.name)}"${q.agent === a.name ? ' selected' : ''}>${a.emoji || '🤖'} ${esc(a.title || a.name)}</option>`).join('')}
       </select>`;

  const chips = ['ok', 'error', 'running', 'stale'].map((o) => {
    const on = outcomes.includes(o);
    const m = outMeta(o);
    const style = on ? `background:${m.bg};color:${m.color};border-color:${m.color}` : '';
    return `<button class="filter-chip${on ? ' on' : ''}" style="${style}" data-act="filter-outcome" data-outcome="${o}">${m.label}</button>`;
  }).join('');

  const ranges = [['24h', 'Last 24 h'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['all', 'All time']]
    .map(([v, label]) => `<option value="${v}"${range === v ? ' selected' : ''}>${label}</option>`).join('');

  const active = !!(q.agent || outcomes.length || q.q || (q.range && q.range !== '7d'));
  const rows = filteredRuns(lockedAgent);
  const scope = state.runs.filter((x) => !lockedAgent || x.agent === lockedAgent).length;

  return `<div class="toolbar">
    ${agentSelect}
    <div style="display:flex;gap:6px">${chips}</div>
    <input class="field text" data-act="filter-q" placeholder="Search prompts…" value="${esc(q.q || '')}">
    <select class="field" data-act="filter-range">${ranges}</select>
    ${active ? `<button class="link-btn" data-act="filter-clear">Clear filters</button>` : ''}
    <span class="spacer"></span>
    <span class="result-count">${rows.length} of ${scope} runs</span>
  </div>`;
}

function renderRunsTable(lockedAgent) {
  const rows = filteredRuns(lockedAgent);
  const visible = rows.slice(0, state.runLimit);
  const arrow = (k) => (state.sortKey === k ? (state.sortDir === 'desc' ? ' ↓' : ' ↑') : '');

  const body = visible.map((r) => {
    const spawn = r.spawnedBy ? agentMeta(r.spawnedBy.fromAgent).title : '';
    return `<tr data-act="open-run" data-agent="${esc(r.agent)}" data-session="${esc(r.sessionId)}">
      <td class="when" title="${esc(exact(r.startedAt))}">${rel(r.startedAt, state.now)}</td>
      ${lockedAgent ? '' : `<td class="nowrap">${agentLink(r.agent)}</td>`}
      <td class="prompt" title="${esc(r.prompt)}${r.promptSource === 'transcript'
        ? '\n\n(recovered from the session transcript — the trajectory event was truncated)' : ''}"><div class="prompt-wrap">
        ${r.source === 'transcript'
          ? `<span class="chip tag" style="background:var(--surface2);color:var(--sub);border:1px solid var(--border-soft)">transcript</span>` : ''}
        ${r.promptSource === 'transcript'
          ? `<span class="chip tag" style="background:var(--warn-bg);color:var(--warn)"
              title="The trajectory event was truncated; this prompt was recovered from the session transcript.">recovered</span>` : ''}
        <span class="prompt-text">${esc(r.prompt)}</span>
      </div></td>
      <td>${outcomePill(r.outcome)}</td>
      <td class="num">${r.steps || 0}</td>
      <td class="num">${tok(r.tokens && r.tokens.total)}</td>
      <td class="nowrap">
        ${r.spawnedBy ? `<span class="chip" style="background:var(--purple-bg);color:var(--purple)" title="spawned by">← ${esc(spawn)}</span>` : ''}
        ${r.handoffsOut ? `<span class="chip" style="background:var(--teal-bg);color:var(--teal)" title="hands off to">→ ${r.handoffsOut}</span>` : ''}
      </td>
    </tr>`;
  }).join('');

  return `${renderToolbar(lockedAgent)}
    <div class="table-scroll"><table>
      <thead><tr>
        <th><button class="sort-btn" data-act="sort" data-key="time">Started${arrow('time')}</button></th>
        ${lockedAgent ? '' : '<th>Agent</th>'}
        <th>Prompt</th><th>Outcome</th><th class="num">Steps</th>
        <th class="num"><button class="sort-btn" data-act="sort" data-key="tokens">Tokens${arrow('tokens')}</button></th>
        <th>Handoffs</th>
      </tr></thead>
      <tbody>${body}</tbody>
    </table></div>
    ${rows.length === 0 ? `<div class="empty">
      <div class="empty-title">No runs match the current filters</div>
      <div class="empty-body">Try widening the time range or clearing filters.</div></div>` : ''}
    ${rows.length > state.runLimit ? `<div class="more">
      <button data-act="more">Show more (${rows.length - state.runLimit} remaining)</button></div>` : ''}`;
}

function memoryRow(w, opts = {}) {
  const m = agentMeta(w.agent);
  const isWrite = (w.tool || '') === 'write';
  const toolStyle = isWrite
    ? 'background:var(--ok-bg);color:var(--ok)'
    : 'background:var(--info-bg);color:var(--info)';
  return `<div class="row">
    <span class="when" title="${esc(exact(w.ts))}">${rel(w.ts, state.now)}</span>
    ${opts.hideAgent ? '' : `<span class="nowrap">${agentLink(w.agent)}</span>`}
    <span class="chip" style="${toolStyle}">${esc(w.tool || 'tool')}</span>
    <a class="path" href="#/memory?path=${encodeURIComponent(w.notePath)}">${esc(w.notePath)}</a>
    <span class="grow"></span>
    ${w.sessionId ? `<a href="#/agents/${encodeURIComponent(w.agent)}/sessions/${encodeURIComponent(w.sessionId)}" style="white-space:nowrap;font-size:12px">session ↗</a>` : ''}
  </div>`;
}

function viewOverview() {
  const { q } = route();
  const start = new Date(state.now);
  start.setHours(0, 0, 0, 0);
  const d0 = start.getTime();
  const today = state.runs.filter((r) => ms(r.startedAt) >= d0);
  const errToday = today.filter((r) => r.outcome === 'error').length;
  const counts = ['active', 'idle', 'stale'].map((s) => state.agents.filter((a) => a.status === s).length);

  const stats = [
    { label: 'Agents', value: state.agents.length, sub: `${counts[0]} active · ${counts[1]} idle · ${counts[2]} stale`, color: 'var(--text)' },
    { label: 'Runs today', value: today.length, sub: `${state.runs.length} in the last 14 days`, color: 'var(--text)' },
    { label: 'Errors today', value: errToday, sub: errToday ? 'needs review' : 'all clear', color: errToday ? 'var(--err)' : 'var(--ok)' },
    { label: 'Tokens today', value: tok(today.reduce((t, r) => t + ((r.tokens && r.tokens.total) || 0), 0)), sub: 'input + output', color: 'var(--text)' },
  ].map((s) => `<div class="card pad">
      <div class="stat-label">${s.label}</div>
      <div class="stat-row"><span class="stat-value" style="color:${s.color}">${esc(String(s.value))}</span>
        <span class="stat-sub">${esc(s.sub)}</span></div>
    </div>`).join('');

  const cards = state.agents.map((a) => {
    const mine = state.runs.filter((r) => r.agent === a.name);
    const spark = sparkline(dayBuckets(mine, 14).map((d) => d.runs));
    return `<a class="agent-card" href="#/agents/${encodeURIComponent(a.name)}">
      <div class="agent-card-head">
        <span class="agent-emoji">${a.emoji || '🤖'}</span>
        <span class="agent-ident">
          <span class="agent-name">${esc(a.title || a.name)}</span>
          <span class="agent-desc">${esc(a.desc || a.name)}</span>
        </span>
        ${statusPill(a.status)}
      </div>
      ${a.currentStep ? `<div class="agent-step">▸ ${esc(a.currentStep)}</div>` : ''}
      <div class="agent-foot">
        <span title="${esc(exact(a.lastRunAt))}">seen <b>${rel(a.lastRunAt, state.now)}</b></span>
        <span>${a.runCount || 0} runs</span>
        <span class="grow"></span>
        ${spark}
      </div>
    </a>`;
  }).join('');

  const tab = q.tab === 'memory' ? 'memory' : 'runs';
  const tabs = [['runs', 'Run timeline'], ['memory', 'Memory commits']].map(([id, label]) =>
    `<button class="tab${tab === id ? ' active' : ''}" data-act="tab" data-tab="${id === 'runs' ? '' : id}">${label}</button>`).join('');

  const panel = tab === 'runs'
    ? renderRunsTable(null)
    : (state.memory.length
      ? state.memory.slice().sort((a, b) => ms(b.ts) - ms(a.ts)).map((w) => memoryRow(w)).join('')
      : `<div class="empty"><div class="empty-title">No vault writes recorded</div>
          <div class="empty-body">Writes are detected from tool.call events touching <span class="mono">memory-map/**/*.md</span>.</div></div>`);

  return `<div class="page">
    <div class="page-head">
      <h1>Overview</h1>
      <span style="color:var(--sub);font-size:13px">${new Date(state.now).toLocaleDateString('en-US', { weekday: 'long', month: 'short', day: 'numeric', year: 'numeric' })}</span>
    </div>
    <div class="stat-grid">${stats}</div>
    <div class="agent-grid">${cards}</div>
    <div class="card clip">
      <div class="tabs">${tabs}</div>
      ${panel}
    </div>
  </div>`;
}

function viewAgent(name) {
  const a = agentByName(name);
  if (!a) {
    return `<div class="page"><div class="crumbs"><a href="#/">Overview</a> / agents / ${esc(name)}</div>
      <div class="empty-state"><div class="icon">❔</div><h2 class="display">Unknown agent</h2>
      <p>No agent named <span class="mono">${esc(name)}</span> is present in the current data directory.</p></div></div>`;
  }
  const { q } = route();
  const mine = state.runs.filter((r) => r.agent === name);
  const errs = mine.filter((r) => r.outcome === 'error').length;
  const days = dayBuckets(mine, 14);
  const maxRuns = Math.max(1, ...days.map((d) => d.runs));
  const maxTok = Math.max(1, ...days.map((d) => d.tokens));

  const runBars = days.map((d) => `<div class="bar-col" title="${esc(dayLabel(d.t0))}: ${d.runs} runs${d.errs ? ', ' + d.errs + ' errors' : ''}">
      <div class="bar errs" style="height:${Math.round((d.errs / maxRuns) * 100)}%"></div>
      <div class="bar runs" style="height:${Math.round(((d.runs - d.errs) / maxRuns) * 100)}%;border-radius:${d.errs ? '0' : '2px 2px 0 0'}"></div>
    </div>`).join('');

  const tokBars = days.map((d) => `<div class="bar-col" title="${esc(dayLabel(d.t0))}: ${tok(d.tokens)} tokens">
      <div class="bar tokens" style="height:${Math.round((d.tokens / maxTok) * 100)}%"></div>
    </div>`).join('');

  // Outcome donut: one arc per outcome, offset by the arcs before it.
  const C = 2 * Math.PI * 54;
  let acc = 0;
  const segs = [];
  const legend = [];
  [['ok', 'var(--ok)'], ['error', 'var(--err)'], ['running', 'var(--info)'], ['stale', 'var(--gold)']].forEach(([o, color]) => {
    const n = mine.filter((r) => r.outcome === o).length;
    if (n > 0 && mine.length) {
      const len = (n / mine.length) * C;
      segs.push(`<circle cx="70" cy="70" r="54" fill="none" stroke="${color}" stroke-width="16"
        stroke-dasharray="${len.toFixed(1)} ${(C - len).toFixed(1)}" stroke-dashoffset="${(-acc).toFixed(1)}"
        transform="rotate(-90 70 70)"></circle>`);
      acc += len;
    }
    legend.push(`<span><span class="swatch" style="background:${color}"></span>${outMeta(o).label} <b>${n}</b></span>`);
  });

  const tab = ['handoffs', 'memory'].includes(q.tab) ? q.tab : 'runs';
  const tabs = [['runs', 'Runs'], ['handoffs', 'Handoffs'], ['memory', 'Memory']].map(([id, label]) =>
    `<button class="tab${tab === id ? ' active' : ''}" data-act="tab" data-tab="${id === 'runs' ? '' : id}">${label}</button>`).join('');

  let panel;
  if (tab === 'runs') {
    panel = renderRunsTable(name);
  } else if (tab === 'handoffs') {
    const hs = state.handoffs
      .filter((h) => h.fromAgent === name || h.toAgent === name)
      .sort((x, y) => ms(y.ts) - ms(x.ts));
    panel = hs.length ? hs.map((h) => {
      const out = h.fromAgent === name;
      const peer = out ? h.toAgent : h.fromAgent;
      const pm = agentMeta(peer);
      return `<div class="row">
        <span class="dir" style="background:${out ? 'var(--teal-bg)' : 'var(--purple-bg)'};color:${out ? 'var(--teal)' : 'var(--purple)'}">${out ? '→' : '←'}</span>
        <span class="dir-label">${out ? 'handed off to' : 'spawned by'}</span>
        <span>${pm.emoji} <a href="#/agents/${encodeURIComponent(peer)}">${esc(pm.title)}</a></span>
        <a class="path" href="#/agents/${encodeURIComponent(h.toAgent)}/sessions/${encodeURIComponent(h.toSessionId)}">${esc(h.toSessionId)}</a>
        <span class="grow"></span>
        <span class="when" title="${esc(exact(h.ts))}">${rel(h.ts, state.now)}</span>
      </div>`;
    }).join('') : `<div class="empty">No handoffs recorded for this agent.</div>`;
  } else {
    const mw = state.memory.filter((w) => w.agent === name).sort((x, y) => ms(y.ts) - ms(x.ts));
    panel = mw.length
      ? mw.map((w) => memoryRow(w, { hideAgent: true })).join('')
      : `<div class="empty">No memory-vault writes from this agent.</div>`;
  }

  const model = a.model || (mine[0] && mine[0].model) || '';

  return `<div class="page">
    <div class="crumbs"><a href="#/">Overview</a> / agents / ${esc(a.name)}</div>
    <div class="agent-head">
      <span class="big-emoji">${a.emoji || '🤖'}</span>
      <div class="agent-head-main">
        <div class="agent-head-title"><h1>${esc(a.title || a.name)}</h1>${statusPill(a.status)}</div>
        <div class="agent-head-meta">${esc(a.desc || a.name)} ·
          <span title="${esc(exact(a.lastRunAt))}">last seen ${rel(a.lastRunAt, state.now)}</span>
          ${model ? ` · <span class="mono" style="font-size:12px">${esc(model)}</span>` : ''}</div>
      </div>
      <div class="head-stats">
        <div class="head-stat"><div class="head-stat-value">${mine.length}</div><div class="head-stat-label">runs · 14d</div></div>
        <div class="head-stat"><div class="head-stat-value" style="color:${errs ? 'var(--err)' : 'var(--ok)'}">${errs}</div><div class="head-stat-label">errors</div></div>
        <div class="head-stat"><div class="head-stat-value">${tok(mine.reduce((t, r) => t + ((r.tokens && r.tokens.total) || 0), 0))}</div><div class="head-stat-label">tokens</div></div>
      </div>
    </div>
    <div class="chart-grid">
      <div class="card pad">
        <div class="chart-title">Runs per day <span>· 14d</span></div>
        <div class="bars">${runBars}</div>
        <div class="axis"><span>${esc(dayLabel(days[0].t0))}</span><span>today</span></div>
      </div>
      <div class="card pad">
        <div class="chart-title">Tokens per day <span>· 14d</span></div>
        <div class="bars">${tokBars}</div>
        <div class="axis"><span>${esc(dayLabel(days[0].t0))}</span><span>today</span></div>
      </div>
      <div class="card pad donut-card">
        <div class="donut-wrap">
          <svg width="120" height="120" viewBox="0 0 140 140" aria-hidden="true">
            <circle cx="70" cy="70" r="54" fill="none" stroke="var(--border-soft)" stroke-width="16"></circle>
            ${segs.join('')}
          </svg>
          <div class="donut-center"><span class="donut-total">${mine.length}</span><span class="donut-unit">runs</span></div>
        </div>
        <div class="legend">${legend.join('')}</div>
      </div>
    </div>
    <div class="card clip">
      <div class="tabs">${tabs}</div>
      ${panel}
    </div>
  </div>`;
}

function viewReplay(agent, sessionId) {
  const m = agentMeta(agent);
  const s = state.session;
  const crumbs = `<div class="crumbs"><a href="#/">Overview</a> /
    <a href="#/agents/${encodeURIComponent(agent)}">${esc(m.title)}</a> / sessions</div>`;

  if (state.sessionKey !== agent + '/' + sessionId) {
    return `<div><div class="replay-head">${crumbs}</div><div class="loading">Loading session…</div></div>`;
  }
  if (!s) {
    return `<div><div class="replay-head">${crumbs}</div><div class="empty" style="padding:60px">Session not found.</div></div>`;
  }

  const run = s.run || state.runs.find((r) => r.sessionId === sessionId && r.agent === agent);
  const outcome = run ? run.outcome : 'ok';
  const om = outMeta(outcome);
  const running = isRunningSession(s);
  const isChat = s.source === 'transcript';
  const srcStyle = s.source === 'trajectory'
    ? 'background:var(--info-bg);color:var(--info)'
    : 'background:var(--purple-bg);color:var(--purple)';

  const children = (s.children || []).map((c) =>
    `<a class="chip" style="background:var(--teal-bg);color:var(--teal)"
      href="#/agents/${encodeURIComponent(c.toAgent)}/sessions/${encodeURIComponent(c.toSessionId)}">→ ${esc(agentMeta(c.toAgent).title)} ${esc(String(c.toSessionId).slice(0, 6))}…</a>`).join('');

  const head = `<div class="replay-head">
    ${crumbs}
    <div class="replay-title">
      <span style="font-size:20px">${m.emoji}</span>
      <span class="name">${esc(m.title)}</span>
      <span class="sid">${esc(sessionId)}</span>
      <span class="chip" style="${srcStyle}">${esc(s.source || 'trajectory')}</span>
      ${pill(om.label, om, running)}
      ${s.badLines ? `<span class="chip" style="background:var(--warn-bg);color:var(--warn)"
        title="unparseable lines in this session file, excluded from replay">⚠ ${s.badLines} bad lines</span>` : ''}
      ${s.parent ? `<a class="chip" style="background:var(--purple-bg);color:var(--purple)"
        href="#/agents/${encodeURIComponent(s.parent.fromAgent)}/sessions/${encodeURIComponent(s.parent.fromSessionId)}">← spawned by ${esc(agentMeta(s.parent.fromAgent).title)}</a>` : ''}
      ${children}
      <span class="spacer"></span>
      <span class="result-count">${(s.events || []).length} ${isChat ? 'messages' : 'events'}${running ? ' · live' : ''}</span>
      ${running ? `<button class="follow-btn" data-act="follow">
        <span class="toggle${state.follow ? ' on' : ''}"><span class="knob"></span></span>auto-follow</button>` : ''}
    </div>
  </div>`;

  if (isChat) {
    const msgs = (s.events || []).map((e) => {
      const user = e.role === 'user';
      return `<div class="msg ${user ? 'user' : 'assistant'}">
        <span class="msg-meta">${user ? 'You' : esc(m.title) + ' ' + m.emoji} · <span title="${esc(exact(e.ts))}">${hms(e.ts)}</span></span>
        <div class="msg-body">${esc(e.content || e.summary || '')}</div>
      </div>`;
    }).join('');
    return `<div>${head}<div class="chat">${msgs}</div></div>`;
  }

  const events = (s.events || []).map((e, i) => {
    const tm = typeMeta(e.type);
    const key = String(e.seq != null ? e.seq : i);
    const open = !!state.expandedEv[key];
    const summary = e.summary || '';
    const isErr = /^(ERROR|FATAL)/.test(summary);
    return `<div>
      <div class="event-line" data-act="toggle-ev" data-key="${esc(key)}">
        <span class="seq">${esc(String(e.seq != null ? e.seq : i + 1))}</span>
        <span class="time" title="${esc(exact(e.ts))}">${hms(e.ts)}</span>
        <span class="type" style="background:${tm.bg};color:${tm.color}">${esc(tm.label)}</span>
        <span class="summary${isErr ? ' err' : ''}">${esc(summary)}</span>
        <span class="caret">${open ? '▾' : '▸'}</span>
      </div>
      ${open ? `<pre>${esc(JSON.stringify(e.data || {}, null, 2))}</pre>` : ''}
    </div>`;
  }).join('');

  return `<div>${head}<div class="events">${events}
    ${running && state.showResume ? `<div class="resume-wrap">
      <button class="resume-btn" data-act="resume">↓ Resume following</button></div>` : ''}
  </div></div>`;
}

// Layered layout. Agents that only delegate go left, agents that are only
// delegated to go right, agents doing both sit in the middle. Agents with no
// handoffs at all are parked on their own row so an edge never appears to
// route through an uninvolved agent.
function topoLayout(names, edges) {
  const out = new Set(edges.map((e) => e.fromAgent));
  const inn = new Set(edges.map((e) => e.toAgent));
  const connected = names.filter((n) => out.has(n) || inn.has(n));
  const isolated = names.filter((n) => !out.has(n) && !inn.has(n));

  const cols = [[], [], []];
  connected.forEach((n) => {
    if (out.has(n) && !inn.has(n)) cols[0].push(n);
    else if (inn.has(n) && !out.has(n)) cols[2].push(n);
    else cols[1].push(n);
  });
  // With nothing in the middle, pull the outer columns inward so the graph
  // reads as one connected shape rather than two distant clusters.
  const xs = cols[1].length ? [130, 340, 550] : [200, 340, 480];

  const graphHeight = isolated.length ? 250 : 380;
  const pos = {};
  cols.forEach((col, ci) => {
    const span = graphHeight / (col.length + 1);
    col.forEach((n, i) => { pos[n] = [xs[ci], Math.round(span * (i + 1))]; });
  });
  isolated.forEach((n, i) => {
    const span = 680 / (isolated.length + 1);
    pos[n] = [Math.round(span * (i + 1)), 320];
  });
  return { pos, isolated: new Set(isolated) };
}

function viewTopology() {
  const cut = rangeCut(state.topoRange);
  const edges = state.handoffs.filter((h) => !cut || ms(h.ts) >= cut);
  const names = state.agents.map((a) => a.name);
  const { pos, isolated } = topoLayout(names, edges);

  const ranges = ['24h', '7d', '30d', 'all'].map((r) =>
    `<button class="${state.topoRange === r ? 'active' : ''}" data-act="topo-range" data-range="${r}">${r}</button>`).join('');

  const groups = {};
  edges.forEach((h) => {
    const k = h.fromAgent + '→' + h.toAgent;
    (groups[k] = groups[k] || []).push(h);
  });

  const edgeSvg = [];
  const edgeBadges = [];
  Object.keys(groups).forEach((k) => {
    const [f, t] = k.split('→');
    if (!pos[f] || !pos[t]) return;
    const [x1, y1] = pos[f];
    const [x2, y2] = pos[t];
    const cx = (x1 + x2) / 2;
    const cy = (y1 + y2) / 2 - 30;
    const mx = (x1 + 2 * cx + x2) / 4;
    const my = (y1 + 2 * cy + y2) / 4;
    const n = groups[k].length;
    const sel = state.selEdge === k;
    const d = `M${x1} ${y1} Q${cx} ${cy} ${x2} ${y2}`;
    edgeSvg.push(`<g data-act="topo-edge" data-edge="${esc(k)}" style="cursor:pointer">
      <path d="${d}" fill="none" stroke="transparent" stroke-width="18"></path>
      <path d="${d}" fill="none" stroke="${sel ? 'var(--info)' : 'var(--border)'}"
        stroke-width="${Math.min(6, 1.5 + n * 0.7).toFixed(1)}" marker-end="url(#acArrow)"></path>
    </g>`);
    edgeBadges.push(`<button class="topo-edge-badge${sel ? ' on' : ''}" data-act="topo-edge" data-edge="${esc(k)}"
      style="left:${((mx / 680) * 100).toFixed(2)}%;top:${(((my - 14) / 380) * 100).toFixed(2)}%;border:1px solid ${sel ? 'var(--info)' : 'var(--border)'}">${n}</button>`);
  });

  const nodeSvg = [];
  const nodeLabels = [];
  state.agents.forEach((a) => {
    const p = pos[a.name];
    if (!p) return;
    const [x, y] = p;
    const st = stMeta(a.status);
    const runs = state.runs.filter((r) => r.agent === a.name && (!cut || ms(r.startedAt) >= cut)).length;
    const off = isolated.has(a.name);
    const r = off ? 26 : 36;
    nodeSvg.push(`<circle cx="${x}" cy="${y}" r="${r}" fill="var(--surface2)" stroke="${st.dot}"
      stroke-width="${off ? 2 : 3}"${off ? ' stroke-dasharray="4 3"' : ''}></circle>`);
    nodeLabels.push(`<a class="topo-node-emoji" href="#/agents/${encodeURIComponent(a.name)}"
        style="left:${((x / 680) * 100).toFixed(2)}%;top:${((y / 380) * 100).toFixed(2)}%${off ? ';font-size:19px' : ''}">${a.emoji || '🤖'}</a>
      <a class="topo-node-label" href="#/agents/${encodeURIComponent(a.name)}"
        style="left:${((x / 680) * 100).toFixed(2)}%;top:${(((y + r + 10) / 380) * 100).toFixed(2)}%">
        <span class="n">${esc(a.title || a.name)}</span>
        <span class="s">${runs} runs · ${off ? 'no handoffs' : st.label.toLowerCase()}</span></a>`);
  });

  const sel = state.selEdge && groups[state.selEdge];
  const selList = (sel || []).slice().sort((a, b) => ms(b.ts) - ms(a.ts)).map((h) => `<div class="row">
      <span class="when" title="${esc(exact(h.ts))}">${rel(h.ts, state.now)}</span>
      <span>${agentMeta(h.fromAgent).emoji} <a class="path" href="#/agents/${encodeURIComponent(h.fromAgent)}/sessions/${encodeURIComponent(h.fromSessionId)}">${esc(h.fromSessionId)}</a></span>
      <span style="color:var(--sub)">→</span>
      <span>${agentMeta(h.toAgent).emoji} <a class="path" href="#/agents/${encodeURIComponent(h.toAgent)}/sessions/${encodeURIComponent(h.toSessionId)}">${esc(h.toSessionId)}</a></span>
    </div>`).join('');

  return `<div class="page narrow">
    <div class="page-head">
      <h1>Handoff topology</h1><span class="spacer"></span>
      <div class="topo-ranges">${ranges}</div>
    </div>
    ${edges.length === 0 ? `<div class="empty-state"><div class="icon">🕸️</div>
      <h2 class="display">No handoffs in this range</h2>
      <p>Handoffs are inferred when one agent makes a spawn-like tool call and another agent starts a session within two minutes.</p></div>`
    : `<div class="card" style="padding:8px">
      <div class="topo-canvas">
        <svg viewBox="0 0 680 380">
          <defs><marker id="acArrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 Z" fill="var(--sub)"></path></marker></defs>
          ${edgeSvg.join('')}
          ${nodeSvg.join('')}
        </svg>
        ${edgeBadges.join('')}
        ${nodeLabels.join('')}
      </div>
    </div>
    <div class="card clip">
      <div class="section-head">${sel ? esc(state.selEdge.replace('→', ' → ')) + ` · ${sel.length} handoffs (${state.topoRange})` : 'Handoff events'}</div>
      ${sel ? selList : `<div class="section-hint">Click an edge to list the underlying handoff events. Click a node to open that agent.</div>`}
    </div>`}
  </div>`;
}

function viewMemory() {
  const { q } = route();
  const tab = q.tab === 'notes' ? 'notes' : 'writes';
  if (tab === 'writes') return viewMemoryWrites();
  return viewMemoryNotes();
}

function memoryTabs(active) {
  return [['writes', 'Observed writes'], ['notes', 'All notes']].map(([id, label]) =>
    `<button class="tab${active === id ? ' active' : ''}" data-act="tab" data-tab="${id === 'writes' ? '' : id}">${label}</button>`).join('');
}

// What actually changed, and when. This is the console's own observation:
// OpenClaw records nothing about memory writes, so anything before this
// console started watching is simply not knowable.
function viewMemoryWrites() {
  const rows = (state.observed || []).map((e) => {
    const m = e.agent ? agentMeta(e.agent) : null;
    const lines = (e.addedLines || []).filter((l) => l.trim());
    return `<div class="card clip" style="margin-bottom:10px">
      <div class="mem-group-head" style="cursor:default">
        <span class="chip" style="background:var(--ok-bg);color:var(--ok)">+${e.added}</span>
        ${e.removed ? `<span class="chip" style="background:var(--err-bg);color:var(--err)">−${e.removed}</span>` : ''}
        <span class="mem-path">${esc(e.notePath)}</span>
        <span class="grow"></span>
        ${m ? `<span>${m.emoji} ${esc(m.title)}</span>` : '<span style="color:var(--sub)">shared</span>'}
        <span class="when" title="${esc(exact(e.ts))}">${rel(e.ts, state.now)}</span>
      </div>
      ${lines.length ? `<pre class="mono" style="margin:0;padding:10px 16px 14px 42px;white-space:pre-wrap;font-size:12px;line-height:1.55;color:var(--ok)">${lines.map((l) => esc(l)).join('\n')}</pre>` : ''}
    </div>`;
  }).join('');

  return `<div class="page narrow">
    <div class="page-head">
      <h1>Memory</h1>
      <span style="color:var(--sub);font-size:13px">what the agents wrote, and when</span>
    </div>
    <div class="card clip"><div class="tabs">${memoryTabs('writes')}</div></div>
    ${rows || `<div class="empty-state"><div class="icon">👁️</div>
      <h2 class="display">No writes observed yet</h2>
      <p>OpenClaw records nothing about memory writes, so this console detects them by
      watching the notes change. It has not seen a change yet — writes that happened before
      it started watching cannot be recovered. Every note already on disk is under
      <b>All notes</b>.</p></div>`}
  </div>`;
}

function viewMemoryNotes() {
  const { q } = route();
  const fa = q.agent || '';
  const fp = q.path || '';
  const ws = state.memory.filter((w) => (!fa || w.agent === fa) && (!fp || String(w.notePath).startsWith(fp)));

  const groups = {};
  ws.forEach((w) => { (groups[w.notePath] = groups[w.notePath] || []).push(w); });
  const paths = Object.keys(groups).sort((a, b) =>
    Math.max(...groups[b].map((w) => ms(w.ts))) - Math.max(...groups[a].map((w) => ms(w.ts))));

  const agentOptions = [`<option value=""${!fa ? ' selected' : ''}>All agents</option>`].concat(
    state.agents.map((a) => `<option value="${esc(a.name)}"${fa === a.name ? ' selected' : ''}>${a.emoji || '🤖'} ${esc(a.title || a.name)}</option>`)).join('');

  const body = paths.map((p, i) => {
    const list = groups[p].slice().sort((a, b) => ms(b.ts) - ms(a.ts));
    const open = state.expandedMem[p] !== undefined ? state.expandedMem[p] : i === 0;
    const emojis = [...new Set(list.map((w) => agentMeta(w.agent).emoji))].join(' ');
    const writes = open ? list.map((w) => `<div class="mem-write">
        <div class="mem-write-meta">
          <span>${agentLink(w.agent)}</span>
          <span class="chip" style="${w.tool === 'write' ? 'background:var(--ok-bg);color:var(--ok)' : 'background:var(--info-bg);color:var(--info)'}">${esc(w.tool || 'tool')}</span>
          <span class="when" title="${esc(exact(w.ts))}">${rel(w.ts, state.now)}</span>
          <span class="grow"></span>
          ${w.sessionId ? `<a href="#/agents/${encodeURIComponent(w.agent)}/sessions/${encodeURIComponent(w.sessionId)}" style="white-space:nowrap">session ↗</a>` : ''}
        </div>
        <pre>${esc(w.content || '')}</pre>
      </div>`).join('') : '';
    return `<div class="card clip">
      <div class="mem-group-head" data-act="toggle-mem" data-path="${esc(p)}">
        <span class="mem-caret">${open ? '▾' : '▸'}</span>
        <span class="mem-path">${esc(p)}</span>
        <span class="mem-count">${list.length}</span>
        <span style="font-size:13px">${emojis}</span>
        <span class="grow"></span>
        <span class="when">latest ${rel(list[0].ts, state.now)}</span>
      </div>
      ${writes}
    </div>`;
  }).join('');

  return `<div class="page narrow">
    <div class="card clip"><div class="tabs">${memoryTabs('notes')}</div></div>
    <div class="page-head">
      <h1>All notes</h1>
      <span style="color:var(--sub);font-size:13px">every note on disk, newest first</span>
      <span class="spacer"></span>
      <select class="field" data-act="mem-agent">${agentOptions}</select>
      <input class="field path" data-act="mem-path" placeholder="Filter by path prefix, e.g. memory-map/tasks/" value="${esc(fp)}">
    </div>
    ${ws.length === 0 ? `<div class="empty-state"><div class="icon">📓</div>
      <h2 class="display">No memory writes found</h2>
      <p>Writes are detected from tool.call events touching <span class="mono">memory-map/**/*.md</span>.
         Adjust the filters or wait for agents to commit notes.</p></div>` : body}
  </div>`;
}


/* -------------------------------------------------------------- wiki view */

// Memory graph, ported from the Memory UI design: a canvas force simulation
// you can drag, zoom and pan, with hover dimming everything that is not a
// neighbour. Positions live in module state so a background refresh does not
// throw away a layout the user has arranged by hand.

const WIKI_TYPES = {
  concept:   { color: '#0066cc', label: 'Concept' },
  entity:    { color: '#009596', label: 'Entity' },
  synthesis: { color: '#5e40be', label: 'Synthesis' },
  report:    { color: '#795600', label: 'Report' },
  index:     { color: '#b8bbbe', label: 'Index' },
  source:    { color: '#8a8d90', label: 'Source' },
};
const wikiType = (t) => WIKI_TYPES[t] || { color: '#8a8d90', label: t || 'Page' };

const sim = {
  nodes: [], links: [], byId: {},
  key: '',                       // which claw the layout belongs to
  view: { ox: 0, oy: 0, scale: 1 },
  // Repel is higher than the design's default: this graph is mostly
  // unconnected pages, which have only repulsion to separate them.
  forces: { center: 0.35, repel: 1.9, linkF: 0.6, linkDist: 95 },
  showLabels: true, search: '', hidden: {}, hover: null, neigh: new Set(),
  canvas: null, raf: null, w: 0, h: 0, dpr: 1, drag: {},
};

// buildSim lays out nodes on a ring as a starting point; the simulation takes
// over from there. Sources are included only for pages the user expanded.
function buildSim(w) {
  const key = state.namespace + '/' + state.claw;
  const pages = w.pages || [];
  const wanted = [];
  pages.forEach((p) => wanted.push(p));
  Object.keys(state.wikiExpanded).forEach((id) => {
    const parent = pages.find((p) => p.id === id);
    if (!parent) return;
    (parent.sourceIds || []).forEach((sid) => {
      const src = (w.sources || {})[sid];
      if (src && !wanted.some((n) => n.id === sid)) wanted.push(src);
    });
  });

  const prev = sim.key === key ? sim.byId : {};
  // Seed on a ring sized to the node count and the measured canvas, so the
  // simulation starts spread out instead of untangling from a knot.
  const cx = (sim.w || 900) / 2, cy = (sim.h || 620) / 2;
  const R = Math.max(160, Math.min(cx, cy) - 60, wanted.length * 11);
  sim.nodes = wanted.map((p, i) => {
    const a = (i / Math.max(1, wanted.length)) * Math.PI * 2;
    const old = prev[p.id];
    return {
      id: p.id, path: p.path, type: p.pageType || 'page',
      label: p.title || p.id,
      short: (p.title || p.id).length > 26 ? (p.title || p.id).slice(0, 25) + '…' : (p.title || p.id),
      r: p.pageType === 'source' ? 4.5 : p.pageType === 'index' ? 6
        : (p.pageType === 'concept' || p.pageType === 'entity') ? 9 : 7,
      x: old ? old.x : cx + R * Math.cos(a), y: old ? old.y : cy + R * Math.sin(a),
      vx: 0, vy: 0, fx: null, fy: null,
    };
  });
  sim.byId = {};
  sim.nodes.forEach((n) => { sim.byId[n.id] = n; });

  sim.links = [];
  (w.edges || []).forEach((e) => {
    const a = sim.byId[e.from], b = sim.byId[e.to];
    if (!a || !b) return;
    sim.links.push({ a: e.from, b: e.to, sa: a, sb: b, kind: e.kind });
  });
  sim.key = key;
}

function simNeighbours(id) {
  const set = new Set();
  sim.links.forEach((l) => {
    if (l.a === id) set.add(l.b);
    if (l.b === id) set.add(l.a);
  });
  return set;
}

const simVisible = (n) => !sim.hidden[n.type];

function simStep() {
  const g = sim.forces, cx = sim.w / 2, cy = sim.h / 2;
  const ns = sim.nodes;
  for (let i = 0; i < ns.length; i++) {
    const a = ns[i];
    if (!simVisible(a)) continue;
    for (let j = i + 1; j < ns.length; j++) {
      const b = ns[j];
      if (!simVisible(b)) continue;
      let dx = a.x - b.x, dy = a.y - b.y;
      const d2 = dx * dx + dy * dy || 0.01, d = Math.sqrt(d2);
      const f = (g.repel * 1100) / d2, fx = (dx / d) * f, fy = (dy / d) * f;
      a.vx += fx; a.vy += fy; b.vx -= fx; b.vy -= fy;
    }
  }
  sim.links.forEach((l) => {
    if (!simVisible(l.sa) || !simVisible(l.sb)) return;
    const dx = l.sb.x - l.sa.x, dy = l.sb.y - l.sa.y;
    const d = Math.sqrt(dx * dx + dy * dy) || 0.01;
    const f = g.linkF * 0.045 * (d - g.linkDist), fx = (dx / d) * f, fy = (dy / d) * f;
    l.sa.vx += fx; l.sa.vy += fy; l.sb.vx -= fx; l.sb.vy -= fy;
  });
  ns.forEach((n) => {
    if (!simVisible(n)) return;
    n.vx += (cx - n.x) * g.center * 0.012;
    n.vy += (cy - n.y) * g.center * 0.012;
    if (n.fx != null) { n.x = n.fx; n.y = n.fy; n.vx = 0; n.vy = 0; return; }
    n.vx *= 0.82; n.vy *= 0.82;
    n.vx = Math.max(-30, Math.min(30, n.vx));
    n.vy = Math.max(-30, Math.min(30, n.vy));
    n.x += n.vx; n.y += n.vy;
  });
}

function simDraw() {
  const c = sim.canvas;
  if (!c) return;
  const ctx = c.getContext('2d'), dark = state.theme === 'dark';
  ctx.setTransform(sim.dpr, 0, 0, sim.dpr, 0, 0);
  ctx.clearRect(0, 0, sim.w, sim.h);
  const v = sim.view;
  ctx.save();
  ctx.translate(v.ox, v.oy);
  ctx.scale(v.scale, v.scale);

  const q = (sim.search || '').trim().toLowerCase();
  const match = (n) => !q || (n.label + ' ' + n.type).toLowerCase().includes(q);
  const anyHL = !!sim.hover || !!q;
  const lit = (n) => (!sim.hover || n.id === sim.hover || sim.neigh.has(n.id)) && match(n);

  sim.links.forEach((l) => {
    if (!simVisible(l.sa) || !simVisible(l.sb)) return;
    const active = (sim.hover && (l.a === sim.hover || l.b === sim.hover)) || (q && match(l.sa) && match(l.sb));
    const dim = anyHL && !active;
    ctx.beginPath();
    ctx.moveTo(l.sa.x, l.sa.y);
    ctx.lineTo(l.sb.x, l.sb.y);
    if (l.kind === 'conflict') {
      ctx.setLineDash([5, 4]);
      ctx.strokeStyle = dark ? '#ff9085' : '#c9190b';
    } else {
      ctx.setLineDash(l.kind === 'source' ? [3, 3] : []);
      ctx.strokeStyle = active ? (dark ? '#73bcf7' : '#0066cc')
        : dark ? (dim ? 'rgba(255,255,255,.05)' : 'rgba(255,255,255,.13)')
               : (dim ? 'rgba(0,0,0,.05)' : 'rgba(0,0,0,.14)');
    }
    ctx.lineWidth = (active ? 1.8 : 0.9) / v.scale;
    ctx.stroke();
  });
  ctx.setLineDash([]);

  sim.nodes.forEach((n) => {
    if (!simVisible(n)) return;
    const dim = anyHL && !lit(n);
    ctx.globalAlpha = dim ? 0.22 : 1;
    ctx.beginPath();
    ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
    ctx.fillStyle = wikiType(n.type).color;
    ctx.fill();
    if (sim.hover === n.id || (q && match(n))) {
      ctx.lineWidth = 2.5 / v.scale;
      ctx.strokeStyle = dark ? '#fff' : '#151515';
      ctx.stroke();
    }
    if (sim.showLabels || sim.hover === n.id || sim.neigh.has(n.id)) {
      ctx.globalAlpha = dim ? 0.3 : 1;
      ctx.fillStyle = dark ? '#cfd2d4' : '#33373b';
      ctx.font = '600 11px "Red Hat Text", sans-serif';
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      ctx.fillText(n.short, n.x, n.y + n.r + 4);
    }
  });
  ctx.restore();
  ctx.globalAlpha = 1;
}

function simTick() {
  if (!sim.canvas || !document.body.contains(sim.canvas)) { sim.raf = null; return; }
  simStep();
  simDraw();
  sim.raf = requestAnimationFrame(simTick);
}

function simResize() {
  const c = sim.canvas;
  if (!c) return;
  const rect = c.getBoundingClientRect();
  sim.dpr = window.devicePixelRatio || 1;
  sim.w = rect.width; sim.h = rect.height;
  c.width = Math.round(rect.width * sim.dpr);
  c.height = Math.round(rect.height * sim.dpr);
}

// world converts a pointer position into simulation coordinates.
function simWorld(e) {
  const rect = sim.canvas.getBoundingClientRect();
  const sx = e.clientX - rect.left, sy = e.clientY - rect.top;
  return { sx, sy, x: (sx - sim.view.ox) / sim.view.scale, y: (sy - sim.view.oy) / sim.view.scale };
}

function simNodeAt(p) {
  for (let i = sim.nodes.length - 1; i >= 0; i--) {
    const n = sim.nodes[i];
    if (!simVisible(n)) continue;
    const dx = n.x - p.x, dy = n.y - p.y;
    if (dx * dx + dy * dy <= (n.r + 6) * (n.r + 6)) return n;
  }
  return null;
}

// attachGraph wires the canvas after each render. Positions and view survive,
// so a refresh never disturbs a layout in progress.
function attachGraph() {
  const c = document.getElementById('wiki-canvas');
  if (!c) return;
  const isNew = c !== sim.canvas;
  sim.canvas = c;
  const hadSize = sim.w > 0;
  simResize();
  // The first build runs before the canvas is measured; re-seed once real
  // dimensions are known so the ring matches the viewport.
  if (!hadSize && sim.w > 0) { sim.key = ''; if (state.wiki) buildSim(state.wiki); }
  if (!isNew) { if (!sim.raf) sim.raf = requestAnimationFrame(simTick); return; }

  c.addEventListener('mousedown', (e) => {
    const p = simWorld(e);
    const n = simNodeAt(p);
    sim.drag = { down: true, moved: false, sx: p.sx, sy: p.sy };
    if (n) { sim.drag.node = n; n.fx = n.x; n.fy = n.y; }
    else { sim.drag.pan = true; sim.drag.ox = sim.view.ox; sim.drag.oy = sim.view.oy; }
  });
  c.addEventListener('wheel', (e) => {
    e.preventDefault();
    const p = simWorld(e);
    const next = Math.max(0.25, Math.min(3, sim.view.scale * (e.deltaY < 0 ? 1.1 : 1 / 1.1)));
    // Zoom about the cursor so the point under it stays put.
    sim.view.ox = p.sx - p.x * next;
    sim.view.oy = p.sy - p.y * next;
    sim.view.scale = next;
  }, { passive: false });

  if (!sim.bound) {
    sim.bound = true;
    window.addEventListener('mousemove', (e) => {
      if (!sim.canvas || !document.body.contains(sim.canvas)) return;
      const p = simWorld(e);
      const d = sim.drag;
      if (d.down) {
        if (Math.abs(p.sx - d.sx) + Math.abs(p.sy - d.sy) > 3) d.moved = true;
        if (d.node) { d.node.fx = p.x; d.node.fy = p.y; }
        else if (d.pan) { sim.view.ox = d.ox + (p.sx - d.sx); sim.view.oy = d.oy + (p.sy - d.sy); }
        return;
      }
      const n = simNodeAt(p);
      const id = n ? n.id : null;
      if (id !== sim.hover) {
        sim.hover = id;
        sim.neigh = id ? simNeighbours(id) : new Set();
        sim.canvas.style.cursor = id ? 'pointer' : 'default';
      }
    });
    window.addEventListener('mouseup', () => {
      const d = sim.drag;
      if (d.down && d.node) {
        if (!d.moved) openWikiPage(d.node.path);
        d.node.fx = null; d.node.fy = null;
      }
      sim.drag = {};
    });
    window.addEventListener('resize', () => { if (sim.canvas) simResize(); });
  }
  if (!sim.raf) sim.raf = requestAnimationFrame(simTick);
}

function viewWiki() {
  if (!state.wiki) {
    loadWiki();
    return `<div class="page narrow"><h1>Memory graph</h1><div class="loading">Reading the wiki…</div></div>`;
  }
  const w = state.wiki;
  if (w.error) {
    return `<div class="page narrow"><h1>Memory graph</h1><div class="empty-state"><div class="icon">📚</div>
      <h2 class="display">Could not read the wiki</h2><p>${esc(w.error)}</p></div></div>`;
  }
  if (!(w.pages || []).length) {
    return `<div class="page narrow"><h1>Memory graph</h1><div class="empty-state"><div class="icon">📚</div>
      <h2 class="display">Nothing synthesized yet</h2>
      <p>This wiki holds ${w.counts.source || 0} imported sources but no concepts, entities, or syntheses.
      Wiki synthesis runs only when an agent is asked to organize what it knows.</p></div></div>`;
  }

  buildSim(w);

  const legend = Object.keys(WIKI_TYPES).filter((k) => w.counts[k]).map((k) => {
    const off = !!sim.hidden[k];
    const t = WIKI_TYPES[k];
    return `<div class="glegend" data-act="wiki-type" data-type="${k}">
      <span class="gswatch" style="background:${off ? 'transparent' : t.color};border-color:${t.color}"></span>
      <span style="flex:1;color:${off ? 'var(--sub)' : 'var(--text)'}">${t.label}</span>
      <span class="mono" style="font-size:11px;color:var(--sub)">${w.counts[k]}</span>
    </div>`;
  }).join('');

  const slider = (act, label, min, max, step, val) => `<label class="gslider">${label}
    <input type="range" min="${min}" max="${max}" step="${step}" value="${val}" data-act="${act}">
  </label>`;

  return `<div class="graph-wrap">
    <div class="graph-canvas-wrap">
      <canvas id="wiki-canvas"></canvas>
      <div class="graph-caption">
        <h1>Memory graph</h1>
        <p>Every page is a node; links are declared relations and provenance. Drag nodes to rearrange,
           scroll to zoom, drag the canvas to pan, and click a node to open it.</p>
      </div>
      <div class="graph-actions">
        <button data-act="graph-recenter">Re-center layout</button>
        <button data-act="graph-reset">Reset zoom</button>
      </div>
    </div>
    <aside class="graph-controls">
      <div class="gc-head">
        <span class="display" style="font-weight:700;font-size:15px">Graph controls</span>
        <span class="mono" style="font-size:11px;color:var(--sub)">${sim.nodes.length}n · ${sim.links.length}e</span>
      </div>
      <div class="gc-search">
        <input placeholder="Highlight nodes…" value="${esc(sim.search)}" data-act="graph-search">
      </div>
      <div>
        <div class="gc-label">Page types · click to filter</div>
        ${legend}
      </div>
      <div>
        <div class="gc-label">Forces</div>
        ${slider('gf-center', 'Center force', 0, 1, 0.05, sim.forces.center)}
        ${slider('gf-repel', 'Repel force', 0.2, 3, 0.1, sim.forces.repel)}
        ${slider('gf-link', 'Link force', 0, 1.5, 0.05, sim.forces.linkF)}
        ${slider('gf-dist', 'Link distance', 40, 200, 5, sim.forces.linkDist)}
      </div>
      <div>
        <div class="gc-label">Display</div>
        <div class="gc-toggle" data-act="graph-labels">
          <span style="font-size:12.5px;color:var(--sub)">Show labels</span>
          <span class="toggle${sim.showLabels ? ' on' : ''}"><span class="knob"></span></span>
        </div>
        <div class="gc-toggle" data-act="graph-sources" style="margin-top:8px">
          <span style="font-size:12.5px;color:var(--sub)">Expand all sources</span>
          <span class="toggle${Object.keys(state.wikiExpanded).length ? ' on' : ''}"><span class="knob"></span></span>
        </div>
      </div>
      ${state.wikiPage ? renderWikiReader() : `<div class="gc-hint">Click a node to read the page.</div>`}
    </aside>
  </div>`;
}

const rel2 = (ts) => rel(ts, state.now);

function renderWikiReader() {
  const { page, content, error } = state.wikiPage;
  const t = wikiType(page.pageType);
  const claims = (page.claims || []).map((c) => `<li style="margin-bottom:6px">
      ${esc(c.text || c.id || '')}
      ${c.status ? `<span class="chip" style="background:var(--surface2);color:var(--sub);margin-left:4px">${esc(c.status)}</span>` : ''}
      ${c.confidence ? `<span class="chip" style="background:var(--info-bg);color:var(--info)">${(c.confidence * 100).toFixed(0)}%</span>` : ''}
    </li>`).join('');
  return `<div class="gc-reader">
    <div class="gc-reader-head">
      <span class="chip" style="background:var(--surface2);color:${t.color};border:1px solid ${t.color}">${t.label}</span>
      <span style="flex:1;font-weight:600;font-size:13px">${esc(page.title || page.path)}</span>
      <button class="link-btn" data-act="wiki-close">close</button>
    </div>
    <div class="when" style="margin-bottom:8px">${rel2(page.lastRefreshedAt || page.modifiedAt)}</div>
    ${error ? `<div class="empty">${esc(error)}</div>` : `
      ${claims ? `<ul style="margin:0 0 10px;padding-left:16px;font-size:12.5px;line-height:1.5">${claims}</ul>` : ''}
      <details><summary style="cursor:pointer;color:var(--link);font-size:12px">Page source</summary>
        <pre class="mono" style="white-space:pre-wrap;font-size:11.5px;line-height:1.5;background:var(--surface2);border:1px solid var(--border-soft);border-radius:6px;padding:8px;margin-top:6px;max-height:280px;overflow:auto">${esc(content || '')}</pre>
      </details>`}
  </div>`;
}

/* ----------------------------------------------------------------- render */

function render() {
  const r = route();
  const root = $('#root');

  if (!state.loaded) {
    root.innerHTML = renderMasthead() + `<div class="loading">Loading agent data…</div>`;
    return;
  }

  let main;
  if (state.scopeError) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🔒</div>
      <h2 class="display">Could not determine your access</h2>
      <p>${esc(state.scopeError)}</p></div></div>`;
  } else if (!state.local && state.claws.length === 0) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗝️</div>
      <h2 class="display">No Claws you can read</h2>
      <p>This console shows the agents of Claws in namespaces you have access to.
      ${state.user ? `You are signed in as <span class="mono">${esc(state.user)}</span> and no Claw` : 'No Claw'}
      is currently visible to you. Ask for access to a namespace that runs one, then reload.</p></div></div>`;
  } else if (state.backendDown) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🔌</div>
      <h2 class="display">Backend unreachable</h2>
      <p>The console could not reach its API. Nothing below is fabricated — the last known data is
      withheld rather than shown as current. Retrying every 5 seconds.</p></div></div>`;
  } else if (state.meta.ok === false) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗂️</div>
      <h2 class="display">No data to show</h2>
      <p>The data directory could not be read, so this console shows nothing rather than fabricated
      figures. Check the mount and permissions for <span class="mono">${esc(state.dataDir)}</span>.</p></div></div>`;
  } else if (state.agents.length === 0) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗂️</div>
      <h2 class="display">No agent activity yet</h2>
      <p>This console reads trajectory and transcript JSONL files from
      <span class="mono">${esc(state.dataDir)}/&lt;agent&gt;/sessions/</span>.
      Once an agent writes its first session, it appears here automatically.</p></div></div>`;
  } else if (r.isReplay) {
    main = viewReplay(r.seg[1], r.seg[3]);
  } else if (r.isAgent) {
    main = viewAgent(r.seg[1]);
  } else if (r.isTopology) {
    main = viewTopology();
  } else if (r.isMemory) {
    main = viewMemory();
  } else if (r.isWiki) {
    main = viewWiki();
  } else {
    main = viewOverview();
  }

  const banner = state.meta.ok === false
    ? `<div class="banner">
        <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="8" fill="#fff"></circle>
          <rect x="7.2" y="3.5" width="1.6" height="6" fill="#b1380b"></rect>
          <rect x="7.2" y="10.8" width="1.6" height="1.6" fill="#b1380b"></rect></svg>
        Agent data directory is unreadable${state.meta.error ? ' (' + esc(state.meta.error) + ')' : ''}.
        Showing nothing rather than lying — nothing below is fabricated.
      </div>` : '';

  root.innerHTML = renderMasthead() + banner +
    `<div class="body">${renderSidebar(r)}<main class="main" id="main">${main}</main></div>`;

  if (r.isReplay && state.follow && isRunningSession(state.session)) scrollToBottom();
  if (r.isWiki) attachGraph();
}

function scrollToBottom() {
  const el = $('#main');
  if (el) el.scrollTop = el.scrollHeight;
}

/* ------------------------------------------------------------- interaction */

function onClick(e) {
  const el = e.target.closest('[data-act]');
  if (!el) return;
  const act = el.dataset.act;

  switch (act) {
    case 'integrity':
      state.integrityOpen = !state.integrityOpen;
      return render();
    case 'theme': {
      const t = state.theme === 'light' ? 'dark' : 'light';
      state.theme = t;
      document.documentElement.setAttribute('data-ac-theme', t);
      try { localStorage.setItem('ac-theme', t); } catch { /* private mode */ }
      return render();
    }
    case 'tab':
      return setQ({ tab: el.dataset.tab });
    case 'sort': {
      const k = el.dataset.key;
      state.sortDir = state.sortKey === k && state.sortDir === 'desc' ? 'asc' : 'desc';
      state.sortKey = k;
      return render();
    }
    case 'more':
      state.runLimit += 25;
      return render();
    case 'filter-outcome': {
      const { q } = route();
      const cur = (q.outcome || '').split(',').filter(Boolean);
      const o = el.dataset.outcome;
      const next = cur.includes(o) ? cur.filter((x) => x !== o) : cur.concat(o);
      return setQ({ outcome: next.join(',') });
    }
    case 'filter-clear':
      return setQ({ agent: '', outcome: '', q: '', range: '' });
    case 'open-run':
      location.hash = `#/agents/${encodeURIComponent(el.dataset.agent)}/sessions/${encodeURIComponent(el.dataset.session)}`;
      return;
    case 'toggle-ev': {
      const k = el.dataset.key;
      state.expandedEv[k] = !state.expandedEv[k];
      return render();
    }
    case 'toggle-mem': {
      const p = el.dataset.path;
      const cur = state.expandedMem[p] !== undefined
        ? state.expandedMem[p]
        : Object.keys(state.expandedMem).length === 0;
      state.expandedMem[p] = !cur;
      return render();
    }
    case 'follow':
      state.follow = !state.follow;
      state.showResume = false;
      render();
      if (state.follow) scrollToBottom();
      return;
    case 'resume':
      state.follow = true;
      state.showResume = false;
      render();
      return scrollToBottom();
    case 'wiki-type': {
      const t = el.dataset.type;
      sim.hidden[t] = !sim.hidden[t];
      return render();
    }
    case 'graph-recenter': {
      // Unpin everything and let the simulation settle again.
      sim.nodes.forEach((n) => { n.fx = null; n.fy = null; n.vx = 0; n.vy = 0; });
      sim.key = '';
      state.wiki = { ...state.wiki };
      return render();
    }
    case 'graph-reset':
      sim.view = { ox: 0, oy: 0, scale: 1 };
      return;
    case 'graph-labels':
      sim.showLabels = !sim.showLabels;
      return render();
    case 'graph-sources': {
      const on = Object.keys(state.wikiExpanded).length > 0;
      state.wikiExpanded = {};
      if (!on) (state.wiki.pages || []).forEach((pg) => { state.wikiExpanded[pg.id] = true; });
      sim.key = '';
      return render();
    }
    case 'wiki-open':
      return openWikiPage(el.dataset.path);
    case 'wiki-close':
      state.wikiPage = null;
      return render();
    case 'topo-range':
      state.topoRange = el.dataset.range;
      state.selEdge = null;
      return render();
    case 'topo-edge': {
      const k = el.dataset.edge;
      state.selEdge = state.selEdge === k ? null : k;
      return render();
    }
    default:
      return;
  }
}

function onChange(e) {
  const el = e.target.closest('[data-act]');
  if (!el) return;
  switch (el.dataset.act) {
    case 'scope': {
      const [ns, claw] = String(el.value).split('/');
      if (!ns || !claw) return;
      state.namespace = ns;
      state.claw = claw;
      // A session id only means something within its own Claw.
      state.session = null;
      state.sessionKey = '';
      state.agents = []; state.runs = []; state.memory = []; state.handoffs = [];
      state.wiki = null; state.wikiPage = null; state.wikiExpanded = {};
      state.loaded = false;
      location.hash = '#/?ns=' + encodeURIComponent(ns) + '&claw=' + encodeURIComponent(claw);
      render();
      tick();
      return;
    }
    case 'filter-agent': return setQ({ agent: el.value });
    case 'filter-range': return setQ({ range: el.value });
    case 'filter-q': return setQ({ q: el.value });
    case 'mem-agent': return setQ({ agent: el.value });
    case 'mem-path': return setQ({ path: el.value });
    case 'graph-search': sim.search = el.value; return;
    case 'gf-center': sim.forces.center = +el.value; return;
    case 'gf-repel': sim.forces.repel = +el.value; return;
    case 'gf-link': sim.forces.linkF = +el.value; return;
    case 'gf-dist': sim.forces.linkDist = +el.value; return;
    default: return;
  }
}

// Scrolling up during a live replay disengages auto-follow, matching the
// behavior of a terminal that stops tailing when you scroll back.
function onScroll(e) {
  const el = e.target;
  if (!el || el.id !== 'main') return;
  const r = route();
  if (!r.isReplay || !isRunningSession(state.session)) return;
  const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
  if (gap > 150 && state.follow) {
    state.follow = false;
    state.showResume = true;
    render();
  }
}

async function onHashChange() {
  state.hash = location.hash || '#/';
  state.runLimit = 25;
  state.integrityOpen = false;
  state.showResume = false;

  const r = route();
  if (r.isReplay) {
    const key = r.seg[1] + '/' + r.seg[3];
    if (state.sessionKey !== key) {
      state.session = null;
      state.sessionKey = '';
      state.follow = true;
      state.expandedEv = {};
      render();
      await loadSession(r.seg[1], r.seg[3]);
    }
  } else {
    state.session = null;
    state.sessionKey = '';
  }
  render();
}

/* -------------------------------------------------------------- bootstrap */

async function tick() {
  state.now = Date.now();
  await refresh();
  const r = route();
  if (r.isReplay && state.sessionKey === r.seg[1] + '/' + r.seg[3]) {
    const grew = await tailSession();
    render();
    if (grew && state.follow) scrollToBottom();
    return;
  }
  render();
}

function start() {
  try {
    const t = localStorage.getItem('ac-theme');
    if (t === 'dark' || t === 'light') state.theme = t;
  } catch { /* private mode */ }
  document.documentElement.setAttribute('data-ac-theme', state.theme);

  document.addEventListener('click', onClick);
  document.addEventListener('change', onChange);
  document.addEventListener('input', (e) => {
    // Debounce free-text filters so each keystroke doesn't rewrite the hash.
    const el = e.target.closest('[data-act]');
    if (el && ['graph-search', 'gf-center', 'gf-repel', 'gf-link', 'gf-dist'].includes(el.dataset.act)) {
      onChange(e);
      return;
    }
    if (!el || !['filter-q', 'mem-path'].includes(el.dataset.act)) return;
    clearTimeout(el._t);
    el._t = setTimeout(() => onChange(e), 250);
  });
  document.addEventListener('scroll', onScroll, true);
  window.addEventListener('hashchange', onHashChange);

  onHashChange();
  tick();
  setInterval(tick, 5000);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', start);
} else {
  start();
}
