/* One screen: what it reports, what it should play, and what to send it.

   Everything on this page is the last report of that screen, not a live feed.
   The screen calls this server; this server never calls the screen. The words on
   the page keep saying so, because it is the thing that surprises people.
*/

import {
  h, fill, toast, banner, statusDot, modal, confirmDialog, typedConfirm,
  factList, fmtAgo, fmtBytes, fmtDuration, progress,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { mountRules } from '../rules.js';
import { sendToScreen } from '../commands.js';
import {
  card, errorText, setText, stateInfo, parseStatus, fmtTemp, preview,
  thumbURL, guessKind, dayChips, daysFromCSV, daysInWords, COMMANDS, commandState,
  fmtClock, fmtDate, isNever, ownPageURL,
} from '../util.js';

const POLL_MS = 10000;

export function mount(main, ctx, deviceID) {
  let view = null;              // the answer of GET /api/admin/devices/{id}
  let groups = [];
  let playlists = [];
  let mediaByName = new Map();
  let rules = null;             // the rule editor of this screen
  let overridesDirty = false;
  let gone = false;
  let timer = 0;

  const head = h('div');
  const notices = h('div');
  const body = h('div');

  fill(main,
    h('a', { class: 'pp-back', href: '#/screens', text: '← All screens' }),
    head, notices, body);

  /* ------------------------------------------------------------------ load */

  async function load(first) {
    try {
      const [got, gotGroups, gotPlaylists] = await Promise.all([
        api('GET', `/api/admin/devices/${encodeURIComponent(deviceID)}`),
        first ? api('GET', '/api/admin/groups') : Promise.resolve(null),
        first ? api('GET', '/api/admin/playlists') : Promise.resolve(null),
      ]);
      if (gone) return;
      view = got;
      if (gotGroups) groups = gotGroups.groups || [];
      if (gotPlaylists) playlists = gotPlaylists.playlists || [];
      if (first) { loadMedia(); draw(); } else { paintLive(); }
    } catch (err) {
      if (gone) return;
      clearInterval(timer);
      fill(body, banner({
        kind: 'danger',
        title: err.status === 404 ? 'There is no screen with this ID' : 'The screen did not load',
        body: errorText(err),
        actions: [h('a', { class: 'pp-btn', href: '#/screens', text: 'Back to the list' })],
      }));
    }
  }

  async function loadMedia() {
    try {
      const out = await api('GET', '/api/admin/media');
      if (gone) return;
      mediaByName = new Map((out.media || []).map((m) => [m.orig_name, m]));
      paintNow();
    } catch { /* the preview then shows the icon of the kind */ }
  }

  function dev() { return (view && view.device) || {}; }
  function status() { return parseStatus(dev()); }
  function titleOf(id) {
    const p = playlists.find((x) => x.id === Number(id));
    return p ? p.title : '';
  }

  /* ------------------------------------------------------------------ head */

  const nameEl = h('h1', { class: 'pp-h1' });
  const metaEl = h('div', { class: 'pp-lead', style: { display: 'flex', gap: '8px', 'flex-wrap': 'wrap', 'align-items': 'center' } });
  const ownPage = h('a', { class: 'pp-btn', target: '_blank', rel: 'noopener', text: 'Open its own page', hidden: true });

  function drawHead() {
    fill(head, h('div', { class: 'pp-page-head' },
      h('div', { style: { 'min-width': 'min(260px, 100%)' } }, nameEl, metaEl),
      h('div', { class: 'pp-btns' },
        ownPage,
        h('button', { type: 'button', class: 'pp-btn', text: 'Rename', onClick: rename }))));
  }

  function paintHead() {
    const d = dev();
    const info = stateInfo(d.state);
    setText(nameEl, d.name || d.id);
    document.title = `${d.name || d.id} — PortaPixel Control`;
    fill(metaEl,
      h('span', { class: 'pp-mono', text: d.id }),
      h('span', { text: '·' }),
      h('span', { text: d.group_name ? `${d.group_name} group` : 'no group' }),
      h('span', { text: '·' }),
      h('span', { class: 'pp-status' }, statusDot(info.kind),
        h('span', { text: d.state === 'online' ? `checked in ${fmtAgo(d.last_seen)}` : info.word })));
    const url = ownPageURL(status());
    ownPage.hidden = !url;
    if (url) ownPage.href = url;
  }

  async function rename() {
    const input = h('input', { class: 'pp-input', type: 'text', value: dev().name || '', autofocus: true, maxlength: '60' });
    const ok = await modal({
      title: 'Rename this screen',
      body: h('div', null,
        h('label', { class: 'pp-label' }, 'Name', input),
        h('div', { class: 'pp-help', text: 'The name is only for this page and the fleet list. The screen keeps its ID.' })),
      actions: [{ label: 'Cancel', value: false }, { label: 'Rename it', value: true, kind: 'primary' }],
      onOpen: (dialog, buttons) => {
        input.addEventListener('keydown', (e) => {
          if (e.key === 'Enter') { e.preventDefault(); buttons[1].click(); }
        });
      },
    });
    if (!ok) return;
    try {
      await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/rename`, { name: input.value.trim() });
      toast('Renamed.');
      await load(false);
      paintHead();
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* --------------------------------------------------------------- notices */

  function paintNotices() {
    const d = dev();
    const parts = [];

    if (d.state === 'conflict') {
      parts.push(banner({
        kind: 'danger',
        title: 'Two boxes are reporting from one token',
        body: h('div', null,
          h('div', null, 'One card was copied, or one box was cloned. This server cannot tell which box is the true screen, so it changes nothing by itself.'),
          h('div', { style: { 'margin-top': '8px' } },
            'The ID on the row is ', h('span', { class: 'pp-mono', text: short(d.hardware_id) }),
            ' and the other box reports ', h('span', { class: 'pp-mono', text: short(d.conflict_hardware_id) }), '.'),
          h('div', { style: { 'margin-top': '8px' } },
            'Resolving it revokes the token. Both boxes then have to ask to join again, and you see which one comes back.')),
        actions: [h('button', {
          type: 'button', class: 'pp-btn pp-btn--danger', text: 'Resolve it',
          onClick: resolveConflict,
        })],
      }));
    }

    if (d.state === 'needs_confirm') {
      parts.push(banner({
        title: 'This screen is running on different hardware',
        body: h('div', null,
          'Its hardware ID changed from ', h('span', { class: 'pp-mono', text: short(d.prev_hardware_id) }),
          ' to ', h('span', { class: 'pp-mono', text: short(d.hardware_id) }),
          ' and it kept its pairing. That is normal when a card moves into a replacement box. Confirm it and the old ID is retired.'),
        actions: [h('button', {
          type: 'button', class: 'pp-btn pp-btn--primary', text: 'Confirm the swap',
          onClick: async () => {
            try {
              await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/confirm-hardware`);
              toast('The swap is confirmed. The old ID is retired.');
              await load(false);
              ctx.store.refresh();
            } catch (err) { toast(errorText(err), 'danger'); }
          },
        })],
      }));
    }

    if (d.sync_error) {
      parts.push(banner({
        kind: 'danger',
        title: 'The new playlist did not fit',
        body: h('div', null,
          h('div', null, 'The screen reports: ', h('b', { text: d.sync_error }),
            '. That is after it cleared everything that it no longer uses.'),
          h('div', { style: { 'margin-top': '8px' } },
            'It downloaded nothing and kept playing what it had. That is the intended behaviour: no half-finished playlist ever reaches a screen.'),
          h('div', { style: { 'margin-top': '8px' } },
            'Either take something out of the playlist, or put this screen on a bigger card.')),
        actions: [
          h('a', { class: 'pp-btn', href: '#/playlists', text: 'Open the playlists' }),
          h('button', {
            type: 'button', class: 'pp-btn pp-btn--danger-outline', text: 'Try the sync again',
            onClick: () => queue('rescan'),
          }),
        ],
      }));
    }

    const warnings = (status().warnings || []).filter((w) => w && w.message);
    if (warnings.length) {
      parts.push(banner({
        kind: 'warn',
        title: warnings.length === 1 ? 'The screen reports a warning' : `The screen reports ${warnings.length} warnings`,
        body: h('ul', { style: { margin: '4px 0 0', 'padding-left': '18px' } },
          warnings.map((w) => h('li', { text: w.message }))),
      }));
    }

    fill(notices, parts);
  }

  async function resolveConflict() {
    const d = dev();
    const ok = await typedConfirm({
      title: 'Revoke the token of this screen?',
      body: h('div', null,
        h('div', null, 'The token goes away. Both boxes stop being able to check in, and each one has to ask to join again with an enrollment token or a pairing code.'),
        h('div', { style: { 'margin-top': '8px' } }, 'Nothing stops playing: a screen keeps the playlist that it already holds.')),
      expect: d.name || d.id,
      label: 'Type the screen name ',
      confirm: 'Revoke the token',
    });
    if (!ok) return;
    try {
      await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/resolve-conflict`);
      toast('The token is revoked. Watch the pending list for the box that comes back.');
      await load(false);
      paintNotices();
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* ------------------------------------------------------------------ body */

  const nowSlot = h('div');
  const factsSlot = h('div');
  const historySlot = h('div');
  const groupPanel = h('div');
  const rulesSlot = h('div');

  const groupSelect = h('select', {
    class: 'pp-select', onChange: () => { overridesDirty = true; paintOverrideHelp(); },
  });
  const playlistSelect = h('select', {
    class: 'pp-select', onChange: () => { overridesDirty = true; paintOverrideHelp(); },
  });
  const onInput = h('input', {
    class: 'pp-input pp-input--mono', type: 'time',
    onInput: () => { overridesDirty = true; paintOverrideHelp(); },
  });
  const offInput = h('input', {
    class: 'pp-input pp-input--mono', type: 'time',
    onInput: () => { overridesDirty = true; paintOverrideHelp(); },
  });
  const dayRow = dayChips({ onChange: () => { overridesDirty = true; paintOverrideHelp(); } });
  const overrideHelp = h('div', { class: 'pp-help' });
  const saveOverrides = h('button', {
    type: 'button', class: 'pp-btn pp-btn--primary', text: 'Save', onClick: applyOverrides,
  });

  function draw() {
    drawHead();
    paintHead();
    paintNotices();

    fill(body,
      h('div', { class: 'pp-cards' },
        h('div', { class: 'pp-stack', style: { flex: '1 1 440px', 'min-width': 'min(320px, 100%)' } },
          card({ title: 'On screen now', body: nowSlot }),
          card({
            title: 'What this screen shows',
            body: [
              h('div', { class: 'pp-fields' },
                h('label', { class: 'pp-label pp-field' }, 'Group', groupSelect),
                h('label', { class: 'pp-label pp-field' }, 'Just for this screen', playlistSelect)),
              h('div', { class: 'pp-help', text: 'The playlist and the times come from the group unless you set them here.' }),
              h('div', { class: 'pp-fields', style: { 'margin-top': '14px' } },
                h('label', { class: 'pp-label pp-field' }, 'Screen on at', onInput),
                h('label', { class: 'pp-label pp-field' }, 'Screen off at', offInput)),
              h('div', { style: { 'margin-top': '12px' } },
                h('div', { class: 'pp-label', text: 'On these days' }),
                h('div', { style: { 'margin-top': '5px' } }, dayRow)),
              overrideHelp,
              h('div', { class: 'pp-btns', style: { 'margin-top': '14px' } },
                saveOverrides,
                h('button', {
                  type: 'button', class: 'pp-btn', text: 'Follow the group again',
                  onClick: clearOverrides,
                })),
              groupPanel,
            ],
          })),
        h('div', { class: 'pp-stack', style: { flex: '1 1 320px', 'min-width': 'min(300px, 100%)' } },
          card({ title: 'Facts', body: factsSlot }),
          card({
            title: 'Send a command',
            body: [
              h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'A command waits in a queue. The screen takes it at its next check-in.' }),
              h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
                COMMANDS.map((c) => h('button', {
                  type: 'button', class: 'pp-btn pp-btn--sm', text: c.label,
                  onClick: () => queue(c.type),
                }))),
              h('div', { style: { 'margin-top': '14px' } },
                h('div', { class: 'pp-label', text: 'The last commands' }), historySlot),
            ],
          }),
          card({
            title: 'Pairing',
            body: [
              h('div', { class: 'pp-help', style: { 'margin-top': '0' } }, pairingWords()),
              h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
                h('a', { class: 'pp-btn', href: '#/screens/add', text: 'Enrollment tokens' }),
                h('button', {
                  type: 'button', class: 'pp-btn pp-btn--danger-outline', text: 'Remove this screen',
                  onClick: removeScreen,
                })),
            ],
          }))),
      h('div', { style: { 'margin-top': '14px' } },
        h('div', { class: 'pp-page-head', style: { 'margin-bottom': '10px' } },
          h('div', null,
            h('div', { class: 'pp-h2', text: 'Times just for this screen' }),
            h('p', { class: 'pp-lead', style: { 'max-width': '640px' } },
              'One rule here replaces every rule of the group. Leave the table empty and the screen follows its group.'))),
        rulesSlot));

    paintOverrideForm();
    paintLive();

    rules = mountRules(rulesSlot, {
      owner: { device_id: deviceID },
      playlists,
      reach: 1,
      fallback: fallbackTitle(),
      onDirty: (dirty) => ctx.setGuard(() => dirty || overridesDirty),
      onSaved: () => { load(false); },
    });
  }

  function pairingWords() {
    const d = dev();
    if (isNever(d.paired_at)) return 'This screen has not finished pairing yet.';
    return `Paired on ${fmtDate(d.paired_at)}. The token lives on the screen. Removing the screen revokes it, and then the screen has to ask to join again.`;
  }

  /* ---- the live parts ---- */

  function paintLive() {
    paintHead();
    paintNotices();
    paintNow();
    paintFacts();
    paintHistory();
    paintGroupPanel();
    if (!overridesDirty) paintOverrideForm();
    if (rules) rules.setFallback(fallbackTitle());
  }

  function paintNow() {
    const st = status();
    const now = st.now_playing;
    if (!now || !now.item) {
      fill(nowSlot, h('div', { class: 'pp-help', style: { 'margin-top': '0' } },
        dev().state === 'pending'
          ? 'It waits for approval. It plays what it holds meanwhile.'
          : 'It has reported nothing yet.'));
      return;
    }
    const kind = now.kind || guessKind(now.item);
    const m = mediaByName.get(now.item);
    fill(nowSlot,
      h('div', { style: { 'max-width': '360px' } },
        preview(kind, m && m.has_thumb ? thumbURL(m.sha256) : null, { wide: true })),
      h('div', { class: 'pp-row', style: { 'justify-content': 'space-between', 'margin-top': '10px' } },
        h('span', { class: 'pp-small' },
          `${now.playlist || 'a playlist'} · item ${Number(now.index || 0) + 1}`),
        h('span', { class: 'pp-small pp-mono pp-muted', text: `reported ${fmtAgo(dev().last_seen)}` })),
      h('div', { class: 'pp-small pp-mono', style: { 'margin-top': '4px' }, title: now.item, text: now.item }),
      h('div', { class: 'pp-help' }, 'This is the last report of the screen, not a live picture.'));
  }

  function paintFacts() {
    const d = dev();
    const st = status();
    const total = Number(st.media_total_bytes) || 0;
    const free = Number(st.media_free_bytes) || 0;
    const bar = progress(total ? free / total : 0, { thin: true });
    bar.style.setProperty('width', '90px');

    fill(factsSlot, factList([
      ['Hardware ID', h('span', { class: 'pp-mono', title: d.hardware_id || '', text: short(d.hardware_id) })],
      ['Version', d.version || '—'],
      ['Last check-in', d.state === 'pending' ? 'not yet' : fmtAgo(d.last_seen)],
      ['Address', st.ips && st.ips.length ? st.ips[0] : (d.last_ip || '—')],
      ['Running for', st.uptime_seconds ? fmtDuration(st.uptime_seconds) : '—'],
      { k: 'Temperature', v: fmtTemp(st.temp_c), kind: Number(st.temp_c) > 75 ? 'danger' : null },
      ['Space on the card', h('span', { class: 'pp-row', style: { gap: '8px', 'justify-content': 'flex-end' } },
        bar, h('span', { class: 'pp-mono', text: total ? `${fmtBytes(free)} of ${fmtBytes(total)}` : '—' }))],
      ['Player', st.browser_state || '—'],
      ['Screen', st.screen_on === undefined ? '—' : (st.screen_on ? 'on' : 'off')],
      ['Time zone', st.timezone || '—'],
      ['Checks in every', d.poll_seconds ? `${d.poll_seconds} s` : 'the server default'],
      ['First seen', fmtDate(d.created_at)],
    ]));
  }

  function paintHistory() {
    const list = (view && view.commands) || [];
    if (list.length === 0) {
      fill(historySlot, h('div', { class: 'pp-help', text: 'Nothing has been sent to this screen yet.' }));
      return;
    }
    fill(historySlot, list.slice(0, 8).map((cmd) => {
      const s = commandState(cmd);
      const label = (COMMANDS.find((c) => c.type === cmd.type) || {}).label || cmd.type;
      return h('div', { class: 'sv-cmd' },
        h('span', null, label, ' — ', h('b', { text: s.word })),
        h('span', { class: 'sv-cmd__when', text: fmtClock(s.when) }));
    }));
  }

  /* The group half of "what this screen shows". It is read-only here: the group
     is edited on its own page, and two places to edit one rule is how a fleet
     ends up with two answers. */
  function paintGroupPanel() {
    const group = view && view.group;
    if (!group) {
      fill(groupPanel, h('div', { class: 'pp-panel', style: { 'margin-top': '14px' } },
        h('div', { class: 'pp-small pp-muted', text: 'This screen is in no group, so only the values above decide what it plays.' })));
      return;
    }
    const groupRules = (view.group_assignments || []);
    const mine = (view.assignments || []).length > 0;
    fill(groupPanel, h('div', { class: 'pp-panel', style: { 'margin-top': '14px' } },
      h('div', { class: 'pp-label', style: { 'margin-bottom': '6px' } }, `From the ${group.name} group`),
      h('div', { class: 'pp-facts' },
        h('div', { class: 'pp-facts__row' },
          h('span', { class: 'pp-facts__k', text: 'Plays when no rule matches' }),
          h('span', { class: 'pp-facts__v', text: titleOf(group.default_playlist_id) || 'nothing' })),
        h('div', { class: 'pp-facts__row' },
          h('span', { class: 'pp-facts__k', text: 'Screen times' }),
          h('span', { class: 'pp-facts__v', text: group.screen_on && group.screen_off
            ? `${group.screen_on} to ${group.screen_off}, ${daysInWords(daysFromCSV(group.screen_days))}`
            : 'always on' })),
        groupRules.map((r) => h('div', { class: 'pp-facts__row' },
          h('span', { class: 'pp-facts__k', text: titleOf(r.playlist_id) || r.playlist_name }),
          h('span', { class: 'pp-facts__v', text: whenWords(r) })))),
      mine ? h('div', { class: 'pp-small', style: { 'margin-top': '10px', color: 'var(--pp-warn-ink)' },
        text: 'This screen has times of its own below, so none of the group times apply to it.' }) : null,
      h('a', { class: 'pp-btn pp-btn--ghost', style: { 'margin-top': '10px' }, href: '#/groups', text: "Edit the group's times →" })));
  }

  function whenWords(rule) {
    const days = daysInWords(rule.days || []);
    if (!rule.start || !rule.end) return `${days}, all day`;
    return `${days}, ${rule.start} to ${rule.end}`;
  }

  /* ---- the overrides form ---- */

  function paintOverrideForm() {
    const d = dev();
    fill(groupSelect,
      h('option', { value: '0', text: 'No group' }),
      groups.map((g) => h('option', { value: String(g.id), text: g.name })));
    groupSelect.value = String(d.group_id || 0);

    fill(playlistSelect,
      h('option', { value: '0', text: 'Follow the group' }),
      playlists.map((p) => h('option', { value: String(p.id), text: p.title })));
    playlistSelect.value = String(d.default_playlist_id || 0);

    onInput.value = d.screen_on || '';
    offInput.value = d.screen_off || '';
    dayRow.set(daysFromCSV(d.screen_days));
    overridesDirty = false;
    paintOverrideHelp();
  }

  function paintOverrideHelp() {
    const group = view && view.group;
    const pid = Number(playlistSelect.value);
    const bits = [];
    bits.push(pid
      ? `This screen plays ${titleOf(pid)} when no rule matches.`
      : (group ? `This screen follows the ${group.name} group.` : 'This screen has no playlist of its own.'));
    if (onInput.value && offInput.value) {
      bits.push(`Its panel is on from ${onInput.value} to ${offInput.value}, ${daysInWords(dayRow.read())}.`);
    } else if (onInput.value || offInput.value) {
      bits.push('Give both times, or leave both empty.');
    }
    setText(overrideHelp, bits.join(' '));
    saveOverrides.disabled = !overridesDirty;
    ctx.setGuard(() => overridesDirty || (rules && rules.isDirty()));
  }

  async function applyOverrides() {
    const wantedGroup = Number(groupSelect.value);
    saveOverrides.disabled = true;
    try {
      if (wantedGroup !== (dev().group_id || 0)) {
        await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/group`, { group_id: wantedGroup });
      }
      await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/overrides`, {
        default_playlist_id: Number(playlistSelect.value),
        screen_on: onInput.value,
        screen_off: offInput.value,
        screen_days: dayRow.read(),
      });
      overridesDirty = false;
      toast('Saved. The screen takes it at its next check-in.');
      await load(false);
      paintLive();
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    } finally {
      paintOverrideHelp();
    }
  }

  async function clearOverrides() {
    const ok = await confirmDialog({
      title: 'Follow the group again?',
      body: 'The playlist and the screen times of this screen go away, and the values of its group take over.',
      confirm: 'Follow the group',
    });
    if (!ok) return;
    try {
      await api('POST', `/api/admin/devices/${encodeURIComponent(deviceID)}/overrides`, {
        default_playlist_id: 0, screen_on: '', screen_off: '', screen_days: [],
      });
      toast('It follows its group again.');
      await load(false);
      paintOverrideForm();
      paintLive();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* ---- commands and removal ---- */

  async function queue(type) {
    const sent = await sendToScreen(deviceID, dev().name || deviceID, type);
    if (sent) { await load(false); paintHistory(); }
  }

  async function removeScreen() {
    const d = dev();
    const ok = await typedConfirm({
      title: `Remove ${d.name || d.id}?`,
      body: h('div', null,
        h('div', null, 'The row goes away with its token, its times and its command history. The box keeps playing what it holds, and it cannot check in any more.'),
        h('div', { style: { 'margin-top': '8px' } }, 'To bring it back, pair it again with an enrollment token or a pairing code.')),
      expect: d.name || d.id,
      label: 'Type the screen name ',
      confirm: 'Remove it',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/admin/devices/${encodeURIComponent(deviceID)}`);
      toast(`${d.name || d.id} is gone.`);
      ctx.clearGuard();
      ctx.store.refresh();
      location.hash = '#/screens';
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  function fallbackTitle() {
    const d = dev();
    if (d.default_playlist_id) return titleOf(d.default_playlist_id);
    const group = view && view.group;
    if (group && group.default_playlist_id) return titleOf(group.default_playlist_id);
    return '';
  }

  /* The page starts here, after every part above is built. */
  load(true);
  timer = setInterval(() => load(false), POLL_MS);

  return {
    destroy() {
      gone = true;
      clearInterval(timer);
      if (rules) rules.destroy();
      document.title = 'PortaPixel Control';
    },
  };
}

/* The first eight characters of a hardware ID. The whole value is a SHA-256 and
   nobody reads 64 characters off a card. */
function short(id) {
  const v = String(id || '');
  return v.length > 12 ? `${v.slice(0, 8)}…` : v || '—';
}
