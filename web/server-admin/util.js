/* Small helpers that more than one page needs.
   Nothing here builds a page. Nothing here calls the API.
*/

import { h, icon, progress, statusDot, fmtBytes, DAYS } from '/shared/ui.js';

/* -------------------------------------------------------------------- days */

/** A group sends its screen days as a comma string and takes them as an array.
    This is the one place in the API where the two differ. */
export function daysFromCSV(value) {
  return String(value || '').split(',').map((d) => d.trim()).filter((d) => DAYS.includes(d));
}

/* ------------------------------------------------------------------- times */

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

/** The state of a command in words: {word, when, kind, note}.
    A command that the server delivered and that no heartbeat acknowledged goes
    out again after ten minutes, three times in all, and then it is expired. */
export function commandState(cmd) {
  const tries = Number(cmd.deliveries) || 0;
  if (cmd.state === 'expired') {
    return {
      word: 'gave up', when: cmd.delivered_at || cmd.queued_at, kind: 'danger',
      note: tries > 1
        ? `The screen took it ${tries} times and never reported back.`
        : 'The screen never reported back. Send it again.',
    };
  }
  if (cmd.state === 'acked' || !isNever(cmd.acked_at)) {
    return { word: 'done', when: cmd.acked_at, kind: 'ok' };
  }
  if (cmd.state === 'delivered' || !isNever(cmd.delivered_at)) {
    return {
      word: 'picked up', when: cmd.delivered_at, kind: 'busy',
      note: tries > 1 ? `Sent ${tries} times; the screen has not reported back yet.` : '',
    };
  }
  return { word: 'queued', when: cmd.queued_at, kind: 'quiet' };
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

/** One cell of a CSV file. A value that starts with an operator is a formula to
    a spreadsheet, and a device name comes from the screen, so it is quoted with
    a leading apostrophe first. */
export function csvCell(v) {
  let s = String(v === null || v === undefined ? '' : v);
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`;
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
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
