/* PortaPixel playlist editor.
   One editor, zero drift: the device admin UI and the control server admin UI
   both mount this module. The device uploads files into the playlist; the
   server picks them from its media library. The `mediaSource` option is the
   only difference.

   The playlist it edits:
     {name, title, transition, shuffle,
      items: [{file|sha256|url, name, kind, duration, mute, max_duration,
               refresh_seconds, thumb}]}
*/

import { h, fill, icon, toast, modal, confirmDialog, fmtDuration, fmtBytes, banner, progress } from './ui.js';
import { warningsFor } from './item-warnings.js';

const TRANSITIONS = [
  ['crossfade', 'Crossfade'],
  ['cut', 'Hard cut'],
  ['push-left', 'Push left'],
  ['push-right', 'Push right'],
  ['push-up', 'Push up'],
  ['push-down', 'Push down'],
];

const KIND_LABEL = { image: 'Image', video: 'Video', url: 'Web page' };

/* The item keys that the editor itself reads and writes. snapshot() puts them
   first, in this order, and keeps every other key that the host page added. */
const ITEM_KEYS = ['file', 'sha256', 'url', 'name', 'kind', 'duration', 'mute',
  'max_duration', 'refresh_seconds', 'thumb'];

/** Mount the editor into `el`.
    playlist:     the playlist object. It is copied, never edited in place.
    mediaSource:  {list(), thumbUrl(item), upload(file, onProgress)}
                  list() gives the library items the user can add.
                  upload() is the device path: it puts a file in the playlist.
    readOnly:     true shows the managed-by banner and disables everything.
    capabilities: {commentLossWarning, configFile, managedBy, folder, tier,
                   decode, warnPrefix, warnAction, impact, saveLabel}
    onSave(playlist):          must return a promise.
    onUpload(file, onProgress): overrides mediaSource.upload.
    onDirty(dirty):            called whenever the dirty state changes.
    Returns {destroy, getPlaylist, isDirty, setPlaylist, markClean}. */
export function mountPlaylistEditor(el, opts = {}) {
  const caps = opts.capabilities || {};
  const src = opts.mediaSource || {};
  const ro = !!opts.readOnly;
  const uploader = opts.onUpload || src.upload || null;

  let pl = adopt(opts.playlist);
  let clean = snapshot(pl);
  let dirty = false;

  /* ---- fixed parts ---- */

  const nameInput = h('input', {
    class: 'pp-input pp-pe__name', type: 'text', value: pl.name || '',
    'aria-label': 'Playlist name', disabled: ro,
    onInput: () => { pl.name = nameInput.value; touch(); },
  });

  /* The first option is the third state: the playlist says nothing and the
     device setting decides. A save must be able to keep that state. */
  const transSelect = h('select', {
    class: 'pp-select', 'aria-label': 'Transition between items', disabled: ro,
    onChange: () => { pl.transition = transSelect.value; touch(); },
  }, [h('option', { value: '', text: 'Device setting' }),
    TRANSITIONS.map(([v, label]) => h('option', { value: v, text: label }))]);
  transSelect.value = pl.transition || '';

  /* A half-checked box is the same third state for shuffle. A click on it makes
     the value explicit; there is no way back to the device setting from here. */
  const shuffleBox = h('input', {
    type: 'checkbox', checked: pl.shuffle === true, indeterminate: pl.shuffle === null, disabled: ro,
    onChange: () => { pl.shuffle = shuffleBox.checked; touch(); },
  });

  const head = h('div', { class: 'pp-pe__head' },
    nameInput,
    h('div', { class: 'pp-pe__opt' }, h('span', { text: 'Between items' }), transSelect),
    h('label', { class: 'pp-check' }, shuffleBox, h('span', { text: 'Shuffle' })));

  const noteSlot = h('div');
  const itemsSlot = h('div');
  const summary = h('div', { class: 'pp-pe__summary' });

  const discardBtn = h('button', {
    type: 'button', class: 'pp-btn', text: 'Discard', disabled: ro,
    onClick: onDiscard,
  });
  const saveBtn = h('button', {
    type: 'button', class: 'pp-btn pp-btn--primary',
    text: caps.saveLabel || 'Save playlist', disabled: ro,
    onClick: onSaveClick,
  });

  const addBtns = h('div', { class: 'pp-btns' });
  if (!ro) {
    if (uploader) addBtns.append(h('button', { type: 'button', class: 'pp-btn', onClick: pickFiles },
      icon('plus'), 'Upload files'));
    if (src.list) addBtns.append(h('button', { type: 'button', class: 'pp-btn', onClick: pickFromLibrary },
      icon('plus'), 'Add from library'));
    addBtns.append(h('button', { type: 'button', class: 'pp-btn', onClick: addUrlItem },
      icon('plus'), 'Add a web page'));
  }

  const fileInput = h('input', {
    type: 'file', multiple: true, class: 'pp-sr-only',
    onChange: () => { uploadFiles([...fileInput.files]); fileInput.value = ''; },
  });

  const foot = h('div', { class: 'pp-pe__foot' },
    h('div', { class: 'pp-pe__foot-left' }, ro ? null : addBtns, summary),
    ro ? null : h('div', { class: 'pp-btns' }, discardBtn, saveBtn));

  const card = h('div', { class: `pp-card pp-pe${ro ? ' pp-pe--ro' : ''}` },
    head,
    caps.impact ? h('div', { class: 'pp-pe__strip' },
      h('span', null, caps.impact.text), caps.impact.note ? h('span', { text: caps.impact.note }) : null) : null,
    noteSlot, itemsSlot, foot);

  fill(el,
    ro && caps.managedBy ? banner({
      kind: 'paired',
      title: `Managed by ${caps.managedBy}`,
      body: "Playlists and schedules come from the server, so they're read-only here. Display, sound and network settings are still yours. Unpairing hands control back to this page.",
    }) : null,
    card, fileInput);

  render();

  /* ---- render ---- */

  function render() {
    // Kiosk hint: exactly one URL item and nothing else.
    const kiosk = pl.items.length === 1 && pl.items[0].kind === 'url';
    fill(noteSlot, kiosk ? h('div', { class: 'pp-note' },
      'One web page and nothing else, so this screen just stays on it — no looping, no gaps. It reloads on the interval below.') : null);

    if (pl.items.length === 0) {
      fill(itemsSlot, emptyState());
    } else {
      fill(itemsSlot,
        h('div', { class: 'pp-pe__cols', 'aria-hidden': 'true' },
          h('span', { class: 'pp-cell', style: { flex: 'none', width: '20px' } }),
          h('span', { class: 'pp-cell pp-cell--grow', text: 'Item' }),
          h('span', { class: 'pp-cell', style: { flex: 'none', width: '112px' }, text: 'Time on screen' }),
          h('span', { class: 'pp-cell', style: { flex: 'none', width: '62px' }, text: 'Sound' }),
          h('span', { class: 'pp-cell', style: { flex: 'none', width: '62px' } })),
        pl.items.map((item, i) => itemRow(item, i)));
    }
    updateSummary();
  }

  function emptyState() {
    return h('div', { class: 'pp-empty' },
      h('div', { class: 'pp-empty__title', text: 'Nothing in this playlist yet' }),
      h('div', { class: 'pp-empty__body' },
        caps.folder
          ? ['Drag images or videos here, or pull the stick and copy them into the ',
            h('span', { class: 'pp-mono', text: caps.folder }), ' folder from any computer. Either way this screen keeps playing what it has.']
          : 'Add media from the library or a web page. Screens keep playing what they have until this playlist is saved.'),
      ro ? null : h('div', { class: 'pp-empty__actions' },
        uploader ? h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Upload files', onClick: pickFiles }) : null,
        src.list ? h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Add from library', onClick: pickFromLibrary }) : null,
        h('button', { type: 'button', class: 'pp-btn', text: 'Add a web page', onClick: addUrlItem })));
  }

  function itemRow(item, i) {
    const kind = item.kind || 'image';
    const sub = h('div');

    const handle = h('span', {
      class: 'pp-drag', role: 'presentation', draggable: !ro, title: 'Drag to reorder',
    }, icon('drag'));

    const thumb = h('span', { class: 'pp-thumb pp-thumb--sm' });
    const url = item.thumb || (src.thumbUrl ? src.thumbUrl(item) : null);
    if (url) thumb.append(h('img', { src: url, alt: '' }));

    const row = h('div', { class: 'pp-pe__row' },
      handle,
      h('div', { class: 'pp-pe__main' },
        thumb,
        h('div', { class: 'pp-pe__names' },
          h('div', { class: 'pp-pe__title', title: label(item), text: label(item) }),
          h('div', { class: 'pp-pe__kind' }, icon(kind === 'url' ? 'url' : kind, 11), KIND_LABEL[kind] || kind))),
      h('div', { class: 'pp-pe__ctl' },
        h('div', { class: 'pp-pe__dur' }, durationField(item, sub)),
        kind === 'video'
          ? h('label', { class: 'pp-check pp-pe__sound' },
            h('input', {
              type: 'checkbox', checked: !item.mute, disabled: ro,
              'aria-label': `Sound for ${label(item)}`,
              onChange: (e) => { item.mute = !e.target.checked; touch(); },
            }), h('span', { text: 'Sound' }))
          : h('span', { class: 'pp-pe__sound', 'aria-hidden': 'true' }),
        h('div', { class: 'pp-pe__move' },
          h('button', {
            type: 'button', class: 'pp-btn pp-btn--icon', disabled: ro || i === 0,
            'aria-label': `Move ${label(item)} up`, onClick: () => move(i, i - 1),
          }, icon('up', 13)),
          h('button', {
            type: 'button', class: 'pp-btn pp-btn--icon', disabled: ro || i === pl.items.length - 1,
            'aria-label': `Move ${label(item)} down`, onClick: () => move(i, i + 1),
          }, icon('down', 13))),
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--icon', disabled: ro,
          'aria-label': `Remove ${label(item)}`, onClick: () => remove(i),
        }, icon('close', 14))));

    const wrap = h('div', { class: 'pp-pe__item' }, row, sub);
    renderSub(item, sub);
    if (!ro) wireDrag(wrap, handle, i);
    return wrap;
  }

  /* Per-item fields. Images and URL items hold a dwell time; a video plays
     its natural length and takes an optional cap instead. */
  function durationField(item, sub) {
    if (item.kind === 'video') {
      return h('span', { class: 'pp-mono pp-muted', style: { 'font-size': '12.5px' }, text: 'full length' });
    }
    const input = h('input', {
      class: 'pp-input pp-input--sm pp-input--mono', type: 'number', min: '1', step: '1',
      value: item.duration ?? '', disabled: ro,
      placeholder: item.kind === 'url' ? 'stays on' : '10',
      'aria-label': `Seconds on screen for ${label(item)}`,
      onInput: () => {
        const v = parseInt(input.value, 10);
        item.duration = Number.isFinite(v) && v > 0 ? v : null;
        renderSub(item, sub);
        touch();
      },
    });
    return input;
  }

  /* Sub-rows under one item: warnings, notes and the extra fields. */
  function renderSub(item, sub) {
    const mixed = pl.items.length > 1;
    const warns = warningsFor(item, caps.decode, caps.tier, { mixed });
    const parts = [];

    for (const w of warns) {
      parts.push(h('div', { class: 'pp-pe__sub' },
        h('div', { class: 'pp-pe__warn' },
          h('span', null, h('b', { text: `${caps.warnPrefix || 'Might not play smoothly here.'} ` }), w),
          caps.warnAction ? h('button', {
            type: 'button', class: 'pp-btn pp-btn--sm pp-btn--warn-outline', text: caps.warnAction.label,
            onClick: () => caps.warnAction.onClick(item),
          }) : null)));
    }

    // A read-only view states the reload interval in words. An editable one
    // shows the field instead, so the same fact is never said twice.
    if (ro && item.kind === 'url' && Number(item.refresh_seconds)) {
      parts.push(h('div', { class: 'pp-pe__sub pp-pe__note' },
        `Reloads every ${fmtDuration(item.refresh_seconds)} so the numbers stay current.`));
    }
    if (!ro && item.kind === 'video') {
      parts.push(h('div', { class: 'pp-pe__sub pp-pe__extra' },
        h('label', { class: 'pp-pe__extra-f' }, 'Stop after',
          numberInput(item, 'max_duration', 'no limit', `Cap in seconds for ${label(item)}`), 's')));
    }
    if (!ro && item.kind === 'url') {
      parts.push(h('div', { class: 'pp-pe__sub pp-pe__extra' },
        h('label', { class: 'pp-pe__extra-f' }, 'Reload every',
          numberInput(item, 'refresh_seconds', 'never', `Reload interval in seconds for ${label(item)}`), 's')));
    }

    fill(sub, parts);
  }

  function numberInput(item, key, placeholder, aria) {
    const input = h('input', {
      class: 'pp-input pp-input--sm pp-input--mono', type: 'number', min: '1', step: '1',
      value: item[key] ?? '', placeholder, 'aria-label': aria, disabled: ro,
      onInput: () => {
        const v = parseInt(input.value, 10);
        item[key] = Number.isFinite(v) && v > 0 ? v : null;
        touch();
      },
    });
    return input;
  }

  function updateSummary() {
    const n = pl.items.length;
    const bits = [`${n} ${n === 1 ? 'item' : 'items'}`];
    const total = passTime();
    if (total.seconds > 0) bits.push(`one pass takes ${total.partial ? 'at least ' : ''}${fmtDuration(total.seconds)}`);
    if (caps.folder) bits.push(`folder ${caps.folder}`);
    fill(summary, bits.join(' · '), dirty ? h('span', { class: 'pp-pe__dirty', text: '  ·  not saved yet' }) : null);
  }

  function passTime() {
    let seconds = 0, partial = false;
    for (const it of pl.items) {
      const d = Number(it.duration) || Number(it.max_duration) || Number(it.length) || 0;
      if (d) seconds += d; else partial = true;
    }
    return { seconds, partial };
  }

  /* ---- edits ---- */

  function move(from, to) {
    if (to < 0 || to >= pl.items.length || from === to) return;
    const [it] = pl.items.splice(from, 1);
    pl.items.splice(to, 0, it);
    touch();
    render();
    // Keep the keyboard on the item that moved. At the first row and at the last
    // row the button that made the move is now disabled, so take the other one
    // of the pair; without this the focus falls to the body.
    const rows = itemsSlot.querySelectorAll('.pp-pe__item');
    const moves = rows[to] ? rows[to].querySelectorAll('.pp-pe__move .pp-btn--icon') : [];
    const wanted = to > from ? 1 : 0;
    const btn = moves[wanted] && !moves[wanted].disabled ? moves[wanted] : moves[wanted ? 0 : 1];
    if (btn && !btn.disabled) btn.focus();
  }

  function remove(i) {
    pl.items.splice(i, 1);
    touch();
    render();
  }

  function addItems(items) {
    if (!items || items.length === 0) return;
    pl.items.push(...items.map(normalizeItem));
    touch();
    render();
  }

  /* ---- drag and drop ---- */
  /* The handle carries the drag so a pointer in a text field still selects
     text. The up and down buttons cover touch and the keyboard. */

  let dragFrom = -1;

  function wireDrag(wrap, handle, i) {
    handle.addEventListener('dragstart', (e) => {
      dragFrom = i;
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', String(i));
      if (e.dataTransfer.setDragImage) e.dataTransfer.setDragImage(wrap, 12, 12);
      wrap.classList.add('pp-draggable--dragging');
    });
    handle.addEventListener('dragend', () => {
      dragFrom = -1;
      for (const n of itemsSlot.querySelectorAll('.pp-pe__item')) {
        n.classList.remove('pp-draggable--dragging', 'pp-draggable--over', 'pp-draggable--over-after');
      }
    });
    wrap.addEventListener('dragover', (e) => {
      if (dragFrom < 0) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      const after = isAfter(e, wrap);
      wrap.classList.toggle('pp-draggable--over', !after);
      wrap.classList.toggle('pp-draggable--over-after', after);
    });
    wrap.addEventListener('dragleave', () => {
      wrap.classList.remove('pp-draggable--over', 'pp-draggable--over-after');
    });
    wrap.addEventListener('drop', (e) => {
      if (dragFrom < 0) return;
      e.preventDefault();
      let to = isAfter(e, wrap) ? i + 1 : i;
      if (dragFrom < to) to -= 1;
      const from = dragFrom;
      dragFrom = -1;
      if (from !== to) { const [it] = pl.items.splice(from, 1); pl.items.splice(to, 0, it); touch(); }
      render();
    });
  }

  function isAfter(e, node) {
    const r = node.getBoundingClientRect();
    return e.clientY > r.top + r.height / 2;
  }

  /* ---- adding content ---- */

  function pickFiles() { fileInput.click(); }

  async function uploadFiles(files) {
    if (!uploader || files.length === 0) return;
    for (const file of files) {
      // progress() starts the bar at zero and carries the aria values. A bar
      // built by hand here has no width, which the stylesheet shows as full.
      const bar = progress(0);
      bar.style.setProperty('margin-top', '6px');
      const line = h('div', { class: 'pp-pe__sub' },
        h('div', { class: 'pp-pe__note', text: `Uploading ${file.name} — ${fmtBytes(file.size)}` }),
        bar);
      itemsSlot.append(line);
      try {
        const added = await uploader(file, (frac) => bar.set(frac));
        line.remove();
        // An uploader that gives nothing back has put the file in the playlist
        // folder under its own name.
        addItems(added ? (Array.isArray(added) ? added : [added]) : [{ file: file.name, name: file.name, size: file.size }]);
      } catch (err) {
        line.remove();
        toast(err.message || `${file.name} did not upload`, 'danger');
      }
    }
  }

  async function pickFromLibrary() {
    let library;
    try {
      library = await src.list();
    } catch (err) {
      toast(err.message || 'The library did not answer', 'danger');
      return;
    }
    const chosen = new Set();
    const grid = h('div', { class: 'pp-grid' }, (library || []).map((m) => {
      const tile = h('button', {
        type: 'button', class: 'pp-tile', 'aria-pressed': 'false',
        onClick: () => {
          const on = chosen.has(m);
          if (on) chosen.delete(m); else chosen.add(m);
          tile.setAttribute('aria-pressed', String(!on));
        },
      },
        h('span', { class: 'pp-thumb pp-thumb--wide' },
          src.thumbUrl && src.thumbUrl(m) ? h('img', { src: src.thumbUrl(m), alt: '' }) : null,
          h('span', { class: 'pp-thumb__label' }, h('span', { class: 'pp-kind pp-kind--chip', text: KIND_LABEL[m.kind] || m.kind || '' }))),
        h('span', { class: 'pp-tile__body' },
          h('span', { class: 'pp-tile__name', text: m.name || m.sha256 || '' }),
          h('span', { class: 'pp-tile__meta', text: m.size ? fmtBytes(m.size) : '' })));
      return tile;
    }));

    const ok = await modal({
      title: 'Add from the library',
      body: (library || []).length
        ? h('div', null, h('div', { class: 'pp-help', style: { 'margin-bottom': '12px' } }, 'Pick the files to add to the end of this playlist.'), grid)
        : h('div', { class: 'pp-help' }, 'The library is empty. Upload media on the Media page first.'),
      actions: [{ label: 'Cancel', value: false }, { label: 'Add', value: true, kind: 'primary' }],
      wide: true,
    });
    if (ok) addItems([...chosen]);
  }

  async function addUrlItem() {
    const urlIn = h('input', { class: 'pp-input pp-input--mono', type: 'url', placeholder: 'https://dashboards.example.com/lobby', autofocus: true });
    const dwellIn = h('input', { class: 'pp-input pp-input--mono', type: 'number', min: '1', step: '1', placeholder: '60' });
    const refreshIn = h('input', { class: 'pp-input pp-input--mono', type: 'number', min: '1', step: '1', placeholder: '300' });

    const ok = await modal({
      title: 'Add a web page',
      body: h('div', null,
        h('label', { class: 'pp-label' }, 'Address', urlIn),
        h('div', { class: 'pp-fields', style: { 'margin-top': '14px' } },
          h('label', { class: 'pp-label pp-field' }, 'Time on screen (seconds)', dwellIn,
            h('span', { class: 'pp-help' }, 'Leave it empty in a playlist of just this page.')),
          h('label', { class: 'pp-label pp-field' }, 'Reload every (seconds)', refreshIn,
            h('span', { class: 'pp-help' }, 'Keeps a dashboard current.')))),
      actions: [{ label: 'Cancel', value: false }, { label: 'Add the page', value: true, kind: 'primary' }],
    });
    if (!ok) return;
    const addr = urlIn.value.trim();
    if (!addr) { toast('That needs an address', 'danger'); return; }
    // The same rule as internal/playlist. Without it one typed address makes
    // every later save of this playlist fail.
    if (!/^https?:\/\//.test(addr)) {
      toast('The address must start with http:// or https://', 'danger');
      return;
    }
    addItems([{
      url: addr, kind: 'url', name: addr.replace(/^https?:\/\//, ''),
      duration: parseInt(dwellIn.value, 10) || null,
      refresh_seconds: parseInt(refreshIn.value, 10) || null,
    }]);
  }

  /* ---- save and discard ---- */

  async function onDiscard() {
    if (dirty && !(await confirmDialog({
      title: 'Drop these changes?',
      body: 'The playlist goes back to the version that is saved. Nothing else changes.',
      confirm: 'Drop them',
      kind: 'danger',
    }))) return;
    pl = JSON.parse(clean);
    syncHead();
    dirty = false;
    if (opts.onDirty) opts.onDirty(false);
    render();
  }

  async function onSaveClick() {
    if (caps.commentLossWarning) {
      const go = await modal({
        title: 'Save this playlist?',
        body: h('div', null,
          'Saving rewrites ',
          h('span', { class: 'pp-mono', text: caps.configFile || 'playlist.toml' }),
          ' on the stick. Your items, times and settings are kept exactly as shown. Comments you typed into that file by hand will be gone.'),
        actions: [
          { label: 'Cancel', value: false },
          { label: 'Save and rewrite', value: true, kind: 'primary', autofocus: true },
        ],
      });
      if (!go) return;
    }
    saveBtn.disabled = true;
    // Take the copy that goes out before the wait. Every other control stays
    // live while the save is in flight, and an edit that the server never got
    // must still count as not saved.
    const sent = snapshot(pl);
    try {
      await (opts.onSave ? opts.onSave(JSON.parse(sent)) : Promise.resolve());
      clean = sent;
      touch();
    } catch (err) {
      toast(err.message || 'That did not save', 'danger');
    } finally {
      saveBtn.disabled = ro;
    }
  }

  /* ---- plumbing ---- */

  function touch() {
    const now = dirty;
    dirty = snapshot(pl) !== clean;
    updateSummary();
    if (now !== dirty && opts.onDirty) opts.onDirty(dirty);
  }

  function markClean() {
    clean = snapshot(pl);
    dirty = false;
    updateSummary();
    if (opts.onDirty) opts.onDirty(false);
  }

  function syncHead() {
    nameInput.value = pl.name || '';
    transSelect.value = pl.transition || '';
    shuffleBox.checked = pl.shuffle === true;
    shuffleBox.indeterminate = pl.shuffle === null || pl.shuffle === undefined;
  }

  function getPlaylist() { return JSON.parse(snapshot(pl)); }

  return {
    getPlaylist,
    isDirty: () => dirty,
    markClean,
    setPlaylist(next) {
      pl = adopt(next);
      clean = snapshot(pl);
      dirty = false;
      syncHead();
      render();
      if (opts.onDirty) opts.onDirty(false);
    },
    destroy() { el.replaceChildren(); },
  };
}

/* --------------------------------------------------------------- helpers */

function adopt(p) {
  const s = p ? JSON.parse(JSON.stringify(p)) : {};
  return {
    name: s.name || '',
    title: s.title || '',
    // An absent transition and an absent shuffle mean "use the device setting".
    // The playlist file keeps that third state, so the editor keeps it too:
    // opening a playlist and saving it must not pin the device values into it.
    transition: s.transition || '',
    shuffle: s.shuffle === null || s.shuffle === undefined ? null : !!s.shuffle,
    items: (s.items || []).map(normalizeItem),
  };
}

function normalizeItem(raw) {
  const it = { ...raw };
  if (!it.kind) it.kind = it.url ? 'url' : guessKind(it.file || it.name || '');
  if (!it.name) it.name = it.url || it.file || it.sha256 || '';
  if (it.kind !== 'video') delete it.mute;
  else it.mute = !!it.mute;
  return it;
}

/* Keep this list the same as videoExt in internal/playlist/kind.go. A file that
   Go calls a video and this function calls an image loses its mute flag at the
   next save. */
function guessKind(name) {
  return /\.(mp4|m4v|mov|webm|mkv|ogv)$/i.test(name) ? 'video' : 'image';
}

function label(item) { return item.name || item.file || item.url || 'item'; }

/* One canonical string per playlist state, used for dirty tracking and for
   handing a copy to onSave. Key order is fixed so it compares reliably.
   An undefined value never reaches the string, so a transition or a shuffle
   that the playlist does not set stays unset and the device value stays live. */
function snapshot(pl) {
  return JSON.stringify({
    name: pl.name, title: pl.title,
    transition: pl.transition || undefined,
    shuffle: pl.shuffle === null || pl.shuffle === undefined ? undefined : pl.shuffle,
    items: pl.items.map(snapshotItem),
  });
}

/* One item in canonical form: the keys that the editor owns, in a fixed order,
   and then every other key in name order. A host page adds fields of its own,
   for example codec, width, height, size and length. The warnings and the pass
   time read them, so a save and a discard must both keep them. */
function snapshotItem(item) {
  const out = {};
  for (const k of ITEM_KEYS) out[k] = item[k] ?? null;
  for (const k of Object.keys(item).sort()) {
    if (!ITEM_KEYS.includes(k)) out[k] = item[k];
  }
  return out;
}
