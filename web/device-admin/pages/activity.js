/* Activity: the ops log of this device, newest first.

   The log is a tab-separated file on the stick, so it survives a power cut. The
   times in it are UTC; this page shows them in the time zone of the device,
   because that is the clock the person in the room reads.
*/

import { h, fill, toast, table, banner, pageHead, errorText } from '/shared/ui.js';
import { api } from '/shared/api.js';
import { deviceTime } from '../util.js';

/* How many lines to ask for. The device keeps the last thousand and takes
   1200 as its largest count. */
const LINES = 200;

/* The kind of each event, from the name that the daemon logs. The list is read
   from the top, so a longer prefix goes before a shorter one. */
const KINDS = [
  ['scheduler.', 'playback'],
  ['browser.url.', 'playback'],
  ['browser.kiosk.', 'playback'],
  ['browser.suspend', 'screen'],
  ['browser.resume', 'screen'],
  ['browser.display.', 'screen'],
  ['browser.', 'player'],
  ['player', 'player'],
  ['library.', 'files'],
  ['media.', 'files'],
  ['playlist.', 'files'],
  ['provision.media', 'files'],
];

const FILTERS = [
  ['', 'Everything'],
  ['playback', 'Playback'],
  ['player', 'Player'],
  ['screen', 'Screen'],
  ['files', 'Files'],
  ['system', 'System'],
];

export function mount(main, ctx) {
  let entries = [];
  let filter = '';
  let auto = true;
  let gone = false;

  const chips = h('div', { class: 'pp-chips' });
  const tableSlot = h('div');
  const problem = h('div');

  /* The chips are built one time and only their state changes. A row that was
     built again at each click would take the keyboard away from the chip that the
     person just pressed.

     THE PLACE OF THIS LINE MATTERS. It was below drawChips() and load(), and a
     "const" cannot be read before its own line runs. drawChips() then stopped the
     whole mount with "Cannot access 'chipButtons' before initialization": no
     chips, no table, and load() never ran either. The page was empty and only the
     console of the browser said why. */
  const chipButtons = FILTERS.map(([value, label]) => h('button', {
    type: 'button', class: 'pp-chip', text: label,
    onClick: () => { filter = value; drawChips(); draw(); },
  }));

  const autoBox = h('input', {
    type: 'checkbox', checked: true,
    onChange: (e) => { auto = e.target.checked; },
  });

  fill(main,
    pageHead('Activity',
      'What this device has done, newest first. It is kept on the stick and trimmed to the last thousand lines, so it survives a power cut.',
      h('div', { class: 'pp-btns' },
        h('label', { class: 'pp-switch' }, autoBox, h('span', { text: 'Keep it fresh' })),
        h('button', { type: 'button', class: 'pp-btn', text: 'Refresh', onClick: () => load() }))),
    problem,
    h('div', { class: 'pp-row', style: { 'margin-bottom': '14px' } }, chips),
    tableSlot);

  drawChips();
  load();
  const timer = setInterval(() => { if (auto) load(); }, 10000);

  // The times are drawn in the time zone of the device, which comes with the
  // first report. Draw the table again when it arrives or when it changes.
  let zone = 'UTC';
  const unsubscribe = ctx.store.subscribe((status) => {
    if (!status || gone) return;
    const next = status.timezone || 'UTC';
    if (next === zone) return;
    zone = next;
    draw();
  });

  function drawChips() {
    if (!chips.firstChild) fill(chips, chipButtons);
    chipButtons.forEach((b, i) => b.setAttribute('aria-pressed', String(filter === FILTERS[i][0])));
  }

  async function load() {
    try {
      const out = await api('GET', `/api/opslog?n=${LINES}`);
      if (gone) return;
      entries = out.entries || [];
      fill(problem);
      draw();
    } catch (err) {
      if (gone) return;
      fill(problem, banner({ kind: 'danger', title: 'The log did not load', body: errorText(err) }));
    }
  }

  function kindOf(event) {
    const name = String(event || '');
    for (const [prefix, kind] of KINDS) {
      if (name.startsWith(prefix)) return kind;
    }
    return 'system';
  }

  function draw() {
    // The file grows downwards; the page reads from the top.
    const rows = entries.slice().reverse()
      .map((e) => ({ ...e, kind: kindOf(e.event) }))
      .filter((e) => !filter || e.kind === filter);

    const card = table({
      columns: [
        { key: 'time', label: 'When', width: '160px', render: (r) => h('span', { class: 'dv-when', text: deviceTime(r.time, zone) }) },
        {
          key: 'event',
          label: 'What happened',
          grow: true,
          render: (r) => h('span', null,
            h('span', { class: 'dv-event', text: r.event }),
            r.details ? h('span', { text: ` — ${r.details}` }) : null),
        },
        { key: 'kind', label: 'Kind', width: '90px', mono: true },
      ],
      rows,
      empty: filter ? 'Nothing of this kind in the last lines of the log.' : 'The log is empty. It fills the first time this device does something.',
      foot: [
        h('span', { text: `Showing ${rows.length} of the last ${entries.length} ${entries.length === 1 ? 'line' : 'lines'}, in ${zone}` }),
        h('button', { type: 'button', class: 'pp-btn pp-btn--ghost', text: 'Download this log', onClick: download }),
      ],
    });
    card.classList.add('dv-log');
    fill(tableSlot, card);
  }

  /* The device has no download route for the log, so the page writes the lines
     that it holds into a file of its own. */
  function download() {
    if (!entries.length) { toast('There is nothing to download yet.', 'danger'); return; }
    const text = entries.map((e) => [e.time, e.event, e.details].join('\t')).join('\n');
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: 'text/plain' }));
    const name = (ctx.store.status && ctx.store.status.device_id) || 'portapixel';
    const link = h('a', { href: url, download: `${name}-ops.log` });
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
      clearInterval(timer);
    },
  };
}
