/* Dashboard: what this one screen is doing, for a person standing in front of
   it or on the same network.

   The page is built once and then written into. Every five seconds the poller
   brings a new report and this file changes the values that are different; it
   never builds the page again. A page that rebuilt itself would lose the
   caret in the root password field and would blink on a slow box.
*/

import {
  h, fill, toast, banner, factList, progress, statusDot,
  confirmDialog, fmtBytes, fmtDuration, fmtAgo, fmtTemp,
  card, pageHead, setText, setShown, errorText,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { deviceTime, notInThisBuild, localMedia, frameURL, playingItem } from '../util.js';

/* status.warnings is a list of {code, message}. The codes are the contract with
   the daemon (internal/manifest/status.go). This page matched the first words of
   the message before the codes existed, and one better sentence broke a banner. */
const CODE = {
  web: 'default-web-password',
  root: 'default-root-password',
  timezone: 'timezone-utc',
  clock: 'clock-unsynced',
  shadow: 'config-shadow',
  managed: 'config-managed-ignored',
  playlist: 'playlist-problem',
  hardware: 'hardware-changed',
};

/* The device keeps hardware_changed true until a fleet server confirms it, and a
   screen that runs on its own has no server to do that. So the dismissal lives
   in this browser, under the ID that the device reports now: a later change gives
   a new ID and the notice comes back. */
const DISMISSED = 'pp-hardware-notice-dismissed';

function noticeDismissed(id) {
  try { return localStorage.getItem(DISMISSED) === id; } catch { return false; }
}

function dismissNotice(id) {
  try { localStorage.setItem(DISMISSED, id); } catch { /* a private window is fine */ }
}

/* The codes that a card of this page already says in its own words. They are
   dropped from the list of other warnings so that nothing is said twice. */
const SAID_ELSEWHERE = new Set([
  CODE.web, CODE.root, CODE.timezone, CODE.clock, CODE.shadow, CODE.managed,
  CODE.playlist, CODE.hardware,
]);

const BROWSER_STATE = {
  running: 'playing',
  starting: 'starting',
  restarting: 'restarting',
  stopped: 'stopped',
  'waiting-for-display': 'waiting for a display',
  disabled: 'switched off',
};

export function mount(main, ctx) {
  let cfg = null;      // the configuration: the image time and the screen times
  let snap = null;     // the playlists: the preview file and the problems
  let entries = [];
  let nagKey = '';
  let previewKey = '';
  let pairKey = '';
  let logKey = '';
  let playing = null;  // {since, seconds} of the item on the screen
  let tick = 0;
  let gone = false;

  /* ---- the parts of the page ---- */

  const lead = h('p', { class: 'pp-lead' });
  const headDot = statusDot('quiet');
  const headText = h('span');
  const stale = h('div', { class: 'pp-banner pp-banner--warn', hidden: true },
    h('div', { class: 'pp-banner__text' },
      h('div', { class: 'pp-banner__title', text: 'The device did not answer the last refresh' }),
      h('div', { class: 'pp-banner__body', text: 'The values below are the last ones it sent. The screen keeps playing.' })));
  const nags = h('div');

  // On screen now.
  const nowPlaylist = h('span');
  const nowMedia = h('span', { class: 'pp-thumb pp-thumb--wide dv-now__thumb' });
  const nowName = h('span', { class: 'pp-thumb__name' });
  const nowCount = h('span', { class: 'pp-muted' });
  const nowLeft = h('span', { class: 'pp-mono' });
  const nowBar = progress(0, { thin: true, brand: true });
  const nowBody = h('div', null,
    h('div', { style: { position: 'relative' } }, nowMedia, nowName),
    h('div', { class: 'dv-now__meta' }, nowCount, nowLeft),
    nowBar);
  const nowEmptyBody = h('div', { class: 'pp-empty__body' });
  const nowEmpty = h('div', { class: 'pp-empty', hidden: true },
    h('div', { class: 'pp-empty__title', text: 'Nothing is on the screen' }),
    nowEmptyBody);
  const nowNext = h('span');
  const nowCardEl = card({
    title: 'On screen now',
    meta: nowPlaylist,
    body: [nowBody, nowEmpty],
    foot: [nowNext, h('button', {
      type: 'button', class: 'pp-btn', text: 'Restart player',
      onClick: () => command('restart-browser', {
        title: 'Restart the player?',
        body: 'The screen goes black for a few seconds and then starts the playlist again.',
        confirm: 'Restart it',
      }),
    })],
  });

  // Screen.
  const screenText = h('div', { class: 'pp-help', style: { 'margin-top': '0' } });
  const screenBtn = h('button', { type: 'button', class: 'pp-btn', text: 'Turn screen off', onClick: toggleScreen });
  const screenCard = card({
    title: 'Screen',
    body: h('div', { class: 'pp-row', style: { 'justify-content': 'space-between' } },
      h('div', { style: { flex: '1 1 200px' } }, screenText), screenBtn),
  });

  // The facts, with a value node for each row so that only the text changes.
  const vUptime = h('span');
  const vTemp = h('span');
  const vLoad = h('span');
  const vRAM = h('span');
  const vNetwork = h('span');
  const vClock = h('span');
  const vVersion = h('span');
  const vSpace = h('span');
  const spaceBar = progress(0);
  const factsCard = card({
    title: 'Health',
    body: [
      factList([
        ['Running for', vUptime],
        ['Temperature', vTemp],
        ['Load', vLoad],
        ['Memory in use', vRAM],
        ['Network', vNetwork],
        ['Clock', vClock],
        ['Version', vVersion],
      ]),
      h('div', { style: { 'margin-top': '14px' } },
        h('div', { class: 'pp-row', style: { 'justify-content': 'space-between', 'margin-bottom': '6px' } },
          h('span', { class: 'pp-small pp-muted', text: 'Space on the stick' }),
          h('span', { class: 'pp-small pp-mono' }, vSpace)),
        spaceBar),
    ],
  });

  // Pairing. The two states are different cards, so this one is rebuilt when
  // the state changes and left alone the rest of the time.
  const pairSlot = h('div');

  const rescanBtn = h('button', {
    type: 'button', class: 'pp-btn', text: 'Look for new files', onClick: rescan,
  });

  // Things you can do now.
  const actionsCard = card({
    title: 'Things you can do now',
    body: [
      h('div', { class: 'pp-btns' },
        rescanBtn,
        h('button', {
          type: 'button', class: 'pp-btn', text: 'Restart player',
          onClick: () => command('restart-browser', {
            title: 'Restart the player?',
            body: 'The screen goes black for a few seconds and then starts the playlist again.',
            confirm: 'Restart it',
          }),
        }),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--danger-outline', text: 'Reboot device',
          onClick: () => command('reboot', {
            title: 'Reboot this device?',
            body: 'The screen stays black for about half a minute. Nothing on the stick changes.',
            confirm: 'Reboot it',
            kind: 'danger',
          }),
        })),
      h('div', { class: 'pp-help', text: 'Copied files onto the stick from a laptop? Look for new files finds them, then add them to a playlist on the Playlists page.' }),
    ],
  });

  // Recent activity.
  const logBody = h('div', { class: 'pp-card__body' });
  const activityCard = h('div', { class: 'pp-card' },
    h('div', { class: 'pp-card__head' },
      h('div', { class: 'pp-h2', text: 'Recent activity' }),
      h('a', { class: 'pp-btn pp-btn--ghost', href: '#/activity', text: 'See everything →' })),
    logBody);

  fill(main,
    pageHead('Dashboard', lead, h('div', { class: 'pp-status' }, headDot, headText)),
    stale,
    nags,
    h('div', { class: 'pp-stack' },
      nowCardEl,
      h('div', { class: 'pp-cards' }, screenCard, factsCard),
      h('div', { class: 'pp-cards' }, pairSlot, actionsCard),
      activityCard));

  /* ---- the report ---- */

  const unsubscribe = ctx.store.subscribe(apply);
  const timer = setInterval(second, 1000);
  loadConfig();
  loadPlaylists();
  loadLog();

  function apply(status, error) {
    setShown(stale, !!error && !!status);
    if (!status) {
      if (error) setText(headText, 'The device did not answer');
      return;
    }

    // Header.
    const state = BROWSER_STATE[status.browser_state] || status.browser_state || 'unknown';
    setText(headText, `${status.screen_on ? 'Screen on' : 'Screen off'} · ${state}`);
    headDot.className = `pp-dot pp-dot--${dotFor(status)}`;
    setText(lead, status.paired
      ? `Managed by ${status.server_url || 'a control server'}. Display, sound and network settings are still yours.`
      : 'Playing on its own. Not connected to a control server.');

    renderNags(status);
    renderNow(status);
    renderScreen(status);
    renderFacts(status);
    renderPairing(status);
    renderLog();
  }

  /* ---- nag banners ---- */

  function renderNags(status) {
    const warnings = status.warnings || [];
    const problems = (snap && snap.problems) || [];
    const hasCode = (code) => warnings.some((w) => w && w.code === code);
    const has = {
      web: hasCode(CODE.web),
      root: hasCode(CODE.root),
      shadow: !!status.config_from_shadow,
      managed: hasCode(CODE.managed),
      timezone: (status.timezone || 'UTC') === 'UTC',
      clock: status.clock_synced === false,
      problems: problems.length,
      hardware: !!status.hardware_changed && !noticeDismissed(status.device_id || ''),
    };
    const others = warnings
      .filter((w) => w && !SAID_ELSEWHERE.has(w.code))
      .map((w) => w.message);

    // Rebuild only when the set of nags changes: the root password field must
    // keep what the user typed while the poller runs.
    const key = JSON.stringify([has, others]);
    if (key === nagKey) return;
    nagKey = key;

    fill(nags,
      has.web ? banner({
        kind: 'danger',
        title: 'Change the admin password',
        body: 'This device still uses the password it shipped with. Anyone on this network can sign in and change what is on the screen.',
        actions: [h('a', { class: 'pp-btn pp-btn--danger', href: '#/settings', text: 'Change it' })],
      }) : null,
      has.root ? rootPasswordNag() : null,
      has.shadow ? banner({
        kind: 'danger',
        title: 'Running from the backup settings',
        body: ['portapixel.toml on the stick is missing or cannot be read, so the device runs from the last good copy it keeps for itself. Open Settings and save once: that writes a good file back onto the stick.'],
        actions: [h('a', { class: 'pp-btn pp-btn--danger', href: '#/settings', text: 'Open Settings' })],
      }) : null,
      has.managed ? banner({
        kind: 'warn',
        title: 'A local setting is not used',
        body: 'A setting in portapixel.toml is managed by the fleet server, so the device does not use the local value.',
      }) : null,
      has.timezone ? banner({
        kind: 'warn',
        title: 'Set the time zone',
        body: 'The clock is on UTC, so scheduled playlists stay off and the default playlist keeps looping. Pick where this screen lives and schedules start working.',
        actions: [h('a', { class: 'pp-btn pp-btn--warn-outline', href: '#/settings', text: 'Set time zone' })],
      }) : null,
      /* The card moved into another box (D21). Nothing is wrong and nothing has
         to be done, so this one is a plain card with one button. */
      has.hardware ? banner({
        kind: 'info',
        title: 'This device has new hardware',
        body: h('div', null,
          h('div', null, 'It now reports the ID ',
            h('span', { class: 'pp-mono', text: status.device_id || '' }),
            '. The Activity page names the ID that it had before.'),
          h('div', { style: { 'margin-top': '6px' } },
            'If you moved this USB stick into a different box, there is nothing else to do: the name, the settings and the pairing all came across.')),
        actions: [h('button', {
          type: 'button', class: 'pp-btn', text: 'Got it',
          onClick: () => {
            dismissNotice(status.device_id || '');
            nagKey = '';
            if (ctx.store.status) renderNags(ctx.store.status);
          },
        })],
      }) : null,
      has.clock ? banner({
        kind: 'warn',
        title: 'The clock is not set yet',
        body: 'The device has not reached a time server, so schedule rules are held and the default playlist plays. It follows the rules the moment the clock comes in.',
      }) : null,
      has.problems ? banner({
        kind: 'warn',
        title: problems.length === 1 ? 'One playlist needs a look' : `${problems.length} playlists need a look`,
        body: h('ul', { style: { margin: '4px 0 0', 'padding-left': '18px' } },
          problems.map((p) => h('li', null,
            p.playlist ? [h('span', { class: 'pp-mono', text: p.playlist }), ' — '] : null,
            p.message))),
        actions: [h('a', { class: 'pp-btn pp-btn--warn-outline', href: '#/playlists', text: 'Open Playlists' })],
      }) : null,
      others.length ? banner({
        kind: 'warn',
        title: 'The device reports this',
        body: h('ul', { style: { margin: '4px 0 0', 'padding-left': '18px' } }, others.map((w) => h('li', { text: w }))),
      }) : null);
  }

  /* The root password is not in portapixel.toml, so it has its own route and
     its own little form (D23). */
  function rootPasswordNag() {
    const input = h('input', {
      class: 'pp-input', type: 'password', autocomplete: 'new-password',
      'aria-label': 'A new password for root',
      placeholder: 'at least 8 characters', style: { 'max-width': '260px' },
    });
    const error = h('div', { class: 'pp-error', hidden: true });
    const button = h('button', { type: 'button', class: 'pp-btn pp-btn--danger', text: 'Set password' });

    button.addEventListener('click', async () => {
      error.hidden = true;
      if (input.value.length < 8) {
        error.textContent = 'That needs eight characters or more.';
        error.hidden = false;
        input.focus();
        return;
      }
      button.disabled = true;
      try {
        await api('POST', '/api/system/root-password', { password: input.value });
        input.value = '';
        toast('The root password is changed.');
        nagKey = '';               // the nag goes at the next report
        ctx.store.refresh();
      } catch (err) {
        // A 501 means the route is not in this build, which is not a fault of the
        // password that the person typed.
        error.textContent = notInThisBuild(err)
          ? 'This build cannot change the root password. The image does it at the first boot.'
          : errorText(err);
        error.hidden = false;
      } finally {
        button.disabled = false;
      }
    });

    return banner({
      kind: 'danger',
      title: 'Change the root password',
      body: h('div', null,
        h('div', { text: 'Remote terminal access is on and root still uses the password the image shipped with. Anyone on this network can log in as root.' }),
        h('label', { class: 'pp-row', style: { 'margin-top': '10px' } },
          h('span', { class: 'pp-sr-only', text: 'A new password for root' }), input, button),
        error),
    });
  }

  /* ---- on screen now ---- */

  function renderNow(status) {
    const np = status.now_playing;
    setShown(nowBody, !!np);
    setShown(nowEmpty, !np);

    if (!np) {
      playing = null;
      setText(nowPlaylist, '');
      setText(nowNext, '');
      setText(nowEmptyBody, emptyReason(status));
      return;
    }

    const list = findPlaylist(np.playlist);
    setText(nowPlaylist, list ? list.title : np.playlist || '');

    // The report names the file. Its index is a place in the shuffled list that
    // the player got, so the page finds the item by its name and hash.
    const items = (list && list.items) || [];
    const { item, index } = playingItem(items, np);
    setText(nowCount, index >= 0 ? `Item ${index + 1} of ${items.length}` : '');

    // The preview is rebuilt only when the file changes: a video element that
    // is replaced every five seconds never shows a frame.
    const key = `${np.playlist}|${np.index}|${np.item}`;
    if (key !== previewKey) {
      previewKey = key;
      fill(nowMedia, previewFor(item, np));
      setText(nowName, np.item || (item && item.name) || '');
    }

    playing = { since: Date.parse(np.since), seconds: itemSeconds(item) };
    second();

    // The next item is known only for a list in its own order.
    const next = index >= 0 && items.length > 1 && !shuffled(list) ? items[(index + 1) % items.length] : null;
    setText(nowNext, next ? `Next: ${next.name} · ${describe(next)}` : '');
  }

  /* The daemon shuffles a playlist when its own shuffle key, or else the device
     setting, says so. With no configuration yet, the order is not known. */
  function shuffled(list) {
    if (list && typeof list.shuffle === 'boolean') return list.shuffle;
    return !cfg || !!cfg.playback.shuffle;
  }

  function emptyReason(status) {
    switch (status.browser_state) {
      case 'waiting-for-display':
        return 'No display answers on the HDMI port. The device waits and keeps looking; this does not count as a fault.';
      case 'disabled':
        return 'The browser is switched off on this build, so there is nothing to show. The rest of this page works.';
      case 'starting':
      case 'restarting':
        return 'The player is starting. It reports the first item in a moment.';
      default:
        return 'The player has not reported an item yet. Check that a playlist holds files that this device can show.';
    }
  }

  /* A still frame is the honest preview: an image element for an image, and a
     video element parked on its first frame for a video. A web page item has no
     file, so it gets its address on the striped placeholder. */
  function previewFor(item, np) {
    const kind = (item && item.kind) || np.kind;
    // The address comes from the API, so it is checked before it goes in a src.
    // Only a path under /media/ of this device is drawn; anything else takes the
    // placeholder, so no off-site request goes out from this page.
    const src = localMedia(item && item.src);
    if (kind === 'image' && src) {
      return h('img', { src, alt: '', loading: 'lazy' });
    }
    if (kind === 'video' && src) {
      const video = h('video', { src: frameURL(src), muted: true, playsinline: true, preload: 'metadata' });
      // The attribute sets the default only. A script that makes the element must
      // also set the property.
      video.muted = true;
      return video;
    }
    return h('span', { class: 'pp-thumb__label' },
      h('span', { class: 'pp-kind pp-kind--chip', text: kind === 'url' ? 'Web page' : String(kind || 'item') }));
  }

  /* How long the item stays on the screen. An image with no time of its own
     takes the device setting; a video plays its natural length, which nothing
     reports, so there is no bar for it. */
  function itemSeconds(item) {
    if (!item) return 0;
    if (item.kind === 'video') return Number(item.max_duration) || 0;
    if (Number(item.duration)) return Number(item.duration);
    if (item.kind === 'image' && cfg) return Number(cfg.playback.image_duration) || 0;
    return 0;
  }

  function describe(item) {
    const bits = [item.kind === 'url' ? 'web page' : item.kind];
    const seconds = itemSeconds(item);
    if (seconds) bits.push(fmtDuration(seconds));
    else if (item.kind === 'video') bits.push('full length');
    if (item.kind === 'video') bits.push(item.mute ? 'no sound' : 'sound on');
    return bits.join(', ');
  }

  /* The countdown runs every second from the time the item started, so the
     numbers move between two reports. */
  function second() {
    tick += 1;
    if (tick % 20 === 0) loadLog();

    if (!playing || !playing.since) { setText(nowLeft, ''); nowBar.set(0); return; }
    const gone = Math.max(0, (Date.now() - playing.since) / 1000);
    if (!playing.seconds) {
      setText(nowLeft, `on screen for ${fmtDuration(gone)}`);
      nowBar.set(0);
      return;
    }
    const left = Math.max(0, Math.ceil(playing.seconds - gone));
    setText(nowLeft, `${left}s left of ${playing.seconds}s`);
    nowBar.set(gone / playing.seconds);
  }

  /* ---- screen ---- */

  function renderScreen(status) {
    const times = cfg && cfg.display.on_time && cfg.display.off_time
      ? ` On at ${cfg.display.on_time}, off again at ${cfg.display.off_time}.` : '';
    if (!status.display_connected) {
      setText(screenText, 'No display is connected. The device waits for one and keeps the playlist ready.');
    } else {
      setText(screenText, `${status.screen_on ? 'The screen is on.' : 'The screen is off.'}${times}`);
    }
    setText(screenBtn, status.screen_on ? 'Turn screen off' : 'Turn screen on');
    screenBtn.dataset.command = status.screen_on ? 'screen-off' : 'screen-on';
  }

  async function toggleScreen() {
    const name = screenBtn.dataset.command || 'screen-off';
    screenBtn.disabled = true;
    try {
      await api('POST', `/api/commands/${name}`);
      toast(name === 'screen-off' ? 'The screen is going off.' : 'The screen is coming on.');
      await ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    } finally {
      screenBtn.disabled = false;
    }
  }

  /* ---- health ---- */

  function renderFacts(status) {
    setText(vUptime, fmtDuration(status.uptime_seconds));
    setText(vTemp, fmtTemp(status.temp_c));
    // A hot box turns its own row red, which is the one number that matters.
    vTemp.closest('.pp-facts__row').className = `pp-facts__row${tempKind(status.temp_c)}`;
    setText(vLoad, Number(status.load || 0).toFixed(2));

    const total = Number(status.ram_total_bytes) || 0;
    const free = Number(status.ram_free_bytes) || 0;
    setText(vRAM, total ? `${fmtBytes(total - free)} of ${fmtBytes(total)}` : '—');

    const ips = (status.ips || []).join(', ');
    setText(vNetwork, [ips, status.mdns_name].filter(Boolean).join(' · ') || 'no address');
    setText(vClock, status.clock_synced
      ? `in step · ${status.timezone || 'UTC'}`
      : `not in step · ${status.timezone || 'UTC'}`);
    setText(vVersion, status.version || '—');

    const mediaTotal = Number(status.media_total_bytes) || 0;
    const mediaFree = Number(status.media_free_bytes) || 0;
    setText(vSpace, mediaTotal ? `${fmtBytes(mediaFree)} free of ${fmtBytes(mediaTotal)}` : '—');
    spaceBar.set(mediaTotal ? (mediaTotal - mediaFree) / mediaTotal : 0);
  }

  function tempKind(c) {
    const n = Number(c) || 0;
    if (n >= 80) return ' pp-facts__row--danger';
    if (n >= 70) return ' pp-facts__row--warn';
    return '';
  }

  /* ---- pairing ---- */

  function renderPairing(status) {
    const key = `${status.paired}|${status.server_url || ''}|${status.pairing_code || ''}|${status.last_sync_result || ''}|${status.sync_error || ''}`;
    if (key === pairKey) return;
    pairKey = key;

    if (status.paired) {
      fill(pairSlot, card({
        title: 'Control server',
        body: [
          h('div', { class: 'pp-status' }, statusDot(status.last_sync_result === 'error' ? 'alert' : 'ok'),
            h('span', null, 'Paired with ', h('span', { class: 'pp-mono', text: status.server_url || 'a server' }))),
          h('div', { class: 'pp-help' },
            status.last_sync && !status.last_sync.startsWith('0001')
              ? `Checked in ${fmtAgo(status.last_sync)}. Playlists and schedules are managed there; this page keeps network, screen and sound.`
              : 'It has not checked in yet. Playlists and schedules are managed there; this page keeps network, screen and sound.'),
          status.sync_error ? h('div', { class: 'pp-error', text: status.sync_error }) : null,
        ],
        foot: [h('span', { text: 'Unpairing hands control back to this page.' }),
          h('a', { class: 'pp-btn', href: '#/settings', text: 'Unpair on Settings' })],
      }));
      return;
    }

    fill(pairSlot, card({
      title: 'Not connected to a server',
      body: [
        h('div', { class: 'pp-help', style: { 'margin-top': '0' } },
          'Everything about this screen is managed from this page. A control server is for running many screens from one place; it is never needed for one.'),
        status.pairing_code
          ? h('div', { style: { 'margin-top': '12px' } },
            h('span', { class: 'pp-code pp-code--md', text: status.pairing_code }),
            h('div', { class: 'pp-help', text: 'This is the code a server admin types to let this screen in. It is on the screen itself too.' }))
          : h('div', { class: 'pp-help', text: 'The pairing code is shown on the screen itself, never over the network.' }),
      ],
      foot: [h('span', { text: 'Pairing keeps playing while it waits.' }),
        h('a', { class: 'pp-btn pp-btn--primary', href: '#/settings', text: 'Pair with a server' })],
    }));
  }

  /* ---- commands ---- */

  async function command(name, ask) {
    if (ask && !(await confirmDialog(ask))) return;
    try {
      await api('POST', `/api/commands/${name}`);
      toast(name === 'reboot' ? 'The device is rebooting.' : 'Done.');
      loadLog();
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function rescan() {
    // A scan of a full stick takes seconds. A second POST in that time would run
    // the whole scan again.
    rescanBtn.disabled = true;
    try {
      const out = await api('POST', '/api/rescan');
      const count = out.playlists || 0;
      toast(`${count} ${count === 1 ? 'playlist' : 'playlists'} on the stick${out.problems && out.problems.length ? `, ${out.problems.length} need a look` : ''}.`);
      await loadPlaylists();
      nagKey = '';
      if (ctx.store.status) renderNags(ctx.store.status);
      loadLog();
    } catch (err) {
      toast(errorText(err), 'danger');
    } finally {
      rescanBtn.disabled = false;
    }
  }

  /* ---- the calls that are not the status ---- */

  async function loadConfig() {
    try {
      cfg = (await api('GET', '/api/config')).config;
      if (gone) return;
      if (ctx.store.status) { previewKey = ''; apply(ctx.store.status, ctx.store.error); }
    } catch { /* the page works without the image time */ }
  }

  async function loadPlaylists() {
    try {
      snap = await api('GET', '/api/playlists');
      if (gone) return;
      nagKey = '';
      previewKey = '';
      if (ctx.store.status) apply(ctx.store.status, ctx.store.error);
    } catch { /* the preview falls back to the name of the file */ }
  }

  async function loadLog() {
    try {
      entries = (await api('GET', '/api/opslog?n=5')).entries || [];
    } catch {
      return;
    }
    if (gone) return;
    renderLog();
  }

  function renderLog() {
    const zone = (ctx.store.status && ctx.store.status.timezone) || 'UTC';
    // The zone is part of the key: the first log arrives before the first
    // report, so the times have to be drawn again once the zone is known.
    const key = `${zone}|${JSON.stringify(entries)}`;
    if (key === logKey) return;
    logKey = key;
    fill(logBody, entries.length
      ? entries.slice().reverse().map((e) => h('div', { class: 'dv-logline' },
        h('span', { class: 'dv-when', text: deviceTime(e.time, zone).slice(11) }),
        h('span', { class: 'dv-logline__what' },
          h('span', { class: 'dv-event', text: e.event }),
          e.details ? h('span', { text: ` — ${e.details}` }) : null)))
      : h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'The log is empty.' }));
  }

  /* The same rule as library.Snapshot.Find: while the device is paired, only a
     fleet playlist plays, and a local playlist with the same name must not
     take its place. */
  function findPlaylist(name) {
    if (!snap || !snap.playlists) return null;
    return snap.playlists.find((p) => p.name === name && (!snap.paired || p.fleet)) || null;
  }

  function dotFor(status) {
    if (!status.display_connected) return 'quiet';
    if (!status.screen_on) return 'quiet';
    if (status.browser_state === 'running') return 'ok';
    if (status.browser_state === 'stopped') return 'alert';
    return 'busy';
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
      clearInterval(timer);
    },
  };
}
