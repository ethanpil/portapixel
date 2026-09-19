/* Groups and times: a set of screens that play the same thing.

   A group holds the playlist that plays when no rule matches, the times of the
   rules, and the hours when the panels are on. A screen with values of its own
   ignores the group ones, and this page says so where it matters.
*/

import {
  h, fill, toast, banner, modal, confirmDialog, statusDot, card, pageHead,
  errorText, setText, dayChips, daysInWords,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { mountRules } from '../rules.js';
import { sendToGroup } from '../commands.js';
import { daysFromCSV, stateInfo, screenHref } from '../util.js';

/* The group that is open. It lives in the module, so a trip to another page and
   back comes back to the same group. */
let pickedId = 0;

export function mount(main, ctx) {
  let groups = [];
  let playlists = [];
  let ruleCounts = new Map();   // group id -> how many time rules it holds
  let rules = null;
  let settingsDirty = false;
  let gone = false;

  const side = h('div', { class: 'pp-split__side' });
  const panel = h('div', { class: 'pp-split__main' });
  const errorSlot = h('div');

  fill(main,
    pageHead('Groups and times',
      'A group is a set of screens that play the same thing. The times are read from the top and the first match wins.',
      h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New group', onClick: createGroup })),
    errorSlot,
    h('div', { class: 'pp-split' }, side, panel));

  load(true);
  const unsubscribe = ctx.store.subscribe(() => {
    // The member counts and the "all checked in" line come from the fleet poll.
    if (!gone && groups.length) { renderList(); paintHeader(); }
  });

  /* ------------------------------------------------------------------ load */

  async function load(first) {
    try {
      const [gotGroups, gotPlaylists, gotRules] = await Promise.all([
        api('GET', '/api/admin/groups'),
        first ? api('GET', '/api/admin/playlists') : Promise.resolve(null),
        api('GET', '/api/admin/assignments'),
      ]);
      if (gone) return;
      groups = gotGroups.groups || [];
      if (gotPlaylists) playlists = gotPlaylists.playlists || [];
      ruleCounts = new Map();
      for (const a of gotRules.assignments || []) {
        if (!a.group_id) continue;
        ruleCounts.set(a.group_id, (ruleCounts.get(a.group_id) || 0) + 1);
      }
      if (!groups.some((g) => g.id === pickedId)) pickedId = (groups[0] && groups[0].id) || 0;
      renderList();
      if (first) openPicked();
    } catch (err) {
      fill(errorSlot, banner({ kind: 'danger', title: 'The groups did not load', body: errorText(err) }));
    }
  }

  function picked() { return groups.find((g) => g.id === pickedId) || null; }
  function titleOf(id) {
    const p = playlists.find((x) => x.id === Number(id));
    return p ? p.title : '';
  }
  function members(id) { return ctx.store.devices.filter((d) => d.group_id === id); }

  /* ------------------------------------------------------------- the list */

  function renderList() {
    const rows = groups.map((g) => {
      const n = ruleCounts.get(g.id) || 0;
      const bits = [titleOf(g.default_playlist_id) || 'no playlist yet'];
      bits.push(n === 0 ? 'no times' : `${n} ${n === 1 ? 'time rule' : 'time rules'}`);
      return h('button', {
        type: 'button', class: 'pp-pick', 'aria-current': String(g.id === pickedId),
        onClick: () => pick(g.id),
      },
        h('div', { class: 'pp-pick__name' },
          h('span', { text: g.name }),
          h('span', { class: 'pp-pick__count', text: String(g.devices) })),
        h('div', { class: 'pp-pick__meta', text: bits.join(' · ') }));
    });

    fill(side, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-card__head' }, h('div', { class: 'pp-h2', text: 'Groups' })),
      rows.length ? rows : h('div', { class: 'pp-card__body' },
        h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'No groups yet.' })),
      h('div', { class: 'pp-card__foot' },
        h('button', { type: 'button', class: 'pp-btn pp-btn--ghost', text: '+ New group', onClick: createGroup }))));
  }

  async function pick(id) {
    if (id === pickedId) return;
    if (!(await leaveOk())) return;
    pickedId = id;
    renderList();
    openPicked();
  }

  async function leaveOk() {
    if (rules && rules.isDirty() && !(await rules.confirmLeave())) return false;
    if (settingsDirty && !(await confirmDialog({
      title: 'Leave these settings?',
      body: 'The values of this group are not saved yet. Leaving drops them.',
      confirm: 'Leave anyway', cancel: 'Stay here', kind: 'danger',
    }))) return false;
    return true;
  }

  /* ------------------------------------------------------------ the panel */

  const headerSlot = h('div');
  const nameInput = h('input', { class: 'pp-input', type: 'text', maxlength: '60', onInput: touch });
  const playlistSelect = h('select', { class: 'pp-select', onChange: touch });
  const onInput = h('input', { class: 'pp-input pp-input--mono', type: 'time', onInput: touch });
  const offInput = h('input', { class: 'pp-input pp-input--mono', type: 'time', onInput: touch });
  const dayRow = dayChips({ onChange: touch });
  const settingsHelp = h('div', { class: 'pp-help' });
  const saveBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Save', disabled: true, onClick: saveSettings });
  const rulesSlot = h('div');

  function openPicked() {
    if (rules) { rules.destroy(); rules = null; }
    const g = picked();
    if (!g) {
      fill(panel, card({
        body: h('div', { class: 'pp-empty', style: { margin: '0' } },
          h('div', { class: 'pp-empty__title', text: 'No group is open' }),
          h('div', { class: 'pp-empty__body', text: 'A group keeps a set of screens on the same playlist and the same times. Make one, then move screens into it from their own pages.' }),
          h('div', { class: 'pp-empty__actions' },
            h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New group', onClick: createGroup }))),
      }));
      return;
    }

    fill(panel,
      headerSlot,
      card({
        title: 'What this group plays',
        body: [
          h('div', { class: 'pp-fields' },
            h('label', { class: 'pp-label pp-field' }, 'Name', nameInput),
            h('label', { class: 'pp-label pp-field' }, 'Plays when no rule matches', playlistSelect)),
          h('div', { class: 'pp-fields', style: { 'margin-top': '14px' } },
            h('label', { class: 'pp-label pp-field' }, 'Screens on at', onInput),
            h('label', { class: 'pp-label pp-field' }, 'Screens off at', offInput)),
          h('div', { style: { 'margin-top': '12px' } },
            h('div', { class: 'pp-label', text: 'On these days' }),
            h('div', { style: { 'margin-top': '5px' } }, dayRow)),
          settingsHelp,
          h('div', { class: 'pp-btns', style: { 'margin-top': '14px' } }, saveBtn,
            h('button', { type: 'button', class: 'pp-btn pp-btn--danger-outline', text: 'Delete this group', onClick: deleteGroup })),
          overrideNote(g),
        ],
      }),
      h('div', { style: { height: '14px' } }),
      rulesSlot);

    paintForm();
    paintHeader();

    rules = mountRules(rulesSlot, {
      owner: { group_id: g.id },
      playlists,
      reach: g.devices,
      fallback: titleOf(g.default_playlist_id),
      onDirty: (dirty) => ctx.setGuard(() => dirty || settingsDirty),
      onSaved: () => load(false),
    });
  }

  /* The line that the wireframe asks for: how many screens, and are they well.
     It comes from the fleet poll, so it stays true while the page is open. */
  function paintHeader() {
    const g = picked();
    if (!g) return;
    const list = members(g.id);
    const bad = list.filter((d) => stateInfo(d.state).flag).length;
    const quiet = list.filter((d) => d.state === 'quiet').length;
    const count = `${list.length} ${list.length === 1 ? 'screen' : 'screens'}`;
    let words = 'no screens yet';
    if (list.length) {
      if (bad) words = `${count} · ${bad} ${bad === 1 ? 'needs' : 'need'} a look`;
      else if (quiet) words = `${count} · ${quiet} quiet`;
      else words = `${count} · all checked in`;
    }
    const kind = bad ? 'alert' : (quiet ? 'quiet' : 'ok');

    fill(headerSlot, h('div', { class: 'pp-page-head', style: { 'margin-bottom': '12px' } },
      h('div', null,
        h('div', { class: 'pp-h1', style: { 'font-size': '18px' }, text: g.name }),
        h('div', { class: 'pp-status', style: { 'margin-top': '5px' } },
          statusDot(kind), h('span', { class: 'pp-small pp-muted', text: words }))),
      h('div', { class: 'pp-btns' },
        h('button', { type: 'button', class: 'pp-btn', text: 'Members', onClick: () => showMembers(g) }),
        h('button', {
          type: 'button', class: 'pp-btn', text: 'Send a command',
          onClick: () => sendToGroup(groups, g.id).then((sent) => { if (sent) ctx.store.refresh(); }),
        }))));
  }

  /* Screens in this group that answer to values of their own. The group page
     must say it, or somebody changes a group and wonders why one screen keeps
     showing something else. */
  function overrideNote(g) {
    const own = members(g.id).filter((d) => d.default_playlist_id || d.screen_on || d.screen_off);
    if (own.length === 0) return null;
    return h('div', { class: 'pp-panel', style: { 'margin-top': '14px' } },
      h('div', { class: 'pp-small' },
        own.length === 1 ? 'One screen in this group has values of its own: ' : `${own.length} screens in this group have values of their own: `,
        own.map((d, i) => [i ? ', ' : null, h('a', { href: screenHref(d.id), text: d.name || d.id })]),
        '. What is set on a screen wins over what is set here.'));
  }

  function paintForm() {
    const g = picked();
    if (!g) return;
    nameInput.value = g.name;
    fill(playlistSelect,
      h('option', { value: '0', text: 'Nothing' }),
      playlists.map((p) => h('option', { value: String(p.id), text: p.title })));
    playlistSelect.value = String(g.default_playlist_id || 0);
    onInput.value = g.screen_on || '';
    offInput.value = g.screen_off || '';
    dayRow.set(daysFromCSV(g.screen_days));
    settingsDirty = false;
    paintHelp();
  }

  function touch() {
    settingsDirty = true;
    paintHelp();
  }

  function paintHelp() {
    const bits = [];
    if (onInput.value && offInput.value) {
      bits.push(`The panels are on from ${onInput.value} to ${offInput.value}, ${daysInWords(dayRow.read())}.`);
    } else if (onInput.value || offInput.value) {
      bits.push('Give both times, or leave both empty for screens that stay on.');
    } else {
      bits.push('With both times empty the screens stay on. Each screen keeps its own time zone.');
    }
    setText(settingsHelp, bits.join(' '));
    saveBtn.disabled = !settingsDirty;
    ctx.setGuard(() => settingsDirty || (rules && rules.isDirty()));
    if (rules) rules.setFallback(titleOf(playlistSelect.value));
  }

  async function saveSettings() {
    const g = picked();
    if (!g) return;
    /* The server takes both screen times or neither, and it takes a day list only
       with both times. Say so here: a 422 that names screen_on is a worse way to
       learn it. */
    const on = onInput.value;
    const off = offInput.value;
    if (!on !== !off) {
      toast('Give a screen-on time and a screen-off time, or leave both empty.', 'danger');
      (on ? offInput : onInput).focus();
      return;
    }
    if (!on && !off && dayRow.read().length) {
      toast('A day list needs both screen times. Give the times, or turn every day back on.', 'danger');
      return;
    }
    saveBtn.disabled = true;
    try {
      await api('PUT', `/api/admin/groups/${g.id}`, {
        name: nameInput.value.trim(),
        default_playlist_id: Number(playlistSelect.value),
        screen_on: onInput.value,
        screen_off: offInput.value,
        screen_days: dayRow.read(),
      });
      settingsDirty = false;
      const n = g.devices;
      toast(n ? `Saved — ${n} ${n === 1 ? 'screen picks' : 'screens pick'} this up at the next check-in.` : 'Saved.');
      await load(false);
      paintForm();
      paintHeader();
      ctx.store.refresh();
    } catch (err) {
      // A toast replaces the one before it, so every field goes in one sentence.
      const named = (err.fields || []).map((f) => f.message).join(' ');
      toast(named || (err.status === 409
        ? 'Another group already has that name. Pick a different one.'
        : errorText(err)), 'danger');
    } finally {
      paintHelp();
    }
  }

  /* ------------------------------------------------------- make and remove */

  async function createGroup() {
    if (!(await leaveOk())) return;
    const input = h('input', { class: 'pp-input', type: 'text', autofocus: true, maxlength: '60' });
    const ok = await modal({
      title: 'New group',
      body: h('div', null,
        h('label', { class: 'pp-label' }, 'Name', input),
        h('div', { class: 'pp-help', text: 'Name it after the place, such as Lobby or Warehouse. Screens move into it from their own pages.' })),
      actions: [{ label: 'Cancel', value: false }, { label: 'Make it', value: true, kind: 'primary' }],
      onOpen: (dialog, buttons) => {
        input.addEventListener('keydown', (e) => {
          if (e.key === 'Enter') { e.preventDefault(); buttons[1].click(); }
        });
      },
    });
    if (!ok) return;
    try {
      const made = await api('POST', '/api/admin/groups', { name: input.value.trim() });
      pickedId = made.id;
      toast('The group is ready. Put screens in it from their own pages.');
      await load(false);
      openPicked();
    } catch (err) {
      const named = (err.fields || []).map((f) => f.message).join(' ');
      toast(named || (err.status === 409
        ? 'Another group already has that name. Pick a different one.'
        : errorText(err)), 'danger');
    }
  }

  async function deleteGroup() {
    const g = picked();
    if (!g) return;
    if (g.devices > 0) {
      await modal({
        title: `${g.name} still holds screens`,
        body: h('div', null,
          `Move its ${g.devices} ${g.devices === 1 ? 'screen' : 'screens'} to another group first. `,
          'A group that went away under a screen would leave that screen with no playlist, and this server never changes what a screen plays by accident.'),
        actions: [{ label: 'All right', value: true, kind: 'primary' }],
      });
      return;
    }
    const ok = await confirmDialog({
      title: `Delete ${g.name}?`,
      body: 'Its time rules go with it. No screen is in it, so nothing stops playing.',
      confirm: 'Delete it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/admin/groups/${g.id}`);
      toast(`${g.name} is gone.`);
      pickedId = 0;
      ctx.clearGuard();
      await load(false);
      openPicked();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  function showMembers(g) {
    const list = members(g.id);
    modal({
      title: `The screens of ${g.name}`,
      wide: true,
      body: list.length
        ? h('div', { class: 'pp-facts' }, list.map((d) => {
          const info = stateInfo(d.state);
          return h('div', { class: 'pp-facts__row' },
            h('span', { class: 'pp-facts__k' },
              h('span', { class: 'pp-status' }, statusDot(info.kind),
                h('a', { href: screenHref(d.id), text: d.name || d.id }))),
            h('span', { class: 'pp-facts__v', text: info.word }));
        }))
        : h('div', { class: 'pp-help' }, 'No screen is in this group yet. Open a screen and set its group there.'),
      actions: [{ label: 'Close', value: true }],
    });
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
      if (rules) rules.destroy();
    },
  };
}
