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
const fmtDuration = (sec) => t('duration', { m: Math.floor(sec / 60), s: Math.floor(sec % 60) });
const isoDate = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 10);

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

    this.lastFocus = null;
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
    await this.load();
    setInterval(() => this.refreshStatus(), 60000);
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
      if (this.data) { this.buildFilter(); this.render(); this.renderAchievements(); }
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
    history.replaceState(null, '', url);
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
      const [stats, rot] = await Promise.all([apiGet(`stats?${q}`), apiGet(`rotation?${q}`)]);
      this.data = stats;
      this.rotation = rot.rotation;
      this.range = this.periodText();
      this.loadTime = ((performance.now() - started) / 1000).toFixed(2);
      this.updateURL();
      this.buildFilter();
      this.render();
      this.renderAchievements();
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
        t('status.squads', { n: tot.squads }), t('status.time', { s: this.loadTime }));
    }
    try {
      const st = await apiGet('status');
      parts.push(t('status.updated', { when: window.i18n.relTime(st.sync.lastSuccess) }));
      if (st.missions.pending > 0) parts.push(t('status.queue', { n: st.missions.pending }));
      if (st.sync.lastError) parts.push(t('status.syncError', { e: st.sync.lastError }));
    } catch { /* status is optional */ }
    $('status').textContent = parts.length ? parts.join(' | ') : t('status.loading');
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
      this.allSquads.length ? el('p', {}, t('filters.squads')) : null,
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
    const { field, dir } = this.sort;
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
      squads ? fmt(x.avgAttendance) : x.attendance,
      fmt(x.accuracy) + '%', x.vehiclesDestroyed, x.killAssists, fmt(x.kd),
      ...this.columns(squads).map(c => this.customValue(x, c)),
    ];
  }

  render() {
    this.table.querySelectorAll('th[data-sort]').forEach(th => {
      if (th.dataset.sort === this.sort.field) th.setAttribute('aria-sort', this.sort.dir === 'asc' ? 'ascending' : 'descending');
      else th.removeAttribute('aria-sort');
    });
    const squads = this.viewMode === 'squads';
    const items = this.items();
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
      // The name is a button so the details are reachable from the keyboard; the whole row is
      // clickable for the mouse.
      const tr = el('tr', { class: 'clickable' },
        el('td', { class: i < 3 ? `rank-${i + 1}` : '' }, i + 1),
        el('td', {}, squads ? side : null, el('button', { type: 'button', class: 'link', onclick: (e) => { e.stopPropagation(); open(); } }, name)),
        el('td', {}, squads ? null : side, second),
        rest.map(v => el('td', {}, v)));
      tr.addEventListener('click', open);
      return tr;
    });
    if (!rows.length) {
      rows.push(el('tr', {}, el('td', { colspan: 11 + this.columns(squads).length, class: 'muted' }, this.data ? t('table.empty') : t('status.loading'))));
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
      { award: a.topTeamKiller, label: 'ach.topTeamKiller', unit: 'unit.teamKills' },
      { award: a.topVehicles, label: 'ach.topVehicles', unit: 'unit.vehicles' },
      { award: a.topSquad && { name: a.topSquad.name, value: a.topSquad.kd }, label: 'ach.topSquad', unit: 'unit.kd', squad: true, sub: () => t('ach.bestSquad') },
      { award: a.longestKill, label: 'ach.longestKill', unit: 'unit.metres', sub: (p) => `${squadOf(p)} · ${p.weapon || ''}`, title: (p) => `${p.name} → ${p.victim}, ${p.weapon} (${p.date})` },
    ];
    $('achievements').replaceChildren(...cards.map(c => {
      const p = c.award;
      const node = el(p ? 'button' : 'div', { type: p ? 'button' : null, class: `award${p ? ' clickable' : ''}`, title: p && c.title ? c.title(p) : null },
        el('div', { class: 'num' }, el('b', {}, p ? (c.squad ? fmt(p.value) : Math.round(p.value)) : 0), el('span', {}, t(c.unit))),
        el('div', { class: 'who' }, el('small', {}, t(c.label)), el('b', {}, p ? p.name : '—'), el('span', {}, p ? (c.sub ? c.sub(p) : squadOf(p)) : '—')));
      if (p) node.addEventListener('click', () => c.squad ? this.openSquad(p.name) : this.openPlayer(p.name));
      return node;
    }));
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
    if (this.lastFocus && document.contains(this.lastFocus)) this.lastFocus.focus();
  }

  replayLink(id, filename, text) {
    if (!this.cfg.ocapUrl || !filename) return text;
    return el('a', {
      href: `${this.cfg.ocapUrl}/recording/${encodeURIComponent(id)}/${encodeURIComponent(filename)}`,
      target: '_blank', rel: 'noopener', onclick: (e) => e.stopPropagation(),
    }, text);
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

  dataTable(headers, rows) {
    return el('div', { class: 'table-wrap', tabindex: 0 }, el('table', {},
      el('thead', {}, el('tr', {}, headers.map(h => el('th', {}, t(h))))),
      el('tbody', {}, rows)));
  }

  async showDetail(title, loader) {
    const m = $('detailModal');
    $('detailTitle').textContent = title;
    $('detailBody').replaceChildren(el('p', { class: 'muted' }, t('detail.loading')));
    this.openModal(m);
    m.querySelector('.modal-box').scrollTop = 0;
    try {
      $('detailBody').replaceChildren(...await loader());
    } catch (e) {
      $('detailBody').replaceChildren(el('p', { class: 'bad' }, t('detail.error', { e: e.message })));
    }
  }

  openPlayer(name) {
    this.showDetail(name, async () => {
      const d = await apiGet(`players/${encodeURIComponent(name)}?${this.query()}`);
      const p = d.player, lk = d.longestKill;
      return [
        el('p', { class: 'subtitle' }, [p.squadRaw ? `[${p.squadRaw}]` : null, this.range, t('detail.firstLast', { first: p.firstSeen, last: p.lastSeen })].filter(Boolean).join(' · ')),
        this.tiles([
          ['col.kills', p.kills], ['col.deaths', p.deaths], ['col.teamKills.title', p.teamKills], ['col.vehicles.title', p.vehiclesDestroyed],
          ['tile.assists', p.killAssists], ['col.kd', fmt(p.kd)], ['col.accuracy', fmt(p.accuracy)], ['tile.missions', p.attendance],
          ['tile.shots', p.shotsFired], ...this.columns(false).map(c => [c.name, this.customValue(p, c), true]),
        ]),
        lk ? el('p', { class: 'note' }, t('detail.longestKill', { d: Math.round(lk.distance), victim: lk.victim, weapon: lk.weapon, date: lk.date })) : null,
        el('div', { class: 'lists' },
          this.list('list.weapons', d.weapons),
          this.list('list.victims', d.victims, (n) => this.openPlayer(n)),
          this.list('list.killedBy', d.killedBy, (n) => this.openPlayer(n)),
          this.list('list.squads', d.squads, (n) => this.openSquad(n))),
        this.dataTable(['col.date', 'col.mission', 'col.map', 'col.squad', 'col.side', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists'],
          d.missions.map(m => el('tr', {},
            el('td', {}, m.date), el('td', {}, this.replayLink(m.missionId, m.filename, m.name)), el('td', {}, m.world),
            el('td', {}, m.squadRaw || '-'), el('td', {}, m.side),
            el('td', {}, m.kills), el('td', {}, m.deaths), el('td', {}, m.teamKills), el('td', {}, m.vehiclesDestroyed), el('td', {}, m.killAssists)))),
      ];
    });
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
          ['col.kd', fmt(s.kd)], ['col.accuracy', fmt(s.accuracy)], ...this.columns(true).map(c => [c.name, this.customValue(s, c), true]),
        ]),
        el('div', { style: 'height:16px' }),
        this.dataTable(['col.name', 'col.tag', 'col.missions', 'col.k', 'col.d', 'col.teamKills', 'col.vehicles', 'col.assists', 'col.accuracy', 'col.kd'],
          d.members.map(p => {
            const tr = el('tr', { class: 'clickable' },
              el('td', {}, p.name), el('td', {}, p.squadRaw || '-'), el('td', {}, p.attendance),
              el('td', {}, p.kills), el('td', {}, p.deaths), el('td', {}, p.teamKills),
              el('td', {}, p.vehiclesDestroyed), el('td', {}, p.killAssists), el('td', {}, fmt(p.accuracy)), el('td', {}, fmt(p.kd)));
            tr.addEventListener('click', () => this.openPlayer(p.name));
            return tr;
          })),
      ];
    });
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
      'col.teamKills', 'col.attendance', 'col.accuracy', 'col.vehicles', 'col.assists', 'col.kd'].map(k => t(k)),
    ...this.columns(squads).map(c => c.name)];
  }

  exportExcel() {
    if (!this.data) return;
    const squads = this.viewMode === 'squads';
    const custom = (x) => this.columns(squads).map(c => (x.custom && x.custom[c.id] !== null && x.custom[c.id] !== undefined ? x.custom[c.id] : ''));
    const rows = this.items().map((x, i) => squads
      ? [i + 1, x.name || t('noSquad'), x.players, x.kills, x.deaths, x.teamKills, x.avgAttendance, x.accuracy, x.vehiclesDestroyed, x.killAssists, x.kd, ...custom(x)]
      : [i + 1, x.name, x.squadRaw || '-', x.kills, x.deaths, x.teamKills, x.attendance, x.accuracy, x.vehiclesDestroyed, x.killAssists, x.kd, ...custom(x)]);
    const sheet = t(squads ? 'xls.squads' : 'xls.players');
    const blob = window.xlsx.workbook(sheet, this.headerLabels(squads), rows, [5, 22, 14, 10, 10, 8, 13, 13, 8, 8, 8]);
    const url = URL.createObjectURL(blob);
    const a = el('a', { href: url, download: `${sheet}_${this.periodText().replace(/[^\d-]+/g, '_')}.xlsx` });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  reportShell(title, footer) {
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
        el('p', { style: 'color:#666;margin:0;font-size:12px;font-weight:700;text-transform:uppercase;letter-spacing:1px' }, t('rep.period')),
        el('p', { style: 'color:#fff;margin:5px 0 0;font-size:20px;font-weight:700' }, this.range))));
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
