/* Schedule: the [[schedule]] rules of this screen.

   The rules are read from the top and the first one that matches wins, so the
   order of the rows is part of the meaning. The rows move with the two arrow
   buttons, and the week grid under them shows what the order adds up to.
*/

import { h, fill, toast, banner, icon } from '/shared/ui.js';
import { api } from '/shared/api.js';
import {
  card, pageHead, setText, errorText, dayChips, daysInWords,
  deviceClock, inWindow,
} from '../util.js';

export function mount(main, ctx) {
  let cfg = null;              // the whole configuration, so a save keeps everything
  let rules = [];              // the working copy
  let defaultPlaylist = '';
  let playlists = [];
  let baseline = '';
  let paired = false;
  let gone = false;

  const banners = h('div');
  const lead = h('p', { class: 'pp-lead' });
  const body = h('div', { class: 'pp-stack' });
  const nowLine = h('div', { class: 'pp-help', style: { 'margin-top': '0' } });

  fill(main, pageHead('Schedule', lead), banners, body);

  const unsubscribe = ctx.store.subscribe((status) => {
    if (!status || gone) return;
    const was = paired;
    paired = !!status.paired;
    if (cfg && was !== paired) render();
    else if (cfg) { renderBanners(); updateNow(); }
  });

  load();

  async function load() {
    try {
      const [view, snap] = await Promise.all([
        api('GET', '/api/config'),
        api('GET', '/api/playlists'),
      ]);
      if (gone) return;
      cfg = view.config;
      playlists = snap.playlists || [];
      rules = (cfg.schedule || []).map((r) => ({
        playlist: r.playlist || '', days: r.days || [], start: r.start || '', end: r.end || '',
      }));
      defaultPlaylist = cfg.playback.default_playlist || '';
      baseline = snapshot();
      render();
    } catch (err) {
      fill(body, banner({ kind: 'danger', title: 'The schedule did not load', body: errorText(err) }));
    }
  }

  function snapshot() {
    return JSON.stringify({ rules, defaultPlaylist });
  }

  function titleOf(name) {
    const p = playlists.find((x) => x.name === name);
    return p ? p.title : name;
  }

  function touch() {
    const dirty = snapshot() !== baseline;
    ctx.setGuard(() => dirty);
    setText(dirtyNote, dirty ? 'not saved yet' : '');
    saveBtn.disabled = paired || !dirty;
    updateNow();
    renderWeek();
  }

  /* ------------------------------------------------------------------ parts */

  const dirtyNote = h('span', { class: 'pp-pe__dirty' });
  const saveBtn = h('button', {
    type: 'button', class: 'pp-btn pp-btn--primary', text: 'Save schedule', disabled: true, onClick: save,
  });
  const rowsSlot = h('div');
  const weekSlot = h('div');
  const defaultSelect = h('select', {
    class: 'pp-select', onChange: () => { defaultPlaylist = defaultSelect.value; touch(); },
  });

  function render() {
    setText(lead, 'Rules are read from the top and the first match wins.');
    renderBanners();

    fill(defaultSelect, playlists.map((p) => h('option', { value: p.name, text: p.title })));
    if (!playlists.some((p) => p.name === defaultPlaylist) && defaultPlaylist) {
      // The configuration names a folder that is not on the stick any more.
      defaultSelect.append(h('option', { value: defaultPlaylist, text: `${defaultPlaylist} (missing)` }));
    }
    defaultSelect.value = defaultPlaylist;
    defaultSelect.disabled = paired;

    fill(body,
      card({
        title: 'When nothing matches',
        body: [
          h('label', { class: 'pp-label', style: { 'max-width': '320px' } }, 'Plays when no rule matches', defaultSelect),
          h('div', { class: 'pp-help', text: 'This is what the screen shows outside every rule, and while the clock is still coming in.' }),
          nowLine,
        ],
      }),
      h('div', { class: `pp-card${paired ? ' dv-ro' : ''}` },
        h('div', { class: 'pp-card__head' },
          h('div', { class: 'pp-h2', text: 'Rules' }),
          h('div', { class: 'pp-card__meta' }, dirtyNote)),
        rowsSlot,
        h('div', { class: 'pp-card__foot' },
          h('button', {
            type: 'button', class: 'pp-btn pp-btn--dashed', disabled: paired, onClick: addRule,
          }, icon('plus'), 'Add a rule'),
          saveBtn)),
      card({ title: 'What the week looks like', body: weekSlot }));

    renderRows();
    touch();
  }

  function renderBanners() {
    const status = ctx.store.status || {};
    fill(banners,
      paired ? banner({
        kind: 'paired',
        title: `Schedules come from ${status.server_url || 'the control server'}`,
        body: 'They are shown here so you can see what this screen is following. Unpair on the Settings page to take them back.',
      }) : null,
      (status.timezone || 'UTC') === 'UTC' ? banner({
        kind: 'warn',
        title: 'These rules are not running yet',
        body: 'No time zone is set, so the device cannot tell when 08:00 is. It plays the default playlist instead. Rules start the moment you set one.',
        actions: [h('a', { class: 'pp-btn pp-btn--warn-outline', href: '#/settings', text: 'Set time zone' })],
      }) : null,
      status.clock_synced === false ? banner({
        kind: 'warn',
        title: 'The clock is not in step yet',
        body: 'Rules are held until the device reaches a time server. The default playlist plays meanwhile, and no rule is lost.',
      }) : null);
  }

  /* ------------------------------------------------------------------ rules */

  function renderRows() {
    if (!rules.length) {
      fill(rowsSlot, h('div', { class: 'pp-empty' },
        h('div', { class: 'pp-empty__title', text: 'No rules yet' }),
        h('div', { class: 'pp-empty__body', text: 'Without a rule the default playlist plays all day, which is a fine way to run one screen. Add a rule when a different playlist belongs in a different hour.' })));
      return;
    }
    fill(rowsSlot, rules.map((rule, i) => ruleRow(rule, i)));
  }

  function ruleRow(rule, i) {
    const error = h('div', { class: 'pp-error', hidden: true, style: { flex: '1 1 100%' } });

    const playlistSelect = h('select', {
      class: 'pp-select', 'aria-label': `Playlist of rule ${i + 1}`, disabled: paired,
      onChange: () => { rule.playlist = playlistSelect.value; touch(); },
    }, playlists.map((p) => h('option', { value: p.name, text: p.title })));
    if (!playlists.some((p) => p.name === rule.playlist)) {
      playlistSelect.append(h('option', { value: rule.playlist, text: `${rule.playlist || 'pick one'} (missing)` }));
    }
    playlistSelect.value = rule.playlist;

    const chips = dayChips({
      days: rule.days, disabled: paired,
      onChange: (days) => { rule.days = days; touch(); },
    });

    const time = (key, label) => h('input', {
      class: 'pp-input pp-input--mono pp-input--sm', type: 'time', value: rule[key],
      'aria-label': `${label} of rule ${i + 1}`, disabled: paired,
      onInput: (e) => { rule[key] = e.target.value; touch(); },
    });

    const move = (to) => {
      if (to < 0 || to >= rules.length) return;
      const [it] = rules.splice(i, 1);
      rules.splice(to, 0, it);
      touch();
      renderRows();
    };

    const row = h('div', { class: 'dv-rule' },
      h('div', { class: 'dv-rule__n', text: `${i + 1}` }),
      h('label', { class: 'pp-label dv-rule__pl' }, 'Play this', playlistSelect),
      h('div', { class: 'dv-rule__when' }, h('div', { class: 'pp-label', text: 'On these days' }),
        h('div', { style: { 'margin-top': '5px' } }, chips)),
      h('div', null, h('div', { class: 'pp-label', text: 'Between' }),
        h('div', { class: 'dv-rule__times', style: { 'margin-top': '5px' } }, time('start', 'Start'), time('end', 'End'))),
      h('div', { class: 'dv-rule__acts' },
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Move rule ${i + 1} up`,
          disabled: paired || i === 0, onClick: () => move(i - 1),
        }, icon('up', 14)),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Move rule ${i + 1} down`,
          disabled: paired || i === rules.length - 1, onClick: () => move(i + 1),
        }, icon('down', 14)),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Remove rule ${i + 1}`, disabled: paired,
          onClick: () => { rules.splice(i, 1); touch(); renderRows(); },
        }, icon('close', 14))));

    row.dataset.rule = String(i);
    row.append(error);
    row.errorSlot = error;
    return row;
  }

  function addRule() {
    rules.push({
      playlist: (playlists[0] && playlists[0].name) || defaultPlaylist,
      days: ['mon', 'tue', 'wed', 'thu', 'fri'],
      start: '08:00', end: '18:00',
    });
    touch();
    renderRows();
  }

  /* --------------------------------------------------------- what plays now */

  function updateNow() {
    const status = ctx.store.status || {};
    const zone = status.timezone || 'UTC';
    const { weekday, minute } = deviceClock(zone);
    const hit = rules.findIndex((r) => inWindow(r.start, r.end, minute, weekday, r.days));
    const fallback = titleOf(defaultPlaylist) || 'nothing';

    if (status.clock_synced === false) {
      setText(nowLine, `The clock is not in step, so every rule is held and ${fallback} plays.`);
      return;
    }
    if (hit < 0) {
      setText(nowLine, `Right now no rule matches, so ${fallback} plays.`);
      return;
    }
    const r = rules[hit];
    setText(nowLine, `Right now rule ${hit + 1} matches: ${titleOf(r.playlist)}, ${daysInWords(r.days)} from ${r.start} to ${r.end}.`);
  }

  /* Seven rows of twenty-four cells. A cell is green when a rule covers the
     middle of that hour, which is the reading a person wants from a picture. */
  function renderWeek() {
    const labels = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
    const grid = h('div', { class: 'dv-week' });
    for (let d = 0; d < 7; d++) {
      const hours = h('div', { class: 'dv-week__hours' });
      for (let hour = 0; hour < 24; hour++) {
        const minute = hour * 60 + 30;
        const hit = rules.findIndex((r) => inWindow(r.start, r.end, minute, d, r.days));
        hours.append(h('span', {
          class: `dv-week__cell${hit >= 0 ? ' dv-week__cell--on' : ''}`,
          title: hit >= 0
            ? `${labels[d]} ${String(hour).padStart(2, '0')}:00 — rule ${hit + 1}, ${titleOf(rules[hit].playlist)}`
            : `${labels[d]} ${String(hour).padStart(2, '0')}:00 — ${titleOf(defaultPlaylist) || 'nothing'}`,
        }));
      }
      grid.append(h('div', { class: 'dv-week__day', text: labels[d] }), hours);
    }

    const axis = h('div', { class: 'dv-week__axis' });
    for (let hour = 0; hour < 24; hour++) axis.append(h('span', { text: String(hour).padStart(2, '0') }));

    fill(weekSlot, grid,
      h('div', { class: 'dv-week', style: { 'margin-top': '2px' } }, h('span'), axis),
      h('div', { class: 'pp-row', style: { 'margin-top': '10px' } },
        h('span', { class: 'pp-status', style: { gap: '6px' } }, h('span', { class: 'pp-swatch pp-swatch--ok' }), h('span', { class: 'pp-small pp-muted', text: 'A rule' })),
        h('span', { class: 'pp-status', style: { gap: '6px' } }, h('span', { class: 'pp-swatch' }), h('span', { class: 'pp-small pp-muted', text: titleOf(defaultPlaylist) || 'default' }))));
  }

  /* ------------------------------------------------------------------- save */

  function clearErrors() {
    for (const row of rowsSlot.querySelectorAll('.dv-rule')) {
      if (row.errorSlot) { row.errorSlot.hidden = true; row.errorSlot.textContent = ''; }
      row.classList.remove('pp-field--invalid');
    }
  }

  function showErrors(fields) {
    clearErrors();
    const rows = [...rowsSlot.querySelectorAll('.dv-rule')];
    const rest = [];
    for (const f of fields) {
      const m = /^schedule\[(\d+)\]/.exec(f.field);
      if (!m) { rest.push(`${f.field}: ${f.message}`); continue; }
      const row = rows[Number(m[1])];
      if (!row || !row.errorSlot) { rest.push(`${f.field}: ${f.message}`); continue; }
      row.errorSlot.textContent = `${f.field.split('.').pop()}: ${f.message}`;
      row.errorSlot.hidden = false;
      row.classList.add('pp-field--invalid');
    }
    if (rest.length) toast(rest.join('; '), 'danger');
  }

  async function save() {
    const bad = rules.findIndex((r) => !r.playlist || !r.start || !r.end);
    if (bad >= 0) {
      showErrors([{ field: `schedule[${bad}].start`, message: 'a rule needs a playlist, a start time and an end time' }]);
      return;
    }
    saveBtn.disabled = true;
    const next = { ...cfg, schedule: rules, playback: { ...cfg.playback, default_playlist: defaultPlaylist } };
    try {
      const applied = await api('PUT', '/api/config', next);
      cfg = next;
      baseline = snapshot();
      clearErrors();
      ctx.clearGuard();
      toast(applied.applied === 'live'
        ? 'Schedule saved. The screen follows it from now.'
        : 'Schedule saved.');
      ctx.store.refresh();
    } catch (err) {
      if (err.fields) showErrors(err.fields);
      else toast(errorText(err), 'danger');
    } finally {
      touch();
    }
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
    },
  };
}
