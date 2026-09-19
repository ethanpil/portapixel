/* The time rules of one owner: a group, or one screen.

   Two pages need the same table, so it lives here. The rules are read from the
   top and the first one that matches wins, which is why the row order is part
   of the meaning: the order is the priority, and a save sends the new priority
   of every row.
*/

import {
  h, fill, icon, toast, confirmDialog, card, errorText, dayChips,
  daysInWords, inWindow, setText,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { clockNow } from './util.js';

/** Mount the rule table and the week picture.
    owner:       {group_id} or {device_id}
    playlists:   the list from GET /api/admin/playlists
    reach:       how many screens a save reaches; it goes in the button
    fallback:    the title of the playlist that plays when no rule matches
    onDirty(dirty), onSaved()
    Returns {destroy, isDirty, setReach, setFallback, reload}. */
export function mountRules(el, opts = {}) {
  const owner = opts.owner || {};
  let playlists = opts.playlists || [];
  let reach = Number(opts.reach) || 0;
  let fallback = opts.fallback || '';
  let rules = [];          // the working copy
  let baseline = '';
  let removed = [];        // the IDs of the rows that the user took away
  let gone = false;

  const rowsSlot = h('div');
  const weekSlot = h('div');
  const dirtyNote = h('span', { class: 'pp-pe__dirty' });
  const nowLine = h('div', { class: 'pp-help', style: { 'margin-top': '0' }, role: 'status', 'aria-live': 'polite' });
  const saveBtn = h('button', {
    type: 'button', class: 'pp-btn pp-btn--primary', disabled: true, onClick: save,
  });

  fill(el,
    h('div', { class: 'pp-card' },
      h('div', { class: 'pp-card__head' },
        h('div', { class: 'pp-h2', text: 'Times' }),
        h('div', { class: 'pp-card__meta' }, dirtyNote)),
      rowsSlot,
      h('div', { class: 'pp-card__foot' },
        h('button', { type: 'button', class: 'pp-btn pp-btn--dashed', onClick: addRule }, icon('plus'), 'Add a rule'),
        saveBtn)),
    card({ title: 'What the week looks like', body: [weekSlot, nowLine] }));

  load();

  /* ------------------------------------------------------------------ load */

  async function load() {
    const query = owner.device_id
      ? `?device_id=${encodeURIComponent(owner.device_id)}`
      : `?group_id=${encodeURIComponent(owner.group_id)}`;
    try {
      const out = await api('GET', `/api/admin/assignments${query}`);
      if (gone) return;
      rules = (out.assignments || []).map((a) => ({
        id: a.id, playlist_id: a.playlist_id, days: a.days || [],
        start: a.start || '', end: a.end || '',
      }));
      removed = [];
      baseline = snapshot();
      renderRows();
      touch();
    } catch (err) {
      fill(rowsSlot, h('div', { class: 'pp-card__body' },
        h('div', { class: 'pp-error', text: errorText(err) })));
    }
  }

  function snapshot() {
    return JSON.stringify(rules.map((r) => [r.playlist_id, r.days, r.start, r.end]));
  }

  function titleOf(id) {
    const p = playlists.find((x) => x.id === Number(id));
    return p ? p.title : '';
  }

  function touch() {
    const dirty = snapshot() !== baseline || removed.length > 0;
    setText(dirtyNote, dirty ? 'not saved yet' : '');
    saveBtn.disabled = !dirty;
    saveBtn.textContent = reach
      ? `Save — ${reach} ${reach === 1 ? 'screen picks' : 'screens pick'} this up`
      : 'Save these times';
    renderWeek();
    if (opts.onDirty) opts.onDirty(dirty);
  }

  /* ----------------------------------------------------------------- rows */

  function renderRows() {
    if (!rules.length) {
      fill(rowsSlot, h('div', { class: 'pp-empty' },
        h('div', { class: 'pp-empty__title', text: 'No times yet' }),
        h('div', { class: 'pp-empty__body', text: 'With no rule the playlist above plays all day, which is how most screens run. Add a rule when a different playlist belongs in a different hour.' })));
      return;
    }
    fill(rowsSlot, rules.map((rule, i) => ruleRow(rule, i)));
  }

  function ruleRow(rule, i) {
    const playlistSelect = h('select', {
      class: 'pp-select', 'aria-label': `Playlist of rule ${i + 1}`,
      onChange: () => { rule.playlist_id = Number(playlistSelect.value); touch(); },
    }, playlists.map((p) => h('option', { value: String(p.id), text: p.title })));
    if (!playlists.some((p) => p.id === Number(rule.playlist_id))) {
      playlistSelect.append(h('option', { value: String(rule.playlist_id || 0), text: 'pick one' }));
    }
    playlistSelect.value = String(rule.playlist_id || 0);

    const chips = dayChips({ days: rule.days, onChange: (days) => { rule.days = days; touch(); } });

    const time = (key, label) => h('input', {
      class: 'pp-input pp-input--mono pp-input--sm', type: 'time', value: rule[key],
      'aria-label': `${label} of rule ${i + 1}`,
      onInput: (e) => { rule[key] = e.target.value; touch(); },
    });

    const move = (to) => {
      if (to < 0 || to >= rules.length) return;
      const [it] = rules.splice(i, 1);
      rules.splice(to, 0, it);
      touch();
      renderRows();
      focusMove(to, to > i);
    };

    const error = h('div', { class: 'pp-error', hidden: true, style: { flex: '1 1 100%' } });
    const row = h('div', { class: 'pp-rule' },
      h('div', { class: 'pp-rule__n', text: `${i + 1}` }),
      h('label', { class: 'pp-label pp-rule__pl' }, 'Play this', playlistSelect),
      h('div', null, h('div', { class: 'pp-label', text: 'On these days' }),
        h('div', { style: { 'margin-top': '5px' } }, chips)),
      h('div', null, h('div', { class: 'pp-label', text: 'Between' }),
        h('div', { class: 'pp-rule__times', style: { 'margin-top': '5px' } }, time('start', 'Start'), time('end', 'End'))),
      h('div', { class: 'pp-rule__acts' },
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Move rule ${i + 1} up`,
          disabled: i === 0, onClick: () => move(i - 1),
        }, icon('up', 14)),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Move rule ${i + 1} down`,
          disabled: i === rules.length - 1, onClick: () => move(i + 1),
        }, icon('down', 14)),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Remove rule ${i + 1}`,
          onClick: () => {
            const [it] = rules.splice(i, 1);
            if (it.id) removed.push(it.id);
            touch();
            renderRows();
          },
        }, icon('close', 14))),
      error);
    row.errorSlot = error;
    return row;
  }

  /* renderRows() builds every row again, so the button that made the move is
     gone and the keyboard would fall to the body. Put it on the row that moved,
     and say in words where the row is now. */
  function focusMove(index, wentDown) {
    const rows = [...rowsSlot.querySelectorAll('.pp-rule')];
    const row = rows[index];
    if (!row) return;
    const btns = row.querySelectorAll('.pp-rule__acts .pp-btn--icon');
    const wanted = btns[wentDown ? 1 : 0];
    const other = btns[wentDown ? 0 : 1];
    if (wanted && !wanted.disabled) wanted.focus();
    else if (other && !other.disabled) other.focus();
    setText(nowLine, `Rule ${index + 1} of ${rules.length}. ${nowWords()}`);
  }

  function addRule() {
    rules.push({
      id: 0, playlist_id: (playlists[0] && playlists[0].id) || 0,
      days: ['mon', 'tue', 'wed', 'thu', 'fri'], start: '08:00', end: '18:00',
    });
    touch();
    renderRows();
  }

  /* ------------------------------------------------------------ the week */

  /* Seven rows of twenty-four cells. A cell is green when a rule covers the
     middle of that hour, which is the reading a person wants from a picture. */
  function renderWeek() {
    const labels = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
    const grid = h('div', { class: 'pp-week' });
    for (let d = 0; d < 7; d++) {
      const hours = h('div', { class: 'pp-week__hours' });
      for (let hour = 0; hour < 24; hour++) {
        const minute = hour * 60 + 30;
        const hit = rules.findIndex((r) => inWindow(r.start, r.end, minute, d, r.days));
        hours.append(h('span', {
          class: `pp-week__cell${hit >= 0 ? ' pp-week__cell--on' : ''}`,
          title: hit >= 0
            ? `${labels[d]} ${String(hour).padStart(2, '0')}:00 — rule ${hit + 1}, ${titleOf(rules[hit].playlist_id)}`
            : `${labels[d]} ${String(hour).padStart(2, '0')}:00 — ${fallback || 'nothing'}`,
        }));
      }
      grid.append(h('div', { class: 'pp-week__day', text: labels[d] }), hours);
    }

    const axis = h('div', { class: 'pp-week__axis' });
    for (let hour = 0; hour < 24; hour++) axis.append(h('span', { text: String(hour).padStart(2, '0') }));

    fill(weekSlot, grid,
      h('div', { class: 'pp-week', style: { 'margin-top': '2px' } }, h('span'), axis),
      h('div', { class: 'pp-row', style: { 'margin-top': '10px' } },
        h('span', { class: 'pp-status', style: { gap: '6px' } },
          h('span', { class: 'pp-swatch pp-swatch--ok' }), h('span', { class: 'pp-small pp-muted', text: 'A rule' })),
        h('span', { class: 'pp-status', style: { gap: '6px' } },
          h('span', { class: 'pp-swatch' }), h('span', { class: 'pp-small pp-muted', text: fallback || 'nothing' }))));
    updateNow();
  }

  /* The one line that answers "so what plays right now?". The hours are those
     of this browser; every screen keeps its own time zone, and the line says
     so where it is shown. */
  function updateNow() {
    setText(nowLine, nowWords());
  }

  function nowWords() {
    const { weekday, minute } = clockNow();
    const hit = rules.findIndex((r) => inWindow(r.start, r.end, minute, weekday, r.days));
    if (hit < 0) return `At this hour no rule matches, so ${fallback || 'nothing'} plays.`;
    const r = rules[hit];
    const when = r.start && r.end ? ` from ${r.start} to ${r.end}` : ' all day';
    return `At this hour rule ${hit + 1} matches: ${titleOf(r.playlist_id) || 'pick a playlist'}, ${daysInWords(r.days)}${when}.`;
  }

  /* ------------------------------------------------------------------ save */

  function clearErrors() {
    for (const row of rowsSlot.querySelectorAll('.pp-rule')) {
      if (row.errorSlot) { row.errorSlot.hidden = true; row.errorSlot.textContent = ''; }
      row.classList.remove('pp-field--invalid');
    }
  }

  function showError(index, message) {
    const rows = [...rowsSlot.querySelectorAll('.pp-rule')];
    const row = rows[index];
    if (!row || !row.errorSlot) { toast(message, 'danger'); return; }
    row.errorSlot.textContent = message;
    row.errorSlot.hidden = false;
    row.classList.add('pp-field--invalid');
  }

  async function save() {
    clearErrors();
    const bad = rules.findIndex((r) => !r.playlist_id);
    if (bad >= 0) { showError(bad, 'This rule needs a playlist.'); return; }
    const halfTime = rules.findIndex((r) => (!!r.start) !== (!!r.end));
    if (halfTime >= 0) {
      showError(halfTime, 'Give a start and an end, or leave both empty for the whole day.');
      return;
    }

    saveBtn.disabled = true;
    try {
      for (const id of removed) {
        await api('DELETE', `/api/admin/assignments/${id}`);
      }
      removed = [];
      // The row order is the priority. Every row gets its number again, so a
      // move of one row cannot leave two rows with the same priority.
      for (let i = 0; i < rules.length; i++) {
        const r = rules[i];
        const body = {
          ...owner, playlist_id: Number(r.playlist_id), days: r.days,
          start: r.start, end: r.end, priority: (i + 1) * 10,
        };
        if (r.id) await api('PUT', `/api/admin/assignments/${r.id}`, body);
        else {
          const made = await api('POST', '/api/admin/assignments', body);
          r.id = made.id;
        }
      }
      baseline = snapshot();
      toast(reach
        ? `Saved — ${reach} ${reach === 1 ? 'screen picks' : 'screens pick'} this up at the next check-in`
        : 'The times are saved.');
      if (opts.onSaved) opts.onSaved();
    } catch (err) {
      /* A toast replaces the one before it, so a loop of them shows only the
         last field. Put each message at the row that it belongs to. */
      if (err.fields && err.fields.length) {
        const row = rules.findIndex((r) => r.id === 0) >= 0 ? rules.findIndex((r) => r.id === 0) : 0;
        showError(row, err.fields.map((f) => f.message).join(' '));
        toast('One of these rules is not usable.', 'danger');
      } else {
        toast(errorText(err), 'danger');
      }
      // The server may have taken some of the rows. Read them again, so the
      // table never shows a state that the server does not hold.
      await load();
      return;
    }
    // A save that lands after the page went away must not put the guard of a dead
    // page on the page that is on the screen now.
    if (gone) return;
    touch();
  }

  /* -------------------------------------------------------------- plumbing */

  return {
    isDirty: () => snapshot() !== baseline || removed.length > 0,
    setReach(n) { reach = Number(n) || 0; touch(); },
    setFallback(title) { fallback = title || ''; renderWeek(); },
    setPlaylists(list) { playlists = list || []; renderRows(); },
    reload: load,
    /** Ask before a reload that would drop unsaved rows. */
    async confirmLeave() {
      if (!this.isDirty()) return true;
      return confirmDialog({
        title: 'Leave these times?',
        body: 'The rules on this page are not saved yet. Leaving drops them.',
        confirm: 'Leave anyway', cancel: 'Stay here', kind: 'danger',
      });
    },
    destroy() { gone = true; el.replaceChildren(); },
  };
}
