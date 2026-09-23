/* Screens: every managed screen, as a list or as a wall.

   The rows come from one call that the poller makes every ten seconds. The
   search, the group filter, the status chips and the list-or-wall switch are all
   work in this page: one fleet is a few hundred rows, so the server sends them
   all and sorts nothing twice.

   The rows are patched in place and never built again, so a refresh does not
   make the page blink and does not lose the place of the pointer.
*/

import {
  h, fill, toast, banner, statusDot, modal, confirmDialog, fmtAgo, pageHead,
  errorText, setText, setShown, setClass, fmtTemp, guessKind, cell,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { sendToGroup } from '../commands.js';
import {
  stateInfo, parseStatus, freeSpace, mediaPreview, mediaIndex, playingMedia, screenHref, download, csvCell,
} from '../util.js';

/* The filter and the view live in the module, so a trip to one screen and back
   comes back to the same picture. Nothing is written to storage: this is the
   state of one tab, not a setting. */
let view = 'list';
let chip = 'all';
let search = '';
let groupFilter = 0;

const CHIPS = [
  { id: 'all', label: 'All' },
  { id: 'online', label: 'Checked in' },
  { id: 'quiet', label: 'Quiet' },
  { id: 'look', label: 'Needs a look' },
];

export function mount(main, ctx) {
  let groups = [];
  let library = mediaIndex([]);  // the library by hash, for the thumbnails
  let gone = false;

  /* One entry for each screen: the nodes plus the function that patches them.
     The map is the reason a refresh never blinks. The frame is the card that
     holds them, and it belongs to one view. */
  const entries = new Map();
  let frame = null;
  /* The group that the admin picked for a waiting request, by its code. The
     pending card is built again at every poll, so the choice cannot live in the
     DOM or it would snap back every ten seconds. */
  const pendingGroup = new Map();

  const noticesSlot = h('div');
  const pendingSlot = h('div');
  const listSlot = h('div');
  const staleNote = h('div', { class: 'pp-banner pp-banner--warn', hidden: true },
    h('div', { class: 'pp-banner__text' },
      h('div', { class: 'pp-banner__title', text: 'The last refresh did not answer' }),
      h('div', { class: 'pp-banner__body', text: 'The rows below are the ones that came in last. Every screen keeps playing either way.' })));

  /* ---- the toolbar ---- */

  const searchInput = h('input', {
    class: 'pp-input sv-tools__search', type: 'search', value: search,
    placeholder: "Search a name, an ID or what's playing", 'aria-label': 'Search the screens',
    onInput: () => { search = searchInput.value; paint(); },
  });

  const groupSelect = h('select', {
    class: 'pp-select', 'aria-label': 'Group',
    onChange: () => { groupFilter = Number(groupSelect.value); paint(); },
  });

  const chipRow = h('div', { class: 'pp-chips' }, CHIPS.map((c) => h('button', {
    type: 'button', class: 'pp-chip', text: c.label, dataset: { chip: c.id },
    'aria-pressed': String(chip === c.id),
    onClick: () => {
      chip = c.id;
      for (const b of chipRow.children) b.setAttribute('aria-pressed', String(b.dataset.chip === chip));
      paint();
    },
  })));

  const viewToggle = h('div', { class: 'pp-seg', role: 'group', 'aria-label': 'How to show the screens' },
    ['list', 'wall'].map((v) => h('button', {
      type: 'button', class: 'pp-seg__btn', text: v === 'list' ? 'List' : 'Wall',
      'aria-pressed': String(view === v),
      onClick: () => {
        if (view === v) return;
        view = v;
        for (const b of viewToggle.children) b.setAttribute('aria-pressed', String((b.textContent === 'List' ? 'list' : 'wall') === view));
        // The frame belongs to one view, so the next paint builds a new one.
        frame = null;
        paint();
      },
    })));

  fill(main,
    pageHead('Screens', 'Every screen checks in on its own schedule. Nothing here reaches out to them.',
      h('div', { class: 'pp-btns' },
        h('button', { type: 'button', class: 'pp-btn', text: 'Export the list', onClick: exportList }),
        h('button', {
          type: 'button', class: 'pp-btn', text: 'Send a command',
          onClick: () => sendToGroup(groups, groupFilter).then((sent) => { if (sent) ctx.store.refresh(); }),
        }),
        h('a', { class: 'pp-btn pp-btn--primary', href: '#/screens/add', text: 'Add screens' }))),
    staleNote,
    noticesSlot,
    pendingSlot,
    h('div', { class: 'sv-tools' }, searchInput, groupSelect, chipRow, h('div', { class: 'pp-spacer pp-hide-sm' }), viewToggle),
    listSlot);

  loadGroups();
  loadMedia();
  const unsubscribe = ctx.store.subscribe(paint);
  if (!ctx.store.loaded) ctx.store.refresh();

  /* ------------------------------------------------------------ side loads */

  async function loadGroups() {
    try {
      const out = await api('GET', '/api/admin/groups');
      if (gone) return;
      groups = out.groups || [];
      paintGroupSelect();
    } catch { /* the filter simply stays at "all groups" */ }
  }

  /* The thumbnail of "on screen now" is the library thumbnail of that file,
     found by the hash that the screen reports. The server takes no screenshots
     (D26), so this is as close to a picture of the screen as the fleet gets.
     A video keeps the icon here. A first frame in each row asks the server
     for a part of each video at each change of a large fleet. */
  async function loadMedia() {
    try {
      const out = await api('GET', '/api/admin/media');
      if (gone) return;
      library = mediaIndex(out.media);
      // The rows hold their preview, so they are built again with the pictures.
      frame = null;
      paint();
    } catch { /* the rows then show the icon of the kind */ }
  }

  function paintGroupSelect() {
    fill(groupSelect,
      h('option', { value: '0', text: 'All groups' }),
      groups.map((g) => h('option', { value: String(g.id), text: `${g.name} (${g.devices})` })));
    groupSelect.value = String(groupFilter);
  }

  /* ---------------------------------------------------------------- paint */

  function paint() {
    if (gone) return;
    const { devices, error, loaded } = ctx.store;
    if (error && !loaded) {
      fill(listSlot, banner({ kind: 'danger', title: 'The fleet did not load', body: errorText(error) }));
      return;
    }
    setShown(staleNote, !!error && loaded);
    paintNotices(devices);
    paintPending(devices);
    paintRows(devices.filter(keep));
  }

  function keep(dev) {
    /* A row with pending:true is an enrollment request and not a screen: it holds
       no group, no playlist, no last check-in and no status. It has its own card
       above the table, so it never becomes a row here (API change 1). */
    if (dev.pending) return false;
    const info = stateInfo(dev.state);
    if (chip === 'online' && dev.state !== 'online') return false;
    if (chip === 'quiet' && dev.state !== 'quiet') return false;
    if (chip === 'look' && !info.flag) return false;
    if (groupFilter && dev.group_id !== groupFilter) return false;
    const q = search.trim().toLowerCase();
    if (!q) return true;
    const now = parseStatus(dev).now_playing || {};
    return [dev.name, dev.id, dev.group_name, now.item, now.playlist]
      .some((v) => String(v || '').toLowerCase().includes(q));
  }

  /* --------------------------------------------------------- the notices */

  /* One card for each screen that reports new hardware. A card that moved into
     a replacement box is the normal reason, and one click ends it (D21). */
  function paintNotices(devices) {
    const swapped = devices.filter((d) => d.state === 'needs_confirm');
    fill(noticesSlot, swapped.map((dev) => banner({
      title: `${dev.name} is running on different hardware`,
      body: h('div', null,
        'Its hardware ID changed from ',
        h('span', { class: 'pp-mono', text: short(dev.prev_hardware_id) }), ' to ',
        h('span', { class: 'pp-mono', text: short(dev.hardware_id) }),
        ' and it kept its pairing. That is normal when a card moves into a replacement box. Confirm it and the old ID is retired.'),
      actions: [
        h('button', {
          type: 'button', class: 'pp-btn', text: "This wasn't us",
          onClick: () => explainDenial(dev),
        }),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--primary', text: 'Confirm the swap',
          onClick: () => confirmSwap(dev),
        }),
      ],
    })));
  }

  async function confirmSwap(dev) {
    try {
      await api('POST', `/api/admin/devices/${encodeURIComponent(dev.id)}/confirm-hardware`);
      toast('The swap is confirmed. The old ID is retired.');
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  function explainDenial(dev) {
    modal({
      title: 'Nobody moved this card?',
      body: h('div', null,
        h('div', null, 'Then this box is not the screen that you paired. Nothing bad has happened yet: it plays what ',
          h('b', { text: dev.name }), ' plays, and it holds a token that you can take away.'),
        h('div', { style: { 'margin-top': '10px' } },
          'Remove the screen on its own page. Its token goes with it, so both boxes have to ask to join again and you see which one comes back.')),
      actions: [
        { label: 'Later', value: false },
        { label: 'Open the screen', value: true, kind: 'primary' },
      ],
    }).then((go) => { if (go) location.hash = screenHref(dev.id).slice(1); });
  }

  /* -------------------------------------------------------- the pending card */

  function paintPending(devices) {
    const waiting = devices.filter((d) => d.state === 'pending');
    if (waiting.length === 0) { fill(pendingSlot); return; }
    fill(pendingSlot, h('div', { class: 'pp-card', style: { 'margin-bottom': '12px' } },
      h('div', { class: 'pp-note', style: { 'font-weight': '600' } },
        waiting.length === 1
          ? 'One screen is waiting to be let in'
          : `${waiting.length} screens are waiting to be let in`),
      waiting.map(pendingRow)));
  }

  function pendingRow(dev) {
    const code = dev.pending_code || '';
    const pick = h('select', {
      class: 'pp-select', 'aria-label': `Group for ${dev.name || dev.id}`,
      onChange: () => { pendingGroup.set(code, pick.value); },
    },
      h('option', { value: '0', text: 'No group yet' }),
      groups.map((g) => h('option', { value: String(g.id), text: g.name })));
    pick.value = pendingGroup.get(code) || '0';

    /* collides_with names a screen that is already paired. The name reads better
       than the ID, so look it up in the fleet that the poller holds. */
    const other = () => {
      const row = ctx.store.device(dev.collides_with);
      return (row && row.name) || dev.collides_with;
    };
    const collideWords = () => `A different machine asks to be ${other()} (${dev.collides_with}), `
      + 'which is already paired. Approve only if you replaced the hardware. '
      + 'Approving signs the old machine out.';

    const act = async (what) => {
      if (what === 'approve' && dev.collides_with && !(await confirmDialog({
        title: `Let this machine be ${other()}?`,
        body: h('div', null,
          h('div', { text: collideWords() }),
          h('div', { style: { 'margin-top': '8px' } },
            'The token of the machine that holds that ID is revoked, so it stops checking in. It keeps playing what it already has.')),
        confirm: 'Let it in',
        cancel: 'Not now',
        kind: 'danger',
      }))) return;
      try {
        // The approve and the reject routes take no body at all. The group is the
        // one value that is worth sending (API change 9).
        const body = what === 'approve' ? { group_id: Number(pick.value) } : null;
        await api('POST', `/api/admin/pending/${encodeURIComponent(code)}/${what}`, body);
        pendingGroup.delete(code);
        toast(what === 'approve'
          ? 'It is in. It gets its token at its next check-in, within one poll interval.'
          : 'It was turned away.');
      } catch (err) {
        if (err.status === 409) toast('That request does not wait any more. Somebody may have answered it already.', 'danger');
        else if (err.status === 404) toast('That request is gone. The list is up to date now.', 'danger');
        else toast(errorText(err), 'danger');
      }
      ctx.store.refresh();
    };

    return h('div', { class: 'sv-wait' },
      h('span', { class: 'pp-code pp-code--md', text: code || '——————' }),
      h('div', { class: 'sv-wait__who' },
        h('div', { style: { 'font-weight': '500' } }, dev.name || 'A new screen'),
        h('div', { class: 'sv-wait__meta' },
          [dev.id, dev.last_ip, short(dev.hardware_id)].filter(Boolean).join(' · ')),
        // The server sets collides_with when the ID of the request is already a
        // paired screen. Letting it in takes that screen over, so the row says so
        // and the click asks again.
        dev.collides_with ? h('div', {
          class: 'pp-small', style: { color: 'var(--pp-danger)', 'margin-top': '3px' },
          text: collideWords(),
        }) : null),
      h('div', { class: 'pp-small pp-muted pp-nowrap', text: `asked ${fmtAgo(dev.created_at)}` }),
      pick,
      h('div', { class: 'pp-btns' },
        h('button', { type: 'button', class: 'pp-btn', text: 'Ignore', onClick: () => act('reject') }),
        h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Let it in', onClick: () => act('approve') })));
  }

  /* ------------------------------------------------------------- the rows */

  function paintRows(list) {
    if (!frame) frame = buildFrame();
    const body = frame.body;

    const wanted = new Set(list.map((d) => d.id));
    for (const [id, entry] of entries) {
      if (!wanted.has(id)) { entry.remove(); entries.delete(id); }
    }
    for (const dev of list) {
      let entry = entries.get(dev.id);
      if (!entry) { entry = view === 'list' ? listRow(dev) : wallTile(dev); entries.set(dev.id, entry); }
      entry.update(dev);
      entry.place(body);
    }

    if (list.length === 0) {
      if (!frame.empty.isConnected) body.append(frame.empty);
    } else if (frame.empty.isConnected) {
      frame.empty.remove();
    }

    const total = ctx.store.devices.length;
    setText(frame.count, list.length === total
      ? `${total} ${total === 1 ? 'screen' : 'screens'}`
      : `${list.length} of ${total} screens`);
  }

  function buildFrame() {
    entries.clear();
    const empty = h('div', { class: 'pp-empty' },
      h('div', { class: 'pp-empty__title', text: 'No screen matches' }),
      h('div', { class: 'pp-empty__body', text: 'Change the search or the filters. A screen that has never checked in is under "Needs a look".' }));

    if (view === 'wall') {
      const body = h('div', { class: 'pp-grid pp-grid--wide' });
      const count = h('span');
      fill(listSlot, body, h('div', { class: 'pp-help' }, count));
      return { body, empty, count };
    }

    const body = h('div', { class: 'pp-table__inner' });
    body.append(h('div', { class: 'pp-table__head' },
      cell({ grow: true }, 'Screen'),
      cell({ width: '104px' }, 'Group'),
      cell({ width: '116px' }, 'Last check-in'),
      cell({ width: '186px' }, 'On screen now'),
      cell({ width: '62px' }, 'Temp'),
      cell({ width: '98px' }, 'Free'),
      cell({ width: '58px' }, 'Version')));
    const count = h('span');
    fill(listSlot, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-table' }, body),
      h('div', { class: 'pp-card__foot' }, count,
        h('span', { text: 'The newest check-in is at the top. A screen that needs a look carries a red edge.' }))));
    return { body, empty, count };
  }

  /* ---- one row of the list view ---- */

  function listRow(dev) {
    const dot = statusDot('quiet');
    const name = h('span');
    const id = h('span', { class: 'sv-name__id' });
    // A title on a button never becomes its accessible name, and the red edge is
    // invisible to a screen reader. So the state goes in the row as words.
    const word = h('span', { class: 'pp-sr-only' });
    const group = h('span', { class: 'pp-small' });
    const seen = h('span', { class: 'pp-cell--mono' });
    const nowSlot = h('span', { class: 'sv-now' });
    const temp = h('span');
    const freeSlot = h('span');
    const version = h('span');

    const row = h('button', {
      type: 'button', class: 'pp-table__row',
      onClick: () => { location.hash = screenHref(dev.id).slice(1); },
    },
      cell({ grow: true }, h('span', { class: 'sv-name' }, dot, h('span', { class: 'pp-trunc' }, name, ' ', id), word)),
      cell({ width: '104px' }, group),
      cell({ width: '116px' }, seen),
      cell({ width: '186px' }, nowSlot),
      cell({ width: '62px', mono: true }, temp),
      cell({ width: '98px' }, freeSlot),
      cell({ width: '58px', mono: true }, version));

    const alertText = h('span');
    const alert = h('div', { class: 'pp-table__alert' }, alertText,
      h('a', { class: 'pp-btn pp-btn--sm', href: screenHref(dev.id), text: 'Open the screen' }));

    let lastItem = null;
    return {
      update(d) {
        const info = stateInfo(d.state);
        const st = parseStatus(d);
        const now = st.now_playing || {};
        dot.className = `pp-dot pp-dot--${info.kind}`;
        row.title = `${d.name} — ${info.word}`;
        setText(word, ` — ${info.word}`);
        setText(name, d.name || d.id);
        setText(id, d.id);
        setText(group, d.group_name || 'No group');
        setText(seen, d.state === 'pending' ? 'not yet' : fmtAgo(d.last_seen));
        setClass(seen.parentElement, 'pp-cell--danger', info.flag);
        setClass(row, 'sv-row--flag', info.flag);

        const item = now.item || '';
        if (item !== lastItem) {
          lastItem = item;
          fill(nowSlot, nowParts(now, d.state));
        }
        setClass(nowSlot, 'sv-now--dim', d.state !== 'online');
        setText(temp, fmtTemp(st.temp_c));
        fill(freeSlot, freeSpace(st.media_free_bytes, st.media_total_bytes));
        setText(version, d.version || '—');

        const text = alertFor(d);
        if (text) { setText(alertText, text); } else if (alert.isConnected) alert.remove();
        this.hasAlert = !!text;
      },
      place(body) {
        body.append(row);
        if (this.hasAlert) body.append(alert);
      },
      remove() { row.remove(); alert.remove(); },
    };
  }

  function nowParts(now, state) {
    if (!now.item) {
      return h('span', { class: 'sv-now__text pp-muted', text: state === 'pending' ? 'waiting for approval' : 'nothing reported' });
    }
    const kind = now.kind || guessKind(now.item);
    return [
      mediaPreview(kind, playingMedia(now, library)),
      h('span', { class: 'sv-now__text', title: now.item, text: now.item }),
    ];
  }

  function alertFor(dev) {
    if (dev.sync_error) {
      return `The sync did not fit — it ${dev.sync_error}. It downloaded nothing and kept playing what it had.`;
    }
    if (dev.state === 'conflict') {
      return 'Two boxes report from one token. Open the screen to sort it out.';
    }
    if (dev.state === 'offline' && dev.last_seen && !String(dev.last_seen).startsWith('0001-')) {
      return `It has not checked in since ${fmtAgo(dev.last_seen)}. It keeps playing what it has.`;
    }
    if (dev.state === 'offline') {
      return 'It has never checked in. Its token may not have reached it.';
    }
    return '';
  }

  /* ---- one tile of the wall view ---- */

  function wallTile(dev) {
    const dot = statusDot('quiet');
    const name = h('span', { class: 'sv-tile__name' });
    const thumbSlot = h('span', { style: { display: 'block' } });
    const playing = h('span', { class: 'pp-tile__use pp-trunc' });
    const seen = h('span');
    const temp = h('span');
    const word = h('span', { class: 'pp-sr-only' });

    const tile = h('a', { class: 'pp-tile', href: screenHref(dev.id), style: { display: 'block' } },
      thumbSlot,
      h('span', { class: 'pp-tile__body' },
        h('span', { class: 'sv-tile__head' }, dot, name, word),
        playing,
        h('span', { class: 'sv-tile__meta' }, seen, temp)));

    let lastItem = null;
    return {
      update(d) {
        const info = stateInfo(d.state);
        const st = parseStatus(d);
        const now = st.now_playing || {};
        dot.className = `pp-dot pp-dot--${info.kind}`;
        setText(name, d.name || d.id);
        setText(word, ` — ${info.word}`);
        setClass(tile, 'sv-row--flag', info.flag);
        const item = now.item || '';
        if (item !== lastItem) {
          lastItem = item;
          const kind = now.kind || guessKind(item);
          fill(thumbSlot, mediaPreview(kind, playingMedia(now, library), { wide: true }));
        }
        setText(playing, now.item || (d.state === 'pending' ? 'waiting for approval' : 'nothing reported'));
        setText(seen, d.state === 'pending' ? 'not yet' : fmtAgo(d.last_seen));
        setText(temp, fmtTemp(st.temp_c));
      },
      place(body) { body.append(tile); },
      remove() { tile.remove(); },
    };
  }

  /* ------------------------------------------------------------- export */

  /* The list as a spreadsheet file. There is no route for it: the rows are
     already in the browser, so the file is made here. */
  function exportList() {
    const head = ['name', 'id', 'group', 'state', 'last check-in', 'version', 'temperature C', 'free bytes', 'on screen now'];
    const lines = [head];
    for (const dev of ctx.store.devices.filter(keep)) {
      const st = parseStatus(dev);
      const now = st.now_playing || {};
      lines.push([dev.name, dev.id, dev.group_name, dev.state, dev.last_seen, dev.version,
        st.temp_c || '', st.media_free_bytes || '', now.item || '']);
    }
    const csv = lines.map((row) => row.map(csvCell).join(',')).join('\r\n');
    download('portapixel-screens.csv', csv, 'text/csv');
    toast(`${lines.length - 1} ${lines.length === 2 ? 'screen' : 'screens'} in the file.`);
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
    },
  };
}

/* The first eight characters of a hardware ID. The whole value is a SHA-256 and
   nobody reads 64 characters off a card. */
function short(id) {
  const v = String(id || '');
  return v.length > 12 ? `${v.slice(0, 8)}…` : v || '—';
}
