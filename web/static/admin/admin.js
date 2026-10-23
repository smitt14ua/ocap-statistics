(() => {
'use strict';

const { t } = window.i18n;
const rel = (iso) => window.i18n.relTime(iso);

const el = (tag, props = {}, ...children) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') node.className = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c !== null && c !== undefined && c !== false) node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};
const $ = (id) => document.getElementById(id);

const toast = (msg, isError = false) => {
  const n = el('div', { class: isError ? 'err' : '' }, msg);
  $('toasts').append(n);
  setTimeout(() => n.remove(), isError ? 7000 : 3500);
};

class UnauthorizedError extends Error { }

class ApiError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }

  // Localised text for known codes, the server message otherwise.
  get text() {
    const key = `err.${this.code}`;
    const s = t(key, { msg: this.message });
    return s === key ? this.message : s;
  }
}

async function api(method, path, body) {
  const opts = { method, headers: { 'X-Requested-With': 'ocap-stats' } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(`../api/admin/${path}`, opts);
  if (res.status === 401 && path !== 'login') throw new UnauthorizedError();
  if (res.status === 204) return null;
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new ApiError(data && data.code, (data && data.error) || `HTTP ${res.status}`);
  return data;
}

// Buttons with data-confirm (a translation key) need a second click within 4 s.
function confirmed(btn) {
  if (!btn.dataset.confirm) return true;
  if (btn.classList.contains('armed')) {
    btn.classList.remove('armed');
    window.i18n.apply(btn.parentElement);
    return true;
  }
  btn.textContent = t(btn.dataset.confirm);
  btn.classList.add('armed');
  setTimeout(() => {
    if (btn.classList.contains('armed')) {
      btn.classList.remove('armed');
      window.i18n.apply(btn.parentElement);
    }
  }, 4000);
  return false;
}

class Admin {
  constructor() {
    this.state = null;
    this.pollTimer = null;
    this.formDirty = false;
    this.previewRows = [];
    this.users = [];
  }

  async start() {
    window.i18n.bindSelect($('lang'));
    window.i18n.bindSelect($('langLogin'));
    window.i18n.apply();
    this.bind();
    try {
      const cfg = await fetch('../api/v1/config').then(r => r.json());
      this.siteTitle = cfg.title;
      if (cfg.logoUrl) $('logoSlot').replaceWith(el('img', { src: cfg.logoUrl, alt: '', class: 'logo' }));
    } catch { /* branding is optional */ }
    this.renderTitle();
    try {
      const me = await api('GET', 'me');
      this.enter(me.username);
    } catch {
      this.showLogin();
    }
  }

  renderTitle() {
    const title = this.siteTitle ? `${t('adm.title')} · ${this.siteTitle}` : t('adm.title');
    $('title').textContent = title;
    document.title = title;
  }

  showLogin() {
    clearTimeout(this.pollTimer);
    $('appView').hidden = true;
    $('loginView').hidden = false;
    $('loginUser').focus();
  }

  async enter(username) {
    $('whoami').textContent = username;
    $('loginView').hidden = true;
    $('appView').hidden = false;
    const tab = location.hash.slice(1);
    if (tab) this.showTab(tab);
    await this.refresh(true);
    this.loadUsers();
  }

  handle(e) {
    if (e instanceof UnauthorizedError) {
      toast(t('session.expired'), true);
      this.showLogin();
      return;
    }
    toast(e instanceof ApiError ? e.text : e.message, true);
  }

  bind() {
    $('loginForm').addEventListener('submit', async (ev) => {
      ev.preventDefault();
      $('loginError').textContent = '';
      try {
        const res = await api('POST', 'login', { username: $('loginUser').value.trim(), password: $('loginPass').value });
        $('loginPass').value = '';
        this.enter(res.username);
      } catch (e) {
        $('loginError').textContent = e instanceof ApiError ? e.text : e.message;
      }
    });
    $('logout').addEventListener('click', async () => {
      await api('POST', 'logout').catch(() => { });
      this.showLogin();
    });

    // Tabs follow the WAI-ARIA tabs pattern: arrows, Home and End move between tabs.
    const tabs = [...document.querySelectorAll('[role=tab]')];
    tabs.forEach((b, i) => {
      b.addEventListener('click', () => this.showTab(b.dataset.tab));
      b.addEventListener('keydown', (e) => {
        const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
        if (next === undefined) return;
        e.preventDefault();
        const tab = tabs[(next + tabs.length) % tabs.length];
        this.showTab(tab.dataset.tab);
        tab.focus();
      });
    });

    document.querySelectorAll('[data-task]').forEach(btn => btn.addEventListener('click', async () => {
      if (!confirmed(btn)) return;
      try {
        await api('POST', `tasks/${btn.dataset.task}`);
        toast(t('toast.queued'));
        this.refresh();
      } catch (e) { this.handle(e); }
    }));

    $('settingsForm').addEventListener('input', () => { this.formDirty = true; });
    $('sHitAssists').addEventListener('change', () => { $('sAssistWindow').disabled = !$('sHitAssists').checked; });
    $('nickForm').addEventListener('input', () => { this.formDirty = true; });
    $('settingsForm').addEventListener('submit', (ev) => { ev.preventDefault(); this.save(); });
    $('nickForm').addEventListener('submit', (ev) => { ev.preventDefault(); this.save(); });
    $('resetSettings').addEventListener('click', async (ev) => {
      if (!confirmed(ev.currentTarget)) return;
      try {
        const res = await api('DELETE', 'settings');
        this.formDirty = false;
        this.afterSave(res);
      } catch (e) { this.handle(e); }
    });
    $('addColumn').addEventListener('click', () => { this.addColumn({ target: 'both', decimals: 2 }); this.formDirty = true; });
    $('saveColumns').addEventListener('click', () => this.save());
    $('columns').addEventListener('input', () => { this.formDirty = true; });
    this.renderVarList();
    $('addSide').addEventListener('click', () => { this.addSide({ name: '', color: '#888888', squads: [] }); this.formDirty = true; });

    $('nPreset').addEventListener('change', () => {
      if ($('nPreset').value) $('nPattern').value = $('nPreset').value;
      else $('nPattern').focus();
      this.formDirty = true;
    });
    $('nPattern').addEventListener('input', () => this.syncPreset());
    $('preview').addEventListener('click', () => this.preview());
    $('previewFilter').addEventListener('input', () => this.renderPreview());

    $('addUser').addEventListener('submit', async (ev) => {
      ev.preventDefault();
      const u = $('newUser').value.trim();
      try {
        await api('POST', 'users', { username: u, password: $('newPass').value });
        toast(t('user.added', { u }));
        $('newUser').value = '';
        $('newPass').value = '';
        this.loadUsers();
      } catch (e) { this.handle(e); }
    });

    document.addEventListener('langchange', () => {
      this.renderTitle();
      if (this.state) { this.renderStatus(); this.renderCustomNote(); }
      if (this.previewRows.length) this.renderPreview();
      if (this.users.length) this.renderUsers();
      document.querySelectorAll('#sides .side, #columns .column-card').forEach(row => window.i18n.apply(row));
      this.renderVarList();
      this.renderColumnsEmpty();
      if (this.state) this.renderSortOptions(this.collectSettings().sort);
    });
  }

  showTab(name) {
    if (!$(`tab-${name}`)) return;
    document.querySelectorAll('[role=tab]').forEach(b => {
      const active = b.dataset.tab === name;
      b.classList.toggle('active', active);
      b.setAttribute('aria-selected', String(active));
      b.tabIndex = active ? 0 : -1;
    });
    document.querySelectorAll('.tab').forEach(tab => tab.classList.toggle('active', tab.id === `tab-${name}`));
    history.replaceState(null, '', `#${name}`);
  }

  // --- status -------------------------------------------------------------

  async refresh(fillForms = false) {
    clearTimeout(this.pollTimer);
    try {
      this.state = await api('GET', 'state');
    } catch (e) {
      this.handle(e);
      return;
    }
    this.renderStatus();
    if (fillForms || !this.formDirty) this.fillForms();
    const busy = this.state.sync.running || this.state.sync.queued.length > 0;
    this.pollTimer = setTimeout(() => this.refresh(), busy ? 2000 : 15000);
  }

  renderStatus() {
    const { missions: m, sync: s, service, failed } = this.state;
    const tile = (label, value, cls = '') => el('div', { class: `tile ${cls}` }, el('div', { class: 'v' }, value), el('div', { class: 'l' }, t(label)));
    $('tiles').replaceChildren(
      tile('st.done', m.done),
      tile('st.pending', m.pending),
      tile('st.errors', m.error, m.error ? 'bad' : ''),
      tile('st.missing', m.missing || 0),
      tile('st.lastSync', rel(s.lastSuccess)));

    const taskName = (task) => t(`task.${task}`);
    const box = $('taskBox');
    if (s.running) {
      const pct = s.progress.total ? Math.round(s.progress.done / s.progress.total * 100) : 0;
      box.replaceChildren(
        el('div', { class: 'row', style: 'margin-bottom:6px' },
          el('span', { class: 'badge warn' }, t('st.running')),
          el('b', {}, taskName(s.task)),
          el('span', { class: 'muted' }, s.progress.total ? `${s.progress.done} / ${s.progress.total}` : t('st.fetchingList'))),
        el('div', { class: 'progress' }, el('div', { style: `width:${pct}%` })));
    } else {
      box.replaceChildren(el('div', { class: 'row' },
        el('span', { class: 'badge ok' }, t('st.idle')),
        s.nextRun ? el('span', { class: 'muted' }, t('st.nextSync', { when: rel(s.nextRun) })) : null,
        s.lastError ? el('span', { class: 'bad' }, t('st.lastError', { e: s.lastError })) : null));
    }
    if (s.queued.length) {
      box.append(el('div', { class: 'muted', style: 'margin-top:6px' }, t('st.queued', { tasks: s.queued.map(taskName).join(', ') })));
    }
    document.querySelectorAll('[data-task]').forEach(b => { b.disabled = s.running && b.dataset.task === s.task; });

    const kv = [
      ['svc.ocap', service.ocapUrl],
      ['svc.dataDir', service.dataDir || t('svc.http')],
      ['svc.auto', service.syncEnabled ? t('svc.every', { i: service.syncInterval }) : t('svc.off')],
      ['svc.concurrency', service.concurrency],
      ['svc.lastRun', s.lastFinish ? t('svc.lastRunText', { date: new Date(s.lastFinish).toLocaleString(window.i18n.lang), p: s.lastProcessed, f: s.lastFailed }) : '—'],
    ];
    $('serviceInfo').replaceChildren(...kv.flatMap(([k, v]) => [el('dt', {}, t(k)), el('dd', {}, v)]));

    $('failedBody').replaceChildren(...(failed.length ? failed.map(f => el('tr', {},
      el('td', {}, f.id), el('td', { class: 'mono' }, f.filename), el('td', {}, f.date), el('td', {}, f.attempts),
      el('td', { class: f.status === 'missing' ? 'muted' : 'bad' }, f.status === 'missing' ? t('failed.missing') : f.lastError),
      el('td', {}, f.status === 'missing' ? '—' : rel(f.nextAttempt))))
      : [el('tr', {}, el('td', { colspan: 6, class: 'muted' }, t('failed.none')))]));
  }

  // --- settings -------------------------------------------------------------

  renderCustomNote() {
    const custom = this.state.custom;
    $('customNote').replaceChildren(
      el('span', { class: `badge ${custom ? 'warn' : 'ok'}` }, t(custom ? 'set.custom' : 'set.fromEnv')), ' ',
      t(custom ? 'set.customText' : 'set.fromEnvText'));
  }

  fillForms() {
    const s = this.state.settings;
    this.renderCustomNote();
    $('sTags').value = (s.tags || []).join(', ');
    $('sDefaultTag').value = s.defaultTag || '';
    $('sSince').value = s.since || '';
    $('sMinDuration').value = Math.round((s.minDuration || 0) / 60);
    $('sMinMissions').value = s.minMissions || 0;
    const assists = s.assists || { hits: true, window: 300 };
    $('sHitAssists').checked = !!assists.hits;
    $('sAssistWindow').value = (assists.window || 300) / 60;
    $('sAssistWindow').disabled = !assists.hits;
    const medical = s.medical || { treatment: 1, revive: 5 };
    $('sMedTreatment').value = medical.treatment;
    $('sMedRevive').value = medical.revive;
    $('sRotationSince').value = s.rotation.since || '';
    $('sides').replaceChildren();
    (s.rotation.sides || []).forEach(side => this.addSide(side));

    $('nPattern').value = s.nickname.squadPattern;
    this.syncPreset();
    $('nAliases').value = Object.entries(s.nickname.aliases || {}).map(([k, v]) => `${k} = ${v}`).join('\n');
    $('nIgnore').value = (s.nickname.ignoreNames || []).join('\n');
    $('columns').replaceChildren();
    (s.columns || []).forEach(c => this.addColumn(c));
    this.renderColumnsEmpty();
    this.renderSortOptions(s.sort || {});
    this.formDirty = false;
  }

  syncPreset() {
    const v = $('nPattern').value;
    const match = [...$('nPreset').options].find(o => o.value && o.value === v);
    $('nPreset').value = match ? match.value : '';
  }

  addSide(side) {
    const row = el('div', { class: 'side' },
      el('label', { class: 'field' }, el('span', { 'data-i18n': 'side.name' }), el('input', { 'data-k': 'name', value: side.name })),
      el('label', { class: 'field' }, el('span', { 'data-i18n': 'side.color' }), el('input', { 'data-k': 'color', type: 'color', value: side.color || '#888888' })),
      el('label', { class: 'field' }, el('span', { 'data-i18n': 'side.squads' }), el('input', { 'data-k': 'squads', value: (side.squads || []).join(', ') })),
      el('button', { type: 'button', class: 'small danger', 'data-i18n-title': 'side.remove', 'data-i18n-aria': 'side.remove', onclick: () => { row.remove(); this.formDirty = true; } }, '✕'));
    window.i18n.apply(row);
    $('sides').append(row);
  }

  // --- default sort ---------------------------------------------------------------

  // renderSortOptions fills the default sort selects: the visible table columns plus saved
  // custom columns shown in that table.
  renderSortOptions(sort) {
    const base = {
      players: [['kills', 'col.kills'], ['deaths', 'col.deaths'], ['teamKills', 'col.teamKills.title'], ['attendance', 'col.attendance'],
        ['accuracy', 'col.accuracy'], ['vehiclesDestroyed', 'col.vehicles.title'], ['killAssists', 'tile.assists'], ['medicalPoints', 'tile.medical'],
        ['kd', 'col.kd'], ['name', 'col.name'], ['squad', 'col.squad']],
      squads: [['kills', 'col.kills'], ['deaths', 'col.deaths'], ['teamKills', 'col.teamKills.title'], ['attendance', 'col.attendance'],
        ['accuracy', 'col.accuracy'], ['vehiclesDestroyed', 'col.vehicles.title'], ['killAssists', 'tile.assists'], ['medicalPoints', 'tile.medical'],
        ['kd', 'col.kd'], ['players', 'col.players'], ['name', 'col.squad']],
    };
    const saved = (this.state && this.state.settings.columns) || [];
    for (const [table, fieldId, orderId] of [['players', 'sortPlayersField', 'sortPlayersOrder'], ['squads', 'sortSquadsField', 'sortSquadsOrder']]) {
      const current = (sort[table] && sort[table].field) || $(fieldId).value || 'kills';
      const custom = saved.filter(c => c.target === 'both' || c.target === table);
      $(fieldId).replaceChildren(
        ...base[table].map(([v, key]) => el('option', { value: v }, t(key))),
        ...custom.map(c => el('option', { value: `custom.${c.id}` }, c.name)));
      $(fieldId).value = [...$(fieldId).options].some(o => o.value === current) ? current : 'kills';
      if (sort[table] && sort[table].order) $(orderId).value = sort[table].order;
    }
  }

  // --- custom columns ------------------------------------------------------------

  renderColumnsEmpty() {
    const box = $('columns');
    box.querySelector('.empty-cols')?.remove();
    if (!box.querySelector('.column-card')) box.append(el('p', { class: 'muted empty-cols' }, t('cols.empty')));
  }

  addColumn(col) {
    const id = `col-${Math.random().toString(36).slice(2, 8)}`;
    const target = el('select', { 'data-k': 'target', id: `${id}-target` },
      ['both', 'players', 'squads'].map(v => el('option', { value: v, 'data-i18n': `cols.target.${v}` })));
    target.value = col.target || 'both';
    const formula = el('textarea', { 'data-k': 'formula', id: `${id}-formula`, class: 'mono', spellcheck: 'false', rows: 2 });
    formula.value = col.formula || '';
    const preview = el('div', { class: 'preview', 'aria-live': 'polite' });
    const card = el('div', { class: 'column-card', 'data-id': col.id || '' },
      el('div', { class: 'grid' },
        el('label', { class: 'field' }, el('span', { 'data-i18n': 'cols.name' }), el('input', { type: 'text', 'data-k': 'name', value: col.name || '', maxlength: 40 })),
        el('label', { class: 'field' }, el('span', { 'data-i18n': 'cols.target' }), target),
        el('label', { class: 'field' }, el('span', { 'data-i18n': 'cols.decimals' }), el('input', { type: 'number', 'data-k': 'decimals', min: 0, max: 6, value: col.decimals ?? 2 }))),
      el('label', { class: 'field', style: 'margin-top:8px' }, el('span', { 'data-i18n': 'cols.formula' }), formula),
      el('div', { class: 'row', style: 'margin-top:8px' },
        el('button', { type: 'button', class: 'small', 'data-i18n': 'cols.check', onclick: () => this.previewColumn(card, preview) }),
        el('span', { class: 'spacer' }),
        el('button', { type: 'button', class: 'small danger', 'data-i18n': 'cols.remove', onclick: () => { card.remove(); this.formDirty = true; this.renderColumnsEmpty(); } })),
      preview);
    window.i18n.apply(card);
    $('columns').querySelector('.empty-cols')?.remove();
    $('columns').append(card);
  }

  columnFrom(card) {
    return {
      id: card.dataset.id || '',
      name: card.querySelector('[data-k=name]').value.trim(),
      target: card.querySelector('[data-k=target]').value,
      decimals: Number(card.querySelector('[data-k=decimals]').value || 0),
      formula: card.querySelector('[data-k=formula]').value.trim(),
    };
  }

  async previewColumn(card, box) {
    const c = this.columnFrom(card);
    try {
      const res = await api('POST', 'preview-column', { formula: c.formula, squads: c.target === 'squads', decimals: c.decimals });
      box.replaceChildren(el('div', { class: 'muted' }, t('cols.previewTitle')),
        el('table', {}, el('tbody', {}, res.rows.map(r => el('tr', {},
          el('td', {}, r.name || t('noSquad')), el('td', { class: 'mono' }, r.value === null ? '—' : Number(r.value).toFixed(c.decimals)))))));
    } catch (e) {
      if (e instanceof UnauthorizedError) return this.handle(e);
      box.replaceChildren(el('div', { class: 'bad' }, e instanceof ApiError && e.code === 'invalid_formula' ? t('err.invalid_formula', { msg: e.message }) : e.message));
    }
  }

  renderVarList() {
    const vars = ['kills', 'deaths', 'teamKills', 'vehiclesDestroyed', 'killAssists', 'shotsFired', 'hits', 'accuracy', 'kd', 'attendance', 'players', 'squadMissions', 'maxAttendance', 'missions', 'isSquad',
      'treatments', 'revives', 'medicalPoints'];
    $('varList').replaceChildren(...vars.flatMap(v => [el('dt', { class: 'mono' }, v), el('dd', {}, t(`var.${v}`))]));
  }

  splitList(v, sep = /[,\n]/) {
    return v.split(sep).map(x => x.trim()).filter(Boolean);
  }

  collectNickname() {
    const aliases = {};
    for (const line of $('nAliases').value.split('\n')) {
      const i = line.indexOf('=');
      if (i < 0) continue;
      const from = line.slice(0, i).trim(), to = line.slice(i + 1).trim();
      if (from && to) aliases[from] = to;
    }
    return { squadPattern: $('nPattern').value, aliases, ignoreNames: this.splitList($('nIgnore').value, /\n/) };
  }

  collectSettings() {
    return {
      tags: this.splitList($('sTags').value),
      defaultTag: $('sDefaultTag').value.trim(),
      since: $('sSince').value,
      minDuration: Number($('sMinDuration').value || 0) * 60,
      minMissions: Math.max(0, Math.round(Number($('sMinMissions').value || 0))),
      nickname: this.collectNickname(),
      assists: { hits: $('sHitAssists').checked, window: Math.round(Number($('sAssistWindow').value || 0) * 60) },
      medical: { treatment: Number($('sMedTreatment').value || 0), revive: Number($('sMedRevive').value || 0) },
      columns: [...$('columns').querySelectorAll('.column-card')].map(card => this.columnFrom(card)),
      sort: {
        players: { field: $('sortPlayersField').value, order: $('sortPlayersOrder').value },
        squads: { field: $('sortSquadsField').value, order: $('sortSquadsOrder').value },
      },
      rotation: {
        since: $('sRotationSince').value,
        sides: [...$('sides').children].map(row => ({
          name: row.querySelector('[data-k=name]').value.trim(),
          color: row.querySelector('[data-k=color]').value,
          squads: this.splitList(row.querySelector('[data-k=squads]').value),
        })),
      },
    };
  }

  async save() {
    try {
      const res = await api('PUT', 'settings', this.collectSettings());
      this.formDirty = false;
      this.afterSave(res);
    } catch (e) { this.handle(e); }
  }

  afterSave(res) {
    const parts = [t('saved')];
    if (res.change.rules) parts.push(t('saved.recompute'));
    if (res.change.sync) parts.push(t('saved.sync'));
    toast(parts.join(', '));
    this.refresh(true);
  }

  // --- nickname preview ---------------------------------------------------------

  async preview() {
    try {
      const res = await api('POST', 'preview-names', { nickname: this.collectNickname() });
      this.previewRows = res.names;
      $('previewBox').hidden = false;
      this.renderPreview();
    } catch (e) { this.handle(e); }
  }

  renderPreview() {
    const all = this.previewRows;
    const term = $('previewFilter').value.trim().toLowerCase();
    const rows = all.filter(r => !term || r.raw.toLowerCase().includes(term) || r.squad.toLowerCase().includes(term));
    const squads = new Set(all.filter(r => r.squad).map(r => r.squad.toLowerCase()));
    $('previewSummary').textContent = t('preview.summary', { n: all.length, s: squads.size, solo: all.filter(r => !r.squad && r.counted).length });
    $('previewBody').replaceChildren(...rows.map(r => el('tr', {},
      el('td', { class: 'mono' }, r.raw),
      el('td', {}, r.name),
      el('td', {}, r.squad || el('span', { class: 'muted' }, t('noSquad'))),
      el('td', {}, r.counted ? '' : el('span', { class: 'warn' }, t('notCounted'))))));
  }

  // --- users ----------------------------------------------------------------------

  async loadUsers() {
    try {
      this.users = (await api('GET', 'users')).users;
      this.renderUsers();
    } catch (e) { this.handle(e); }
  }

  renderUsers() {
    const users = this.users;
    const me = $('whoami').textContent.toLowerCase();
    $('usersBody').replaceChildren(...users.map(u => {
      const isMe = u.username.toLowerCase() === me;
      const del = el('button', { class: 'small danger', 'data-confirm': 'btn.deleteConfirm', 'data-i18n': 'btn.delete', disabled: isMe || users.length <= 1 });
      del.addEventListener('click', async () => {
        if (!confirmed(del)) return;
        try {
          await api('DELETE', `users/${encodeURIComponent(u.username)}`);
          toast(t('user.deleted', { u: u.username }));
          this.loadUsers();
        } catch (e) { this.handle(e); }
      });
      const pass = el('input', { type: 'password', class: 'inline', placeholder: t('ph.newPass'), 'aria-label': `${t('ph.newPass')}: ${u.username}`, autocomplete: 'new-password', style: 'max-width:200px' });
      const setPass = el('button', { class: 'small' }, t('btn.setPass'));
      setPass.addEventListener('click', async () => {
        try {
          await api('PUT', `users/${encodeURIComponent(u.username)}/password`, { password: pass.value });
          pass.value = '';
          if (isMe) {
            toast(t('pass.changedSelf'));
            this.showLogin();
          } else {
            toast(t('pass.changed', { u: u.username }));
          }
        } catch (e) { this.handle(e); }
      });
      const actions = el('div', { class: 'row' }, pass, setPass, del);
      window.i18n.apply(actions);
      return el('tr', {},
        el('td', {}, el('b', {}, u.username), isMe ? el('span', { class: 'muted' }, ` ${t('users.you')}`) : null),
        el('td', {}, new Date(u.createdAt).toLocaleDateString(window.i18n.lang)),
        el('td', {}, actions));
    }));
  }
}

new Admin().start();
})();
