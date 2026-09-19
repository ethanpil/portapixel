/* The PortaPixel fallback screen (D18).

   It is a view of the same page, not a second page. The player shows it when
   there is no playable content. It must tell a person who stands in front of
   the television how to reach the device.

   All of the data comes from /api/status and /api/player/qr.svg. The view owns
   its timers. hide() stops all of them, so nothing collects while the device
   plays content.
*/

const STATUS_MS = 10000;     // get the status again
const CLOCK_MS = 1000;       // move the clock
const SHIFT_MS = 210000;     // move the layout a little, every 3.5 minutes

/** A short ring of small offsets. A still picture for weeks can burn a
    screen, so the whole layout moves between these places. */
const SHIFTS = [[0, 0], [10, 6], [-8, 10], [6, -8], [-10, -6], [12, 2], [2, 12], [-12, -2]];

export class Fallback {
  constructor(root, key) {
    this.root = root;
    this.key = key;
    this.shift = root.querySelector('#fb-shift');
    this.name = root.querySelector('#fb-name');
    this.id = root.querySelector('#fb-id');
    this.url = root.querySelector('#fb-url');
    this.ips = root.querySelector('#fb-ips');
    this.pair = root.querySelector('#fb-pair');
    this.code = root.querySelector('#fb-code');
    this.qrbox = root.querySelector('#fb-qrbox');
    this.qr = root.querySelector('#fb-qr');
    this.time = root.querySelector('#fb-time');
    this.date = root.querySelector('#fb-date');
    this.warn = root.querySelector('#fb-warn');

    this.visible = false;
    this.timers = [];
    this.zone = '';
    this.fmtTime = null;
    this.fmtDate = null;
    this.at = 0;
    this.qrAsked = false;

    // A QR code that the daemon cannot make must not leave a white hole.
    this.qr.addEventListener('error', () => { this.qrbox.hidden = true; });
  }

  /** Show the screen and start the timers. Safe to call again. */
  show() {
    if (this.visible) { void this.refresh(); return; }
    this.visible = true;
    this.root.hidden = false;
    if (!this.qrAsked) {
      this.qrAsked = true;
      // An <img> cannot send a header, so the secret goes in the query, like
      // the SSE stream does (ARCHITECTURE.md section 6).
      this.qr.src = '/api/player/qr.svg?k=' + encodeURIComponent(this.key);
    }
    void this.refresh();
    this.tick();
    this.timers.push(setInterval(() => void this.refresh(), STATUS_MS));
    this.timers.push(setInterval(() => this.tick(), CLOCK_MS));
    this.timers.push(setInterval(() => this.move(), SHIFT_MS));
  }

  /** Hide the screen and stop every timer. */
  hide() {
    if (!this.visible) return;
    this.visible = false;
    for (const t of this.timers) clearInterval(t);
    this.timers = [];
    this.root.hidden = true;
  }

  /** Get the status and paint it. A status that does not answer leaves the
      last values on the screen and says so. */
  async refresh() {
    let status = null;
    try {
      const res = await fetch('/api/status', { cache: 'no-store', headers: { Accept: 'application/json' } });
      if (res.ok) status = await res.json();
    } catch { /* the daemon is busy or starts up */ }
    if (!status) {
      this.warn.textContent = 'The device software does not answer yet.';
      return;
    }
    this.paint(status);
  }

  paint(s) {
    this.name.textContent = s.name || 'PortaPixel';
    this.id.textContent = s.device_id || '';

    const host = s.mdns_name || (s.ips && s.ips[0]) || '';
    this.url.textContent = host ? 'http://' + host + '/' : 'no network yet';

    const ips = (s.ips || []).join('    ');
    this.ips.textContent = ips || 'no address yet';

    const code = s.pairing_code || '';
    this.pair.hidden = !code;
    if (code) this.code.textContent = code;

    const lines = [];
    if (s.clock_synced === false) lines.push('Waiting for the clock.');
    if (s.config_from_shadow) lines.push('The configuration comes from the backup copy on the device.');
    for (const w of s.warnings || []) lines.push(w);
    this.warn.textContent = lines.join('   •   ');

    const zone = s.timezone || '';
    if (zone !== this.zone) { this.zone = zone; this.makeFormats(zone); this.tick(); }
  }

  /** Build the two clock formats for the time zone of the device (D40). */
  makeFormats(zone) {
    const time = { hour: '2-digit', minute: '2-digit', hour12: false };
    const date = { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' };
    if (zone) { time.timeZone = zone; date.timeZone = zone; }
    try {
      this.fmtTime = new Intl.DateTimeFormat(undefined, time);
      this.fmtDate = new Intl.DateTimeFormat(undefined, date);
    } catch {
      // A bad zone name must not stop the clock. Use the browser zone.
      delete time.timeZone;
      delete date.timeZone;
      this.fmtTime = new Intl.DateTimeFormat(undefined, time);
      this.fmtDate = new Intl.DateTimeFormat(undefined, date);
    }
  }

  tick() {
    if (!this.fmtTime) this.makeFormats('');
    const now = new Date();
    const time = this.fmtTime.format(now);
    const date = this.fmtDate.format(now);
    if (this.time.textContent !== time) this.time.textContent = time;
    if (this.date.textContent !== date) this.date.textContent = date;
  }

  move() {
    this.at = (this.at + 1) % SHIFTS.length;
    const [x, y] = SHIFTS[this.at];
    this.shift.style.transform = `translate(${x}px, ${y}px)`;
  }
}
