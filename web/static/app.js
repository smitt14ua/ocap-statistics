(() => {
'use strict';

const { t } = window.i18n;

const el = (tag, props = {}, ...children) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') node.className = v;
    else if (k === 'style') node.style.cssText = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};
const $ = (id) => document.getElementById(id);

const fmt = (v, digits = 2) => Number(v || 0).toFixed(digits);
// Medical points are whole unless the weights are fractional.
const fmtPoints = (v) => Number.isInteger(v || 0) ? String(v || 0) : fmt(v);
const fmtDuration = (sec) => t('duration', { m: Math.floor(sec / 60), s: Math.floor(sec % 60) });
// Elapsed time in the unit that keeps the number short: 850 µs, 4.2 ms, 142 ms, 3.25 s, 1 min 5 s.
// Browsers coarsen performance.now() to ~0.1 ms, so nothing finer than microseconds is meaningful.
const fmtElapsed = (ms) => {
  const num = (v, digits) => v.toLocaleString(window.i18n.lang, { maximumFractionDigits: digits });
  if (ms < 1) return t('time.us', { n: num(ms * 1000, 0) });
  if (ms < 1000) return t('time.ms', { n: num(ms, ms < 10 ? 1 : 0) });
  if (ms < 60000) return t('time.s', { n: num(ms / 1000, ms < 10000 ? 2 : 1) });
  return fmtDuration(ms / 1000);
};
const isoDate =(d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 10);
// fmtClock formats a time in the mission: 5:09 or 1:05:09.
const fmtClock = (sec) => {
  sec = Math.max(0, Math.floor(sec));
  const pad = (n) => String(n).padStart(2, '0');
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = sec % 60;
  return h ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
};

// The usual Arma colours of the sides. They only fill a dot next to the side name.
const SIDE_COLORS = { WEST: '#3b82f6', EAST: '#ef4444', GUER: '#22c55e', CIV: '#a855f7' };
const sideDot = (side) => el('span', { class: 'swatch', style: `background:${SIDE_COLORS[side] || '#888'}`, 'aria-hidden': 'true' });

// svg builds an SVG element as el() builds HTML ones, but flattens nested child arrays fully.
const svg = (tag, attrs = {}, ...children) => {
  const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs)) if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v);
  for (const c of children.flat(Infinity)) {
    if (c !== null && c !== undefined && c !== false) node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};

// fmtMonth shows a YYYY-MM month in the page language: "Oct 26", or "October 2026" when long.
const fmtMonth = (m, long = false) => new Date(Number(m.slice(0, 4)), Number(m.slice(5, 7)) - 1, 1)
  .toLocaleDateString(window.i18n.lang, long ? { month: 'long', year: 'numeric' } : { month: 'short', year: '2-digit' });
const fmtTick = (v) => Number.isInteger(v) ? String(v) : fmt(v, 1);

// niceMax rounds the top of an axis up to 1, 2 or 5 × a power of ten.
const niceMax = (v) => {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return [1, 2, 5, 10].map(m => m * p).find(m => m >= v);
};

// trendChart draws one monthly series as columns or a line, with a tooltip per month on
// hover. value returns null for a month without data, which leaves a gap in a line.
function trendChart(points, { title, value, kind, format }) {
  const W = 320, H = 132, L = 34, R = 8, T = 10, B = 22;
  const vals = points.map(value);
  const top = niceMax(Math.max(0, ...vals.filter(v => v !== null)));
  const band = (W - L - R) / points.length;
  const x = (i) => L + band * (i + 0.5);
  const y = (v) => T + (H - T - B) * (1 - v / top);
  const grid = [0, top / 2, top].map(v => [
    svg('line', { class: 'grid', x1: L, x2: W - R, y1: y(v), y2: y(v) }),
    svg('text', { class: 'tick', x: L - 6, y: y(v) + 3.5, 'text-anchor': 'end' }, fmtTick(v))]);
  // Month labels: about six, always the last one, never two crowded together.
  const step = Math.ceil(points.length / 6);
  const labelled = points.map((_, i) => i % step === 0);
  const last = points.length - 1;
  if (!labelled[last]) {
    if (last % step < step * 0.6) labelled[last - last % step] = false;
    labelled[last] = true;
  }
  const months = points.map((p, i) => labelled[i] && svg('text', { class: 'tick', x: x(i), y: H - 6, 'text-anchor': 'middle' }, fmtMonth(p.month)));

  let marks;
  if (kind === 'bar') {
    const w = Math.min(24, band * 0.6);
    // Columns grow from the baseline with a 4px rounded top.
    marks = vals.map((v, i) => {
      if (!v) return null;
      const x0 = x(i) - w / 2, x1 = x(i) + w / 2, y0 = y(v), yb = y(0), r = Math.min(4, yb - y0, w / 2);
      return svg('path', { class: 'bar', d: `M${x0} ${yb}V${y0 + r}Q${x0} ${y0} ${x0 + r} ${y0}H${x1 - r}Q${x1} ${y0} ${x1} ${y0 + r}V${yb}Z` });
    });
  } else {
    let d = '', pen = false;
    vals.forEach((v, i) => {
      if (v === null) { pen = false; return; }
      d += `${pen ? 'L' : 'M'}${x(i)} ${y(v)}`;
      pen = true;
    });
    // Markers on every month while they fit, otherwise only on the latest one.
    const dots = vals.map((v, i) => v !== null && (points.length <= 24 || i === last) && svg('circle', { class: 'dot', cx: x(i), cy: y(v), r: 4 }));
    marks = [svg('path', { class: 'line', d }), dots];
  }

  const tip = el('div', { class: 'trend-tip', hidden: true });
  const cursor = svg('line', { class: 'cursor', y1: T, y2: H - B, visibility: 'hidden' });
  const show = (i) => {
    const p = points[i], v = vals[i];
    tip.textContent = [`${fmtMonth(p.month, true)}: ${v === null ? '—' : format(v)}`, kind === 'bar' ? null : t('trend.missionsN', { n: p.missions })].filter(Boolean).join(' · ');
    tip.style.left = `${x(i) / W * 100}%`;
    tip.hidden = false;
    cursor.setAttribute('x1', x(i));
    cursor.setAttribute('x2', x(i));
    cursor.setAttribute('visibility', 'visible');
  };
  const hide = () => { tip.hidden = true; cursor.setAttribute('visibility', 'hidden'); };
  // Each month's whole column is the hover target, wider than its mark.
  const hits = points.map((_, i) => {
    const r = svg('rect', { class: 'hit', x: L + band * i, y: T, width: band, height: H - T - B });
    r.addEventListener('pointerenter', () => show(i));
    return r;
  });
  const chart = svg('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': title }, grid, months, cursor, marks, hits);
  chart.addEventListener('pointerleave', hide);
  return el('figure', { class: 'trend' }, el('figcaption', {}, title), el('div', { class: 'trend-plot' }, chart, tip));
}

// A cell sorts by its data-sort value when it has one, otherwise by its text; empty cells and
// dashes have no value and always go last.
const sortKey = (td) => {
  const v = td.dataset.sort ?? td.textContent.trim();
  return v === '' || v === '—' || v === '-' ? null : v;
};

// sortable turns the headers of a table into sort buttons (columns with data-nosort stay
// plain). Numeric columns sort descending first, text ascending. initial = [column, ascending]
// marks the order the rows already come in. It returns a function that sorts the rows again,
// for tables whose rows are replaced.
function sortable(table, initial = null) {
  const head = table.tHead.rows[0];
  const body = table.tBodies[0];
  let state = initial;
  const keys = (col) => [...body.rows].map(r => (r.cells.length === head.cells.length ? sortKey(r.cells[col]) : null));
  const numeric = (ks) => ks.every(k => k === null || !Number.isNaN(Number(k)));
  const mark = () => [...head.cells].forEach((th, i) => {
    if (state && state[0] === i) th.setAttribute('aria-sort', state[1] ? 'ascending' : 'descending');
    else th.removeAttribute('aria-sort');
  });
  const apply = () => {
    const rows = [...body.rows];
    // A single "no data" row spans all columns and is not sorted.
    if (!state || rows.some(r => r.cells.length !== head.cells.length)) return;
    const [col, asc] = state;
    const ks = keys(col);
    const num = numeric(ks);
    const order = rows.map((_, i) => i).sort((a, b) => {
      const x = ks[a], y = ks[b];
      if (x === null || y === null) return (x === null) - (y === null) || a - b;
      const c = num ? Number(x) - Number(y) : x.localeCompare(y, undefined, { sensitivity: 'base', numeric: true });
      return (asc ? c : -c) || a - b;
    });
    body.append(...order.map(i => rows[i]));
  };
  [...head.cells].forEach((th, col) => {
    if ('nosort' in th.dataset) return;
    const button = el('button', { type: 'button', class: 'sort' }, ...th.childNodes);
    th.replaceChildren(button);
    button.addEventListener('click', () => {
      state = state && state[0] === col ? [col, !state[1]] : [col, !numeric(keys(col))];
      mark();
      apply();
    });
  });
  mark();
  return apply;
}

// html2canvas is only needed for screenshots, so it is loaded on demand and pinned by
// Subresource Integrity.
const LIBS = {
  html2canvas: {
    src: 'https://cdnjs.cloudflare.com/ajax/libs/html2canvas/1.4.1/html2canvas.min.js',
    integrity: 'sha384-ZZ1pncU3bQe8y31yfZdMFdSpttDoPmOZg2wguVK9almUodir1PghgT0eY7Mrty8H',
  },
};

function loadLib(name) {
  if (window[name]) return Promise.resolve(window[name]);
  const lib = LIBS[name];
  lib.promise ??= new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = lib.src;
    script.integrity = lib.integrity;
    script.crossOrigin = 'anonymous';
    script.onload = () => resolve(window[name]);
    script.onerror = () => {
      lib.promise = null;
      script.remove();
      reject(new Error(t('n.libMissing', { lib: name })));
    };
    document.head.append(script);
  });
  return lib.promise;
}

// busyTask returns the sync task that is changing the statistics right now, or null. A routine
// sync that finds no new recordings does not count, so the notice does not flash every interval.
function busyTask(sync) {
  if (sync.running && (sync.task !== 'sync' || sync.progress.total > 0)) return sync.task;
  return ['resync', 'recompute'].find(task => sync.queued.includes(task)) || null;
}

async function apiGet(path) {
  const res = await fetch(`api/v1/${path}`);
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  return res.json();
}

class StatsApp {
  constructor() {
    this.cfg = { title: 'OCAP Statistics', rotation: { sides: [] } };
    this.data = null;
    this.rotation = null;
    this.viewMode = 'players';
    this.sort = { field: 'kills', dir: 'desc' };
    this.sortedView = null;
    this.allSquads = [];
    this.selected = new Set();
    this.singles = true;
    this.table = $('statsTable');

    // The player signed in through Steam (api/auth/me) and the names they played under.
    this.me = null;
    this.mine = new Set();
    this.accountReady = Promise.resolve();

    this.lastFocus = null;
    // The view shown in the detail modal and the views it was opened from (for Back).
    this.detailView = null;
    this.detailStack = [];
  }

  async init() {
    window.i18n.bindSelect($('lang'));
    window.i18n.apply();
    this.bind();
    try {
      this.cfg = { ...this.cfg, ...(await apiGet('config')) };
    } catch { /* defaults */ }
    if (this.cfg.title) { document.title = this.cfg.title; $('title').textContent = this.cfg.title; }
    if (this.cfg.logoUrl) $('logoSlot').replaceWith(el('img', { src: this.cfg.logoUrl, alt: '', class: 'logo' }));

    const params = new URLSearchParams(location.search);
    // Without a configured rotation the "current rotation" period does not exist.
    if (!this.rotationSince()) $('periodRotation').remove();
    if (params.get('from') || params.get('to')) {
      $('from').value = params.get('from') || '';
      $('to').value = params.get('to') || '';
      $('period').value = 'custom';
    } else {
      this.applyPeriod(params.get('period') || this.defaultPeriod());
    }
    this.setView(params.get('view') === 'squads' ? 'squads' : 'players', false);
    const mission = params.get('mission');
    this.accountReady = this.loadAccount();
    await this.load();
    const login = { failed: 'auth.failed', claims: 'auth.claims', nosteam: 'auth.noSteam' }[params.get('login')];
    if (login) this.toast(t(login === 'auth.noSteam' ? 'n.warning' : 'n.error'), t(login, { name: this.cfg.loginName }), true);
    if (/^\d+$/.test(mission || '')) this.openMission(mission);
    else if (params.get('player')) this.openPlayer(params.get('player'));
  }

  bind() {
    $('load').addEventListener('click', () => this.load());
    $('period').addEventListener('change', (e) => {
      if (e.target.value === 'custom') return;
      this.applyPeriod(e.target.value);
      this.load();
    });
    for (const id of ['from', 'to']) $(id).addEventListener('change', () => { $('period').value = 'custom'; });
    let debounce;
    $('search').addEventListener('input', () => { clearTimeout(debounce); debounce = setTimeout(() => this.render(), 200); });
    this.table.querySelector('thead').addEventListener('click', (e) => {
      const th = e.target.closest('th[data-sort]');
      if (!th || !e.target.closest('button')) return;
      const field = th.dataset.sort;
      if (this.viewMode === 'squads' && field === 'squad') return;
      if (field === this.sort.field) this.sort.dir = this.sort.dir === 'asc' ? 'desc' : 'asc';
      else this.sort = { field, dir: ['name', 'squad'].includes(field) ? 'asc' : 'desc' };
      this.render();
    });
    $('viewPlayers').addEventListener('click', () => this.setView('players'));
    $('viewSquads').addEventListener('click', () => this.setView('squads'));
    $('exportExcel').addEventListener('click', () => this.exportExcel());
    $('copyTop10').addEventListener('click', () => this.copyTop10());
    $('copyRotation').addEventListener('click', () => this.copyRotation());
    $('showRotation').addEventListener('click', () => this.openRotation());
    $('showMissions').addEventListener('click', () => this.openMissions());
    $('showMatrix').addEventListener('click', () => this.openMatrix());
    $('detailBack').addEventListener('click', () => this.backDetail());

    document.querySelectorAll('.modal').forEach(m => m.addEventListener('click', (e) => {
      if (e.target === m || e.target.closest('[data-close]')) this.closeModal(m);
    }));
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape') return;
      const open = [...document.querySelectorAll('.modal.open')].pop();
      if (open) this.closeModal(open);
    });
    document.addEventListener('langchange', () => {
      this.renderHeaders();
      this.renderAccount();
      if (this.data) { this.buildFilter(); this.render(); this.renderAchievements(); this.renderMe(); }
      this.refreshStatus();
      for (const m of document.querySelectorAll('.modal.open')) this.closeModal(m);
    });
  }

  // rotationSince is the start of the current rotation from the settings, if any.
  rotationSince() {
    return (this.cfg.rotation && this.cfg.rotation.since) || '';
  }

  // defaultPeriod is the current rotation when one is configured, otherwise this month.
  defaultPeriod() {
    return this.rotationSince() ? 'rotation' : 'month';
  }

  applyPeriod(period) {
    const now = new Date();
    const y = now.getFullYear(), m = now.getMonth();
    const ranges = {
      month: [new Date(y, m, 1), new Date(y, m + 1, 0)],
      prevMonth: [new Date(y, m - 1, 1), new Date(y, m, 0)],
      year: [new Date(y, 0, 1), new Date(y, 11, 31)],
    };
    if (period === 'rotation' && this.rotationSince()) {
      $('period').value = 'rotation';
      $('from').value = this.rotationSince();
      $('to').value = '';
      return;
    }
    if (!ranges[period] && period !== 'all') period = 'month';
    const r = ranges[period];
    $('period').value = period;
    $('from').value = r ? isoDate(r[0]) : '';
    $('to').value = r ? isoDate(r[1]) : '';
  }

  query() {
    const q = new URLSearchParams();
    if ($('from').value) q.set('from', $('from').value);
    if ($('to').value) q.set('to', $('to').value);
    return q.toString();
  }

  periodText() {
    const from = $('from').value, to = $('to').value;
    return !from && !to ? t('period.all') : `${from || '…'} — ${to || '…'}`;
  }

  updateURL() {
    const url = new URL(location.href);
    url.search = '';
    const period = $('period').value;
    if (period === 'custom') {
      if ($('from').value) url.searchParams.set('from', $('from').value);
      if ($('to').value) url.searchParams.set('to', $('to').value);
    } else if (period !== this.defaultPeriod()) {
      url.searchParams.set('period', period);
    }
    if (this.viewMode === 'squads') url.searchParams.set('view', 'squads');
    // An open mission or player is part of the address, so the link can be shared.
    for (const [k, v] of Object.entries((this.detailView && this.detailView.link) || {})) url.searchParams.set(k, v);
    history.replaceState(null, '', url);
  }

  // shareURL is a link to what the detail view shows, for the period on screen and with the
  // page language, which the server uses for the link preview in chats.
  shareURL() {
    const url = new URL(location.href);
    url.search = '';
    for (const id of ['from', 'to']) if ($(id).value) url.searchParams.set(id, $(id).value);
    for (const [k, v] of Object.entries(this.detailView.link || {})) url.searchParams.set(k, v);
    url.searchParams.set('lang', window.i18n.lang);
    return url.href;
  }

  async copyLink(button) {
    try {
      await navigator.clipboard.writeText(this.shareURL());
      this.toast(t('n.success'), t('n.linkCopied'));
    } catch {
      this.toast(t('n.error'), t('n.copyFailed'), true);
    }
    button.focus();
  }

  // shareBar offers a link to the view and a picture of card() for chats.
  shareBar(card) {
    const image = el('button', { type: 'button', class: 'small' }, t('btn.copyCard'));
    image.addEventListener('click', () => this.capture(card(), image, 'n.cardCopied'));
    return el('div', { class: 'share-bar' },
      el('button', { type: 'button', class: 'small', onclick: (e) => this.copyLink(e.currentTarget) }, t('btn.copyLink')), image);
  }

  async load() {
    const from = $('from').value, to = $('to').value;
    if (from && to && from > to) {
      this.toast(t('n.error'), t('n.dateOrder'), true);
      return;
    }
    for (const id of ['load', 'exportExcel', 'period']) $(id).disabled = true;
    $('spinner').classList.remove('hidden');
    const started = performance.now();
    try {
      const q = this.query();
      // The rows of the signed-in player are highlighted, so the account comes first.
      const [stats, rot] = await Promise.all([apiGet(`stats?${q}`), apiGet(`rotation?${q}`), this.accountReady]);
      this.data = stats;
      this.rotation = rot.rotation;
      this.range = this.periodText();
      this.loadTime = performance.now() - started;
      this.updateURL();
      this.buildFilter();
      this.render();
      this.renderAchievements();
      this.renderMe();
      const hasRotation = this.rotation.sides && this.rotation.sides.length > 0;
      $('showRotation').classList.toggle('hidden', !hasRotation);
      $('copyRotation').classList.toggle('hidden', !hasRotation);
      $('copyTop10').classList.remove('hidden');
      if (stats.totals.missions === 0) this.toast(t('n.warning'), t('n.noMissions'), true);
    } catch (e) {
      console.error(e);
      this.toast(t('n.error'), t('n.loadFailed', { e: e.message }), true);
    } finally {
      for (const id of ['load', 'exportExcel', 'period']) $(id).disabled = false;
      $('spinner').classList.add('hidden');
      this.refreshStatus();
    }
  }

  async refreshStatus() {
    const parts = [];
    if (this.data) {
      const tot = this.data.totals;
      parts.push(t('status.missions', { n: tot.missions }), t('status.players', { n: tot.players }),
        t('status.squads', { n: tot.squads }));
      // Technical detail: shown in the footer, not among the figures in the header.
      $('loadTime').textContent = t('status.time', { t: fmtElapsed(this.loadTime) });
      $('loadTime').hidden = false;
    }
    let st = null;
    try {
      st = await apiGet('status');
      // When new data arrived, not when the last sync that may have found nothing finished.
      if (st.missions.lastFetched) parts.push(t('status.updated', { when: window.i18n.relTime(st.missions.lastFetched) }));
      if (st.missions.pending > 0) parts.push(t('status.queue', { n: st.missions.pending }));
      if (st.sync.lastError) parts.push(t('status.syncError', { e: st.sync.lastError }));
    } catch { /* status is optional */ }
    $('status').textContent = parts.length ? parts.join(' | ') : t('status.loading');

    const task = st && busyTask(st.sync);
    const wasBusy = this.syncTask;
    this.syncTask = task;
    this.renderSyncNotice(st);
    // Poll often while statistics are changing, and reload them once the work is done.
    clearTimeout(this.statusTimer);
    this.statusTimer = setTimeout(() => this.refreshStatus(), task ? 5000 : 60000);
    if (wasBusy && !task && st) this.load();
  }

  // renderSyncNotice warns that the figures may be incomplete while a sync, recompute or resync runs.
  renderSyncNotice(st) {
    const task = this.syncTask;
    $('syncNotice').hidden = !task;
    if (!task) return;
    $('syncNoticeTitle').textContent = t(`notice.${task}`);
    const p = st.sync.running && st.sync.task === task ? st.sync.progress : null;
    $('syncNoticeProgress').textContent = p && p.total > 0 ? t('notice.progress', { done: p.done, total: p.total }) : '';
  }

  setView(mode, update = true) {
    this.viewMode = mode;
    $('viewPlayers').classList.toggle('active', mode === 'players');
    $('viewSquads').classList.toggle('active', mode === 'squads');
    // Each table opens with the default sort configured in the admin panel.
    if (mode !== this.sortedView) {
      this.sort = this.defaultSort(mode);
      this.sortedView = mode;
    }
    this.renderHeaders();
    if (update) { this.updateURL(); this.render(); }
  }

  defaultSort(mode) {
    const squads = mode === 'squads';
    const s = this.cfg.sort && this.cfg.sort[mode];
    const valid = s && s.field && (!s.field.startsWith('custom.') || this.columns(squads).some(c => `custom.${c.id}` === s.field))
      && !(squads && s.field === 'squad');
    return valid ? { field: s.field, dir: s.order === 'asc' ? 'asc' : 'desc' } : { field: 'kills', dir: 'desc' };
  }

  // columns returns the custom columns shown in the players or squads view.
  columns(squads) {
    return (this.cfg.columns || []).filter(c => c.target === 'both' || c.target === (squads ? 'squads' : 'players'));
  }

  customValue(x, c) {
    const v = x.custom ? x.custom[c.id] : null;
    return v === null || v === undefined ? '—' : Number(v).toFixed(c.decimals);
  }

  renderHeaders() {
    const squads = this.viewMode === 'squads';
    // Custom columns follow K/D; the summary row gets one empty cell spanning them.
    this.table.querySelectorAll('.custom-col').forEach(n => n.remove());
    const cols = this.columns(squads);
    $('kdHeader').after(...cols.map(c => el('th', { scope: 'col', class: 'custom-col', 'data-sort': `custom.${c.id}` },
      el('button', { type: 'button', class: 'sort', title: c.formula }, c.name))));
    if (cols.length) $('summaryKD').after(el('td', { class: 'custom-col', colspan: cols.length }));
    $('nameHeader').textContent = t(squads ? 'col.squad' : 'col.name');
    $('squadHeader').textContent = t(squads ? 'col.players' : 'col.squad');
    // Squads count missions per player and players per mission; the second column is theirs only.
    $('attendanceHeader').textContent = t(squads ? 'col.avgAttendance' : 'col.attendance');
    $('attendanceHeader').title = t(squads ? 'col.avgAttendance.title' : 'col.attendance.title');
    $('avgPlayersHeader').hidden = !squads;
    $('summaryMid').colSpan = squads ? 6 : 5;
    const ph = t(squads ? 'filters.searchSquads' : 'filters.searchPlayers');
    $('search').placeholder = ph;
    $('search').setAttribute('aria-label', ph);
  }

  // --- squad filter ------------------------------------------------------------

  buildFilter() {
    const box = $('squadFilter');
    const prev = this.allSquads.length ? new Set(this.selected) : null;
    const hadAll = prev && prev.size === this.allSquads.length;
    this.allSquads = this.data.squads.filter(s => s.name).map(s => s.name)
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
    this.selected = new Set(this.allSquads.filter(s => !prev || hadAll || prev.has(s)));

    const all = el('input', { type: 'checkbox' });
    const solo = el('input', { type: 'checkbox' });
    solo.checked = this.singles;
    const boxes = [];
    const syncAll = () => {
      all.checked = this.selected.size === this.allSquads.length;
      all.indeterminate = this.selected.size > 0 && !all.checked;
    };
    box.replaceChildren(
      el('p', {}, t('filters.general')),
      el('label', {}, all, t('filters.allSquads')),
      el('label', {}, solo, t('noSquad')),
      // replaceChildren, unlike el(), would write a null as the text "null".
      ...(this.allSquads.length ? [el('p', {}, t('filters.squads'))] : []),
      ...this.allSquads.map(squad => {
        const cb = el('input', { type: 'checkbox' });
        cb.checked = this.selected.has(squad);
        cb.addEventListener('change', () => {
          if (cb.checked) this.selected.add(squad); else this.selected.delete(squad);
          syncAll();
          this.render();
        });
        boxes.push(cb);
        return el('label', {}, cb, this.sideSwatch(squad), squad);
      }));
    syncAll();
    all.addEventListener('change', () => {
      this.selected = new Set(all.checked ? this.allSquads : []);
      boxes.forEach(b => { b.checked = all.checked; });
      syncAll();
      this.render();
    });
    solo.addEventListener('change', () => { this.singles = solo.checked; this.render(); });
  }

  filtered() {
    return !this.singles || this.selected.size !== this.allSquads.length;
  }

  items() {
    if (!this.data) return [];
    const term = $('search').value.trim().toLowerCase();
    const squads = this.viewMode === 'squads';
    const list = squads
      ? this.data.squads.filter(s => (!term || (s.name || t('noSquad')).toLowerCase().includes(term)) &&
        (s.name ? this.selected.has(s.name) : this.singles))
      : this.data.players.filter(p => (!term || p.name.toLowerCase().includes(term)) &&
        (p.squad ? this.selected.has(p.squad) : this.singles));
    return this.sortItems(list, squads, this.sort);
  }

  sortItems(list, squads, { field, dir }) {
    const value = (x) => {
      if (field === 'name') return (x.name || '').toLowerCase();
      if (field === 'squad') return (x.squadRaw || '').toLowerCase();
      if (field === 'attendance' && squads) return x.avgAttendance;
      if (field.startsWith('custom.')) {
        const v = x.custom ? x.custom[field.slice(7)] : null;
        return v === null || v === undefined ? -Infinity : v;
      }
      return x[field] ?? 0;
    };
    return list.sort((a, b) => {
      const av = value(a), bv = value(b);
      const c = typeof av === 'string' ? av.localeCompare(bv) : av - bv;
      return (dir === 'asc' ? c : -c) || (a.name || '').localeCompare(b.name || '');
    });
  }

  // sideOf returns the rotation side a squad belongs to: the first side with a tag the squad
  // name contains (case-insensitive), the same matching as the rotation report.
  sideOf(squad) {
    if (!squad) return null;
    const u = squad.toUpperCase();
    return ((this.cfg.rotation && this.cfg.rotation.sides) || [])
      .find(s => (s.squads || []).some(tag => tag && u.includes(tag.toUpperCase()))) || null;
  }

  // sideSwatch marks a squad with its rotation side colour. The colour is user-defined, so it
  // only fills the dot; the side name is its accessible name and tooltip.
  sideSwatch(squad) {
    const side = this.sideOf(squad);
    return side && el('span', { class: 'swatch', style: `background:${side.color || '#888'}`, role: 'img', 'aria-label': side.name, title: side.name });
  }

  row(x, squads) {
    return [
      squads ? (x.name || t('noSquad')) : x.name,
      squads ? x.players : (x.squadRaw || '-'),
      x.kills, x.deaths, x.teamKills,
      ...(squads ? [fmt(this.attendanceRate(x)) + '%', fmt(x.avgPlayers)] : [x.attendance]),
      fmt(x.accuracy) + '%', x.vehiclesDestroyed, x.killAssists, this.medical(x), fmt(x.kd),
      ...this.columns(squads).map(c => this.customValue(x, c)),
    ];
  }

  // attendanceRate is the share of the period's missions an average player of the squad played.
  attendanceRate(s) {
    const missions = this.data.totals.missions;
    return missions ? Math.round(s.avgAttendance / missions * 10000) / 100 : 0;
  }

  // medical shows medical points with the treatments and revives behind them as a tooltip.
  medical(x) {
    return el('span', { title: t('med.cell', { t: x.treatments, r: x.revives }) }, fmtPoints(x.medicalPoints));
  }

  render() {
    this.table.querySelectorAll('th[data-sort]').forEach(th => {
      if (th.dataset.sort === this.sort.field) th.setAttribute('aria-sort', this.sort.dir === 'asc' ? 'ascending' : 'descending');
      else th.removeAttribute('aria-sort');
    });
    const squads = this.viewMode === 'squads';
    const items = this.items();
    const mySquads = this.mySquads();
    $('totalKills').textContent = items.reduce((s, x) => s + x.kills, 0);
    $('totalDeaths').textContent = items.reduce((s, x) => s + x.deaths, 0);
    $('totalTeamKills').textContent = items.reduce((s, x) => s + x.teamKills, 0);
    // K/D of the visible rows together: their kills over their deaths.
    const deaths = items.reduce((s, x) => s + x.deaths, 0);
    $('summaryKD').textContent = items.length ? fmt(items.reduce((s, x) => s + x.kills, 0) / Math.max(deaths, 1)) : '';

    const rows = items.map((x, i) => {
      const open = () => squads ? this.openSquad(x.name) : this.openPlayer(x.name);
      const [name, second, ...rest] = this.row(x, squads);
      const side = this.sideSwatch(squads ? x.name : x.squad);
      const mine = squads ? mySquads.has(x.name) : this.mine.has(x.name);
      // The name is a button so the details are reachable from the keyboard; the whole row is
      // clickable for the mouse.
      const tr = el('tr', { class: mine ? 'clickable mine' : 'clickable' },
        el('td', { class: i < 3 ? `rank-${i + 1}` : '' }, i + 1),
        el('td', {}, squads ? side : null, el('button', { type: 'button', class: 'link', onclick: (e) => { e.stopPropagation(); open(); } }, name),
          mine ? this.youBadge(squads) : null),
        el('td', {}, squads ? null : side, second),
        rest.map(v => el('td', {}, v)));
      tr.addEventListener('click', open);
      return tr;
    });
    if (!rows.length) {
      rows.push(el('tr', {}, el('td', { colspan: (squads ? 13 : 12) + this.columns(squads).length, class: 'muted' }, this.data ? t('table.empty') : t('status.loading'))));
    }
    this.table.querySelector('tbody').replaceChildren(...rows);
  }

  // --- achievements ------------------------------------------------------------------

  renderAchievements() {
    const a = this.data.achievements;
    const squadOf = (p) => p.squadRaw ? `[${p.squadRaw}]` : t('noSquad');
    const cards = [
      { award: a.topKiller, label: 'ach.topKiller', unit: 'unit.kills' },
      { award: a.topAssists, label: 'ach.topAssists', unit: 'unit.assists' },
      { award: a.topMedic, label: 'ach.topMedic', unit: 'unit.points', points: true },
      { award: a.topTeamKiller, label: 'ach.topTeamKiller', unit: 'unit.teamKills' },
      { award: a.topVehicles, label: 'ach.topVehicles', unit: 'unit.vehicles' },
      { award: a.topSquad && { name: a.topSquad.name, value: a.topSquad.kd }, label: 'ach.topSquad', unit: 'unit.kd', squad: true, sub: () => t('ach.bestSquad') },
      { award: a.longestKill, label: 'ach.longestKill', unit: 'unit.metres', sub: (p) => `${squadOf(p)} · ${p.weapon || ''}`, title: (p) => `${p.name} → ${p.victim}, ${p.weapon} (${p.date})` },
    ];
    const mySquads = this.mySquads();
    $('achievements').replaceChildren(...cards.map(c => {
      const p = c.award;
      const mine = p && (c.squad ? mySquads.has(p.name) : this.mine.has(p.name));
      const node = el(p ? 'button' : 'div', { type: p ? 'button' : null, class: `award${p ? ' clickable' : ''}${mine ? ' mine' : ''}`, title: p && c.title ? c.title(p) : null },
        el('div', { class: 'num' }, el('b', {}, p ? (c.squad ? fmt(p.value) : c.points ? fmtPoints(p.value) : Math.round(p.value)) : 0), el('span', {}, t(c.unit))),
        // The card itself is highlighted; screen readers get the words instead.
        el('div', { class: 'who' }, el('small', {}, t(c.label), mine ? this.youBadge(c.squad) : null), el('b', {}, p ? p.name : '—'), el('span', {}, p ? (c.sub ? c.sub(p) : squadOf(p)) : '—')));
      if (p) node.addEventListener('click', () => c.squad ? this.openSquad(p.name) : this.openPlayer(p.name));
      return node;
    }));
  }

  // --- signed-in player ----------------------------------------------------------------

  // loadAccount asks who is signed in (Steam or the OpenID Connect provider, cfg.login); the
  // page works the same without it.
  async loadAccount() {
    this.me = null;
    if (this.cfg.login) {
      try {
        const res = await fetch('api/auth/me', { cache: 'no-store' });
        if (res.ok) this.me = await res.json();
      } catch { /* signed out */ }
    }
    this.mine = new Set(this.me && this.me.signedIn ? this.me.names.map(n => n.name) : []);
    this.renderAccount();
  }

  // myPlayer is the signed-in player in the loaded period: the most recent of their names that
  // played in it.
  myPlayer() {
    if (!this.data) return null;
    for (const n of this.mine) {
      const p = this.data.players.find(x => x.name === n);
      if (p) return p;
    }
    return null;
  }

  mySquads() {
    return new Set(this.data ? this.data.players.filter(p => p.squad && this.mine.has(p.name)).map(p => p.squad) : []);
  }

  // youBadge names the highlight for screen readers; on screen the row colour is enough.
  youBadge(squad) {
    return el('span', { class: 'sr-only' }, ` (${t(squad ? 'me.yourSquad' : 'me.you')})`);
  }

  renderAccount() {
    const box = $('account');
    if (!this.cfg.login) { box.replaceChildren(); return; }
    if (!this.me || !this.me.signedIn) {
      const path = this.cfg.login === 'oidc' ? 'api/auth/oidc' : 'api/auth/steam';
      const a = el('a', { class: 'btn primary', href: path }, t('auth.signIn', { name: this.cfg.loginName }));
      // The provider brings the visitor back to the page as it is now (period, view, open mission).
      a.addEventListener('click', () => { a.href = `${path}?return=${encodeURIComponent(location.search)}`; });
      box.replaceChildren(a);
      return;
    }
    const profile = el('button', { type: 'button', class: 'primary', onclick: () => this.openPlayer((this.myPlayer() || this.me.names[0]).name) }, t('auth.profile'));
    // The OpenID Connect provider gives a nickname and maybe an avatar; Steam gives neither.
    const who = this.cfg.login === 'oidc' && this.me.name
      ? el('span', { class: 'who-me' }, this.avatar(this.me.name, this.me.picture), el('span', { class: 'who-name' }, this.me.name))
      : null;
    box.replaceChildren(...(who ? [who] : []), ...(this.me.names.length ? [profile] : []),
      el('button', { type: 'button', onclick: () => this.logout() }, t('auth.signOut')));
  }

  // avatar is the provider's picture, or the first letter of the name when there is none or it
  // does not load (the URL may have changed since the sign-in). The name is shown next to it.
  avatar(name, picture) {
    const placeholder = el('span', { class: 'avatar placeholder', 'aria-hidden': 'true' }, ([...name][0] || '?').toUpperCase());
    if (!picture) return placeholder;
    const img = el('img', { class: 'avatar', src: picture, alt: '', width: 28, height: 28, referrerpolicy: 'no-referrer' });
    img.addEventListener('error', () => img.replaceWith(placeholder));
    return img;
  }

  // Signs out of this site only; a session at the OpenID Connect provider stays.
  async logout() {
    try {
      const res = await fetch('api/auth/logout', { method: 'POST', headers: { 'X-Requested-With': 'ocap-stats' } });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
    } catch (e) {
      this.toast(t('n.error'), e.message, true);
      return;
    }
    this.me = null;
    this.mine = new Set();
    this.renderAccount();
    if (this.data) { this.render(); this.renderAchievements(); }
    this.renderMe();
  }

  // renderMe fills the card with the signed-in player's statistics for the loaded period.
  async renderMe() {
    const card = $('meCard');
    card.hidden = !(this.me && this.me.signedIn);
    if (card.hidden || !this.data) return;
    const token = this.meToken = {};
    let p = this.myPlayer();
    const ranked = !!p;
    // Players below the "minimum missions" setting are not in the table, but have statistics.
    if (!p && this.me.names.length && this.cfg.minMissions > 0) {
      try {
        p = (await apiGet(`players/${encodeURIComponent(this.me.names[0].name)}?${this.query()}`)).player;
      } catch { /* no missions in the period */ }
      if (this.meToken !== token) return;
    }
    if (!this.me.steamId) { $('meBody').replaceChildren(el('p', {}, t('me.noSteam', { name: this.cfg.loginName }))); return; }
    if (!this.me.names.length) { $('meBody').replaceChildren(el('p', {}, t('me.noGames'))); return; }
    if (!p) { $('meBody').replaceChildren(el('p', {}, t('me.noneInPeriod', { period: this.range }))); return; }

    const players = this.sortItems([...this.data.players], false, this.defaultSort('players'));
    const rank = ranked ? players.findIndex(x => x.name === p.name) + 1 : 0;
    const others = [...this.mine].filter(n => n !== p.name);
    $('meBody').replaceChildren(el('div', { class: 'me-grid' },
      el('div', { class: 'me-who' },
        el('button', { type: 'button', class: 'link name', onclick: () => this.openPlayer(p.name) }, p.name),
        el('small', {}, `${p.squadRaw ? `[${p.squadRaw}]` : t('noSquad')} · ${this.range}`),
        rank ? el('span', { class: 'rank', title: t('me.rankTitle') }, t('me.rank', { n: rank, total: players.length }))
          : el('small', {}, t('me.unranked', { n: this.cfg.minMissions })),
        others.length ? el('small', {}, t('me.alsoAs', { names: others.join(', ') })) : null),
      el('div', { class: 'me-stats' }, [
        ['col.kills', p.kills], ['col.deaths', p.deaths], ['col.kd', fmt(p.kd)], ['tile.assists', p.killAssists],
        ['col.vehicles.title', p.vehiclesDestroyed], ['tile.medical', fmtPoints(p.medicalPoints)], ['col.accuracy', fmt(p.accuracy)], ['tile.missions', p.attendance],
        ...this.columns(false).map(c => [c.name, this.customValue(p, c), true]),
      ].map(([label, value, literal]) => {
        return el('div', {}, el('b', {}, value), el('span', {}, literal ? label : t(label)));
      }))));
  }

  // --- modals ----------------------------------------------------------------------

  // Open modals make the rest of the page inert, which also keeps keyboard focus inside them.
  openModal(m) {
    if (!m.classList.contains('open')) this.lastFocus = document.activeElement;
    for (const other of document.querySelectorAll('.modal.open')) if (other !== m) other.classList.remove('open');
    m.classList.add('open');
    document.querySelector('.page').inert = true;
    requestAnimationFrame(() => m.querySelector('[data-close]').focus());
  }

  closeModal(m) {
    m.classList.remove('open');
    document.querySelector('.page').inert = false;
    if (m.id === 'detailModal') {
      this.detailView = null;
      this.detailStack = [];
      this.updateURL();
    }
    if (this.lastFocus && document.contains(this.lastFocus)) this.lastFocus.focus();
  }

  // replayURL is the recording in the OCAP player, or '' when no OCAP address is configured.
  replayURL(id, filename) {
    if (!this.cfg.ocapUrl || !filename) return '';
    return `${this.cfg.ocapUrl}/recording/${encodeURIComponent(id)}/${encodeURIComponent(filename)}`;
  }

  tiles(items) {
    // A third element marks a literal label (custom column names are not translated).
    return el('div', { class: 'tiles' }, items.map(([label, value, literal]) =>
      el('div', { class: 'tile' }, el('div', { class: 'v' }, value), el('div', { class: 'l' }, literal ? label : t(label)))));
  }

  list(title, items, onClick) {
    return el('div', { class: 'list' }, el('h4', {}, t(title)),
      items.length ? el('ol', {}, items.map(it => {
        const label = it.name || t('noSquad');
        const name = onClick
          ? el('button', { type: 'button', class: 'link', title: label, onclick: () => onClick(it.name) }, label)
          : el('span', { title: label }, label);
        return el('li', {}, name, el('span', {}, it.count));
      })) : el('div', { class: 'muted' }, t('list.empty')));
  }

  // dataTable builds a table; with sort ([column, ascending] of the order the rows come in) its
  // columns are sortable, or only the columns listed in only.
  dataTable(headers, rows, sort = null, only = null) {
    const table = el('table', {},
      el('thead', {}, el('tr', {}, headers.map((h, i) => el('th', { 'data-nosort': only && !only.includes(i) ? '' : null }, t(h))))),
      el('tbody', {}, rows));
    if (sort) sortable(table, sort);
    return el('div', { class: 'table-wrap', tabindex: 0 }, table);
  }

  // showDetail shows a view in the detail modal. A view opened from another one (missions →
  // mission → player …) keeps that one on a stack, and Back returns to it. The loader gets the
  // view object, where it may keep state (the open tab) and update the title.
  showDetail(title, loader, link = null) {
    const open = $('detailModal').classList.contains('open') && this.detailView;
    this.detailStack = open ? [...this.detailStack, this.detailView] : [];
    this.detailView = { title, loader, link };
    this.renderDetail();
  }

  backDetail() {
    if (!this.detailStack.length) return;
    this.detailView = this.detailStack.pop();
    this.renderDetail();
  }

  async renderDetail() {
    const m = $('detailModal');
    const view = this.detailView;
    $('detailTitle').textContent = view.title;
    $('detailBack').hidden = !this.detailStack.length;
    $('detailBody').replaceChildren(el('p', { class: 'muted' }, t('detail.loading')));
    this.openModal(m);
    this.updateURL();
    m.querySelector('.modal-box').scrollTop = 0;
    let body;
    try {
      body = await view.loader(view);
    } catch (e) {
      body = [el('p', { class: 'bad' }, t('detail.error', { e: e.message }))];
    }
    // Another view may have been opened while this one was loading.
    if (this.detailView !== view) return;
    $('detailTitle').textContent = view.title;
    this.updateURL(); // the loader may have changed the link (a player's current name)
    // Loaders may return nested arrays and null for parts they leave out.
    $('detailBody').replaceChildren(...body.flat().filter(n => n !== null && n !== undefined && n !== false));
  }

  openPlayer(name) {
    this.showDetail(name, async (view) => {
      const d = await apiGet(`players/${encodeURIComponent(name)}?${this.query()}`);
      const p = d.player, lk = d.longestKill;
      // An older name of a player opens it under its current one.
      view.title = p.name;
      view.link = { player: p.name };
      return [
        el('p', { class: 'subtitle' }, [p.squadRaw ? `[${p.squadRaw}]` : null, this.range, t('detail.firstLast', { first: p.firstSeen, last: p.lastSeen })].filter(Boolean).join(' · ')),
        p.aliases && p.aliases.length ? el('p', { class: 'subtitle' }, t('me.alsoAs', { names: p.aliases.join(', ') })) : null,
        this.shareBar(() => this.playerCard(d)),
        this.tiles([
          ['col.kills', p.kills], ['col.deaths', p.deaths], ['col.teamKills.title', p.teamKills], ['col.vehicles.title', p.vehiclesDestroyed],
          ['tile.assists', p.killAssists], ['col.kd', fmt(p.kd)], ['col.accuracy', fmt(p.accuracy)], ['tile.missions', p.attendance],
          ['tile.shots', p.shotsFired], ['tile.medical', fmtPoints(p.medicalPoints)], ['tile.treatments', p.treatments], ['tile.revives', p.revives],
          ...this.columns(false).map(c => [c.name, this.customValue(p, c), true]),
        ]),
        lk ? el('p', { class: 'note' }, t('detail.longestKill', { d: Math.round(lk.distance), victim: lk.victim, weapon: lk.weapon, date: lk.date })) : null,
        el('div', { class: 'lists' },
          this.list('list.weapons', d.weapons),
          this.list('list.victims', d.victims, (n) => this.openPlayer(n)),
          this.list('list.killedBy', d.killedBy, (n) => this.openPlayer(n)),
          this.list('list.squads', d.squads, (n) => this.openSquad(n))),
        this.trend(d.trend),
        this.dataTable(['col.date', 'col.mission', 'col.map', 'col.squad', 'col.side', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists', 'col.medical'],
          d.missions.map(m => el('tr', {},
            el('td', {}, m.date), el('td', {}, el('button', { type: 'button', class: 'link', onclick: () => this.openMission(m.missionId, m.name) }, m.name)), el('td', {}, m.world),
            el('td', {}, m.squadRaw || '-'), el('td', {}, m.side),
            el('td', {}, m.kills), el('td', {}, m.deaths), el('td', {}, m.teamKills), el('td', {}, m.vehiclesDestroyed), el('td', {}, m.killAssists),
            el('td', {}, this.medical(m))))),
      ];
    }, { player: name });
  }

  // trend shows a player's months over all time: missions, kills per mission and K/D, each
  // its own chart (one scale per chart), with the numbers in a table below.
  trend(points) {
    if (!points || points.length < 2) return null;
    const perMission = (p) => p.missions ? p.kills / p.missions : null;
    return el('section', { class: 'trend-box' },
      el('h4', {}, t('trend.title')),
      el('div', { class: 'trends' },
        trendChart(points, { title: t('trend.missions'), kind: 'bar', value: p => p.missions, format: String }),
        trendChart(points, { title: t('trend.kpm'), kind: 'line', value: perMission, format: v => fmt(v, 2) }),
        trendChart(points, { title: t('trend.kd'), kind: 'line', value: p => p.missions ? p.kd : null, format: v => fmt(v, 2) })),
      el('details', {}, el('summary', {}, t('trend.table')),
        this.dataTable(['col.month', 'tile.missions', 'col.kills', 'col.deaths', 'trend.kpm', 'col.kd', 'tile.assists', 'tile.medical'],
          [...points].reverse().map(p => el('tr', {},
            el('td', { 'data-sort': p.month }, fmtMonth(p.month, true)), el('td', {}, p.missions), el('td', {}, p.kills), el('td', {}, p.deaths),
            el('td', {}, p.missions ? fmt(perMission(p)) : '—'), el('td', {}, p.missions ? fmt(p.kd) : '—'),
            el('td', {}, p.killAssists), el('td', {}, fmtPoints(p.medicalPoints)))))));
  }

  openSquad(name) {
    this.showDetail(name || t('noSquad'), async () => {
      const d = await apiGet(`squads/${encodeURIComponent(name || '-')}?${this.query()}`);
      const s = d.squad;
      return [
        el('p', { class: 'subtitle' }, `${this.range} · ${t('detail.squadOnly')}`),
        this.tiles([
          ['col.players', s.players], ['tile.missions', s.missions], ['col.kills', s.kills], ['col.deaths', s.deaths],
          ['col.teamKills.title', s.teamKills], ['col.vehicles.title', s.vehiclesDestroyed], ['tile.assists', s.killAssists],
          ['col.kd', fmt(s.kd)], ['col.accuracy', fmt(s.accuracy)], ['tile.medical', fmtPoints(s.medicalPoints)], ['tile.treatments', s.treatments],
          ['tile.revives', s.revives], ...this.columns(true).map(c => [c.name, this.customValue(s, c), true]),
        ]),
        el('div', { class: 'lists' },
          this.list('list.victims', d.killed, (n) => this.openSquad(n)),
          this.list('list.killedBy', d.killedBy, (n) => this.openSquad(n))),
        el('div', { style: 'height:16px' }),
        this.dataTable(['col.name', 'col.tag', 'col.missions', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists', 'col.medical', 'col.accuracy', 'col.kd'],
          d.members.map(p => {
            const mine = this.mine.has(p.name);
            const tr = el('tr', { class: mine ? 'clickable mine' : 'clickable' },
              el('td', {}, p.name, mine ? this.youBadge(false) : null), el('td', {}, p.squadRaw || '-'), el('td', {}, p.attendance),
              el('td', {}, p.kills), el('td', {}, p.deaths), el('td', {}, p.teamKills),
              el('td', {}, p.vehiclesDestroyed), el('td', {}, p.killAssists), el('td', {}, this.medical(p)), el('td', {}, fmt(p.accuracy)), el('td', {}, fmt(p.kd)));
            tr.addEventListener('click', () => this.openPlayer(p.name));
            return tr;
          })),
      ];
    });
  }

  // --- missions ----------------------------------------------------------------------

  openMissions() {
    this.showDetail(t('missions.title'), async () => {
      const d = await apiGet(`missions?${this.query()}`);
      return [
        el('p', { class: 'subtitle' }, `${this.periodText()} · ${t('status.missions', { n: d.total })}`),
        d.missions.length ? this.dataTable(['col.date', 'col.mission', 'col.map', 'col.duration', 'col.players', 'col.winner', 'col.replay'],
          d.missions.map(m => {
            const open = () => this.openMission(m.id, m.name);
            const replay = this.replayURL(m.id, m.filename);
            const tr = el('tr', { class: 'clickable' },
              // Missions of one day keep their order (newest recording first) when sorted by date.
              el('td', { 'data-sort': `${m.date} ${String(m.id).padStart(12, '0')}` }, m.date),
              el('td', {}, el('button', { type: 'button', class: 'link', onclick: (e) => { e.stopPropagation(); open(); } }, m.name)),
              el('td', {}, m.world), el('td', { 'data-sort': m.duration }, fmtDuration(m.duration)), el('td', {}, m.playerCount),
              el('td', { title: m.endMessage || null }, m.endSide ? [sideDot(m.endSide), m.endSide] : el('span', { class: 'muted' }, '—')),
              el('td', {}, replay ? el('a', { href: replay, target: '_blank', rel: 'noopener', 'aria-label': t('mission.openReplay'), title: t('mission.openReplay'), onclick: (e) => e.stopPropagation() }, '▶') : '—'));
            tr.addEventListener('click', open);
            return tr;
          }), [0, false], [0, 3, 4]) : el('p', { class: 'muted' }, t('n.noMissions')),
      ];
    });
  }

  // --- squad vs squad -----------------------------------------------------------------

  // openMatrix shows the kills between squads as a heat map: rows killed, columns died.
  openMatrix() {
    this.showDetail(t('matrix.title'), async (view) => {
      const { matrix } = await apiGet(`squad-matrix?${this.query()}`);
      const note = el('p', { class: 'subtitle' }, `${this.periodText()} · ${t('matrix.note')}`);
      if (!matrix.cells.length) return [note, el('p', { class: 'muted' }, t('matrix.empty'))];
      // A dozen squads fit a screen; the rest are a click away.
      const top = 12, squads = matrix.squads;
      const body = el('div');
      const toggle = squads.length > top ? el('button', { type: 'button', class: 'small' }) : null;
      const render = () => {
        body.replaceChildren(this.matrixTable(matrix, view.all ? squads : squads.slice(0, top)));
        if (toggle) toggle.textContent = view.all ? t('matrix.top', { n: top }) : t('matrix.all', { n: squads.length });
      };
      if (toggle) toggle.addEventListener('click', () => { view.all = !view.all; render(); });
      render();
      return [note, toggle, body];
    });
  }

  matrixTable(matrix, squads) {
    const cells = new Map(matrix.cells.map(c => [`${c.killer}\n${c.victim}`, c]));
    const max = Math.max(1, ...matrix.cells.map(c => c.kills));
    const kills = new Map(), deaths = new Map();
    for (const c of matrix.cells) {
      kills.set(c.killer, (kills.get(c.killer) || 0) + c.kills);
      deaths.set(c.victim, (deaths.get(c.victim) || 0) + c.kills);
    }
    const mine = new Set([...this.mySquads()].map(s => s.toLowerCase()));
    const isMine = (s) => s !== '' && mine.has(s.toLowerCase());
    const label = (s) => s || t('noSquad');
    const head = el('tr', {}, el('th', { scope: 'col', class: 'corner' }, t('matrix.corner')),
      squads.map(s => el('th', { scope: 'col', class: isMine(s) ? 'mine' : null }, label(s))),
      el('th', { scope: 'col' }, t('matrix.total')));
    const rows = squads.map(k => el('tr', {},
      el('th', { scope: 'row', class: isMine(k) ? 'mine' : null },
        el('button', { type: 'button', class: 'link', onclick: () => this.openSquad(k) }, label(k))),
      squads.map(v => {
        const c = cells.get(`${k}\n${v}`);
        const n = c ? c.kills : 0, tk = c ? c.teamKills : 0;
        const title = [n ? t('matrix.cell', { killer: label(k), victim: label(v), n }) : null, tk ? t('matrix.teamKills', { n: tk }) : null].filter(Boolean).join(' · ');
        // The cell's share of the largest number shades it; the number itself stays readable.
        return el('td', { class: k === v ? 'self' : 'heat', style: n ? `--heat:${(n / max).toFixed(3)}` : null, title: title || null },
          n || null, tk ? el('small', { class: 'tk' }, `${t('col.teamKills')} ${tk}`) : null);
      }),
      el('td', { class: 'total' }, kills.get(k) || 0)));
    const foot = el('tr', {}, el('th', { scope: 'row' }, t('matrix.total')),
      squads.map(v => el('td', { class: 'total' }, deaths.get(v) || 0)), el('td'));
    return el('div', { class: 'table-wrap', tabindex: 0 },
      el('table', { class: 'matrix' }, el('thead', {}, head), el('tbody', {}, rows, foot)));
  }

  openMission(id, name) {
    this.showDetail(name || '…', async (view) => {
      const d = await apiGet(`missions/${encodeURIComponent(id)}`);
      view.title = d.mission.name;
      return this.missionBody(d, view);
    }, { mission: String(id) });
  }

  missionBody(d, view) {
    const m = d.mission;
    const players = [...d.players].sort((a, b) => b.kills - a.kills || b.killAssists - a.killAssists || a.name.localeCompare(b.name));
    const sum = (field) => players.reduce((s, p) => s + p[field], 0);
    const replay = this.replayURL(m.id, m.filename);
    const feed = this.missionFeed(d, view);
    const mySquads = new Set(players.filter(p => p.squad && this.mine.has(p.name)).map(p => p.squad.toLowerCase()));
    const tabs = this.missionTabs(view, {
      players: this.dataTable(['col.name', 'col.squad', 'col.side', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists', 'col.medical', 'tile.shots', 'col.hits', 'col.accuracy'],
        players.map(p => {
          // A player opens the kill feed filtered to what they did and what happened to them.
          const open = () => { feed.select(p.name); tabs.show('feed'); };
          const mine = this.mine.has(p.name);
          const tr = el('tr', { class: mine ? 'clickable mine' : 'clickable' },
            el('td', { 'data-sort': p.name }, el('button', { type: 'button', class: 'link', onclick: (e) => { e.stopPropagation(); open(); } }, p.name), mine ? this.youBadge(false) : null,
              // The name played under here is shown, and the current one of a player who has renamed since.
              p.player ? el('small', { class: 'now-as' }, t('mission.nowAs', { name: p.player })) : null),
            el('td', {}, p.squadRaw || '-'), el('td', {}, sideDot(p.side), p.side),
            el('td', {}, p.kills), el('td', {}, p.deaths), el('td', {}, p.teamKills), el('td', {}, p.vehiclesDestroyed), el('td', {}, p.killAssists),
            el('td', {}, this.medical(p)), el('td', {}, p.shotsFired), el('td', {}, p.hits), el('td', {}, p.shotsFired ? fmt(Math.min(100, p.hits / p.shotsFired * 100)) : fmt(0)));
          tr.addEventListener('click', open);
          return tr;
        }), [3, false]),
      squads: this.dataTable(['col.squad', 'col.players', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists', 'col.medical', 'col.accuracy', 'col.kd'],
        [...d.squads].sort((a, b) => b.kills - a.kills || a.name.localeCompare(b.name)).map(s => el('tr', { class: mySquads.has(s.name.toLowerCase()) ? 'mine' : null },
          el('td', { 'data-sort': s.name || t('noSquad') }, s.name || t('noSquad'), mySquads.has(s.name.toLowerCase()) ? this.youBadge(true) : null), el('td', {}, s.players), el('td', {}, s.kills), el('td', {}, s.deaths), el('td', {}, s.teamKills),
          el('td', {}, s.vehiclesDestroyed), el('td', {}, s.killAssists), el('td', {}, this.medical(s)), el('td', {}, fmt(s.accuracy)), el('td', {}, fmt(s.kd)))), [2, false]),
      feed: feed.node,
    });
    return [
      el('p', { class: 'subtitle' }, [m.date, m.world, fmtDuration(m.duration), m.tag, m.author && t('mission.author', { a: m.author })].filter(Boolean).join(' · ')),
      this.shareBar(() => this.missionCard(d)),
      el('div', { class: 'mission-head' },
        el('div', { class: 'result' }, m.endSide
          ? [el('span', {}, sideDot(m.endSide), el('b', {}, t('mission.winner', { side: m.endSide }))), m.endMessage ? el('span', { class: 'muted' }, m.endMessage) : null]
          : el('span', { class: 'muted' }, t('mission.noWinner'))),
        replay ? el('a', { class: 'btn primary', href: replay, target: '_blank', rel: 'noopener' }, el('span', { 'aria-hidden': 'true' }, '▶'), t('mission.openReplay')) : null),
      d.counted ? null : el('p', { class: 'note warn' }, t('mission.notCounted')),
      this.tiles([['col.players', players.length], ['col.kills', sum('kills')], ['col.deaths', sum('deaths')], ['col.teamKills.title', sum('teamKills')],
        ['col.vehicles.title', sum('vehiclesDestroyed')], ['tile.assists', sum('killAssists')]]),
      this.sideCards(players, m.endSide),
      tabs.node,
    ];
  }

  // sideCards sums the players of each side.
  sideCards(players, winner) {
    const sides = new Map();
    for (const p of players) {
      const s = sides.get(p.side) || { side: p.side, players: 0, kills: 0, deaths: 0, teamKills: 0, vehiclesDestroyed: 0, killAssists: 0, medicalPoints: 0 };
      s.players++;
      for (const f of ['kills', 'deaths', 'teamKills', 'vehiclesDestroyed', 'killAssists', 'medicalPoints']) s[f] += p[f];
      sides.set(p.side, s);
    }
    return el('div', { class: 'side-cards' }, [...sides.values()].sort((a, b) => b.players - a.players).map(s =>
      el('div', { class: 'side-card', style: `border-top-color:${SIDE_COLORS[s.side] || '#888'}` },
        el('h4', {}, sideDot(s.side), s.side || '—', s.side && s.side === winner ? el('span', { class: 'badge ok' }, t('mission.won')) : null),
        el('dl', {}, [['col.players', s.players], ['col.k', s.kills, 'col.kills'], ['col.d', s.deaths, 'col.deaths'],
          ['col.teamKills', s.teamKills, 'col.teamKills.title'], ['col.vehicles', s.vehiclesDestroyed, 'col.vehicles.title'], ['col.assists', s.killAssists, 'tile.assists'],
          ['col.medical', fmtPoints(s.medicalPoints), 'tile.medical']]
          .map(([label, value, title]) => el('div', {}, el('dt', { title: title ? t(title) : null }, t(label)), el('dd', {}, value)))))));
  }

  // missionTabs builds WAI-ARIA tabs over the panels; view remembers the open tab for Back.
  missionTabs(view, panels) {
    const names = Object.keys(panels);
    const tabs = names.map(name => el('button', { type: 'button', role: 'tab', id: `mtab-${name}`, 'aria-controls': `mpanel-${name}` }, t(`mission.tab.${name}`)));
    const sections = names.map(name => el('div', { class: 'tab', role: 'tabpanel', id: `mpanel-${name}`, 'aria-labelledby': `mtab-${name}` }, panels[name]));
    const show = (name, focus = false) => {
      view.tab = name;
      names.forEach((n, i) => {
        const on = n === name;
        tabs[i].classList.toggle('active', on);
        tabs[i].setAttribute('aria-selected', String(on));
        tabs[i].tabIndex = on ? 0 : -1;
        sections[i].classList.toggle('active', on);
        if (on && focus) tabs[i].focus();
      });
    };
    tabs.forEach((b, i) => {
      b.addEventListener('click', () => show(names[i]));
      b.addEventListener('keydown', (e) => {
        const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: names.length - 1 }[e.key];
        if (next === undefined) return;
        e.preventDefault();
        show(names[(next + names.length) % names.length], true);
      });
    });
    show(names.includes(view.tab) ? view.tab : names[0]);
    return { node: [el('div', { class: 'tabs', role: 'tablist', 'aria-label': t('mission.tabs') }, tabs), ...sections], show };
  }

  // missionFeed lists the deaths of the mission in order, with the players who got an assist
  // for each, and can be narrowed to one player.
  missionFeed(d, view) {
    const delay = d.mission.captureDelay || 1;
    // Assists belong to the death with the same frame and victim. Deaths of AI killed by AI are
    // not kills of the statistics, so their assists make rows of their own.
    const events = d.kills.map(k => ({ ...k, assists: [] }));
    const byDeath = new Map();
    for (const e of events) if (!byDeath.has(`${e.frame}|${e.victim}`)) byDeath.set(`${e.frame}|${e.victim}`, e);
    for (const a of d.assists) {
      const key = `${a.frame}|${a.victim}`;
      if (!byDeath.has(key)) {
        const e = { frame: a.frame, killer: a.killer, victim: a.victim, victimSquad: a.victimSquad, victimKind: a.victimKind, assists: [] };
        byDeath.set(key, e);
        events.push(e);
      }
      byDeath.get(key).assists.push(a);
    }
    events.sort((a, b) => a.frame - b.frame);

    const involves = (e, name) => (e.killer === name && e.killerIsPlayer) || (e.victim === name && e.victimKind === 'player') ||
      e.assists.some(a => a.player === name);
    const who = (name, squad, side, kind, me) => {
      if (!name) return el('span', { class: 'who none' }, t('feed.unknown'));
      return el('span', { class: `who${kind === 'player' ? '' : ' ai'}${me ? ' me' : ''}` },
        side ? sideDot(side) : null, squad ? el('span', { class: 'squad' }, `[${squad}] `) : null, name);
    };
    const row = (e, me) => {
      const suicide = e.killer === e.victim && !e.killerIsPlayer && e.victimKind === 'player';
      return el('tr', {},
        el('td', { class: 'time', 'data-sort': e.frame }, fmtClock(e.frame * delay)),
        el('td', {}, suicide ? el('span', { class: 'who none' }, '—') : who(e.killer, e.killerSquad, e.killerSide, e.killerIsPlayer ? 'player' : 'ai', e.killer === me && e.killerIsPlayer),
          // The recording named no killer; the kill was credited for the last damage.
          e.cause ? el('span', { class: 'badge', title: t(`feed.cause.${e.cause}.title`) }, t(`feed.cause.${e.cause}`)) : null),
        el('td', {}, who(e.victim, e.victimSquad, e.victimSide, e.victimKind, e.victim === me && e.victimKind === 'player'),
          e.teamKill ? el('span', { class: 'badge bad', title: t('col.teamKills.title') }, t('col.teamKills')) : null,
          suicide ? el('span', { class: 'badge' }, t('feed.suicide')) : null,
          e.victimKind === 'ai' ? el('span', { class: 'badge' }, t('feed.ai')) : null,
          e.victimKind === 'vehicle' ? el('span', { class: 'badge' }, e.vehicleClass || t('feed.vehicle')) : null),
        el('td', { class: 'weapon' }, this.weaponCell(e.weapon)),
        el('td', { class: 'num', 'data-sort': e.distance || '' }, e.distance ? Math.round(e.distance) : '—'),
        el('td', { class: 'assists' }, e.assists.length ? e.assists.flatMap((a, i) => [i ? ', ' : null,
          el('span', { class: `who${a.player === me ? ' me' : ''}`, title: t(`assist.${a.kind}`) }, a.player)]) : '—'));
    };

    const select = el('select', { id: 'feedPlayer' },
      el('option', { value: '' }, t('feed.allPlayers')),
      [...d.players].sort((a, b) => a.name.localeCompare(b.name)).map(p => el('option', { value: p.name }, p.name)));
    // Picking "everyone" again in a long list is tedious, so a selected player has a reset button.
    const reset = el('button', { type: 'button', class: 'small', hidden: true }, el('span', { 'aria-hidden': 'true' }, '×'), t('feed.reset'));
    // The profile opens under the player's current name.
    const profile = el('button', { type: 'button', class: 'small', hidden: true }, t('feed.profile'));
    profile.addEventListener('click', () => {
      const p = d.players.find(x => x.name === select.value);
      if (p) this.openPlayer(p.player || p.name);
    });
    const summary = el('span', { class: 'summary', 'aria-live': 'polite' });
    const body = el('tbody');
    // Only time and distance sort; the feed opens in time order.
    const sortableCols = ['col.time', 'col.distance'];
    const table = el('table', { class: 'feed' },
      el('thead', {}, el('tr', {}, ['col.time', 'col.killer', 'col.victim', 'col.weapon', 'col.distance', 'col.assistedBy']
        .map(h => el('th', { 'data-nosort': sortableCols.includes(h) ? null : '' }, t(h))))),
      body);
    const resort = sortable(table, [0, true]);
    const render = () => {
      const me = select.value;
      view.player = me;
      reset.hidden = profile.hidden = !me;
      const list = me ? events.filter(e => involves(e, me)) : events;
      const p = me && d.players.find(x => x.name === me);
      summary.textContent = p ? t('feed.summary', { k: p.kills, d: p.deaths, a: p.killAssists, v: p.vehiclesDestroyed }) : '';
      body.replaceChildren(...(list.length ? list.map(e => row(e, me))
        : [el('tr', {}, el('td', { colspan: 6, class: 'muted' }, t('list.empty')))]));
      resort();
    };
    select.addEventListener('change', render);
    reset.addEventListener('click', () => { select.value = ''; render(); select.focus(); });
    select.value = view.player || '';
    render();
    return {
      node: [
        el('div', { class: 'feed-filter' }, el('label', { for: 'feedPlayer' }, t('col.player')), select, reset, profile, summary),
        el('div', { class: 'table-wrap', tabindex: 0 }, table),
      ],
      select: (name) => { select.value = name; render(); },
    };
  }

  // weaponCell splits OCAP's "Vehicle: weapon" into two lines, the vehicle first.
  weaponCell(weapon) {
    if (!weapon) return '—';
    const i = weapon.indexOf(': ');
    return i < 0 ? weapon : [weapon.slice(0, i), el('small', {}, weapon.slice(i + 2))];
  }

  openRotation() {
    const rot = this.rotation;
    $('rotationSince').textContent = rot.since ? t('rot.since', { date: new Date(rot.since).toLocaleDateString(window.i18n.lang) }) : '';
    $('rotationCards').replaceChildren(
      ...rot.sides.map(s => el('div', { class: 'rotation-card', style: `border-color:${s.color || '#888'}` },
        el('h3', {}, s.name), el('div', { class: 'score' }, s.wins), el('div', { class: 'squads' }, s.squads.join(', ')))),
      el('div', { class: 'rotation-card' }, el('h3', {}, t('rot.draws')), el('div', { class: 'score' }, rot.draws), el('div', { class: 'squads' }, t('rot.drawsText'))));
    const rows = rot.matches.map(m => {
      const side = m.winner >= 0 ? rot.sides[m.winner] : null;
      return el('tr', {}, el('td', {}, m.date), el('td', {}, m.name), el('td', {}, m.world), el('td', {}, fmtDuration(m.duration)),
        // The side colour is user-defined, so it marks the cell instead of colouring the text.
        el('td', side ? { class: 'winner' } : { class: 'muted' },
          side ? [el('span', { class: 'swatch', style: `background:${side.color || '#888'}`, 'aria-hidden': 'true' }), side.name] : t('rot.draw')));
    });
    if (!rows.length) rows.push(el('tr', {}, el('td', { colspan: 5, class: 'muted' }, t('rot.empty'))));
    $('rotationBody').replaceChildren(...rows);
    this.openModal($('rotationModal'));
  }

  // --- export ----------------------------------------------------------------------

  headerLabels(squads) {
    return [...['col.rank', squads ? 'col.squad' : 'col.name', squads ? 'col.players' : 'col.squad', 'col.kills', 'col.deaths',
      'col.teamKills', ...(squads ? ['col.avgAttendance', 'col.avgPlayers'] : ['col.attendance']), 'col.accuracy', 'col.vehicles', 'col.assists', 'col.medical', 'col.kd'].map(k => t(k)),
    ...this.columns(squads).map(c => c.name)];
  }

  exportExcel() {
    if (!this.data) return;
    const squads = this.viewMode === 'squads';
    const custom = (x) => this.columns(squads).map(c => (x.custom && x.custom[c.id] !== null && x.custom[c.id] !== undefined ? x.custom[c.id] : ''));
    const rows = this.items().map((x, i) => squads
      ? [i + 1, x.name || t('noSquad'), x.players, x.kills, x.deaths, x.teamKills, this.attendanceRate(x), x.avgPlayers, x.accuracy, x.vehiclesDestroyed, x.killAssists, x.medicalPoints, x.kd, ...custom(x)]
      : [i + 1, x.name, x.squadRaw || '-', x.kills, x.deaths, x.teamKills, x.attendance, x.accuracy, x.vehiclesDestroyed, x.killAssists, x.medicalPoints, x.kd, ...custom(x)]);
    const sheet = t(squads ? 'xls.squads' : 'xls.players');
    const widths = [5, 22, 14, 10, 10, 8, ...(squads ? [16, 16] : [10]), 13, 8, 8, 8, 8];
    const blob = window.xlsx.workbook(sheet, this.headerLabels(squads), rows, widths);
    const url = URL.createObjectURL(blob);
    const a = el('a', { href: url, download: `${sheet}_${this.periodText().replace(/[^\d-]+/g, '_')}.xlsx` });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  // reportShell is the frame of a picture: title, logo, a corner caption (the period by
  // default) and a footer to append last.
  reportShell(title, footer, corner = [t('rep.period'), this.range]) {
    const box = el('div', {
      style: 'position:absolute;left:-9999px;top:0;width:1080px;padding:40px;border-radius:16px;border:1px solid #333;' +
        "background:linear-gradient(145deg,#1a1a1a,#0d0d0d);font-family:'Inter',sans-serif;color:#eee",
    });
    const left = el('div', { style: 'display:flex;align-items:center;gap:24px' });
    if (this.cfg.logoUrl) left.append(el('img', { src: this.cfg.logoUrl, crossorigin: 'anonymous', style: 'height:70px' }));
    left.append(el('h2', { style: 'color:#fff;margin:0;font-size:32px;font-weight:900;letter-spacing:2px;text-transform:uppercase' }, title));
    box.append(el('div', { style: 'display:flex;justify-content:space-between;align-items:center;margin-bottom:36px;padding-bottom:20px;border-bottom:2px solid rgba(72,187,120,.5)' },
      left,
      el('div', { style: 'text-align:right' },
        el('p', { style: 'color:#666;margin:0;font-size:12px;font-weight:700;text-transform:uppercase;letter-spacing:1px' }, corner[0]),
        el('p', { style: 'color:#fff;margin:5px 0 0;font-size:20px;font-weight:700' }, corner[1]))));
    const foot = el('div', { style: 'margin-top:30px;display:flex;justify-content:space-between;border-top:1px solid #333;padding-top:14px;font-size:11px' },
      el('span', { style: 'color:#555;font-weight:700;letter-spacing:1px' }, footer),
      el('span', { style: 'color:#666' }, new Date().toLocaleString(window.i18n.lang)));
    return { box, foot };
  }

  // capture renders box to a PNG and copies it to the clipboard. The ClipboardItem gets a
  // promise right away, so the click still counts as user activation while the library loads.
  async capture(box, button, okKey) {
    if (!window.ClipboardItem || !navigator.clipboard) {
      this.toast(t('n.error'), t('n.copyFailed'), true);
      return;
    }
    const label = button.textContent;
    button.textContent = t('btn.creating');
    button.disabled = true;
    document.body.append(box);
    const png = (async () => {
      const html2canvas = await loadLib('html2canvas');
      const canvas = await html2canvas(box, { backgroundColor: '#0d0d0d', scale: 2, logging: false, useCORS: true });
      return new Promise((resolve, reject) => canvas.toBlob(b => b ? resolve(b) : reject(new Error('toBlob failed')), 'image/png'));
    })();
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': png })]);
      this.toast(t('n.success'), t(okKey));
    } catch (e) {
      console.error(e);
      this.toast(t('n.error'), t('n.copyFailed'), true);
    } finally {
      box.remove();
      button.textContent = label;
      button.disabled = false;
    }
  }

  copyTop10() {
    const squads = this.viewMode === 'squads';
    const items = this.items().slice(0, 10);
    if (!items.length) { this.toast(t('n.warning'), t('n.nothingToCopy'), true); return; }
    let title = t(squads ? 'rep.top10Squads' : 'rep.top10Players');
    if (!squads && this.selected.size === 1 && !this.singles) title = t('rep.top10Of', { squad: [...this.selected][0] });
    const { box, foot } = this.reportShell(title, 'OCAP STATISTICS');
    const rankColor = ['#ffd700', '#c0c0c0', '#cd7f32'];
    const th = (s) => el('th', { style: 'color:#48bb78;text-align:left;padding:14px 10px;font-size:12px;font-weight:800;text-transform:uppercase;letter-spacing:1px;border-bottom:2px solid #333' }, s);
    box.append(el('table', { style: 'width:100%;border-collapse:collapse' },
      el('thead', {}, el('tr', {}, this.headerLabels(squads).map(th))),
      el('tbody', {}, items.map((x, i) => {
        const cells = [i + 1, ...this.row(x, squads)];
        return el('tr', { style: `background:${i % 2 ? 'transparent' : 'rgba(255,255,255,.02)'};border-bottom:1px solid rgba(255,255,255,.05)` },
          cells.map((c, ci) => {
            let style = 'padding:14px 10px;font-size:15px;color:#eee;';
            if (ci === 0) style += `font-weight:900;font-size:18px;color:${rankColor[i] || '#555'};`;
            if (ci === 1) style += 'color:#fff;font-weight:700;';
            if (ci === cells.length - 1) style += 'color:#48bb78;font-weight:800;';
            return el('td', { style }, c);
          }));
      }))), foot);
    this.capture(box, $('copyTop10'), 'n.top10Copied');
  }

  // cardTiles is the grid of big numbers of a card: [label, value, accent].
  cardTiles(items) {
    return el('div', { style: 'display:grid;grid-template-columns:repeat(4,1fr);gap:14px' }, items.map(([label, value, accent]) =>
      el('div', { style: 'background:rgba(255,255,255,.03);border-radius:12px;padding:18px 20px;border-bottom:3px solid ' + (accent ? '#48bb78' : '#333') },
        el('div', { style: `color:${accent ? '#48bb78' : '#fff'};font-size:40px;font-weight:900;line-height:1.1` }, value),
        el('div', { style: 'color:#888;margin-top:6px;font-size:12px;font-weight:700;text-transform:uppercase;letter-spacing:1px' }, label))));
  }

  cardFooter() {
    return (this.cfg.title || 'OCAP Statistics').toUpperCase();
  }

  // playerCard is the picture of a player's profile for chats.
  playerCard(d) {
    const p = d.player, lk = d.longestKill;
    // The squad tag goes before the name, as in the game.
    const name = p.squadRaw ? `[${p.squadRaw}] ${p.name}` : p.name;
    const { box, foot } = this.reportShell(name, this.cardFooter());
    const line = (text) => el('p', { style: 'color:#aaa;margin:0 0 8px;font-size:16px' }, text);
    // append() would show a null as text.
    box.append(...[
      line(t('detail.firstLast', { first: p.firstSeen, last: p.lastSeen })),
      p.aliases && p.aliases.length ? line(t('me.alsoAs', { names: p.aliases.join(', ') })) : null,
      el('div', { style: 'height:16px' }),
      this.cardTiles([
        [t('col.kills'), p.kills], [t('col.deaths'), p.deaths], [t('col.kd'), fmt(p.kd), true], [t('tile.assists'), p.killAssists],
        [t('col.vehicles.title'), p.vehiclesDestroyed], [t('tile.missions'), p.attendance], [t('col.accuracy'), fmt(p.accuracy)], [t('tile.medical'), fmtPoints(p.medicalPoints)],
      ]),
      el('div', { style: 'margin-top:22px;display:flex;flex-direction:column;gap:6px' },
        lk ? line(t('detail.longestKill', { d: Math.round(lk.distance), victim: lk.victim, weapon: lk.weapon, date: lk.date })) : null,
        d.weapons.length ? line(`${t('list.weapons')}: ${d.weapons.slice(0, 3).map(w => w.name).join(', ')}`) : null),
      foot].filter(Boolean));
    return box;
  }

  // missionCard is the picture of a mission's result for chats.
  missionCard(d) {
    const m = d.mission;
    const { box, foot } = this.reportShell(m.name, this.cardFooter(), [t('col.date'), m.date]);
    const players = [...d.players].sort((a, b) => b.kills - a.kills || b.killAssists - a.killAssists || a.name.localeCompare(b.name));
    const sum = (f) => players.reduce((s, p) => s + p[f], 0);
    const dot = (side) => el('span', { style: `display:inline-block;width:14px;height:14px;border-radius:50%;margin-right:10px;background:${SIDE_COLORS[side] || '#888'}` });
    const th = (s, right) => el('th', { style: `color:#48bb78;text-align:${right ? 'right' : 'left'};padding:10px;font-size:12px;font-weight:800;text-transform:uppercase;letter-spacing:1px;border-bottom:2px solid #333` }, s);
    const td = (s, style = '') => el('td', { style: 'padding:10px;font-size:15px;color:#eee;' + style }, s);
    box.append(
      el('p', { style: 'color:#aaa;margin:0 0 18px;font-size:16px' }, [m.world, fmtDuration(m.duration), m.author && t('mission.author', { a: m.author })].filter(Boolean).join(' · ')),
      el('div', { style: 'margin-bottom:22px;font-size:26px;font-weight:800;color:#fff;display:flex;align-items:center' },
        m.endSide ? [dot(m.endSide), t('mission.winner', { side: m.endSide }), m.endMessage ? el('span', { style: 'margin-left:14px;font-size:18px;font-weight:400;color:#aaa' }, m.endMessage) : null]
          : el('span', { style: 'color:#aaa' }, t('mission.noWinner'))),
      this.cardTiles([[t('col.players'), players.length], [t('col.kills'), sum('kills'), true], [t('col.deaths'), sum('deaths')], [t('tile.assists'), sum('killAssists')]]),
      el('table', { style: 'width:100%;border-collapse:collapse;margin-top:24px' },
        el('thead', {}, el('tr', {}, th(t('col.name')), th(t('col.squad')), th(t('col.side')), th(t('col.k'), true), th(t('col.d'), true), th(t('col.assists'), true))),
        el('tbody', {}, players.slice(0, 5).map((p, i) => el('tr', { style: `background:${i % 2 ? 'transparent' : 'rgba(255,255,255,.02)'}` },
          td(p.name, 'font-weight:700;color:#fff'), td(p.squadRaw || '-'), td([dot(p.side), p.side]),
          td(p.kills, 'text-align:right;font-weight:800;color:#48bb78'), td(p.deaths, 'text-align:right'), td(p.killAssists, 'text-align:right'))))),
      foot);
    return box;
  }

  copyRotation() {
    const rot = this.rotation;
    if (!rot || !rot.sides.length) return;
    const { box, foot } = this.reportShell(t('rep.rotation'), 'OCAP STATISTICS');
    const card = (name, score, text, color) => el('div', { style: `flex:1;min-width:260px;background:rgba(255,255,255,.03);padding:28px;border-radius:12px;border-bottom:5px solid ${color};text-align:center` },
      el('h3', { style: 'color:#888;margin:0 0 10px;font-size:14px;font-weight:800;text-transform:uppercase;letter-spacing:2px' }, name),
      el('div', { style: 'color:#fff;font-size:64px;font-weight:900' }, score),
      el('p', { style: 'color:#666;margin:0;font-size:11px' }, text));
    box.append(el('div', { style: 'display:flex;gap:24px' },
      rot.sides.map(s => card(s.name, s.wins, s.squads.join(', '), s.color || '#888')),
      card(t('rot.draws'), rot.draws, t('rot.drawsText'), '#a0a0a0')), foot);
    this.capture(box, $('copyRotation'), 'n.rotationCopied');
  }

  toast(title, message, isError = false) {
    const n = el('div', { class: isError ? 'err' : '' }, el('b', {}, title), message);
    $('toasts').append(n);
    setTimeout(() => n.remove(), isError ? 6000 : 3500);
  }
}

document.addEventListener('DOMContentLoaded', () => new StatsApp().init());
})();
