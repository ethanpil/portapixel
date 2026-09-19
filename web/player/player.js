/* PortaPixel player.

   This page is the only thing that the television shows. It plays the active
   playlist, it gives the browser to the daemon for a URL item, and it shows
   the fallback screen when there is no content.

   It speaks the protocol of docs/ARCHITECTURE.md section 7a. The browser opens
   /player?k=<secret>[&resume=<index>]. Each call carries the secret in the
   X-PortaPixel-Player header. The SSE stream and the QR picture carry it in
   the query, because those two cannot send a header.

   Two rules hold this file together:
     - Every failure path ends with "show the next item". A screen that stops
       is the worst failure of a signage player.
     - Nothing collects. There is one frame counter, one heartbeat timer, one
       event stream, and at most one timer per item.
*/

import { Stage } from './stage.js';
import { Fallback } from './fallback.js';

const HEARTBEAT_MS = 5000;
const STALL_MS = 10000;          // a video that does not move for this long
const HB_DEAD_MS = 60000;        // heartbeats fail for this long
const EMPTY_RETRY_MS = 15000;    // the manifest says there is no content
const FAILED_RETRY_MS = 30000;   // every item of one loop failed
const DEFAULT_IMAGE_SECONDS = 10;

const query = new URLSearchParams(location.search);
const KEY = query.get('k') || '';

const stage = new Stage(document.getElementById('stage'));
const fallback = new Fallback(document.getElementById('fallback'), KEY);

let frames = 0;                  // grows with each drawn frame (D45)
let frameAsked = false;          // one animation frame is on its way
let playlistName = '';
let items = [];
let tier = 'high';
let transition = 'cut';
let transitionMs = 0;
let index = 0;
let generation = 0;              // a new number stops the loop that runs now
let pending = null;              // {item, promise} the item that loads next
let command = '';                // "playlist" or "grace", from the SSE stream
let failed = 0;                  // items that failed since the last good one
let note = '';                   // one line of trouble for the next heartbeat
let mode = 'boot';               // boot | playing | fallback | hold | handoff
let hbFailedAt = 0;
let hbTimer = 0;
let retryTimer = 0;
let stopDwell = null;

/* ------------------------------------------------------------- transport */

/* Every error keeps what the daemon said. The API answers {"error": "..."} on
   every route, and that sentence is what the ops log needs: "403 /api/..."
   alone does not say which rule refused the call. */
function failed(res, path, text) {
  let why = (text || '').slice(0, 200);
  try {
    const parsed = JSON.parse(text);
    if (parsed && parsed.error) why = parsed.error;
  } catch { /* the body is not JSON */ }
  return new Error(res.status + ' ' + path + (why ? ': ' + why : ''));
}

async function getJSON(path) {
  const res = await fetch(path, {
    cache: 'no-store',
    headers: { Accept: 'application/json', 'X-PortaPixel-Player': KEY },
  });
  const text = await res.text();
  if (!res.ok) throw failed(res, path, text);
  return JSON.parse(text);
}

async function postJSON(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    cache: 'no-store',
    headers: {
      'Content-Type': 'application/json',
      'X-PortaPixel': '1',
      'X-PortaPixel-Player': KEY,
    },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw failed(res, path, text);
  if (!text) return {};
  try { return JSON.parse(text); } catch { return {}; }
}

/* ---------------------------------------------------------- frame counter */

/* The daemon watches this number. A number that stops while the heartbeat
   still arrives means the glass is frozen (D45).

   One frame per heartbeat, not sixty per second. A chain of animation frames
   that never ends keeps the compositor awake for weeks to add 1 to a number,
   and this device runs for months on a Raspberry Pi. A frozen compositor
   delivers no frame at all, so the signal is the same: the counter stops.

   The number only grows, and the daemon reads a lower number as a page that
   loaded again (ARCHITECTURE 7a). */
function countFrame() {
  if (frameAsked) return;
  frameAsked = true;
  requestAnimationFrame(() => {
    frameAsked = false;
    frames += 1;
  });
}

/* --------------------------------------------------------------- heartbeat */

function heartbeatBody() {
  const item = mode === 'playing' ? items[index] : null;
  const state = mode === 'fallback' ? 'fallback'
    : (mode === 'handoff' || mode === 'hold') ? 'handoff' : 'playing';
  const body = {
    playlist: playlistName,
    index: item ? item.index : 0,
    name: item ? (item.name || '') : '',
    kind: item ? item.kind : '',
    state,
    frames,
  };
  const el = stage.current && stage.current.el;
  if (item && item.kind === 'video' && el) {
    body.position = Math.round(el.currentTime * 10) / 10;
  }
  // "note" is an extra field. It makes a problem visible in the ops log.
  if (note) body.note = note;
  return body;
}

/* The daemon is the watchdog, not the player. A failed heartbeat changes
   nothing here. But a long silence can mean that the daemon restarted, so the
   player gets the manifest again when the calls work again. */
async function beat() {
  const body = heartbeatBody();
  // Ask for the next frame now, so that the counter of the next heartbeat is
  // one higher when the glass is alive.
  countFrame();
  try {
    await postJSON('/api/player/heartbeat', body);
    // Keep a note that came in while this call ran.
    if (body.note && note === body.note) note = '';
    const gap = hbFailedAt ? Date.now() - hbFailedAt : 0;
    hbFailedAt = 0;
    if (gap >= HB_DEAD_MS) ask('playlist');
  } catch (e) {
    if (!hbFailedAt) hbFailedAt = Date.now();
    // Keep what the daemon said. The next heartbeat that works carries it, so
    // the fault is in the ops log and not only in a console that nobody reads.
    if (!note) note = 'the heartbeat failed: ' + ((e && e.message) || e);
  }
}

/* ------------------------------------------------------------ event stream */

function openEvents() {
  // EventSource opens the stream again by itself. No retry loop here.
  const src = new EventSource('/api/player/events?k=' + encodeURIComponent(KEY));
  src.addEventListener('playlist', () => ask('playlist'));
  src.addEventListener('grace', () => ask('grace'));
  src.addEventListener('reload', () => location.reload());
}

/** Hold a command until the end of the item that plays now. The fallback
    screen and a playlist of one item have no end, so they act at once. */
function ask(what) {
  command = what;
  if (mode !== 'playing') { void obey(); return; }
  if (items.length < 2 && stopDwell) stopDwell('interrupt');
}

/** Do the held command. Return true when the play loop must stop. */
async function obey() {
  const what = command;
  command = '';
  if (what === 'playlist') { await start(null); return true; }
  if (what === 'grace') { await holdForRestart(); return true; }
  return false;
}

/** The daemon wants to restart the browser. Show black, then say ready. */
async function holdForRestart() {
  generation += 1;
  dropPending();
  stopRetry();
  fallback.hide();
  stage.clear();
  mode = 'hold';
  try { await postJSON('/api/player/ready', {}); } catch { /* it restarts us anyway */ }
}

/* ----------------------------------------------------------- the playlist */

/** Get the manifest and start to play. resume is the index from the URL, or
    null.

    Two "playlist" events that arrive together made two play loops: one apply of
    the configuration sent one event for each field it changed, and each loop
    thought it was the current one. The list then played at double speed with two
    video decoders. The number that this call minted goes to the loop, and a
    newer start stops this one at each await. */
async function start(resume) {
  const mine = ++generation;
  dropPending();
  stopRetry();

  let m = null;
  try {
    m = await getJSON('/api/player/manifest');
  } catch (e) {
    if (mine !== generation) return;
    note = 'the manifest did not load: ' + e.message;
    console.warn('[player]', note);
    showFallback(EMPTY_RETRY_MS);
    return;
  }
  if (mine !== generation) return;

  tier = (m && m.tier) || 'high';
  const list = m && m.playlist;
  if (!m || m.fallback || !list || !Array.isArray(list.items) || list.items.length === 0) {
    showFallback(EMPTY_RETRY_MS);
    return;
  }

  playlistName = list.name || '';
  items = list.items;
  transition = list.transition || 'cut';
  transitionMs = Number(list.transition_ms) || 0;
  failed = 0;

  let at = 0;
  if (resume !== null && resume !== undefined) {
    const n = parseInt(resume, 10);
    if (Number.isFinite(n)) at = ((n % items.length) + items.length) % items.length;
  }

  // A black screen gets a hard cut. This is also the way back from a URL item
  // (D14): a page change cannot be anything but a cut.
  // The fallback screen stays until the first item is really on the stage. A
  // playlist that needs time to load must not make the screen go black.
  const cutIn = stage.current === null;
  loop(at, cutIn, mine).catch(fatal);
}

/** The last net. A screen that stops is worse than the fallback screen. */
function fatal(e) {
  note = 'the player hit an error: ' + ((e && e.message) || e);
  console.error('[player]', e);
  showFallback(FAILED_RETRY_MS);
}

/** Show the items, one after the other, until something stops the loop.
    mine is the number that start() minted for this loop. */
async function loop(at, cutIn, mine) {
  index = at;
  let cut = cutIn;

  while (mine === generation) {
    const item = items[index];
    if (!item) { showFallback(FAILED_RETRY_MS); return; }

    // A URL item belongs to the daemon (plan 3.2).
    if (item.kind === 'url') {
      const gone = await handOff(item);
      if (mine !== generation || gone) return;
      // A URL that the daemon cannot reach is normal (D19). It counts, so
      // that a playlist of only bad URLs ends at the fallback screen.
      if (!skipItem(item, 'the daemon cannot reach the page (D19)')) return;
      continue;
    }

    const single = items.length === 1;
    let name = cut ? 'cut' : transition;
    let ms = cut ? 0 : transitionMs;
    let ready = null;

    try {
      ready = await takePending(item);
      if (!ready) {
        if (blackBetween(stage.current && stage.current.item, item)) {
          // D14, low tier: only one video decoder may run. Fade out, free the
          // decoder, then load the next video and fade it in.
          ms = Math.round(transitionMs / 2);
          name = 'crossfade';
          await stage.fadeOut(ms);
          if (mine !== generation) return;
        }
        ready = await stage.prepare(item, single);
      }
      if (mine !== generation) { Stage.teardown(ready); return; }
      const said = await stage.present(ready, name, ms);
      if (said) { note = said; console.warn('[player]', said); }
    } catch (e) {
      if (mine !== generation) return;
      if (!skipItem(item, e.message)) return;
      continue;
    }

    // Something can stop the loop while the transition runs.
    if (mine !== generation) return;

    cut = false;
    failed = 0;
    if (mode !== 'playing') {
      mode = 'playing';
      fallback.hide();
    }

    // Load the next item while this one plays.
    const next = (index + 1) % items.length;
    if (!single && items[next].kind !== 'url' && !blackBetween(item, items[next])) {
      startPending(items[next]);
    }

    const why = await dwell(item, ready, single);
    if (mine !== generation) return;
    if (command && await obey()) return;
    if (why === 'fail') {
      if (!skipItem(item, 'the item stopped')) return;
      continue;
    }
    index = next;
  }
}

/** Wait out one item. Resolve with "ok", "fail" or "interrupt". */
function dwell(item, ready, single) {
  return new Promise((resolve) => {
    const ac = new AbortController();
    const timers = [];
    let watch = 0;
    let done = false;
    const end = (why) => {
      if (done) return;
      done = true;
      ac.abort();
      for (const t of timers) clearTimeout(t);
      if (watch) clearInterval(watch);
      // Only the dwell that owns the hook may clear it. A newer item has already
      // put its own hook there, and clearing that one would make the next
      // "playlist" event wait for an item that has no end.
      if (stopDwell === end) stopDwell = null;
      resolve(why);
    };
    stopDwell = end;

    if (item.kind !== 'video') {
      // One image alone stays on the screen. Nothing needs to change.
      if (single) return;
      const secs = Number(item.duration) > 0 ? Number(item.duration) : DEFAULT_IMAGE_SECONDS;
      timers.push(setTimeout(() => end('ok'), secs * 1000));
      return;
    }

    const el = ready.el;
    if (!single) {
      // One video alone has the loop attribute. It never ends, and a cap on
      // the length has nothing to change to.
      el.addEventListener('ended', () => end('ok'), { signal: ac.signal });
      const cap = Number(item.max_duration) || 0;
      if (cap > 0) timers.push(setTimeout(() => end('ok'), cap * 1000));
    }
    el.addEventListener('error', () => end('fail'), { signal: ac.signal });

    // A stall shows as a time that does not move. This one check covers
    // "stalled", "waiting" and a decoder that dies without an error.
    let last = -1;
    let stuck = 0;
    watch = setInterval(() => {
      if (el.ended) return;
      if (el.currentTime === last) {
        stuck += 1000;
        if (stuck >= STALL_MS) end('fail');
        return;
      }
      last = el.currentTime;
      stuck = 0;
    }, 1000);
  });
}

/** Count a bad item and go to the next one. Return false when every item of
    one full loop failed; then the fallback screen comes up. */
function skipItem(item, why) {
  failed += 1;
  note = `item ${item.index} (${item.name || item.kind}) skipped: ${why}`;
  console.warn('[player]', note);
  if (failed >= items.length) {
    note = `every item of the playlist failed (${items.length}); last: ${why}`;
    showFallback(FAILED_RETRY_MS);
    return false;
  }
  index = (index + 1) % items.length;
  return true;
}

/** True when the change between two items must go through black (D14). */
function blackBetween(out, into) {
  return tier === 'low' && !!out && !!into && out.kind === 'video' && into.kind === 'video';
}

/* --------------------------------------------------------- next item ready */

function startPending(item) {
  pending = { item, promise: stage.prepare(item, false) };
  // The loop reads the result later. This stops an unhandled rejection.
  pending.promise.catch(() => {});
}

/** Take the item that loads now. Wait for it if it is not ready. Throw when
    the load failed, so that one bad item costs one load, not two. */
async function takePending(item) {
  if (!pending || pending.item !== item) return null;
  const mine = pending;
  pending = null;
  return await mine.promise;
}

function dropPending() {
  if (!pending) return;
  const mine = pending;
  pending = null;
  mine.promise.then((ready) => Stage.teardown(ready), () => {});
}

/* --------------------------------------------------------------- URL items */

/** Tell the daemon that the next item is a URL. Return true when the daemon
    took the browser; then this page stops. */
async function handOff(item) {
  stage.pause();                 // keep the picture, stop the sound
  let reply = null;
  try {
    reply = await postJSON('/api/player/url-item', { index: item.index });
  } catch (e) {
    // The daemon did not answer. Treat the URL as skipped (D19) and play on.
    note = `the url-item call failed: ${e.message}`;
    console.warn('[player]', note);
    return false;
  }
  if (reply && reply.skip) return false;

  // The daemon navigates the browser away. Stop all of our work.
  generation += 1;
  dropPending();
  stopRetry();
  if (stopDwell) stopDwell('interrupt');
  mode = 'handoff';
  void beat();
  if (hbTimer) { clearInterval(hbTimer); hbTimer = 0; }
  return true;
}

/* --------------------------------------------------------- fallback screen */

function showFallback(retryMs) {
  generation += 1;
  dropPending();
  mode = 'fallback';
  stage.clear();
  fallback.show();
  startRetry(retryMs);
}

function startRetry(ms) {
  stopRetry();
  retryTimer = setInterval(() => {
    if (mode === 'fallback') void start(null);
  }, ms);
}

function stopRetry() {
  if (retryTimer) { clearInterval(retryTimer); retryTimer = 0; }
}

/* -------------------------------------------------------------------- boot */

openEvents();
hbTimer = setInterval(beat, HEARTBEAT_MS);
void beat();
void start(query.get('resume'));
