/* Small helpers that more than one page needs.
   Nothing here builds a page. Nothing here calls the API.
*/

import { h } from '/shared/ui.js';

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

/** Write text only when it is different. The dashboard refreshes every five
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

/** True when the device answers that a route is in the route table but not in
    this build. The 501 comes from the daemon; a 404 comes from a route that the
    daemon does not name at all yet. */
export function notInThisBuild(err) {
  return !!err && (err.status === 501 || err.status === 404);
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
      // A rule with no day at all would mean "every day" in the file, which is
      // not what an empty row of chips looks like. So the last day stays.
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

  // All seven days go back as an empty list: that is how the file says
  // "every day", and a save must not turn it into seven names.
  el.read = () => (picked.size === 7 ? [] : DAYS.filter((d) => picked.has(d)));
  el.set = (next) => { picked = new Set(next && next.length ? next : DAYS); draw(); };
  el.setDisabled = (value) => { off = !!value; draw(); };
  draw();
  return el;
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

/** Does a rule cover this moment? This is the same rule that the scheduler
    keeps: a window that ends before it starts goes past midnight, and then the
    hours after midnight belong to the day before. */
export function inWindow(start, end, minute, weekday, days) {
  const s = toMinutes(start);
  const e = toMinutes(end);
  if (s === null || e === null || s === e) return false;
  if (s < e) return minute >= s && minute < e && dayPermitted(days, weekday);
  if (minute >= s) return dayPermitted(days, weekday);
  if (minute < e) return dayPermitted(days, (weekday + 6) % 7);
  return false;
}

/** An empty day list means every day. Day 0 is Monday here, as in the file. */
export function dayPermitted(days, weekday) {
  if (!days || days.length === 0) return true;
  return days.includes(DAYS[weekday]);
}

/** The day and the minute of the moment, in the time zone of the device.
    Returns {weekday, minute} with Monday as day 0. */
export function deviceClock(timezone) {
  const now = new Date();
  let parts;
  try {
    parts = new Intl.DateTimeFormat('en-GB', {
      timeZone: timezone || 'UTC', weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false,
    }).formatToParts(now);
  } catch {
    parts = new Intl.DateTimeFormat('en-GB', {
      weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false,
    }).formatToParts(now);
  }
  const get = (t) => (parts.find((p) => p.type === t) || {}).value || '';
  const short = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
  const weekday = Math.max(0, short.indexOf(get('weekday').slice(0, 3).toLowerCase()));
  return { weekday, minute: Number(get('hour')) * 60 + Number(get('minute')) };
}

/** A log time in the time zone of the device, as "2026-09-18 14:03:11". */
export function deviceTime(iso, timezone) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return String(iso || '');
  const opts = {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  };
  try {
    return new Intl.DateTimeFormat('sv-SE', { ...opts, timeZone: timezone || 'UTC' }).format(new Date(t));
  } catch {
    return new Intl.DateTimeFormat('sv-SE', opts).format(new Date(t));
  }
}

/** The list of days in words, for a line of help text. */
export function daysInWords(days) {
  if (!days || days.length === 0 || days.length === 7) return 'every day';
  if (days.length === 5 && DAYS.slice(0, 5).every((d) => days.includes(d))) return 'Mon to Fri';
  if (days.length === 2 && days.includes('sat') && days.includes('sun')) return 'Sat and Sun';
  return DAYS.filter((d) => days.includes(d)).map((d) => d[0].toUpperCase() + d.slice(1)).join(', ');
}

/* -------------------------------------------------------------------- misc */

/** The temperature as a short string. */
export function fmtTemp(c) {
  const n = Number(c);
  if (!n) return '—';
  return `${n.toFixed(1)} °C`;
}

/** The kind of a file from its name. The device says the kind for an item that
    is in a playlist; a file that was just uploaded has only a name. */
export function guessKind(name) {
  return /\.(mp4|m4v|mov|webm|mkv)$/i.test(String(name)) ? 'video' : 'image';
}

/** A path under /media/, escaped one part at a time. */
export function mediaURL(playlist, file) {
  return `/media/${encodeURIComponent(playlist)}/${encodeURIComponent(file)}`;
}
