/* Small helpers that more than one page of the DEVICE UI needs.
   Nothing here builds a page. Nothing here calls the API.

   The page furniture, the error sentence, the day chips and the time rules are
   in /shared/ui.js: the control server UI needs every one of them too, and two
   copies of one rule drift.
*/

/* -------------------------------------------------------------------- errors */

/** True when the device answers that a route is in the route table but not in
    this build. The 501 comes from the daemon; a 404 comes from a route that the
    daemon does not name at all yet. */
export function notInThisBuild(err) {
  return !!err && (err.status === 501 || err.status === 404);
}

/* -------------------------------------------------------------------- times */

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

/* -------------------------------------------------------------------- misc */

/** A path under /media/, escaped one part at a time. */
export function mediaURL(playlist, file) {
  return `/media/${encodeURIComponent(playlist)}/${encodeURIComponent(file)}`;
}
