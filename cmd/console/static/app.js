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

// Agents with no configured identity emoji render the OpenClaw mark (the same
// asset as the masthead logo) instead of a generic placeholder. The <img>
// sizes in em units so it tracks the surrounding font wherever emojis appear.
const LOGO_EMOJI = '<img class="emoji-logo" src="openclaw.svg" alt="OpenClaw">';
const emojiHtml = (e) => e || LOGO_EMOJI;


const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

// Reverses esc so a value escaped for display can be matched against the
// unescaped paths and titles the note index stores.
const unesc = (s) => String(s ?? '').replace(/&(amp|lt|gt|quot|#39);/g, (_, e) =>
  ({ amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'" }[e]));

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
  backendDown: false,
  clawError: null,
  runsTotal: 0,
  runsWanted: 0,
  integrityOpen: false,
  runLimit: 25,
  sortKey: 'time',
  sortDir: 'desc',
  expandedEv: {},
  expandedDirs: {},
  expandedRuns: {},
  noteContent: {},   // note path -> fetched body, for the notes reader
  noteLoading: {},
  follow: true,
  showResume: false,
  runScrolled: false,
  topoRange: '7d',
  selEdge: null,
  // data
  agents: [],
  runs: [],
  memory: [],
  memoryTotal: 0,
  observed: [],
  watchingSince: '',
  memPersistent: false,
  handoffs: [],
  origins: [],
  handoffSessions: 0,
  handoffLinked: 0,
  meta: { ok: true, error: '', badLines: 0, scannedFiles: 0, unreadableFiles: 0, skippedSessions: 0 },
  // replay-scoped
  session: null,
  sessionKey: '',
  // multi-tenant scope
  local: false,
  user: '',
  claws: [],          // [{namespace, name, ready}]
  userMenuOpen: false,
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
  // Segments are decoded: a run id is now part of the path, and it is not
  // guaranteed to be URL-safe the way a session UUID is.
  const seg = p.split('/').filter(Boolean).map((s) => {
    try { return decodeURIComponent(s); } catch { return s; }
  });
  return { seg, q };
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

// loadNote fetches one note's body on demand. In-flight and completed reads are
// both remembered, so a five-second re-render does not re-request the file.
async function loadNote(path) {
  if (state.noteContent[path] !== undefined || state.noteLoading[path]) return;
  state.noteLoading[path] = true;
  try {
    const d = await getJSON('api/memory/note' + scopeQuery({ path }));
    state.noteContent[path] = d.content || '';
  } catch (err) {
    state.noteContent[path] = '(could not read this note: ' + String(err.message || err) + ')';
  }
  delete state.noteLoading[path];
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
    // A session holds many runs. The trailing /runs/<id> names which one the
    // reader came in on, so the page can open that one instead of guessing.
    runId: isReplay && seg[4] === 'runs' ? seg[5] || '' : '',
    isTopology: seg[0] === 'topology',
    isMemory: seg[0] === 'memory',
    isWiki: seg[0] === 'wiki',
    isOverview: !isReplay && !isAgent && !['topology', 'memory', 'wiki'].includes(seg[0]),
  };
}

/* -------------------------------------------------------------- data layer */

// Errors carry the status and whatever the server said. Collapsing them to
// "HTTP 403" lost the distinction between the console being broken and the
// reader not being allowed, which are opposite problems with opposite fixes.
async function getJSON(url) {
  const res = await fetch(url, { headers: { accept: 'application/json' } });
  if (!res.ok) {
    let detail = '';
    try { detail = (await res.json()).error || ''; } catch { /* not JSON */ }
    const err = new Error(detail || 'HTTP ' + res.status);
    err.status = res.status;
    err.detail = detail;
    throw err;
  }
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
      getJSON('api/runs' + scopeQuery({ limit: state.runsWanted || 500 })),
      getJSON('api/memory' + scopeQuery({ limit: 2000 })),
      getJSON('api/handoffs' + scopeQuery()),
      getJSON('api/health' + scopeQuery()),
    ]);
    Object.assign(state, {
      agents: agents.agents || [],
      runs: runs.runs || [],
      runsTotal: runs.total || 0,
      memory: memory.writes || [],
      memoryTotal: memory.total || 0,
      observed: memory.observed || [],
      watchingSince: memory.watchingSince || '',
      memPersistent: !!memory.persistent,
      handoffs: handoffs.handoffs || [],
      origins: handoffs.origins || [],
      handoffSessions: handoffs.sessions || 0,
      handoffLinked: handoffs.linked || 0,
      meta: health.data || state.meta,
      backendDown: false, clawError: null, loaded: true,
    });
  } catch (err) {
    state.loaded = true;
    // A 4xx is an answer, not a failure: this Claw has no running pod, or this
    // user may not exec into it. Reporting either as "backend unreachable"
    // blamed the console for a condition it had correctly detected, and sent
    // the reader looking in entirely the wrong place.
    if (err.status >= 400 && err.status < 500) {
      state.clawError = { status: err.status, detail: err.detail || String(err.message || '') };
      state.backendDown = false;
    } else {
      state.backendDown = true;
      state.clawError = null;
    }
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
    emoji: emojiHtml(a && a.emoji),
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
  const integ = (state.meta.badLines || 0) + (state.meta.unreadableFiles || 0)
    + (state.meta.truncatedEvents || 0) + (state.meta.skippedSessions || 0);
  const showInteg = state.loaded && integ > 0 && state.meta.ok !== false;

  return `<header class="masthead">
    <a class="brand" href="#/">
      <img class="brand-logo" src="openclaw.svg" alt="" aria-hidden="true">
      <span class="brand-text">
        <span class="brand-title">Agent Console</span>
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
    <button class="icon-btn" data-act="theme" title="Toggle light/dark">${state.theme === 'light' ? '☾' : '☀'}</button>
    <span class="masthead-user">${esc(state.user)}</span>
    ${renderUserMenu()}
    ${state.integrityOpen ? `<div class="popover">
      <h3 class="display">Data integrity</h3>
      <div class="popover-grid">
        <span style="color:var(--sub)">Files scanned</span><span class="mono">${state.meta.scannedFiles || 0}</span>
        <span style="color:var(--sub)">Unparseable lines</span><span class="mono" style="color:var(--warn)">${state.meta.badLines || 0}</span>
        <span style="color:var(--sub)">Unreadable files</span><span class="mono" style="color:var(--warn)">${state.meta.unreadableFiles || 0}</span>
        <span style="color:var(--sub)">Truncated events</span><span class="mono" style="color:var(--warn)">${state.meta.truncatedEvents || 0}</span>
        <span style="color:var(--sub)">Sessions past the cap</span><span class="mono" style="color:var(--warn)">${state.meta.skippedSessions || 0}</span>
      </div>
      <div class="popover-note">Counted, never hidden. Unparseable lines are excluded from every figure here.
        Sessions past the cap are the oldest beyond the 200 newest per agent; they are not parsed, so
        their runs are absent from every count on this page.
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

// Identity and sign-out. The oauth-proxy sidecar owns the session, so signing
// out means hitting its endpoint (/oauth/ is the openshift fork's default
// proxy prefix) rather than clearing anything here. This ends the proxy
// session only: the cluster OAuth server's SSO session survives, so signing
// in again without ending that session too returns the same user. In local
// mode there is no proxy and no session, so nothing is shown.
function renderUserMenu() {
  if (state.local) return '';
  if (!state.user && !state.scopeError) return '';
  const initials = state.user
    ? state.user.replace(/@.*$/, '').split(/[.\-_ ]/)
        .filter(Boolean).slice(0, 2).map((s) => s[0].toUpperCase()).join('') || '?'
    : '?';
  return `<div class="usermenu">
    <button class="avatar" data-act="user-menu" title="${esc(state.user || 'Session')}">${esc(initials)}</button>
    ${state.userMenuOpen ? `<div class="usermenu-pop">
      <div class="usermenu-name">${state.user ? esc(state.user) : 'Unknown'}</div>
      <div class="usermenu-sub">${state.scopeError ? 'Session may have expired' : 'Signed in through OpenShift'}</div>
      <a class="usermenu-out" href="/oauth/sign_out">Sign out</a>
    </div>` : ''}
  </div>`;
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
      <span>${emojiHtml(a.emoji)}</span>
      <span class="nav-agent-name">${esc(a.title || a.name)}</span>
      <span class="dot sm${m.pulse ? ' pulse' : ''}" style="background:${m.dot}"></span>
    </a>`;
  }).join('');

  return `<nav class="sidebar">
    ${nav}
    <div class="nav-section">AGENTS</div>
    ${agents}
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
        ${state.agents.map((a) => `<option value="${esc(a.name)}"${q.agent === a.name ? ' selected' : ''}>${a.emoji ? a.emoji + ' ' : ''}${esc(a.title || a.name)}</option>`).join('')}
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
  // How many runs exist, against how many were fetched. Counting only what was
  // loaded reported a truncated list as the whole history — and since runs
  // arrive newest first, the missing ones were the oldest, so "All time" was
  // quietly not all time.
  const missing = Math.max(0, (state.runsTotal || 0) - state.runs.length);

  return `<div class="toolbar">
    ${agentSelect}
    <div style="display:flex;gap:6px">${chips}</div>
    <input class="field text" data-act="filter-q" placeholder="Search prompts…" value="${esc(q.q || '')}">
    <select class="field" data-act="filter-range">${ranges}</select>
    ${active ? `<button class="link-btn" data-act="filter-clear">Clear filters</button>` : ''}
    <span class="spacer"></span>
    ${missing ? `<span class="result-count" style="color:var(--warn)"
      title="Runs are fetched newest first, so the ones not loaded are the oldest.">${missing} older
      run${missing === 1 ? '' : 's'} not loaded</span>
      <button class="link-btn" data-act="load-all-runs">Load all ${state.runsTotal}</button>` : ''}
    <span class="result-count">${rows.length} of ${scope} loaded</span>
  </div>`;
}

function renderRunsTable(lockedAgent) {
  const rows = filteredRuns(lockedAgent);
  const visible = rows.slice(0, state.runLimit);
  const arrow = (k) => (state.sortKey === k ? (state.sortDir === 'desc' ? ' ↓' : ' ↑') : '');

  const body = visible.map((r) => {
    const spawn = r.spawnedBy ? agentMeta(r.spawnedBy.fromAgent).title : '';
    return `<tr data-act="open-run" data-agent="${esc(r.agent)}" data-session="${esc(r.sessionId)}" data-run="${esc(r.runId)}">
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
    <div class="table-scroll" data-scroll-key="runs-table"><table>
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
        <span class="agent-emoji">${emojiHtml(a.emoji)}</span>
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
      : `<div class="empty"><div class="empty-title">No memory notes yet</div>
          <div class="empty-body">Notes are read from this Claw's memory stores:
            <span class="mono">workspace/memory</span>, <span class="mono">workspace/wiki</span>,
            and each agent's own <span class="mono">memory/dreaming</span>.</div></div>`);

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
      <span class="big-emoji">${emojiHtml(a.emoji)}</span>
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

const runHref = (agent, sessionId, runId) =>
  `#/agents/${encodeURIComponent(agent)}/sessions/${encodeURIComponent(sessionId)}` +
  (runId ? `/runs/${encodeURIComponent(runId)}` : '');

// A session is one continuous conversation, and OpenClaw records every run of
// it in a single file — up to 48 on a real Claw. Showing that file as one flat
// event list meant the run you clicked was indistinguishable from the 47 you
// did not, so the page is built from runs, with only the one you opened
// expanded.
function viewSession(agent, sessionId, wantRun) {
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

  const running = isRunningSession(s);
  const isChat = s.source === 'transcript';
  const srcStyle = s.source === 'trajectory'
    ? 'background:var(--info-bg);color:var(--info)'
    : 'background:var(--purple-bg);color:var(--purple)';

  const children = (s.children || []).map((c) =>
    `<a class="chip" style="background:var(--teal-bg);color:var(--teal)"
      href="${runHref(c.toAgent, c.toSessionId, '')}">→ ${esc(agentMeta(c.toAgent).title)} ${esc(String(c.toSessionId).slice(0, 6))}…</a>`).join('');

  const runs = s.runs || [];
  const totals = runs.reduce((t, r) => ({
    tokens: t.tokens + ((r.tokens && r.tokens.total) || 0),
    errors: t.errors + (r.outcome === 'error' ? 1 : 0),
  }), { tokens: 0, errors: 0 });

  const head = `<div class="replay-head session-head">
    ${crumbs}
    <div class="replay-title">
      <span style="font-size:20px">${m.emoji}</span>
      <span class="name">${esc(m.title)}</span>
      <span class="sid">${esc(sessionId)}</span>
      <span class="chip" style="${srcStyle}">${esc(s.source || 'trajectory')}</span>
      ${s.badLines ? `<span class="chip" style="background:var(--warn-bg);color:var(--warn)"
        title="unparseable lines in this session file, excluded from replay">⚠ ${s.badLines} bad lines</span>` : ''}
      ${s.parent ? `<a class="chip" style="background:var(--purple-bg);color:var(--purple)"
        href="${runHref(s.parent.fromAgent, s.parent.fromSessionId, s.parent.fromRunId)}">← spawned by ${esc(agentMeta(s.parent.fromAgent).title)}</a>` : ''}
      ${children}
      <span class="spacer"></span>
      ${s.total > (s.events || []).length ? `<span class="chip" style="background:var(--warn-bg);color:var(--warn)"
        title="This session is longer than one page of events. What is shown starts from the beginning of the file.">
        showing ${(s.events || []).length} of ${s.total} events</span>` : ''}
      <span class="result-count">${isChat
        ? `${(s.events || []).length} messages`
        : `${runs.length} run${runs.length === 1 ? '' : 's'} · ${(s.events || []).length} events${
            totals.errors ? ` · ${totals.errors} failed` : ''} · ${tok(totals.tokens)} tokens`}${running ? ' · live' : ''}</span>
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

  // Events carry the run that produced them, so the file partitions cleanly.
  const byRun = new Map();
  (s.events || []).forEach((e, i) => {
    const k = e.runId || '(no run id)';
    if (!byRun.has(k)) byRun.set(k, []);
    byRun.get(k).push({ ...e, _i: i });
  });

  // Runs the summary knows about, then any run that only the raw events
  // mention — an event is never dropped because its run was not derived.
  const known = new Set(runs.map((r) => r.runId));
  const extra = [...byRun.keys()].filter((k) => !known.has(k))
    .map((k) => ({ runId: k, sessionId, agent, outcome: '', prompt: '',
      startedAt: (byRun.get(k)[0] || {}).ts }));
  const ordered = runs.concat(extra)
    .sort((a, b) => ms(a.startedAt) - ms(b.startedAt));

  // Default open: the run the URL names, else the newest, so arriving without
  // a run still lands on something rather than a wall of collapsed rows.
  const fallback = ordered.length ? ordered[ordered.length - 1].runId : '';
  const defaultOpen = wantRun && ordered.some((r) => r.runId === wantRun) ? wantRun : fallback;

  const blocks = ordered.map((r, i) => {
    const evs = byRun.get(r.runId) || [];
    const open = state.expandedRuns[r.runId] !== undefined
      ? state.expandedRuns[r.runId] : r.runId === defaultOpen;
    const current = r.runId === wantRun;
    const om = outMeta(r.outcome || (evs.length ? 'ok' : ''));
    const t = r.tokens || {};
    return `<div class="run-block${current ? ' current' : ''}" id="run-${esc(r.runId)}">
      <div class="run-head" data-act="toggle-run" data-run="${esc(r.runId)}">
        <span class="caret">${open ? '▾' : '▸'}</span>
        <span class="idx">#${i + 1}</span>
        <span class="when" title="${esc(exact(r.startedAt))}">${hms(r.startedAt)}</span>
        ${r.outcome ? outcomePill(r.outcome) : ''}
        <span class="rp"${r.promptTruncated ? ' style="color:var(--sub);font-style:italic"' : ''}>${esc(r.prompt || '(no prompt recorded)')}</span>
        ${r.promptSource === 'transcript' ? `<span class="chip tag" style="background:var(--warn-bg);color:var(--warn)"
          title="The trajectory event was truncated; this prompt was recovered from the session transcript.">recovered</span>` : ''}
        <span class="rmeta">${evs.length} ev${t.total ? ' · ' + tok(t.total) : ''}</span>
      </div>
      ${open ? `<div class="run-body">
        ${r.prompt ? `<div class="run-prompt">${esc(r.prompt)}</div>` : ''}
        <div class="run-facts">
          <span>run <b class="mono">${esc(r.runId)}</b></span>
          ${r.model ? `<span>model <b>${esc(r.model)}</b></span>` : ''}
          ${t.total ? `<span>tokens <b>${tok(t.input)} in</b> · <b>${tok(t.output)} out</b>${
            t.cache ? ` · <b>${tok(t.cache)}</b> cached` : ''}</span>` : ''}
          <span>${esc(exact(r.startedAt))}</span>
        </div>
        ${renderEvents(evs)}
      </div>` : ''}
    </div>`;
  }).join('');

  return `<div>${head}<div class="session">${blocks}
    ${running && state.showResume ? `<div class="resume-wrap">
      <button class="resume-btn" data-act="resume">↓ Resume following</button></div>` : ''}
  </div></div>`;
}

function renderEvents(evs) {
  const rows = evs.map((e) => {
    const tm = typeMeta(e.type);
    const key = String(e.seq != null ? e.seq : e._i);
    const open = !!state.expandedEv[key];
    const summary = e.summary || '';
    const isErr = /^(ERROR|FATAL)/.test(summary);
    return `<div>
      <div class="event-line" data-act="toggle-ev" data-key="${esc(key)}">
        <span class="seq">${esc(String(e.seq != null ? e.seq : e._i + 1))}</span>
        <span class="time" title="${esc(exact(e.ts))}">${hms(e.ts)}</span>
        <span class="type" style="background:${tm.bg};color:${tm.color}">${esc(tm.label)}</span>
        <span class="summary${isErr ? ' err' : ''}">${esc(summary)}</span>
        <span class="caret">${open ? '▾' : '▸'}</span>
      </div>
      ${open ? `<pre data-scroll-key="ev:${esc(key)}">${esc(JSON.stringify(e.data || {}, null, 2))}</pre>` : ''}
    </div>`;
  }).join('');
  return `<div class="events" style="padding:4px 8px 6px">${rows}</div>`;
}

// Layered layout. Agents that only delegate go left, agents that are only
// delegated to go right, agents doing both sit in the middle. Agents with no
// handoffs at all are parked on their own row so an edge never appears to
// route through an uninvolved agent.
// Two layouts, because agent fleets come in two shapes.
//
// A pipeline — some agents only delegate, others only receive — reads best
// left to right in columns. A fleet where everyone talks to everyone has no
// such order, and the column layout collapsed it: every agent counted as both
// a source and a sink, so all of them stacked into the middle column on one
// vertical line with their edges drawn on top of each other. That shape gets a
// ring, where every pair has a clear line between them.
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

  const pos = {};
  const ends = cols[0].length + cols[2].length;
  if (ends === 0 || connected.length > 7) {
    // No pipeline to read, or too many nodes for three columns.
    const cx = 340;
    const cy = isolated.length ? 160 : 185;
    const R = connected.length <= 2 ? 105 : Math.min(135, 74 + connected.length * 17);
    connected.forEach((n, i) => {
      const a = -Math.PI / 2 + (i / connected.length) * Math.PI * 2;
      pos[n] = [Math.round(cx + R * Math.cos(a)), Math.round(cy + R * Math.sin(a))];
    });
  } else {
    const xs = cols[1].length ? [130, 340, 550] : [200, 340, 480];
    const graphHeight = isolated.length ? 250 : 380;
    cols.forEach((col, ci) => {
      const span = graphHeight / (col.length + 1);
      col.forEach((n, i) => { pos[n] = [xs[ci], Math.round(span * (i + 1))]; });
    });
  }

  isolated.forEach((n, i) => {
    const span = 680 / (isolated.length + 1);
    pos[n] = [Math.round(span * (i + 1)), 330];
  });
  return { pos, isolated: new Set(isolated) };
}

// edgePath bows each direction of a pair to its own side and stops the line at
// the node's edge. Drawn centre to centre with a fixed upward bow, A->B and
// B->A landed on the same curve and the arrowheads hid under the circles.
function edgePath(x1, y1, x2, y2, count, r1, r2) {
  const dx = x2 - x1, dy = y2 - y1;
  const len = Math.hypot(dx, dy) || 1;
  // The offset is perpendicular to the direction of travel, and that direction
  // reverses between A->B and B->A, so the two bow to opposite sides on their
  // own. Signing this by anything else — node name order, say — cancels the
  // reversal out and puts both curves back on top of each other.
  const bow = Math.min(52, 24 + count * 4);
  const cx = (x1 + x2) / 2 - (dy / len) * bow;
  const cy = (y1 + y2) / 2 + (dx / len) * bow;

  // A quadratic's tangent at an endpoint points along (endpoint - control), so
  // backing off along it clears the circle without distorting the curve.
  const trim = (px, py, r) => {
    const tx = px - cx, ty = py - cy;
    const tl = Math.hypot(tx, ty) || 1;
    return [px - (tx / tl) * r, py - (ty / tl) * r];
  };
  const [sx, sy] = trim(x1, y1, r1 + 2);
  const [ex, ey] = trim(x2, y2, r2 + 9);
  return {
    d: `M${sx.toFixed(1)} ${sy.toFixed(1)} Q${cx.toFixed(1)} ${cy.toFixed(1)} ${ex.toFixed(1)} ${ey.toFixed(1)}`,
    // Badge sits on the curve at t=0.5.
    mx: (x1 + 2 * cx + x2) / 4, my: (y1 + 2 * cy + y2) / 4,
  };
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

  const radiusOf = (name) => (isolated.has(name) ? 26 : 36);
  const edgeSvg = [];
  const edgeBadges = [];
  Object.keys(groups).forEach((k) => {
    const [f, t] = k.split('→');
    if (!pos[f] || !pos[t]) return;
    const [x1, y1] = pos[f];
    const [x2, y2] = pos[t];
    const n = groups[k].length;
    const sel = state.selEdge === k;
    const { d, mx, my } = edgePath(x1, y1, x2, y2, n, radiusOf(f), radiusOf(t));
    edgeSvg.push(`<g data-act="topo-edge" data-edge="${esc(k)}" style="cursor:pointer">
      <path d="${d}" fill="none" stroke="transparent" stroke-width="18"></path>
      <path d="${d}" fill="none" stroke="${sel ? 'var(--info)' : 'var(--border)'}"
        stroke-width="${Math.min(6, 1.5 + n * 0.5).toFixed(1)}" marker-end="url(#acArrow)"></path>
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
        style="left:${((x / 680) * 100).toFixed(2)}%;top:${((y / 380) * 100).toFixed(2)}%${off ? ';font-size:19px' : ''}">${emojiHtml(a.emoji)}</a>
      <a class="topo-node-label" href="#/agents/${encodeURIComponent(a.name)}"
        style="left:${((x / 680) * 100).toFixed(2)}%;top:${(((y + r + 10) / 380) * 100).toFixed(2)}%">
        <span class="n">${esc(a.title || a.name)}</span>
        <span class="s">${runs} runs · ${off ? 'no handoffs' : st.label.toLowerCase()}</span></a>`);
  });

  const sel = state.selEdge && groups[state.selEdge];
  const selList = (sel || []).slice().sort((a, b) => ms(b.ts) - ms(a.ts)).map((h) => `<div class="row">
      <span class="when" title="${esc(exact(h.ts))}">${rel(h.ts, state.now)}</span>
      <span class="chip" style="${h.tool
        ? 'background:var(--ok-bg);color:var(--ok)' : 'background:var(--warn-bg);color:var(--warn)'}"
        title="${h.tool ? 'The runtime recorded this message and what carried it.' : 'Inferred from timing, not recorded by the runtime.'}"
        >${esc(h.tool || 'inferred')}</span>
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
    ${renderOrigins()}
  </div>`;
}

// What the topology can and cannot see, and where the work actually comes from.
//
// An edge exists only where OpenClaw stamped a parent onto the delegated
// prompt, which it does on one delivery path. On a real fleet that produced two
// edges against 239 sessions — not because the agents do not collaborate, but
// because nothing recorded it. Saying so, and showing the triggers the runtime
// does record, beats a graph that quietly implies it knows the whole story.
const TRIGGERS = {
  user: 'started by a user or an API call',
  cron: 'fired by a schedule',
  heartbeat: 'periodic wake-up',
  memory: 'memory maintenance',
  unrecorded: 'no trigger recorded',
};

function renderOrigins() {
  const origins = state.origins || [];
  if (!origins.length) return '';
  const linked = state.handoffLinked || 0;
  const sessions = state.handoffSessions || 0;
  const rows = origins.map((o) => {
    const m = agentMeta(o.agent);
    return `<div class="row">
      <span class="nowrap">${m.emoji} <a href="#/agents/${encodeURIComponent(o.agent)}">${esc(m.title)}</a></span>
      <span class="chip" style="background:var(--surface2);color:var(--sub)">${esc(o.trigger)}</span>
      <span style="color:var(--sub);font-size:12px">${esc(TRIGGERS[o.trigger] || 'trigger recorded by the runtime')}</span>
      ${o.kind && o.kind !== 'unknown'
        ? `<span class="chip" style="background:var(--info-bg);color:var(--info)" title="shape of the session key">${esc(o.kind)}</span>` : ''}
      <span class="grow"></span>
      <span class="mono" style="font-size:12px">${o.sessions} session${o.sessions === 1 ? '' : 's'}</span>
      <span class="when">${o.lastAt ? rel(o.lastAt, state.now) : ''}</span>
    </div>`;
  }).join('');

  return `<div class="card clip">
    <div class="section-head">How work starts · ${sessions} sessions, ${linked} with a recorded parent</div>
    <div class="section-hint">An edge above is drawn only where the runtime stamped the originating session onto
      the delegated prompt. It does that on one delivery path, so most sessions carry no parent at all — that is a
      gap in what OpenClaw records, not evidence the agents worked alone. These are the triggers it does record.</div>
    ${rows}
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

// What actually changed, and when. Every entry carries the lines that appeared:
// OpenClaw records nothing about memory writes, so a claim that an agent wrote
// something is only worth making when the diff is there to back it up.
function viewMemoryWrites() {
  const rows = (state.observed || []).map((e) => {
    const m = e.agent ? agentMeta(e.agent) : null;
    const lines = (e.addedLines || []).filter((l) => l.trim());
    return `<div class="card clip" style="margin-bottom:10px">
      <div class="mem-group-head" style="cursor:default">
        <span class="chip" style="background:var(--ok-bg);color:var(--ok)">+${e.added}</span>
        ${e.removed ? `<span class="chip" style="background:var(--err-bg);color:var(--err)">−${e.removed}</span>` : ''}
        ${e.created ? `<span class="chip" style="background:var(--purple-bg);color:var(--purple)"
          title="This note did not exist at the previous observation.">new note</span>` : ''}
        <a class="mem-path" href="#/memory?tab=notes&note=${encodeURIComponent(e.notePath)}">${esc(e.notePath)}</a>
        <span class="grow"></span>
        ${m ? `<span>${m.emoji} ${esc(m.title)}</span>` : '<span style="color:var(--sub)">shared</span>'}
        <span class="when" title="${esc(exact(e.ts))}">${rel(e.ts, state.now)}</span>
      </div>
      ${lines.length ? `<div class="diff">${lines.map((l) => `<div class="line">${esc(l)}</div>`).join('')}
        ${e.truncated ? `<div class="rest">…and ${e.truncated} more added line${e.truncated === 1 ? '' : 's'}, withheld to keep the feed readable.</div>` : ''}
      </div>` : ''}
    </div>`;
  }).join('');

  return `<div class="page">
    <div class="page-head">
      <h1>Memory</h1>
      <span style="color:var(--sub);font-size:13px">what the agents wrote, and when</span>
    </div>
    <div class="card clip"><div class="tabs">${memoryTabs('writes')}</div></div>
    ${state.watchingSince ? `<div class="watch-note">Watching since
      <b title="${esc(exact(state.watchingSince))}">${rel(state.watchingSince, state.now)}</b>.
      ${state.memPersistent
        ? 'The record is stored, so it survives restarts and covers writes made while nobody was looking.'
        : 'No state directory is configured, so this record is held in memory and is lost on restart.'}</div>` : ''}
    ${rows || `<div class="empty-state"><div class="icon">👁️</div>
      <h2 class="display">${state.watchingSince ? 'No writes observed yet' : 'Not watching yet'}</h2>
      <p>OpenClaw records nothing about memory writes, so this console detects them by comparing
      each note against the last content it saw${state.watchingSince ? `, which it first recorded ${rel(state.watchingSince, state.now)}` : ''}.
      Only changes it can show a diff for appear here. Every note on disk is under <b>All notes</b>.</p></div>`}
  </div>`;
}

/* ------------------------------------------------------------- notes tree */

// The notes are a filesystem, so they are shown as one. A flat list ordered by
// mtime made it impossible to see the shape of what an agent knows — which
// stores exist, which agent owns which, how a day's notes relate to the wiki.

// buildTree turns flat paths into nested directories. Files carry the note
// record so a row can show who wrote it and when.
function buildTree(notes) {
  const root = { dirs: new Map(), files: [], latest: 0, count: 0 };
  notes.forEach((w) => {
    const parts = String(w.notePath).split('/');
    const file = parts.pop();
    let node = root;
    node.count++;
    node.latest = Math.max(node.latest, ms(w.ts));
    parts.forEach((seg) => {
      if (!node.dirs.has(seg)) node.dirs.set(seg, { dirs: new Map(), files: [], latest: 0, count: 0 });
      node = node.dirs.get(seg);
      node.count++;
      node.latest = Math.max(node.latest, ms(w.ts));
    });
    node.files.push({ ...w, name: file });
  });
  return root;
}

// dirOpen decides whether a directory shows its children. Ancestors of the
// selected note are always open, so a link to a deep note lands with the path
// to it visible rather than collapsed.
function dirOpen(prefix, selected) {
  if (state.expandedDirs[prefix] !== undefined) return state.expandedDirs[prefix];
  if (selected && (selected + '/').startsWith(prefix + '/')) return true;
  return prefix.split('/').length <= 2;
}

function renderTree(node, prefix, selected, depth) {
  const pad = 8 + depth * 13;
  const dirs = [...node.dirs.entries()].sort((a, b) => b[1].latest - a[1].latest);
  const files = node.files.slice().sort((a, b) => ms(b.ts) - ms(a.ts));

  const dirRows = dirs.map(([name, child]) => {
    const path = prefix ? prefix + '/' + name : name;
    const open = dirOpen(path, selected);
    return `<div>
      <div class="tnode tdir" style="padding-left:${pad}px" data-act="toggle-dir" data-dir="${esc(path)}">
        <span class="tcaret">${open ? '▾' : '▸'}</span>
        <span class="tname">${esc(name)}</span>
        <span class="tcount">${child.count}</span>
      </div>
      ${open ? renderTree(child, path, selected, depth + 1) : ''}
    </div>`;
  }).join('');

  const fileRows = files.map((f) => {
    const on = f.notePath === selected;
    return `<a class="tnode tfile${on ? ' on' : ''}" style="padding-left:${pad + 13}px"
       href="#/memory?tab=notes&note=${encodeURIComponent(f.notePath)}">
      <span class="tname">${esc(f.name)}</span>
      <span class="twhen" title="${esc(exact(f.ts))}">${rel(f.ts, state.now)}</span>
    </a>`;
  }).join('');

  return dirRows + fileRows;
}

// resolveNotePath turns a link written inside a note into a store-relative
// path, the same way the server resolves body links when it builds the graph.
// Returns "" for anything that is not a note reference.
function resolveNotePath(basePath, target) {
  if (!target || /^[a-z][a-z0-9+.-]*:/i.test(target)) return '';
  target = target.split('#')[0].trim();
  if (!target) return '';
  if (!target.endsWith('.md')) target += '.md';
  if (target.startsWith('/')) return target.replace(/^\/+/, '');
  const parts = String(basePath || '').split('/');
  parts.pop(); // drop the filename; links resolve against the directory
  target.split('/').forEach((seg) => {
    if (seg === '' || seg === '.') return;
    if (seg === '..') parts.pop();
    else parts.push(seg);
  });
  return parts.join('/');
}

// noteIndex is what a link can point at: every note on disk, plus a title
// lookup for [[wikilinks]], which name a page rather than a path.
function noteIndex() {
  const byPath = new Set((state.memory || []).map((w) => w.notePath));
  const byTitle = {};
  const add = (p) => { if (p && p.title && p.path) byTitle[p.title.toLowerCase()] = p.path; };
  if (state.wiki) {
    (state.wiki.pages || []).forEach(add);
    Object.values(state.wiki.sources || {}).forEach(add);
  }
  return { byPath, byTitle };
}

// A deliberately small markdown renderer. Input is agent-written and untrusted,
// so everything is escaped first and only then given structure — no raw HTML
// from a note ever reaches the page.
//
// opts.basePath and opts.link make the note's own links live. A page that cites
// its sources is only half readable if following the citation means finding the
// file by hand. A link is only rendered as a link when its target actually
// exists, so following one never lands on an error.
function renderMarkdown(src, opts) {
  const { basePath = '', link = null } = opts || {};
  const idx = link ? noteIndex() : null;

  const noteHref = (path) => (link === 'notes'
    ? `#/memory?tab=notes&note=${encodeURIComponent(path)}`
    : null);

  // A resolved target becomes an anchor for the notes tree (real navigation) or
  // a click target for the wiki reader (which swaps the page in place).
  const linkTo = (path, label) => {
    const href = noteHref(path);
    return href
      ? `<a class="md-wl" href="${href}">${label}</a>`
      : `<a class="md-wl" data-act="wiki-link" data-path="${esc(path)}">${label}</a>`;
  };

  const resolve = (target) => {
    if (!idx) return '';
    const p = resolveNotePath(basePath, target);
    return p && idx.byPath.has(p) ? p : '';
  };

  return renderMarkdownBody(src, { resolve, linkTo, idx });
}

function renderMarkdownBody(src, ctx) {
  let text = String(src || '');
  let front = '';
  const fm = text.match(/^---\n([\s\S]*?)\n---\n?/);
  if (fm) {
    front = fm[1];
    text = text.slice(fm[0].length);
  }

  const blocks = [];
  // Fenced code is lifted out before anything else touches it.
  text = text.replace(/```[^\n]*\n([\s\S]*?)```/g, (_, code) =>
    ` ${blocks.push(`<pre class="md-code">${esc(code.replace(/\n$/, ''))}</pre>`) - 1} `);

  const { resolve, linkTo, idx } = ctx || {};

  // s is escaped up front so no raw note HTML reaches the page; the captured
  // link groups are therefore already escaped. Resolution matches against the
  // unescaped path/title the index holds, while the rendered href keeps the
  // single escaping an attribute needs (escaping it again corrupts the URL).
  const inline = (s) => esc(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    // [[Wikilinks]] name a page by title rather than by path.
    .replace(/\[\[([^\]|#]+)(?:[|#]([^\]]*))?\]\]/g, (m, name, alias) => {
      const label = (alias || name).trim();
      const path = idx && idx.byTitle[unesc(name).trim().toLowerCase()];
      return path ? linkTo(path, label) : `<span class="md-wl">${label}</span>`;
    })
    .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, label, target) => {
      const path = resolve ? resolve(unesc(target)) : '';
      if (path) return linkTo(path, label);
      // An off-site link stays a link; anything else is shown as plain text
      // rather than offered as a link that would go nowhere.
      if (/^https?:\/\//i.test(target)) {
        return `<a class="md-wl" href="${target}" target="_blank" rel="noopener noreferrer">${label}</a>`;
      }
      return `<span class="md-wl">${label}</span>`;
    })
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');

  const out = [];
  let list = null;
  const closeList = () => { if (list) { out.push(`</${list}>`); list = null; } };

  text.split('\n').forEach((raw) => {
    const line = raw.replace(/\s+$/, '');
    const ph = line.match(/^ (\d+) $/);
    if (ph) { closeList(); out.push(blocks[+ph[1]]); return; }
    if (!line.trim()) { closeList(); return; }

    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) { closeList(); out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`); return; }
    if (/^(---+|\*\*\*+)$/.test(line.trim())) { closeList(); out.push('<hr>'); return; }
    const q = line.match(/^>\s?(.*)$/);
    if (q) { closeList(); out.push(`<blockquote>${inline(q[1])}</blockquote>`); return; }

    const ul = line.match(/^\s*[-*+]\s+(.*)$/);
    const ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
    if (ul || ol) {
      const want = ul ? 'ul' : 'ol';
      if (list !== want) { closeList(); out.push(`<${want}>`); list = want; }
      out.push(`<li>${inline((ul || ol)[1])}</li>`);
      return;
    }
    closeList();
    out.push(`<p>${inline(line)}</p>`);
  });
  closeList();

  return (front ? `<details class="md-front"><summary>frontmatter</summary><pre>${esc(front)}</pre></details>` : '')
    + out.join('\n');
}

function viewMemoryNotes() {
  const { q } = route();
  const selected = q.note || '';
  const search = (q.q || '').toLowerCase();
  const notes = state.memory.filter((w) =>
    !search || String(w.notePath).toLowerCase().includes(search));

  const tree = buildTree(notes);
  const shown = state.memory.length;
  const total = state.memoryTotal || shown;

  let reader;
  if (!selected) {
    reader = `<div class="empty-state" style="margin:24px"><div class="icon">📄</div>
      <h2 class="display">Pick a note</h2>
      <p>Every durable note this Claw holds is on the left: the shared
      <span class="mono">workspace/memory</span> and <span class="mono">workspace/wiki</span> stores,
      and each agent's own consolidation output.</p></div>`;
  } else {
    loadNote(selected);
    const body = state.noteContent[selected];
    const note = state.memory.find((w) => w.notePath === selected);
    const owner = note && note.agent ? agentMeta(note.agent) : null;
    reader = `<div class="reader-head">
        <span class="reader-title">${esc(selected.split('/').pop())}</span>
        ${owner ? `<span class="chip" style="background:var(--surface2);color:var(--sub)">${owner.emoji} ${esc(owner.title)}</span>`
          : '<span class="chip" style="background:var(--surface2);color:var(--sub)">shared</span>'}
        ${note ? `<span class="when" title="${esc(exact(note.ts))}">${rel(note.ts, state.now)}</span>` : ''}
        <span class="grow"></span>
        <span class="mono reader-path">${esc(selected)}</span>
      </div>
      ${body === undefined
        ? '<div class="loading">Reading…</div>'
        : `<article class="md">${renderMarkdown(body, { basePath: selected, link: 'notes' })}</article>`}`;
  }

  return `<div class="page">
    <div class="page-head">
      <h1>Memory</h1>
      <span style="color:var(--sub);font-size:13px">every note this Claw holds</span>
    </div>
    <div class="card clip"><div class="tabs">${memoryTabs('notes')}</div></div>
    ${shown < total ? `<div class="watch-note">Showing the ${shown} most recently written of
      ${total} notes.</div>` : ''}
    <div class="notes-layout">
      <aside class="tree">
        <div class="tree-search">
          <input placeholder="Filter by path…" data-act="mem-search" value="${esc(q.q || '')}">
        </div>
        ${notes.length
          ? `<div class="tree-body" data-scroll-key="tree">${renderTree(tree, '', selected, 0)}</div>`
          : `<div class="gc-hint" style="padding:14px">No note path matches that filter.</div>`}
      </aside>
      <section class="reader">${reader}</section>
    </div>
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
//
// Nodes are keyed by the server's `key`, never by `id`. Index pages carry no
// frontmatter and so have no id: keying on it collapsed all four onto the empty
// string, and every edge naming one by path then failed to resolve and was
// dropped, which is what made the index pages render as orphans.
function buildSim(w) {
  const key = state.namespace + '/' + state.claw;
  const pages = w.pages || [];
  const nkey = (p) => p.key || p.id || p.path;
  const wanted = [];
  pages.forEach((p) => wanted.push(p));
  Object.keys(state.wikiExpanded).forEach((id) => {
    const parent = pages.find((p) => nkey(p) === id);
    if (!parent) return;
    (parent.sourceIds || []).forEach((sid) => {
      const src = (w.sources || {})[sid];
      if (src && !wanted.some((n) => nkey(n) === nkey(src))) wanted.push(src);
    });
  });

  const prev = sim.key === key ? sim.byId : {};
  // Seed on a ring sized to the node count and the measured canvas, so the
  // simulation starts spread out instead of untangling from a knot.
  const cx = (sim.w || 900) / 2, cy = (sim.h || 620) / 2;
  const R = Math.max(160, Math.min(cx, cy) - 60, wanted.length * 11);
  sim.nodes = wanted.map((p, i) => {
    const a = (i / Math.max(1, wanted.length)) * Math.PI * 2;
    const old = prev[nkey(p)];
    return {
      id: nkey(p), path: p.path, type: p.pageType || 'page',
      label: p.title || nkey(p),
      short: (p.title || nkey(p)).length > 26 ? (p.title || nkey(p)).slice(0, 25) + '…' : (p.title || nkey(p)),
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
    <aside class="graph-controls" data-scroll-key="graph-controls">
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
        <div class="gc-toggle" data-act="wiki-type" data-type="index" style="margin-top:8px"
             title="Index pages are generated tables of contents. They tie the graph together, but they are scaffolding rather than knowledge.">
          <span style="font-size:12.5px;color:var(--sub)">Show index pages</span>
          <span class="toggle${sim.hidden.index ? '' : ' on'}"><span class="knob"></span></span>
        </div>
        <div class="gc-toggle" data-act="graph-sources" style="margin-top:8px">
          <span style="font-size:12.5px;color:var(--sub)">Expand all sources</span>
          <span class="toggle${Object.keys(state.wikiExpanded).length ? ' on' : ''}"><span class="knob"></span></span>
        </div>
      </div>
      <div class="gc-hint">Click a node to read the page it stands for.</div>
    </aside>
    ${state.wikiPage ? renderWikiReader() : ''}
  </div>`;
}

const rel2 = (ts) => rel(ts, state.now);

// Clicking a node opens the page itself. The point of the graph is to find a
// memory worth reading; showing only its metadata and hiding the actual note
// behind a "page source" disclosure made the last step the hardest one.
function renderWikiReader() {
  const { page, content, error } = state.wikiPage;
  const t = wikiType(page.pageType);
  const claims = (page.claims || []).map((c) => `<li>
      ${esc(c.text || c.id || '')}
      ${c.status ? `<span class="chip" style="background:var(--surface2);color:var(--sub);margin-left:4px">${esc(c.status)}</span>` : ''}
      ${c.confidence ? `<span class="chip" style="background:var(--info-bg);color:var(--info)">${(c.confidence * 100).toFixed(0)}%</span>` : ''}
    </li>`).join('');

  return `<section class="wiki-reader" data-scroll-key="wiki:${esc(page.path || page.id || '')}">
    <div class="reader-head">
      <span class="chip" style="background:var(--surface2);color:${t.color};border:1px solid ${t.color}">${t.label}</span>
      <span class="reader-title">${esc(page.title || page.path)}</span>
      <span class="when">${rel2(page.lastRefreshedAt || page.modifiedAt)}</span>
      <span class="grow"></span>
      <span class="mono reader-path">${esc(page.path || '')}</span>
      <button class="link-btn" data-act="wiki-close">close</button>
    </div>
    ${error
      ? `<div class="empty">${esc(error)}</div>`
      : `${claims ? `<div class="wiki-claims"><div class="gc-label">Claims</div><ul>${claims}</ul></div>` : ''}
         <article class="md">${renderMarkdown(content, { basePath: page.path, link: 'wiki' })}</article>`}
  </section>`;
}

/* ----------------------------------------------------------------- render */

// Every render replaces the document, which resets the scroll of anything that
// scrolls. That is invisible on a static page and unusable on a live one: the
// five-second refresh threw the reader back to the top of a session, and so did
// expanding a single event. Scroll positions are captured before the swap and
// put back after, unless the route itself changed — a new page should start at
// the top.
// Anything that scrolls declares a data-scroll-key, and that key carries
// whatever identifies the thing being scrolled. Matching keys before and after
// a render means the position is kept; a key that changed — a different route,
// a different note, a different wiki page — means it is not, so a new thing to
// read still starts at the top. Both axes are restored: a maintained list of
// selectors and scrollTop alone missed the horizontal scroll inside an event
// payload, and missed the wiki reader entirely.
function captureScroll() {
  const out = {};
  document.querySelectorAll('[data-scroll-key]').forEach((el) => {
    if (el.scrollTop || el.scrollLeft) out[el.dataset.scrollKey] = [el.scrollTop, el.scrollLeft];
  });
  return out;
}

function restoreScroll(saved) {
  document.querySelectorAll('[data-scroll-key]').forEach((el) => {
    const pos = saved[el.dataset.scrollKey];
    if (!pos) return;
    el.scrollTop = pos[0];
    el.scrollLeft = pos[1];
  });
}

function render() {
  const r = route();
  const root = $('#root');
  const saved = captureScroll();
  // What the main pane is showing. Opening a different note or run is a
  // different thing to read, so it starts at the top rather than inheriting a
  // scroll position from whatever was there before.
  const mainKey = 'main:' + r.seg.join('/') + '|' + (r.q.note || '');

  if (!state.loaded) {
    root.innerHTML = renderMasthead() + `<div class="loading">Loading agent data…</div>`;
    return;
  }

  let main;
  if (state.scopeError) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🔒</div>
      <h2 class="display">Could not determine your access</h2>
      <p>${esc(state.scopeError)}</p>
      <p style="margin-top:12px"><a href="/oauth/sign_out" class="link-btn" style="font-size:14px">Sign out and re-authenticate</a></p></div></div>`;
  } else if (!state.local && state.claws.length === 0) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗝️</div>
      <h2 class="display">No Claws you can read</h2>
      <p>This console shows the agents of Claws in namespaces you have access to.
      ${state.user ? `You are signed in as <span class="mono">${esc(state.user)}</span> and no Claw` : 'No Claw'}
      is currently visible to you. Ask for access to a namespace that runs one, then reload.</p></div></div>`;
  } else if (state.clawError) {
    main = viewClawError();
  } else if (state.backendDown) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🔌</div>
      <h2 class="display">Backend unreachable</h2>
      <p>The console could not reach its API. Nothing below is fabricated — the last known data is
      withheld rather than shown as current. Retrying every 5 seconds.</p></div></div>`;
  } else if (state.meta.ok === false) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗂️</div>
      <h2 class="display">No data to show</h2>
      <p>The agent data could not be read, so this console shows nothing rather than fabricated
      figures.${state.meta.error ? ` The scan reported: <span class="mono">${esc(state.meta.error)}</span>.` : ''}</p>
      </div></div>`;
  } else if (state.agents.length === 0) {
    main = `<div class="page"><div class="empty-state"><div class="icon">🗂️</div>
      <h2 class="display">No agent activity yet</h2>
      <p>This console reads trajectory and transcript JSONL files from each agent's
      <span class="mono">sessions/</span> directory inside the Claw. Once an agent writes its first
      session, it appears here automatically.</p></div></div>`;
  } else if (r.isReplay) {
    main = viewSession(r.seg[1], r.seg[3], r.runId);
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
    `<div class="body">${renderSidebar(r)}
      <main class="main" id="main" data-scroll-key="${esc(mainKey)}">${main}</main></div>`;
  makeActionable(root);
  restoreScroll(saved);

  if (r.isReplay && state.follow && isRunningSession(state.session)) scrollToBottom();
  // Arriving on a run deep in a long session should land on that run, not at
  // the top of a file it shares with forty others.
  if (r.isReplay && r.runId && !state.runScrolled) {
    const el = document.getElementById('run-' + r.runId);
    if (el) {
      el.scrollIntoView({ block: 'center' });
      state.runScrolled = true;
    }
  }
  if (r.isWiki) attachGraph();
}

// Why this Claw could not be read. Each case is a different problem with a
// different fix, so each says which it is and what would change it.
function viewClawError() {
  const { status, detail } = state.clawError;
  const where = state.claw ? `<span class="mono">${esc(state.namespace)}/${esc(state.claw)}</span>` : 'this Claw';
  let icon = '⚠️';
  let title = 'This Claw could not be read';
  let body = `<p>The console asked the Kubernetes API about ${where} and was refused.</p>`;

  if (status === 403) {
    icon = '🔒';
    title = 'You do not have access to this Claw';
    body = `<p>${state.user ? `<span class="mono">${esc(state.user)}</span> can` : 'You can'} see ${where}
      in the picker, but reading its agents needs <span class="mono">create pods/exec</span> in that
      namespace, and the API server refused it. The console holds no access of its own — every read is
      made as you — so this is a permission to be granted, not a bug to be worked around.</p>`;
  } else if (status === 404) {
    icon = '💤';
    title = 'No running pod for this Claw';
    body = `<p>${where} exists, but has no running pod to read from. A Claw that is scaled to zero,
      still starting, or failing to schedule looks like this. Its history becomes readable again as
      soon as a pod is.</p>`;
  } else if (status === 401) {
    icon = '🔑';
    title = 'Not signed in';
    body = `<p>The console did not receive an authenticated identity. Reloading the page will send you
      back through OpenShift login.</p>`;
  }

  return `<div class="page"><div class="empty-state">
    <div class="icon">${icon}</div>
    <h2 class="display">${title}</h2>
    ${body}
    <p class="mono" style="font-size:12px;color:var(--sub)">${esc(detail || 'HTTP ' + status)}</p>
    <p style="font-size:13px">Pick another Claw from the menu above, or retry — the console keeps polling.</p>
  </div></div>`;
}

function scrollToBottom() {
  const el = $('#main');
  if (el) el.scrollTop = el.scrollHeight;
}

/* ------------------------------------------------------------- interaction */

// Native controls (a, button, select, sliders, text inputs) are focusable and
// key-activated already; only the delegated data-act elements built on div/tr/g
// need help.
const NATIVE_FOCUSABLE = /^(A|BUTTON|INPUT|SELECT|TEXTAREA)$/;
const NATIVE_ACTS = new Set(['graph-search', 'gf-center', 'gf-repel', 'gf-link', 'gf-dist',
  'filter-q', 'mem-search', 'scope', 'filter-agent', 'filter-range', 'mem-agent']);

// Give every click target a keyboard equivalent: make it focusable and let
// Enter/Space reach the same dispatcher, so expanding a run, opening an event,
// or walking the note tree does not require a mouse.
function makeActionable(root) {
  root.querySelectorAll('[data-act]').forEach((el) => {
    if (NATIVE_FOCUSABLE.test(el.tagName) || el.hasAttribute('tabindex') || NATIVE_ACTS.has(el.dataset.act)) return;
    el.setAttribute('tabindex', '0');
    el.setAttribute('role', 'button');
  });
}

function onKeydown(e) {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const el = e.target.closest('[data-act]');
  if (!el || NATIVE_FOCUSABLE.test(el.tagName) || NATIVE_ACTS.has(el.dataset.act)) return;
  e.preventDefault();
  onClick(e);
}

function onClick(e) {
  const el = e.target.closest('[data-act]');
  if (!el) {
    // A click outside dismisses whatever popover is open.
    if ((state.userMenuOpen || state.integrityOpen) && !e.target.closest('.usermenu-pop, .popover')) {
      state.userMenuOpen = false;
      state.integrityOpen = false;
      render();
    }
    return;
  }
  const act = el.dataset.act;

  switch (act) {
    case 'integrity':
      state.integrityOpen = !state.integrityOpen;
      state.userMenuOpen = false;
      return render();
    case 'user-menu':
      state.userMenuOpen = !state.userMenuOpen;
      state.integrityOpen = false;
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
    case 'load-all-runs':
      // Opted into explicitly, and kept for this Claw: polling for hundreds of
      // runs every five seconds is not something to turn on by default.
      state.runsWanted = Math.max(state.runsTotal, 500);
      render();
      return tick();
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
      location.hash = runHref(el.dataset.agent, el.dataset.session, el.dataset.run);
      return;
    case 'toggle-run': {
      const id = el.dataset.run;
      state.expandedRuns[id] = !state.expandedRuns[id];
      return render();
    }
    case 'toggle-ev': {
      const k = el.dataset.key;
      state.expandedEv[k] = !state.expandedEv[k];
      return render();
    }
    case 'toggle-dir': {
      const d = el.dataset.dir;
      const { q } = route();
      state.expandedDirs[d] = !dirOpen(d, q.note || '');
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
      if (!on) (state.wiki.pages || []).forEach((pg) => {
        state.wikiExpanded[pg.key || pg.id || pg.path] = true;
      });
      sim.key = '';
      return render();
    }
    case 'wiki-open':
    case 'wiki-link':
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
      state.runsWanted = 0; state.runsTotal = 0; state.clawError = null;
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
    case 'mem-search': return setQ({ q: el.value });
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
    state.runScrolled = false;
    if (state.sessionKey !== key) {
      state.session = null;
      state.sessionKey = '';
      state.follow = true;
      state.expandedEv = {};
      state.expandedRuns = {};
      render();
      await loadSession(r.seg[1], r.seg[3]);
    } else {
      // Moving between runs of the same session needs no refetch; just let the
      // newly named run take over as the one that is open.
      state.expandedRuns = {};
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

// Each refresh is five impersonated exec reads that routinely outrun the 5s
// interval on a busy Claw. A self-scheduling loop that waits for tick() to
// finish before arming the next one stops requests stacking up and landing out
// of order.
function scheduleTick(delay) {
  setTimeout(async () => {
    if (!document.hidden) {
      try { await tick(); } catch { /* next tick retries */ }
    }
    scheduleTick(5000);
  }, delay);
}

function start() {
  try {
    const t = localStorage.getItem('ac-theme');
    if (t === 'dark' || t === 'light') state.theme = t;
  } catch { /* private mode */ }
  document.documentElement.setAttribute('data-ac-theme', state.theme);

  document.addEventListener('click', onClick);
  document.addEventListener('keydown', onKeydown);
  document.addEventListener('change', onChange);
  document.addEventListener('input', (e) => {
    // Debounce free-text filters so each keystroke doesn't rewrite the hash.
    const el = e.target.closest('[data-act]');
    if (el && ['graph-search', 'gf-center', 'gf-repel', 'gf-link', 'gf-dist'].includes(el.dataset.act)) {
      onChange(e);
      return;
    }
    if (!el || !['filter-q', 'mem-search'].includes(el.dataset.act)) return;
    clearTimeout(el._t);
    el._t = setTimeout(() => onChange(e), 250);
  });
  document.addEventListener('scroll', onScroll, true);
  window.addEventListener('hashchange', onHashChange);

  onHashChange();
  // The loop owns the first refresh too; firing tick() separately would let the
  // scheduled one overlap it when a cold read runs past the interval.
  scheduleTick(0);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', start);
} else {
  start();
}
