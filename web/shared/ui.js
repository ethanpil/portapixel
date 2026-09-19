/* PortaPixel shared UI helpers.
   Plain ES module. No dependencies. No build step.

   The DOM builder never accepts HTML strings, so data cannot become markup.
   Set text with children or the `text` attribute.
*/

/* ------------------------------------------------------------ DOM builder */

const PROPS = new Set(['value', 'checked', 'selected', 'disabled', 'indeterminate', 'readOnly', 'htmlFor']);

/** Build an element. `attrs` holds attributes, `on*` listeners, `style`, `dataset`.
    Children can be strings, numbers, Nodes, arrays, or null. */
export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) applyAttrs(el, attrs);
  append(el, children);
  return el;
}

function applyAttrs(el, attrs) {
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined) continue;
    if (k === 'text') { el.textContent = String(v); continue; }
    if (k === 'class' || k === 'className') { el.className = v; continue; }
    if (k === 'style') {
      if (typeof v === 'string') el.setAttribute('style', v);
      else for (const [p, pv] of Object.entries(v)) el.style.setProperty(p, pv);
      continue;
    }
    if (k === 'dataset') { for (const [p, pv] of Object.entries(v)) el.dataset[p] = pv; continue; }
    if (k.startsWith('on') && typeof v === 'function') { el.addEventListener(k.slice(2).toLowerCase(), v); continue; }
    if (PROPS.has(k)) { el[k] = v; continue; }
    if (v === false) continue;
    el.setAttribute(k, v === true ? '' : String(v));
  }
}

function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false || c === true) continue;
    if (Array.isArray(c)) { append(el, c); continue; }
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

/** Empty an element and put new children in it. */
export function fill(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

/* ----------------------------------------------------------------- Icons */
/* Small inline SVG set. The Latin font subset has no glyph for the drag
   handle, and a drawn icon scales better than a character. */

const PATHS = {
  drag: 'M3 4.5h10M3 8h10M3 11.5h10',
  up: 'M3 9.5 8 4.5l5 5',
  down: 'M3 6.5 8 11.5l5-5',
  close: 'M4 4l8 8M12 4l-8 8',
  plus: 'M8 3v10M3 8h10',
  image: 'M2.5 3.5h11v9h-11zM2.5 10l3-3 3 3 2-2 3 3',
  video: 'M2.5 3.5h11v9h-11zM6.5 6l4 2-4 2z',
  url: 'M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11M2.5 8h11M8 2.5c1.6 1.5 2.4 3.4 2.4 5.5S9.6 12 8 13.5C6.4 12 5.6 10.1 5.6 8S6.4 4 8 2.5',
};

/** One of: drag, up, down, close, plus, image, video, url. */
export function icon(name, size = 16) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  const p = document.createElementNS(ns, 'path');
  p.setAttribute('d', PATHS[name] || PATHS.plus);
  p.setAttribute('fill', 'none');
  p.setAttribute('stroke', 'currentColor');
  p.setAttribute('stroke-width', '1.5');
  p.setAttribute('stroke-linecap', 'round');
  p.setAttribute('stroke-linejoin', 'round');
  svg.append(p);
  return svg;
}

/* ----------------------------------------------------------------- Format */

/** Bytes as a short human string, for example "3.4 GB". */
export function fmtBytes(n) {
  if (n === null || n === undefined || Number.isNaN(Number(n))) return '—';
  n = Number(n);
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** Seconds as a short human string, for example "2m 13s". */
export function fmtDuration(seconds) {
  if (seconds === null || seconds === undefined || Number.isNaN(Number(seconds))) return '—';
  let s = Math.max(0, Math.round(Number(seconds)));
  if (s < 60) return `${s}s`;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); s -= m * 60;
  if (h) return `${h}h ${String(m).padStart(2, '0')}m`;
  return s ? `${m}m ${s}s` : `${m}m`;
}

/** How long ago a time was, for example "40 seconds ago". */
export function fmtAgo(when) {
  const t = when instanceof Date ? when.getTime() : Date.parse(when);
  if (Number.isNaN(t)) return '—';
  const d = Math.round((Date.now() - t) / 1000);
  if (d < 5) return 'just now';
  if (d < 60) return `${d} seconds ago`;
  const steps = [[60, 'minute'], [3600, 'hour'], [86400, 'day'], [604800, 'week']];
  for (let i = 0; i < steps.length; i++) {
    const [unit, name] = steps[i];
    const next = steps[i + 1];
    if (!next || d < next[0]) {
      const n = Math.floor(d / unit);
      return `${n} ${name}${n === 1 ? '' : 's'} ago`;
    }
  }
  return new Date(t).toLocaleDateString();
}

/* ------------------------------------------------------------ Small parts */

/** A status dot. Kind: ok, alert, busy or quiet. */
export function statusDot(kind = 'quiet') {
  return h('span', { class: `pp-dot pp-dot--${kind}`, 'aria-hidden': 'true' });
}

/** A pill badge. Kind: plain (default), brand, warn or danger. */
export function badge(text, kind) {
  const cls = ['pp-badge'];
  if (kind && kind !== 'plain') cls.push(`pp-badge--${kind}`);
  return h('span', { class: cls.join(' '), text });
}

/** A notice banner. Kind: info, warn, danger or paired.
    `body` and `title` take text or nodes. `actions` takes buttons. */
export function banner({ kind = 'info', title, body, actions } = {}) {
  return h('div', { class: `pp-banner pp-banner--${kind}`, role: kind === 'danger' ? 'alert' : null },
    h('div', { class: 'pp-banner__text' },
      title ? h('div', { class: 'pp-banner__title' }, title) : null,
      body ? h('div', { class: 'pp-banner__body' }, body) : null),
    actions && actions.length ? h('div', { class: 'pp-banner__actions' }, actions) : null);
}

/** Key and value rows. Pairs take [label, value] or {k, v, kind}. */
export function factList(pairs) {
  return h('div', { class: 'pp-facts' }, pairs.filter(Boolean).map((p) => {
    const { k, v, kind } = Array.isArray(p) ? { k: p[0], v: p[1] } : p;
    return h('div', { class: `pp-facts__row${kind ? ` pp-facts__row--${kind}` : ''}` },
      h('span', { class: 'pp-facts__k' }, k),
      h('span', { class: 'pp-facts__v' }, v));
  }));
}

/** A progress bar. The returned element carries set(value) for 0 to 1. */
export function progress(value = 0, opts = {}) {
  const bar = h('div', { class: 'pp-progress__bar' });
  const el = h('div', {
    class: `pp-progress${opts.thin ? ' pp-progress--thin' : ''}${opts.brand ? ' pp-progress--brand' : ''}`,
    role: 'progressbar', 'aria-valuemin': '0', 'aria-valuemax': '100',
  }, bar);
  el.set = (v) => {
    const pct = Math.round(Math.min(1, Math.max(0, Number(v) || 0)) * 100);
    bar.style.width = `${pct}%`;
    el.setAttribute('aria-valuenow', String(pct));
  };
  el.set(value);
  return el;
}

/** A spinner. Give it a label so a screen reader says what it waits for. */
export function spinner(label) {
  return h('span', { class: 'pp-spinner', role: 'status', 'aria-label': label || 'Working' });
}

/* ----------------------------------------------------------------- Tables */

/** Build a table card.
    columns: [{key, label, width, grow, mono, align, render(row)}]
    rows:    array of objects
    empty:   text or node shown when rows is empty
    onRowClick(row): makes each row a button
    rowAlert(row):   text or node shown in an inset alert under the row
    foot:    node put in the footer bar */
export function table({ columns, rows, empty, onRowClick, rowAlert, foot } = {}) {
  const inner = h('div', { class: 'pp-table__inner' });
  inner.append(h('div', { class: 'pp-table__head' }, columns.map((c) => cell(c, h('span', { text: c.label || '' })))));

  if (!rows || rows.length === 0) {
    inner.append(h('div', { class: 'pp-empty' }, h('div', { class: 'pp-empty__body' }, empty || 'Nothing here yet')));
  } else {
    for (const row of rows) {
      const cells = columns.map((c) => cell(c, c.render ? c.render(row) : String(row[c.key] ?? '')));
      const line = onRowClick
        ? h('button', { type: 'button', class: 'pp-table__row', onClick: () => onRowClick(row) }, cells)
        : h('div', { class: 'pp-table__row' }, cells);
      inner.append(line);
      const alert = rowAlert && rowAlert(row);
      if (alert) inner.append(h('div', { class: 'pp-table__alert' }, alert));
    }
  }

  return h('div', { class: 'pp-card' },
    h('div', { class: 'pp-table' }, inner),
    foot ? h('div', { class: 'pp-card__foot' }, foot) : null);
}

function cell(c, content) {
  const cls = ['pp-cell'];
  if (c.grow) cls.push('pp-cell--grow');
  if (c.mono) cls.push('pp-cell--mono');
  if (c.align === 'right') cls.push('pp-cell--right');
  const el = h('span', { class: cls.join(' ') }, content);
  if (c.width && !c.grow) { el.style.flex = 'none'; el.style.width = c.width; }
  return el;
}

/* ------------------------------------------------------------------ Toast */

let toastEl = null;
let toastTimer = 0;

/** Show one short message at the bottom of the page. A new one replaces the
    old one, as in the wireframes. Kind: info (default) or danger. */
export function toast(msg, kind = 'info') {
  if (!toastEl) {
    toastEl = h('div', { class: 'pp-toast', role: 'status', 'aria-live': 'polite' });
    document.body.append(toastEl);
  }
  clearTimeout(toastTimer);
  toastEl.className = `pp-toast${kind === 'danger' ? ' pp-toast--danger' : ''}`;
  toastEl.textContent = msg;
  toastEl.style.display = '';
  // Restart the entrance animation.
  toastEl.style.animation = 'none';
  void toastEl.offsetWidth;
  toastEl.style.animation = '';
  toastTimer = setTimeout(() => { toastEl.style.display = 'none'; }, 2600);
}

/* ------------------------------------------------------------------ Modal */

let modalOpen = null;

/** Show a modal dialog. Returns a promise with the value of the button the
    user pressed, or null if the user cancelled.
    actions: [{label, value, kind, autofocus}] — kind maps to a button class.
    onOpen(dialog, buttons): runs once the dialog is in the page. */
export function modal({ title, body, actions, wide, onOpen } = {}) {
  if (modalOpen) modalOpen();           // one dialog at a time
  const acts = actions && actions.length ? actions : [{ label: 'Close', value: null }];
  const lastFocus = document.activeElement;

  return new Promise((resolve) => {
    const titleId = `pp-modal-title-${Math.random().toString(36).slice(2, 8)}`;
    const btns = acts.map((a) => h('button', {
      type: 'button',
      class: `pp-btn${a.kind ? ` pp-btn--${a.kind}` : ''}`,
      text: a.label,
      onClick: () => done(a.value === undefined ? a.label : a.value),
    }));

    const dialog = h('div', {
      class: 'pp-modal', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': titleId,
      tabindex: '-1', style: wide ? { 'max-width': '620px' } : null,
      onKeydown: trap,
    },
      h('div', { class: 'pp-modal__title', id: titleId }, title || ''),
      body ? h('div', { class: 'pp-modal__body' }, body) : null,
      h('div', { class: 'pp-modal__actions' }, btns));

    const overlay = h('div', {
      class: 'pp-overlay',
      onMousedown: (e) => { if (e.target === overlay) done(null); },
    }, dialog);

    function trap(e) {
      if (e.key === 'Escape') { e.preventDefault(); done(null); return; }
      if (e.key !== 'Tab') return;
      const f = focusable(dialog);
      if (f.length === 0) return;
      const first = f[0], last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    }

    function done(value) {
      if (modalOpen !== close) return;
      close();
      resolve(value);
    }
    function close() {
      modalOpen = null;
      overlay.remove();
      if (lastFocus && lastFocus.focus) lastFocus.focus();
    }
    modalOpen = close;

    document.body.append(overlay);
    if (onOpen) onOpen(dialog, btns);
    const wanted = acts.findIndex((a) => a.autofocus);
    const auto = dialog.querySelector('[autofocus]');
    (auto || btns[wanted >= 0 ? wanted : btns.length - 1] || dialog).focus();
  });
}

function focusable(root) {
  return [...root.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')]
    .filter((el) => !el.disabled && el.offsetParent !== null);
}

/** Ask a yes or no question. Returns true when the user confirms. */
export async function confirmDialog({ title, body, confirm = 'Yes', cancel = 'Cancel', kind = 'primary' } = {}) {
  const v = await modal({
    title, body,
    actions: [
      { label: cancel, value: false },
      { label: confirm, value: true, kind, autofocus: true },
    ],
  });
  return v === true;
}

/** Ask the user to type a value back before a dangerous action runs.
    Used by install-to-disk and unpair. Returns true when the typed value
    matches `expect` exactly. */
export function typedConfirm({ title, body, expect, confirm = 'Confirm', label } = {}) {
  const hintId = `pp-typed-${Math.random().toString(36).slice(2, 8)}`;
  const input = h('input', {
    class: 'pp-input pp-input--mono', type: 'text', autocomplete: 'off',
    spellcheck: 'false', autofocus: true, 'aria-describedby': hintId,
  });
  const wrap = h('div', null,
    body ? h('div', null, body) : null,
    h('label', { class: 'pp-label', style: { 'margin-top': '14px', display: 'block' } },
      label || 'Type ', h('span', { class: 'pp-mono', style: { color: 'var(--pp-ink)' } }, expect), ' to go on',
      input),
    h('div', { class: 'pp-help', id: hintId }, 'It must match exactly.'));

  return modal({
    title, body: wrap,
    actions: [
      { label: 'Cancel', value: false },
      { label: confirm, value: 'go', kind: 'danger' },
    ],
    // Block the dangerous button until the typed text matches.
    onOpen: (dialog, btns) => {
      const btn = btns[1];
      const sync = () => { btn.disabled = input.value !== expect; };
      input.addEventListener('input', sync);
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && input.value === expect) { e.preventDefault(); btn.click(); }
      });
      sync();
    },
  }).then((v) => v === 'go');
}

/* ----------------------------------------------------------------- Router */

/** Minimal hash router.
    map: {'': fn, dashboard: fn, 'screens/:id': fn} — one colon segment allowed
    per key, passed to the handler as a string.
    Sets aria-current="page" on every [data-nav] whose value is the active key.
    Returns {go(path), current(), refresh()}. */
export function route(map, opts = {}) {
  const fallback = opts.fallback || map[''] || Object.values(map)[0];
  const keys = Object.keys(map);
  let current = '';

  function run() {
    const path = location.hash.replace(/^#\/?/, '');
    const parts = path.split('/').filter(Boolean);
    let hit = null, arg = null, key = '';

    for (const k of keys) {
      const kp = k.split('/').filter(Boolean);
      if (kp.length !== parts.length) continue;
      let arg1 = null, ok = true;
      for (let i = 0; i < kp.length; i++) {
        if (kp[i].startsWith(':')) arg1 = decodeURIComponent(parts[i]);
        else if (kp[i] !== parts[i]) { ok = false; break; }
      }
      if (ok) { hit = map[k]; arg = arg1; key = k; break; }
    }

    current = key;
    const top = (key || path).split('/')[0];
    for (const el of document.querySelectorAll('[data-nav]')) {
      if (el.dataset.nav === top) el.setAttribute('aria-current', 'page');
      else el.removeAttribute('aria-current');
    }
    // On a phone the nav is a scrolling chip row. Bring the active chip into
    // view without moving the page.
    const chip = document.querySelector(`.pp-chipnav [data-nav="${CSS.escape(top)}"]`);
    if (chip && chip.parentElement.scrollWidth > chip.parentElement.clientWidth) {
      const bar = chip.parentElement;
      bar.scrollLeft = chip.offsetLeft - (bar.clientWidth - chip.offsetWidth) / 2;
    }
    (hit || fallback)(arg);
  }

  addEventListener('hashchange', run);
  run();
  return {
    go(path) {
      const want = `#/${String(path).replace(/^#\/?/, '')}`;
      if (location.hash === want) run(); else location.hash = want;
    },
    current: () => current,
    refresh: run,
  };
}

/* ------------------------------------------------------------------ Shell */

/** Build the top bar, the sidebar and the phone chip row that both admin UIs
    share. Returns {el, main, topbar, sidebar}.
    brand:    the product name, default "PortaPixel"
    subtitle: text or nodes next to the brand, after a divider
    tools:    nodes on the right of the top bar
    nav:      [{id, label}] — the id is the hash route and the data-nav value
    footer:   nodes for the block under the sidebar links
    wide:     true gives the wider main column the server UI uses */
export function renderShell({ brand = 'PortaPixel', subtitle, tools, nav = [], footer, wide } = {}) {
  const navButtons = () => nav.map((n) => h('a', {
    class: 'pp-nav__item', href: `#/${n.id}`, dataset: { nav: n.id }, text: n.label,
  }));

  const topbar = h('div', { class: 'pp-topbar' },
    h('div', { class: 'pp-brand' },
      h('span', { class: 'pp-brand__mark' }),
      h('span', { class: 'pp-brand__name', text: brand })),
    subtitle ? h('div', { class: 'pp-rule pp-hide-sm' }) : null,
    subtitle ? h('div', { class: 'pp-topbar__id pp-trunc' }, subtitle) : null,
    h('div', { class: 'pp-spacer' }),
    tools ? h('div', { class: 'pp-topbar__tools' }, tools) : null);

  const sidebar = h('nav', { class: 'pp-sidebar', 'aria-label': 'Pages' },
    h('div', { class: 'pp-nav' }, navButtons()),
    footer ? h('div', { class: 'pp-sidebar__foot' }, footer) : null);

  const chipnav = h('nav', { class: 'pp-chipnav', 'aria-label': 'Pages' }, navButtons());
  const main = h('main', { class: `pp-main${wide ? ' pp-main--wide' : ''}`, id: 'pp-main' });

  const el = h('div', { class: 'pp-app' }, topbar, chipnav, h('div', { class: 'pp-shell' }, sidebar, main));
  return { el, main, topbar, sidebar, chipnav };
}
