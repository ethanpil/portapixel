/* Small helpers that more than one page needs.
   Nothing here builds a page. Nothing here calls the API.
*/

import { h, icon, progress, statusDot, fmtBytes } from '/shared/ui.js';

/* ----------------------------------------------------------- page furniture */

/** The title block of a page. `right` goes on the other end of the line. */
export function pageHead(title, lead, right) {
  return h('div', { class: 'pp-page-head' },
    h('div', { style: { 'min-width': 'min(260px, 100%)' } },
      h('h1', { class: 'pp-h1', text: title }),
      lead ? h('p', { class: 'pp-lead' }, lead) : null),
    right || null);
}

/** A card with a head, a body and an optional footer bar. */
export function card({ title, meta, body, foot, cls } = {}) {
  return h('div', { class: `pp-card${cls ? ` ${cls}` : ''}` },
    title ? h('div', { class: 'pp-card__head' },
      h('div', { class: 'pp-h2' }, title),
      meta ? h('div', { class: 'pp-card__meta' }, meta) : null) : null,
    body ? h('div', { class: 'pp-card__body' }, body) : null,
    foot ? h('div', { class: 'pp-card__foot' }, foot) : null);
}

/** Write text only when it is different. The fleet list refreshes every ten
    seconds; a write that changes nothing would still make the browser lay the
    line out again, and a selection in the text would be lost. */
export function setText(el, text) {
  const next = text === null || text === undefined ? '' : String(text);
  if (el.textContent !== next) el.textContent = next;
}

/** Show or hide an element without moving anything else. */
export function setShown(el, shown) {
  if (el.hidden === !shown) return;
  el.hidden = !shown;
}

/** Set a class only when it must change. */
export function setClass(el, name, on) {
  if (el.classList.contains(name) === !!on) return;
  el.classList.toggle(name, !!on);
}

/* ------------------------------------------------------------------- errors */

/** The sentence to show for a failed call. A 422 answer carries one message
    for each field, and a toast can hold two or three of them. */
export function errorText(err) {
  const fields = err && err.fields;
  if (!fields || fields.length === 0) return (err && err.message) || 'That did not work.';
  const parts = fields.slice(0, 3).map((f) => `${f.field}: ${f.message}`);
  if (fields.length > 3) parts.push(`and ${fields.length - 3} more`);
  return parts.join('; ');
}

/* -------------------------------------------------------------------- days */

export const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const DAY_INITIAL = ['M', 'T', 'W', 'T', 'F', 'S', 'S'];
const DAY_FULL = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];

/** Seven toggle chips. An empty list means every day, which is what the
    scheduler does with it, so all seven chips show as pressed.
    Returns the element; it carries read() and set(days). */
export function dayChips({ days = [], disabled = false, onChange } = {}) {
  let picked = new Set(days.length ? days : DAYS);
  let off = disabled;
  const el = h('div', { class: 'pp-days', role: 'group', 'aria-label': 'Days' });

  // The buttons are made once and only their state changes. A row that was
  // built again on each click would take the keyboard away from the chip that
  // the user just pressed.
  const buttons = DAYS.map((d, i) => h('button', {
    type: 'button', class: 'pp-day', text: DAY_INITIAL[i], title: DAY_FULL[i],
    'aria-label': DAY_FULL[i],
    onClick: () => {
      // A rule with no day at all would mean "every day" on the device, which
      // is not what an empty row of chips looks like. So the last day stays.
      if (picked.has(d) && picked.size === 1) return;
      if (picked.has(d)) picked.delete(d); else picked.add(d);
      draw();
      if (onChange) onChange(el.read());
    },
  }));
  el.append(...buttons);

  function draw() {
    buttons.forEach((b, i) => {
      b.setAttribute('aria-pressed', String(picked.has(DAYS[i])));
      b.disabled = off;
    });
  }

  // All seven days go back as an empty list: that is how the server says
  // "every day", and a save must not turn it into seven names.
  el.read = () => (picked.size === 7 ? [] : DAYS.filter((d) => picked.has(d)));
  el.set = (next) => { picked = new Set(next && next.length ? next : DAYS); draw(); };
  el.setDisabled = (value) => { off = !!value; draw(); };
  draw();
  return el;
}

/** The list of days in words, for a line of help text. */
export function daysInWords(days) {
  if (!days || days.length === 0 || days.length === 7) return 'every day';
  if (days.length === 5 && DAYS.slice(0, 5).every((d) => days.includes(d))) return 'Mon to Fri';
  if (days.length === 2 && days.includes('sat') && days.includes('sun')) return 'Sat and Sun';
  return DAYS.filter((d) => days.includes(d)).map((d) => d[0].toUpperCase() + d.slice(1)).join(', ');
}

/** A group sends its screen days as a comma string and takes them as an array.
    This is the one place in the API where the two differ. */
export function daysFromCSV(value) {
  return String(value || '').split(',').map((d) => d.trim()).filter((d) => DAYS.includes(d));
}

/* ------------------------------------------------------------------- times */

/** "HH:MM" as minutes after midnight, or null. */
export function toMinutes(value) {
  const m = /^(\d{1,2}):(\d{2})$/.exec(String(value || '').trim());
  if (!m) return null;
  const hours = Number(m[1]);
  const mins = Number(m[2]);
  if (hours > 23 || mins > 59) return null;
  return hours * 60 + mins;
}

/** Does a rule cover this moment? This is the same rule that the device keeps:
    a window that ends before it starts goes past midnight, and then the hours
    after midnight belong to the day before. */
export function inWindow(start, end, minute, weekday, days) {
  const s = toMinutes(start);
  const e = toMinutes(end);
  // An empty start and an empty end cover the whole day.
  if (!String(start || '').trim() && !String(end || '').trim()) return dayPermitted(days, weekday);
  if (s === null || e === null || s === e) return false;
  if (s < e) return minute >= s && minute < e && dayPermitted(days, weekday);
  if (minute >= s) return dayPermitted(days, weekday);
  if (minute < e) return dayPermitted(days, (weekday + 6) % 7);
  return false;
}

/** An empty day list means every day. Day 0 is Monday here, as on the device. */
export function dayPermitted(days, weekday) {
  if (!days || days.length === 0) return true;
  return days.includes(DAYS[weekday]);
}

/** The day and the minute of this moment in the browser. Monday is day 0.
    Screens keep their own time zone, so this is "roughly now" for the person
    who reads the page, and the line that uses it says so. */
export function clockNow() {
  const now = new Date();
  return { weekday: (now.getDay() + 6) % 7, minute: now.getHours() * 60 + now.getMinutes() };
}

/** A time of day in the time zone of this browser, as "20:41:12". */
export function fmtClock(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  return new Intl.DateTimeFormat('en-GB', {
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(new Date(t));
}

/** A date in words, as "4 August 2026". An empty time gives an em dash. */
export function fmtDate(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t) || String(iso || '').startsWith('0001-')) return '—';
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'long', year: 'numeric' }).format(new Date(t));
}

/** True for a time value that means "never". */
export function isNever(iso) {
  return !iso || String(iso).startsWith('0001-01-01');
}

/* ------------------------------------------------------------------ screens */

/* One word for each state, and the colour of its dot. The sidebar counts
   offline, pending, conflict and needs_confirm as "needs a look", so those
   states all take the red dot. */
const STATES = {
  online: { kind: 'ok', word: 'checked in', flag: false },
  quiet: { kind: 'quiet', word: 'quiet', flag: false },
  offline: { kind: 'alert', word: 'not seen for a day', flag: true },
  pending: { kind: 'alert', word: 'waiting for approval', flag: true },
  conflict: { kind: 'alert', word: 'two boxes, one token', flag: true },
  needs_confirm: { kind: 'alert', word: 'new hardware', flag: true },
};

/** What a state looks like: {kind, word, flag}. */
export function stateInfo(state) {
  return STATES[state] || { kind: 'quiet', word: state || 'unknown', flag: false };
}

/** The dot and the words of one screen state. */
export function stateLine(state) {
  const info = stateInfo(state);
  return h('span', { class: 'pp-status' }, statusDot(info.kind), h('span', { text: info.word }));
}

/** The last heartbeat of a screen, parsed. It is a JSON string in the device
    row, and it is empty before the first check-in. */
export function parseStatus(device) {
  if (!device || !device.status) return {};
  try {
    const out = JSON.parse(device.status);
    return out && typeof out === 'object' ? out : {};
  } catch {
    return {};
  }
}

/** The temperature as a short string. */
export function fmtTemp(c) {
  const n = Number(c);
  if (!n) return '—';
  return `${n.toFixed(1)} °C`;
}

/** The kind of a file from its name, for an item that carries no kind. */
export function guessKind(name) {
  const n = String(name || '');
  if (/^https?:\/\//i.test(n)) return 'url';
  if (/\.(mp4|m4v|mov|webm|mkv|ogv)$/i.test(n)) return 'video';
  if (/\.(jpg|jpeg|png|gif|webp|avif|bmp|svg)$/i.test(n)) return 'image';
  return 'unknown';
}

/** The thumbnail route of one library object. */
export function thumbURL(sha256) {
  return `/api/admin/media/${encodeURIComponent(sha256)}/thumb`;
}

/** A 52x32 preview of one item: the thumbnail when there is one, else the icon
    of its kind. `url` may be null. */
export function preview(kind, url, opts = {}) {
  const cls = opts.wide ? 'pp-thumb pp-thumb--wide' : 'pp-thumb pp-thumb--sm';
  if (url) return h('span', { class: cls }, h('img', { src: url, alt: '', loading: 'lazy' }));
  const name = kind === 'video' ? 'video' : (kind === 'url' ? 'url' : 'image');
  return h('span', { class: opts.wide ? `${cls} sv-blank` : 'sv-icon' }, icon(name, opts.wide ? 26 : 16));
}

/** A free-space bar with its number. */
export function freeSpace(freeBytes, totalBytes) {
  const free = Number(freeBytes) || 0;
  const total = Number(totalBytes) || 0;
  const bar = progress(total ? free / total : 0, { thin: true });
  const low = total > 0 && free / total < 0.12;
  return h('span', { class: 'sv-free' },
    bar,
    h('span', { class: `sv-free__n${low ? ' pp-cell--danger' : ''}`, text: total ? fmtBytes(free) : '—' }));
}

/* ----------------------------------------------------------------- commands */

/** The commands that a screen takes, with the words that a person reads. */
export const COMMANDS = [
  { type: 'restart-browser', label: 'Restart the player', body: 'The picture goes away for a few seconds and comes back on the same item.' },
  { type: 'reboot', label: 'Reboot', body: 'The screen goes dark for about half a minute while the box starts again.' },
  { type: 'screen-on', label: 'Screen on', body: 'The panel comes on now, whatever its own times say.' },
  { type: 'screen-off', label: 'Screen off', body: 'The panel goes dark now. Its own times bring it back.' },
  { type: 'rescan', label: 'Look for new files', body: 'The screen checks its own storage and syncs with this server again.' },
  { type: 'update', label: 'Install the approved version', body: 'The screen installs the version that the Versions page approved, and puts itself back on the old one if it cannot come up.' },
];

/** The state of a command in words. */
export function commandState(cmd) {
  if (cmd.state === 'acked' || !isNever(cmd.acked_at)) return { word: 'done', when: cmd.acked_at };
  if (cmd.state === 'delivered' || !isNever(cmd.delivered_at)) return { word: 'picked up', when: cmd.delivered_at };
  return { word: 'queued', when: cmd.queued_at };
}

/* -------------------------------------------------------------------- links */

/** The page of one screen in this UI. */
export function screenHref(id) {
  return `#/screens/${encodeURIComponent(id)}`;
}

/** The address of a screen's own admin page, or null.
    The value comes from the screen itself, so only a plain host name or IP
    address becomes a link. Anything else is dropped. */
export function ownPageURL(status) {
  const candidates = [status.mdns_name, ...(Array.isArray(status.ips) ? status.ips : [])];
  for (const raw of candidates) {
    const value = String(raw || '').trim();
    if (!value) continue;
    if (/^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,60}[a-zA-Z0-9])?$/.test(value)) return `http://${value}/`;
  }
  return null;
}

/** Put text on the clipboard. Returns a promise that gives true when it
    worked. A page served over plain HTTP has no clipboard API in some
    browsers, so there is a fallback. */
export async function copyText(text) {
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall through to the old way */ }
  try {
    const area = h('textarea', { class: 'pp-sr-only', value: text });
    document.body.append(area);
    area.select();
    const done = document.execCommand('copy');
    area.remove();
    return done;
  } catch {
    return false;
  }
}

/** Offer a file to the browser. The media page and the fleet export use it. */
export function download(name, text, type = 'text/plain') {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const link = h('a', { href: url, download: name });
  document.body.append(link);
  link.click();
  link.remove();
  // The object URL holds the blob until it is given back.
  setTimeout(() => URL.revokeObjectURL(url), 4000);
}
