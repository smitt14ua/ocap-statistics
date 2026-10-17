// Localisation shared by the statistics page and the admin panel. The strings live in
// i18n/<lang>.js, one file per language.
// The language is picked from the browser settings and can be switched manually; the choice
// is remembered in localStorage.
(function () {
  const LANGS = { en: 'English', be: 'Беларуская', cs: 'Čeština', de: 'Deutsch', kk: 'Қазақша', pl: 'Polski', uk: 'Українська' };
  const STORAGE_KEY = 'ocapstats.lang';

  // Filled by the per-language files in i18n/, which load before this script.
  const DICT = window.I18N || {};

  function stored() {
    try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
  }

  function detect() {
    const saved = stored();
    if (saved && LANGS[saved]) return saved;
    for (const l of navigator.languages || [navigator.language || 'en']) {
      const code = String(l).toLowerCase().split('-')[0];
      if (LANGS[code]) return code;
    }
    return 'en';
  }

  let current = detect();

  function t(key, vars) {
    let s = (DICT[current] && DICT[current][key]) ?? DICT.en[key] ?? key;
    if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
    return s;
  }

  function apply(root = document) {
    document.documentElement.lang = current;
    root.querySelectorAll('[data-i18n]').forEach(n => { n.textContent = t(n.dataset.i18n); });
    root.querySelectorAll('[data-i18n-placeholder]').forEach(n => { n.placeholder = t(n.dataset.i18nPlaceholder); });
    root.querySelectorAll('[data-i18n-title]').forEach(n => { n.title = t(n.dataset.i18nTitle); });
    root.querySelectorAll('[data-i18n-aria]').forEach(n => { n.setAttribute('aria-label', t(n.dataset.i18nAria)); });
  }

  function set(lang) {
    if (!LANGS[lang] || lang === current) return;
    current = lang;
    try { localStorage.setItem(STORAGE_KEY, lang); } catch { /* storage unavailable */ }
    apply();
    document.dispatchEvent(new CustomEvent('langchange', { detail: lang }));
  }

  // Fills a <select> with the languages and keeps it in sync.
  function bindSelect(select) {
    select.replaceChildren(...Object.entries(LANGS).map(([code, name]) => {
      const o = document.createElement('option');
      o.value = code;
      o.textContent = name;
      return o;
    }));
    select.value = current;
    select.addEventListener('change', () => set(select.value));
    document.addEventListener('langchange', () => { select.value = current; });
  }

  // Relative time like "5 min ago" / "in 3 h".
  function relTime(iso) {
    if (!iso) return '—';
    const sec = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
    const abs = Math.abs(sec);
    if (abs < 10) return t('rel.justNow');
    let span;
    if (abs < 60) span = t('rel.s', { n: abs });
    else if (abs < 3600) span = t('rel.m', { n: Math.floor(abs / 60) });
    else if (abs < 86400) span = t('rel.h', { n: Math.floor(abs / 3600) });
    else return new Date(iso).toLocaleString(current);
    return t(sec >= 0 ? 'rel.ago' : 'rel.in', { t: span });
  }

  window.i18n = { t, apply, set, bindSelect, relTime, get lang() { return current; }, langs: LANGS };
})();
