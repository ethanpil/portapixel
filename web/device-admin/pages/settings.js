/* Settings: everything in portapixel.toml, in one hand-written form.

   The form is written out field by field on purpose (plan section 10). It is
   about twenty fields; a machine that built them from a schema would cost more
   than it saved and would give every field the same flat help text.

   Three things the form has to get right:
     - A secret comes back as the mask. Sending the mask back keeps the value
       that the device has, and sending an empty field clears it.
     - The device says what a save needs: nothing, a browser restart or a
       reboot. The answer of the save is what the page reports.
     - A paired device keeps its hardware settings and gives up the rest (D48).
*/

import {
  h, fill, toast, banner, statusDot, spinner, modal, confirmDialog, typedConfirm,
  fmtAgo, card, pageHead, setText, setShown, errorText, dayChips,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { notInThisBuild, nameToSave } from '../util.js';

const MASK = '********';

const TRANSITIONS = [
  ['crossfade', 'Crossfade'], ['cut', 'Hard cut'],
  ['push-left', 'Push left'], ['push-right', 'Push right'],
  ['push-up', 'Push up'], ['push-down', 'Push down'],
];

export function mount(main, ctx) {
  /* The comment-loss warning is shown one time for each visit to this page (D16).
     A person who saves four times in a row does not need it four times, and a
     person who opens the page again tomorrow does need it again. It was a module
     value before, which made "each visit" mean "each tab". */
  let commentWarningShown = false;

  let cfg = null;
  let baseline = '';
  let playlists = [];
  let paired = false;
  let gone = false;
  const errors = new Map();   // field path -> the element that shows its message

  /* The fields that GET /api/pair names in managed_fields, while this device is
     paired. The list comes from the fleet manifest and can get shorter, so this
     page holds no copy of it (D48): it only maps a field path to a control. */
  let managedFields = new Set();

  const banners = h('div');
  const result = h('div');
  const form = h('div', { class: 'pp-stack' });
  const savebar = h('div');

  fill(main,
    pageHead('Settings', 'Everything here also lives in a plain text file on the stick, so you can set a device up on a laptop before it ever boots.'),
    banners, result, form, savebar);

  const unsubscribe = ctx.store.subscribe((status) => {
    if (!status || gone) return;
    const was = paired;
    paired = !!status.paired;
    if (!cfg) return;
    // The pairing flipped, so managed_fields is stale: read it again before the
    // controls and the notes change.
    if (was !== paired) readPair().then(() => { if (!gone) { applyPaired(); renderPairing(); } });
    renderHints();
  });

  load();

  /* ------------------------------------------------------------------ fields */

  /* One labelled control, with a slot for the message that a 422 answer gives
     for this exact key. */
  function field(path, label, control, help) {
    const error = h('div', { class: 'pp-error', hidden: true });
    const wrap = h('label', { class: 'pp-label pp-field' },
      label, control, error, help ? h('div', { class: 'pp-help' }, help) : null);
    if (path) errors.set(path, { error, wrap });
    return wrap;
  }

  /* The same thing for a control that is not a single input, such as the day
     chips or a segmented pill. */
  function group(path, label, control, help) {
    const error = h('div', { class: 'pp-error', hidden: true });
    const wrap = h('div', { class: 'pp-field' },
      h('div', { class: 'pp-label', text: label }),
      h('div', { style: { 'margin-top': '5px' } }, control),
      error, help ? h('div', { class: 'pp-help' }, help) : null);
    if (path) errors.set(path, { error, wrap });
    return wrap;
  }

  function text(attrs = {}) {
    return h('input', { class: `pp-input${attrs.mono ? ' pp-input--mono' : ''}`, type: 'text', ...strip(attrs), onInput: touch });
  }
  function secret(attrs = {}) {
    return h('input', { class: 'pp-input', type: 'password', autocomplete: 'new-password', ...strip(attrs), onInput: touch });
  }
  function number(attrs = {}) {
    return h('input', { class: 'pp-input pp-input--mono', type: 'number', ...strip(attrs), onInput: touch });
  }
  function clock(attrs = {}) {
    return h('input', { class: 'pp-input pp-input--mono', type: 'time', ...strip(attrs), onInput: touch });
  }
  function select(options, attrs = {}) {
    return h('select', { class: 'pp-select', ...strip(attrs), onChange: touch },
      options.map(([value, label]) => h('option', { value, text: label })));
  }
  function toggle(label, help) {
    const input = h('input', { type: 'checkbox', onChange: touch });
    const wrap = h('div', { class: 'pp-field' },
      h('label', { class: 'pp-switch' }, input, h('span', { text: label })),
      help ? h('div', { class: 'pp-help' }, help) : null);
    wrap.input = input;
    return wrap;
  }
  function strip(attrs) {
    const out = { ...attrs };
    delete out.mono;
    return out;
  }

  /* Two or more options in one pill. It carries a `value` like an input, so the
     form reads every control the same way. */
  function segmented(options, ariaLabel) {
    let value = options[0][0];
    const el = h('div', { class: 'pp-seg', role: 'group', 'aria-label': ariaLabel });
    let disabled = false;
    function draw() {
      fill(el, options.map(([v, label]) => h('button', {
        type: 'button', class: 'pp-seg__btn', text: label,
        'aria-pressed': String(String(v) === String(value)), disabled,
        onClick: () => { value = v; draw(); touch(); },
      })));
    }
    Object.defineProperty(el, 'value', {
      get: () => value,
      set: (v) => { value = v; draw(); },
    });
    Object.defineProperty(el, 'disabled', {
      get: () => disabled,
      set: (v) => { disabled = !!v; draw(); },
    });
    draw();
    return el;
  }

  /* -------------------------------------------------------------- the controls */

  // Device.
  // 63 is manifest.MaxNameLength, the rule of the daemon and of the fleet server.
  const cName = text({ maxlength: '63', autocomplete: 'off' });
  const cID = h('input', { class: 'pp-input pp-input--mono', type: 'text', readonly: true });
  const cTimezone = timezoneControl();
  const cTier = select([['auto', 'Automatic'], ['low', 'Treat as low-power'], ['high', 'Treat as full-power']]);
  const nameHelp = h('div', { class: 'pp-help' });
  const tierHelp = h('div', { class: 'pp-help' });

  // Network.
  const cMode = segmented([['dhcp', 'Get an address automatically'], ['static', 'Fixed address']], 'How it gets an address');
  const cAddress = text({ mono: true, placeholder: '192.168.1.50/24' });
  const cGateway = text({ mono: true, placeholder: '192.168.1.1' });
  const cDNS = text({ mono: true, placeholder: '192.168.1.1, 1.1.1.1' });
  const cSSID = text({ maxlength: '32', autocomplete: 'off' });
  const cPSK = secret({ placeholder: 'the WiFi key' });
  /* A free text field and not a list of countries. The value is a two-letter
     regulatory domain, the device checks it, and a list of 250 countries in a
     form of twenty fields is a list that nobody reads. */
  const cCountry = text({ maxlength: '2', autocomplete: 'off', placeholder: 'US', style: { 'max-width': '90px', 'text-transform': 'uppercase' } });
  const staticBlock = h('div');
  const wifiHelp = h('div', { class: 'pp-help' });

  // Display.
  const cRotation = segmented([['0', 'Normal'], ['90', '90°'], ['180', '180°'], ['270', '270°']], 'Rotation');
  const cVideoMode = text({ mono: true, placeholder: 'trust the display' });
  const cPowerMethod = select([
    ['auto', 'Automatic — HDMI control when the display answers'],
    ['cec', 'HDMI control (CEC) only'],
    ['dpms', 'Monitor power saving only'],
    ['none', 'Never turn it off'],
  ]);
  const cOnTime = clock();
  const cOffTime = clock();
  const cPowerDays = dayChips({ onChange: touch });
  const powerHelp = h('div', { class: 'pp-help' });

  // Audio.
  const cOutput = select([
    ['auto', 'Automatic — HDMI first'], ['hdmi', 'HDMI'],
    ['analog', 'Headphone jack'], ['usb', 'USB speaker'],
  ]);
  const cVolume = h('input', {
    class: 'pp-range', type: 'range', min: '0', max: '100', step: '1',
    'aria-label': 'Volume', onInput: () => { volumeLabel(); touch(); },
  });
  const volumeText = h('span');

  // Playback.
  const cDefault = select([]);
  const cTransition = select(TRANSITIONS);
  const cTransitionMS = number({ min: '0', max: '5000', step: '50' });
  const cImageDuration = number({ min: '1', max: '3600', step: '1' });
  const cShuffle = toggle('Shuffle the items', 'A new order each time the playlist starts.');
  const cNightly = clock();

  // Watchdog. The device watches the player and the browser, and it restarts or
  // reboots when they stop (D30).
  const cWatchdog = toggle('Watch the screen and recover on its own',
    'Off means that a frozen page stays on the screen until somebody looks at it. Leave it on.');
  const cHeartbeat = number({ min: '10', max: '600', step: '5' });
  const cRestarts = number({ min: '0', max: '20', step: '1' });
  const cWindow = number({ min: '1', max: '1440', step: '5' });

  // Server.
  const cPoll = number({ min: '10', max: '3600', step: '5' });
  const pairSlot = h('div');

  // Web.
  const cPort = number({ min: '1', max: '65535', step: '1' });
  const cPassword = secret({ placeholder: 'at least 8 characters', autocomplete: 'new-password' });
  const cPassword2 = secret({ placeholder: 'the same again', autocomplete: 'new-password' });
  const passwordError = h('div', { class: 'pp-error', hidden: true });

  // The rest.
  const cSSH = toggle('Remote terminal access is on', 'Useful when something needs a look. Its password is separate from this page.');
  const cUpdates = toggle('Install updates on its own', 'Off is the safe default: you decide when a screen changes. An update that will not come up rolls itself back.');
  const cLogging = toggle('Keep system logs on the stick', 'Normally the logs stay in memory to spare the flash. Turn this on only while chasing a fault that survives a reboot.');

  // The notes next to the fields that the fleet server owns while the device is
  // paired (D48). Their text names the server, so it is set at render time.
  const managedNote = () => h('div', {
    class: 'pp-note', hidden: true,
    style: { 'border-radius': '8px', 'margin-bottom': '14px' },
  });
  const playbackNote = managedNote();
  const screenTimesNote = managedNote();

  const dirtyNote = h('span', { class: 'pp-pe__dirty' });
  const saveBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Save settings', disabled: true, onClick: save });
  const discardBtn = h('button', { type: 'button', class: 'pp-btn', text: 'Discard', disabled: true, onClick: discard });

  function timezoneControl() {
    let zones = null;
    try { zones = Intl.supportedValuesOf('timeZone'); } catch { zones = null; }
    if (!zones || !zones.length) {
      // An engine without the list still gets a usable field.
      return h('input', {
        class: 'pp-input pp-input--mono', type: 'text', placeholder: 'Europe/London',
        autocomplete: 'off', spellcheck: 'false', onInput: touch,
      });
    }
    return h('select', { class: 'pp-select pp-select--mono', onChange: touch },
      [h('option', { value: 'UTC', text: 'UTC — not set' }),
        zones.filter((z) => z !== 'UTC').map((z) => h('option', { value: z, text: z }))]);
  }

  function volumeLabel() {
    setText(volumeText, `${cVolume.value}%`);
  }

  /* ------------------------------------------------------------------ loading */

  async function load() {
    try {
      const [view, snap] = await Promise.all([api('GET', '/api/config'), api('GET', '/api/playlists')]);
      if (gone) return;
      cfg = view.config;
      playlists = snap.playlists || [];
      fill(banners,
        view.from_shadow ? banner({
          kind: 'danger',
          title: 'These settings come from the backup copy',
          body: 'portapixel.toml on the stick is missing or cannot be read. Save once and a good file goes back onto the stick.',
        }) : null,
        view.warning ? banner({ kind: 'warn', title: 'The settings file has a fault', body: view.warning }) : null);
      build();
      fillForm();
      baseline = JSON.stringify(readForm());
      applyPaired();
      renderPairing();
      renderHints();
      touch();
    } catch (err) {
      fill(form, banner({ kind: 'danger', title: 'The settings did not load', body: errorText(err) }));
    }
  }

  function build() {
    fill(form,
      card({
        title: 'This device',
        body: [
          field('device.name', 'Name', cName, nameHelp),
          field(null, 'ID', cID, 'It comes from the hardware and cannot be edited.'),
          field('device.timezone', 'Time zone', cTimezone, 'Schedules use this. The clock itself syncs over the network.'),
          field('device.tier', 'Performance', cTier, tierHelp),
        ],
      }),
      card({
        title: 'Network',
        meta: h('span', { class: 'pp-badge pp-badge--mono', text: 'needs a reboot' }),
        body: [
          group('network.mode', 'How it gets an address', cMode),
          staticBlock,
          field('network.wifi_ssid', 'WiFi network name', cSSID, 'Leave it empty when the device is on ethernet.'),
          field('network.wifi_psk', 'WiFi password', cPSK, wifiHelp),
          field('network.wifi_country', 'WiFi country', cCountry,
            'Two letters, for example US or DE. Leave it empty and some radios show fewer channels, so a 5 GHz network can be invisible.'),
        ],
      }),
      card({
        title: 'Screen',
        body: [
          group('display.rotation', 'Rotation', cRotation, 'The browser restarts to turn the picture.'),
          field('display.video_mode', 'Force the output mode', cVideoMode,
            'Leave it empty and the display is trusted. Fill it in only when a display or an HDMI splitter reports the wrong modes, for example 1920x1080@60.'),
          field('display.power_method', 'Turning the screen on and off', cPowerMethod, powerHelp),
          screenTimesNote,
          h('div', { class: 'pp-fields' },
            field('display.off_time', 'Off at', cOffTime),
            field('display.on_time', 'Back on at', cOnTime)),
          group('display.power_days', 'On these days', cPowerDays,
            'Set both times to turn the screen off overnight. It saves the panel and the power, and the nightly restart is skipped while the screen is off.'),
        ],
      }),
      card({
        title: 'Sound',
        body: [
          field('audio.output', 'Output', cOutput, 'Videos play with sound unless a playlist item is muted.'),
          group('audio.volume', 'Volume', h('div', null,
            h('div', { class: 'pp-small pp-mono pp-muted', style: { 'margin-bottom': '4px' } }, volumeText), cVolume)),
        ],
      }),
      card({
        title: 'Playback',
        body: [
          playbackNote,
          field('playback.default_playlist', 'Plays when nothing is scheduled', cDefault),
          h('div', { class: 'pp-fields' },
            field('playback.transition', 'Between items', cTransition),
            field('playback.transition_ms', 'Fade length (ms)', cTransitionMS)),
          h('div', { class: 'pp-fields' },
            field('playback.image_duration', 'Default image time (seconds)', cImageDuration),
            field('playback.nightly_restart', 'Nightly restart', cNightly)),
          cShuffle,
          h('div', { class: 'pp-help', text: 'The nightly restart clears anything a long-running browser has collected. Leave it on unless it lands in an hour that the screen is needed.' }),
        ],
      }),
      card({
        title: 'Watchdog',
        body: [
          cWatchdog,
          field('watchdog.heartbeat_timeout', 'Restart the browser after this much silence (seconds)', cHeartbeat,
            'The page reports in every 5 seconds. 30 seconds is six missed reports. 10 to 600.'),
          h('div', { class: 'pp-fields' },
            field('watchdog.restarts_before_reboot', 'Reboot after this many restarts', cRestarts),
            field('watchdog.restart_window', 'Counted inside (minutes)', cWindow)),
          h('div', { class: 'pp-help', text: 'When a browser restart does not help, the device reboots itself. Set the number of restarts to 0 and it never reboots on its own.' }),
        ],
      }),
      card({
        title: 'Control server',
        body: [pairSlot, field('server.poll_seconds', 'Check in every (seconds) — used when the server names none', cPoll,
          'How often the device asks the server for work. The server usually names the interval, and its number wins; this one is the fallback. Ten seconds is the shortest it takes.')],
      }),
      card({
        title: 'Who can get in',
        body: [
          h('div', { class: 'pp-fields' },
            field('web.password', 'New password for this page', cPassword),
            field(null, 'The same again', cPassword2)),
          passwordError,
          h('div', { class: 'pp-help', text: 'Leave both empty to keep the password you have.' }),
          field('web.port', 'Port for this page', cPort, 'Changing it needs a reboot, and the address you type changes with it.'),
          h('div', { style: { 'border-top': '1px solid var(--pp-border-faint)', 'margin-top': '14px', 'padding-top': '14px' } }, cSSH),
        ],
      }),
      card({ title: 'Updates', body: [cUpdates] }),
      card({ title: 'Logging', body: [cLogging] }));

    fill(staticBlock,
      h('div', { class: 'pp-fields' },
        field('network.address', 'Address', cAddress),
        field('network.gateway', 'Router', cGateway)),
      field('network.dns', 'DNS servers', cDNS, 'One or more addresses, separated by commas.'));

    fill(savebar, h('div', { class: 'pp-savebar' },
      h('div', null,
        h('div', { text: 'Saving rewrites the settings file on the stick. Notes you typed into it yourself are dropped.' }),
        dirtyNote),
      h('div', { class: 'pp-btns' }, discardBtn, saveBtn)));
  }

  /* ------------------------------------------------------- the form and the file */

  function fillForm() {
    const status = ctx.store.status || {};

    cName.value = cfg.device.name || '';
    cID.value = status.device_id || cfg.device.id || '';
    setValue(cTimezone, cfg.device.timezone || 'UTC');
    cTier.value = cfg.device.tier || 'auto';

    cMode.value = cfg.network.mode || 'dhcp';
    cAddress.value = cfg.network.address || '';
    cGateway.value = cfg.network.gateway || '';
    cDNS.value = (cfg.network.dns || []).join(', ');
    cSSID.value = cfg.network.wifi_ssid || '';
    cPSK.value = cfg.network.wifi_psk || '';
    cCountry.value = cfg.network.wifi_country || '';

    cRotation.value = String(cfg.display.rotation || 0);
    cVideoMode.value = cfg.display.video_mode || '';
    cPowerMethod.value = cfg.display.power_method || 'auto';
    cOnTime.value = cfg.display.on_time || '';
    cOffTime.value = cfg.display.off_time || '';
    cPowerDays.set(cfg.display.power_days || []);

    cOutput.value = cfg.audio.output || 'auto';
    cVolume.value = String(cfg.audio.volume ?? 100);
    volumeLabel();

    fill(cDefault, playlists.map((p) => h('option', { value: p.name, text: p.title })));
    if (cfg.playback.default_playlist && !playlists.some((p) => p.name === cfg.playback.default_playlist)) {
      cDefault.append(h('option', { value: cfg.playback.default_playlist, text: `${cfg.playback.default_playlist} (missing)` }));
    }
    cDefault.value = cfg.playback.default_playlist || '';
    cTransition.value = cfg.playback.transition || 'crossfade';
    cTransitionMS.value = String(cfg.playback.transition_ms ?? 500);
    cImageDuration.value = String(cfg.playback.image_duration ?? 10);
    cShuffle.input.checked = !!cfg.playback.shuffle;
    cNightly.value = cfg.playback.nightly_restart || '';

    const watchdog = cfg.watchdog || {};
    cWatchdog.input.checked = watchdog.enabled !== false;
    cHeartbeat.value = String(watchdog.heartbeat_timeout ?? 30);
    cRestarts.value = String(watchdog.restarts_before_reboot ?? 4);
    cWindow.value = String(watchdog.restart_window ?? 60);

    cPoll.value = String(cfg.server.poll_seconds ?? 60);

    cPort.value = String(cfg.web.port ?? 80);
    cPassword.value = '';
    cPassword2.value = '';

    cSSH.input.checked = !!cfg.ssh.enabled;
    cUpdates.input.checked = !!cfg.updates.auto;
    cLogging.input.checked = !!cfg.logging.persist;

    showStatic();
  }

  function setValue(control, value) {
    if (control.tagName === 'SELECT' && !Array.from(control.options).some((o) => o.value === value)) {
      control.append(h('option', { value, text: value }));
    }
    control.value = value;
  }

  /* Build the configuration that goes to the device. It starts from the copy
     that came back, so every key that this form does not show, above all the
     schedule rules, goes back exactly as it was. */
  function readForm() {
    const next = JSON.parse(JSON.stringify(cfg));

    next.device.name = cName.value.trim();
    next.device.timezone = cTimezone.value.trim();
    next.device.tier = cTier.value;

    next.network.mode = cMode.value;
    next.network.address = cAddress.value.trim();
    next.network.gateway = cGateway.value.trim();
    next.network.dns = cDNS.value.split(',').map((s) => s.trim()).filter(Boolean);
    next.network.wifi_ssid = cSSID.value.trim();
    next.network.wifi_psk = cPSK.value;
    next.network.wifi_country = cCountry.value.trim().toUpperCase();

    next.display.rotation = Number(cRotation.value);
    next.display.video_mode = cVideoMode.value.trim();
    next.display.power_method = cPowerMethod.value;
    next.display.on_time = cOnTime.value;
    next.display.off_time = cOffTime.value;
    next.display.power_days = cPowerDays.read();

    next.audio.output = cOutput.value;
    next.audio.volume = Number(cVolume.value);

    next.playback.default_playlist = cDefault.value;
    next.playback.transition = cTransition.value;
    next.playback.transition_ms = Number(cTransitionMS.value) || 0;
    next.playback.image_duration = Number(cImageDuration.value) || 0;
    next.playback.shuffle = cShuffle.input.checked;
    next.playback.nightly_restart = cNightly.value;

    /* A daemon that is older than this page sends no [watchdog] table, and the
       copy above then has no object to write into. */
    next.watchdog = next.watchdog || {};
    next.watchdog.enabled = cWatchdog.input.checked;
    next.watchdog.heartbeat_timeout = Number(cHeartbeat.value) || 0;
    next.watchdog.restarts_before_reboot = Number(cRestarts.value) || 0;
    next.watchdog.restart_window = Number(cWindow.value) || 0;

    next.server.poll_seconds = Number(cPoll.value) || 0;

    next.web.port = Number(cPort.value) || 0;
    // An empty pair of password fields means "keep the one you have", which is
    // exactly what the mask does.
    next.web.password = cPassword.value ? cPassword.value : (cfg.web.password || MASK);

    next.ssh.enabled = cSSH.input.checked;
    next.updates.auto = cUpdates.input.checked;
    next.logging.persist = cLogging.input.checked;

    return next;
  }

  function showStatic() {
    setShown(staticBlock, cMode.value === 'static');
  }

  function touch() {
    showStatic();
    if (!cfg) return;
    const dirty = JSON.stringify(readForm()) !== baseline;
    ctx.setGuard(() => dirty);
    setText(dirtyNote, dirty ? 'Changes are not saved yet.' : '');
    saveBtn.disabled = !dirty;
    discardBtn.disabled = !dirty;
  }

  /* The lines of help that only the live report can write. */
  function renderHints() {
    const status = ctx.store.status || {};
    fill(nameHelp, 'Also its address on the network: ',
      h('span', { class: 'pp-mono', text: status.mdns_name || '—' }));
    setText(tierHelp, status.tier
      ? `Detected as ${status.tier === 'low' ? 'low-power' : 'full-power'}${status.arch ? ` (${status.arch})` : ''}.`
      : 'The device decides on its own unless you pick one.');
    fill(wifiHelp, cfg && cfg.network.wifi_psk
      ? ['Leave the dots to keep the key you have; clear the field to remove it.',
        (status.ips || []).length ? h('div', null, 'Currently at ', h('span', { class: 'pp-mono', text: status.ips[0] })) : null]
      : 'Only needed on WiFi.');
    setText(powerHelp, status.display_connected
      ? 'A display is connected. Automatic uses HDMI control when the display answers and monitor power saving when it does not.'
      : 'No display answers at the moment, so the device cannot say which method will work.');
  }

  /* The field path of GET /api/pair's managed_fields -> the control it owns on
     this page. Everything not named here stays enabled while paired: the other
     playback fields, shuffle, the nightly restart and auto update are the local
     defaults of this screen, not the fleet's (D48). "schedule" has no control
     here: the whole rules editor is the Schedule page. */
  const MANAGED_CONTROLS = {
    'playback.default_playlist': [cDefault],
    'display.on_time': [cOnTime],
    'display.off_time': [cOffTime],
    'display.power_days': [cPowerDays],
  };

  function setControlDisabled(control, on) {
    if (typeof control.setDisabled === 'function') control.setDisabled(on);
    else control.disabled = on;
  }

  /* Disables exactly the controls that managed_fields names, and shows the
     note that says which server owns them next to each disabled group. */
  function applyPaired() {
    for (const [path, controls] of Object.entries(MANAGED_CONTROLS)) {
      const on = managedFields.has(path);
      for (const control of controls) setControlDisabled(control, on);
    }
    const server = (pairState && pairState.server_name) || 'The control server';
    const text = `${server} manages this while the device is paired. Unpair to take it back.`;
    setText(playbackNote, text);
    setShown(playbackNote, managedFields.has('playback.default_playlist'));
    setText(screenTimesNote, text);
    setShown(screenTimesNote, ['display.on_time', 'display.off_time', 'display.power_days']
      .some((path) => managedFields.has(path)));
  }

  /* --------------------------------------------------------------- the errors */

  function clearErrors() {
    for (const { error, wrap } of errors.values()) {
      error.hidden = true;
      error.textContent = '';
      wrap.classList.remove('pp-field--invalid');
    }
    passwordError.hidden = true;
  }

  function showErrors(fields) {
    clearErrors();
    const rest = [];
    for (const f of fields) {
      // network.dns[0] and the like belong to the one field that holds the list.
      const path = f.field.replace(/\[\d+\]$/, '');
      const slot = errors.get(path);
      if (!slot) { rest.push(`${f.field}: ${f.message}`); continue; }
      slot.error.textContent = f.message;
      slot.error.hidden = false;
      slot.wrap.classList.add('pp-field--invalid');
    }
    const first = form.querySelector('.pp-field--invalid');
    if (first) first.scrollIntoView({ block: 'center' });
    if (rest.length) toast(rest.join('; '), 'danger');
  }

  /* ----------------------------------------------------------------- the save */

  async function save() {
    clearErrors();

    if (cPassword.value || cPassword2.value) {
      if (cPassword.value !== cPassword2.value) {
        passwordError.textContent = 'The two passwords are not the same.';
        passwordError.hidden = false;
        cPassword2.focus();
        return;
      }
      if (cPassword.value.length < 8) {
        passwordError.textContent = 'That needs eight characters or more.';
        passwordError.hidden = false;
        cPassword.focus();
        return;
      }
    }

    if (!commentWarningShown) {
      const go = await modal({
        title: 'Save these settings?',
        body: h('div', null,
          'Saving rewrites ', h('span', { class: 'pp-mono', text: 'portapixel.toml' }),
          ' on the stick. Your values are kept exactly as shown, and the comments that PortaPixel writes come back. Comments you typed into that file by hand will be gone.'),
        actions: [
          { label: 'Cancel', value: false },
          { label: 'Save and rewrite', value: true, kind: 'primary', autofocus: true },
        ],
      });
      if (!go) return;
      commentWarningShown = true;
    }

    const next = readForm();
    saveBtn.disabled = true;
    try {
      try {
        const running = (await api('GET', '/api/config')).config.device.name;
        next.device.name = nameToSave(next.device.name, cfg.device.name, running);
      } catch { /* the save goes on with the name of the form */ }
      if (gone) return;
      const applied = await api('PUT', '/api/config', next);
      // The device holds the secrets; the copy we keep holds the mask again.
      cfg = next;
      cfg.web.password = MASK;
      if (cfg.network.wifi_psk) cfg.network.wifi_psk = MASK;
      if (cfg.server.token) cfg.server.token = MASK;
      fillForm();
      applyPaired();
      baseline = JSON.stringify(readForm());
      ctx.clearGuard();
      showApplied(applied);
      ctx.store.refresh();
    } catch (err) {
      /* PUT /api/config refuses the whole body with 403 when it changes a field
         that the fleet server owns, and it names every one of those fields in
         `fields`, in the shape of a 422. Reuse the same field-error display, and
         say the one sentence of the refusal in a toast (D48). Signing out stays
         a matter for 401 only. */
      if (err.fields && err.fields.length) showErrors(err.fields);
      if (err.status === 403) toast(err.message, 'danger');
      else if (err.fields && err.fields.length) toast('Some settings are not correct.', 'danger');
      else toast(errorText(err), 'danger');
    } finally {
      touch();
    }
  }

  /* The device says what its own change needs. Saying it in words is the whole
     point: a setting that looks applied and is not is the worst answer. */
  function showApplied(applied) {
    const changes = applied.changes || [];
    const names = changes.map((c) => c.field);
    const list = names.length ? h('div', { class: 'pp-small pp-mono', style: { 'margin-top': '6px' }, text: names.join(', ') }) : null;

    if (!changes.length) {
      toast('Nothing was different, so nothing changed.');
      fill(result);
      return;
    }
    if (applied.applied === 'reboot') {
      fill(result, banner({
        kind: 'warn',
        title: 'Saved. One change needs a reboot',
        body: h('div', null, 'Everything else is live already. The screen keeps playing until you reboot.', list),
        actions: [h('button', {
          type: 'button', class: 'pp-btn pp-btn--primary', text: 'Reboot now',
          onClick: async () => {
            try {
              await api('POST', '/api/commands/reboot');
              toast('The device is rebooting.');
            } catch (err) { toast(errorText(err), 'danger'); }
          },
        })],
      }));
      return;
    }
    if (applied.applied === 'browser') {
      fill(result, banner({
        kind: 'info',
        title: 'Saved. The browser restarts',
        body: h('div', null, 'The screen goes black for a few seconds and comes back with the new setting.', list),
      }));
      toast('Settings saved to the stick.');
      return;
    }
    fill(result, banner({
      kind: 'paired',
      title: 'Saved and live',
      body: h('div', null, 'Nothing has to restart.', list),
    }));
    toast('Settings saved to the stick.');
  }

  function discard() {
    fillForm();
    applyPaired();
    clearErrors();
    fill(result);
    touch();
  }

  /* --------------------------------------------------------------- pairing */

  let pairState = null;    // the PairState of GET /api/pair
  let pairAvailable = true;
  let pairTimer = 0;

  async function readPair() {
    try {
      pairState = await api('GET', '/api/pair');
      pairAvailable = true;
    } catch (err) {
      if (!notInThisBuild(err)) throw err;
      pairAvailable = false;
      pairState = null;
    }
    // managed_fields is present only while paired (D48). Anything else is an
    // empty set, so every control on this page stays enabled.
    managedFields = new Set((pairState && pairState.managed_fields) || []);
  }

  /* The pending state is the only one that polls. The timer must stop on EVERY
     way out of it, not only on the way to "paired": a Cancel goes to "unpaired",
     and a timer that kept running would rebuild the address and token fields
     under the hands of the person who is typing in them. */
  function stopPairPoll() {
    clearInterval(pairTimer);
    pairTimer = 0;
  }

  readPair().catch(() => { pairAvailable = false; managedFields = new Set(); }).then(() => {
    if (gone) return;
    applyPaired();
    if (cfg) renderPairing();
  });

  function renderPairing() {
    const status = ctx.store.status || {};
    const state = pairState && pairState.status ? pairState.status : (paired ? 'paired' : 'unpaired');
    if (state !== 'pending') stopPairPoll();

    if (state === 'paired' || paired) {
      fill(pairSlot,
        h('div', { class: 'pp-status' }, statusDot(status.last_sync_result === 'error' ? 'alert' : 'ok'),
          h('span', null, 'Paired with ', h('span', { class: 'pp-mono', text: (pairState && pairState.server_url) || status.server_url || 'a server' }))),
        h('div', { class: 'pp-help' },
          status.last_sync && !String(status.last_sync).startsWith('0001')
            ? `Checked in ${fmtAgo(status.last_sync)}, every ${cfg.server.poll_seconds} seconds. Playlists and schedules are managed there; this page keeps network, screen and sound.`
            : 'Playlists and schedules are managed there; this page keeps network, screen and sound.'),
        status.sync_error ? h('div', { class: 'pp-error', text: status.sync_error }) : null,
        /* The address is http:// on a network that is not local, so the token of
           this screen goes over the internet in clear text (the daemon raises the
           same warning in status.warnings). */
        pairState && pairState.insecure
          ? h('div', { class: 'pp-banner pp-banner--warn', style: { 'margin-top': '12px', 'margin-bottom': '0' } },
            h('div', { class: 'pp-banner__text' },
              h('div', { class: 'pp-banner__title', text: 'This address is not encrypted' }),
              h('div', { class: 'pp-banner__body', text: 'The address starts with http:// and it is not on your network, so the token of this screen travels in clear text. Use https://.' })))
          : null,
        h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
          h('button', { type: 'button', class: 'pp-btn pp-btn--danger-outline', text: 'Unpair', onClick: unpair })),
        pairAvailable ? null : notAvailableNote());
      return;
    }

    if (state === 'pending') {
      fill(pairSlot,
        h('div', null, h('span', { class: 'pp-code pp-code--lg', text: pairState.pairing_code || '—' })),
        h('div', { class: 'pp-status', style: { 'margin-top': '12px' } }, spinner('Waiting for approval'),
          h('span', { text: 'Waiting for someone to approve this code' })),
        h('div', { class: 'pp-help' },
          'Whoever runs ', h('span', { class: 'pp-mono', text: pairState.server_url || cfg.server.url || 'the server' }),
          ' sees this device waiting under pending devices. The code is on the screen too. It keeps playing meanwhile.'),
        h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
          h('button', { type: 'button', class: 'pp-btn', text: 'Cancel', onClick: unpair })));
      if (!pairTimer) pairTimer = setInterval(() => readPair().then(() => renderPairing()).catch(() => {}), 5000);
      return;
    }

    /* A token that is already in portapixel.toml comes back from GET /api/config
       as the mask. The mask is not a token: it must never leave this page for any
       route but PUT /api/config, and the daemon would write the eight stars into
       the file as the real value. So the field stays EMPTY and the help text says
       that a token is saved. */
    const saved = cfg.server.token === MASK;
    const url = h('input', {
      class: 'pp-input pp-input--mono', type: 'url', value: cfg.server.url || '',
      placeholder: 'https://signage.example.com', disabled: !pairAvailable,
    });
    const token = h('input', {
      class: 'pp-input pp-input--mono', type: 'text', value: '',
      placeholder: saved ? 'a token is saved on this device' : 'leave empty to pair by code',
      autocomplete: 'off', disabled: !pairAvailable,
    });
    const connect = h('button', {
      type: 'button', class: 'pp-btn pp-btn--primary', text: 'Connect', disabled: !pairAvailable,
      onClick: () => connectTo(url.value.trim(), token.value.trim(), connect, saved),
    });

    fill(pairSlot,
      h('div', { class: 'pp-help', style: { 'margin-top': '0' } },
        'This screen runs on its own and needs no server. A server is for running many screens from one place: it then owns the playlists and the times, and this page keeps the hardware settings.'),
      h('div', { class: 'pp-fields', style: { 'margin-top': '12px' } },
        h('label', { class: 'pp-label pp-field' }, 'Server address', url),
        h('label', { class: 'pp-label pp-field' }, 'Invite token', token,
          h('div', { class: 'pp-help', text: saved
            ? 'A token is saved on this device already, and the device uses it on its own. Type one here only to put another token in its place.'
            : 'With a token the pairing is instant. Without one the device shows a six-character code for the server admin to approve.' }))),
      h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } }, connect),
      pairAvailable ? null : notAvailableNote());
  }

  function notAvailableNote() {
    return h('div', { class: 'pp-banner pp-banner--info', style: { 'margin-top': '14px', 'margin-bottom': '0' } },
      h('div', { class: 'pp-banner__text' },
        h('div', { class: 'pp-banner__title', text: 'Pairing is not in this build yet' }),
        h('div', { class: 'pp-banner__body', text: 'The device answers this route with "not implemented yet". Standalone is the whole product; pairing arrives in a later version.' })));
  }

  async function connectTo(url, token, button, saved) {
    if (!url) { toast('That needs a server address.', 'danger'); return; }
    // The mask is never a token. A person who pasted the eight stars back means
    // "the one that is saved", which is an empty field here.
    if (token === MASK) token = '';
    /* The daemon writes the address and the token into portapixel.toml before it
       enrolls, and an empty token writes an empty token. So a Connect with no
       token replaces the saved one. Say so before it happens. */
    if (!token && saved && !(await confirmDialog({
      title: 'Pair by code and drop the saved token?',
      body: 'This device holds an invite token already. Connecting with the token field empty takes that token out of the settings file and asks the server for a six-character code instead.',
      confirm: 'Pair by code',
      cancel: 'Go back',
      kind: 'danger',
    }))) return;

    button.disabled = true;
    try {
      const out = await api('POST', '/api/pair', token ? { url, token } : { url });
      /* The daemon answers with the whole state, and its address is the one that
         it cleaned up: no slash at the end, no fragment. */
      pairState = out && out.status ? out : { status: 'unpaired', server_url: url };
      managedFields = new Set((pairState && pairState.managed_fields) || []);
      pairAvailable = true;
      applyPaired();
      toast(out.status === 'paired' ? 'Paired.' : 'Waiting for the server to let this device in.');
      await reloadConfig();
      renderPairing();
      ctx.store.refresh();
    } catch (err) {
      if (notInThisBuild(err)) { pairAvailable = false; renderPairing(); }
      else if (err.status === 409) {
        // Another tab, or the fallback screen, paired this device first. Read
        // the state it left behind instead of guessing at it.
        toast('This device is already paired. Unpair it first.', 'danger');
        await readPair();
        applyPaired();
        renderPairing();
      } else toast(errorText(err), 'danger');
    } finally {
      button.disabled = false;
    }
  }

  /* Read the configuration again after a pairing change. The daemon wrote the
     address and the token into portapixel.toml itself, and the copy that this
     page holds is what the next Save sends. Without this the next Save would put
     the old, empty [server] block back and the device could never enroll again. */
  async function reloadConfig() {
    let view;
    try {
      view = await api('GET', '/api/config');
    } catch {
      return;   // the pairing worked; the form keeps what it has
    }
    if (gone || !view.config) return;
    const dirty = JSON.stringify(readForm()) !== baseline;
    if (dirty) {
      // The user is in the middle of an edit. Take only the block that the pairing
      // changed, so nothing that was typed is lost.
      cfg.server = view.config.server;
      return;
    }
    cfg = view.config;
    fillForm();
    applyPaired();
    baseline = JSON.stringify(readForm());
    touch();
  }

  async function unpair() {
    const name = (ctx.store.status && ctx.store.status.name) || cfg.device.name || 'this screen';
    const ok = await typedConfirm({
      title: 'Unpair this screen?',
      body: 'The server stops managing it. The playlists and the schedule of this page take over again, and the fleet playlists stop playing. The screen keeps showing something the whole time.',
      expect: name,
      label: 'Type the name of this screen, ',
      confirm: 'Unpair it',
    });
    if (!ok) return;
    try {
      await api('DELETE', '/api/pair');
      pairState = { status: 'unpaired' };
      managedFields = new Set();
      applyPaired();
      /* The daemon takes the address and the token out of portapixel.toml. Read
         the file back instead of guessing what it now holds. */
      await reloadConfig();
      toast('Unpaired. This page is in charge again.');
      renderPairing();
      ctx.store.refresh();
    } catch (err) {
      if (notInThisBuild(err)) { pairAvailable = false; renderPairing(); }
      else toast(errorText(err), 'danger');
    }
  }

  return {
    destroy() {
      gone = true;
      clearInterval(pairTimer);
      unsubscribe();
    },
  };
}
