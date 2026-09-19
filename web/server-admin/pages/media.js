/* Media: the library that every playlist draws from.

   One file is one object, named by the hash of its bytes, so the same file
   uploaded twice keeps one copy. Nothing is converted here: what goes up is what
   plays.
*/

import {
  h, fill, toast, banner, modal, confirmDialog, progress, fmtBytes, icon,
} from '/shared/ui.js';
import { api, upload } from '/shared/api.js';
import {
  card, pageHead, errorText, thumbURL, guessKind, fmtDate, preview,
} from '../util.js';

export function mount(main, ctx) {
  let media = [];
  let totals = { files: 0, bytes: 0, max_bytes: 0, free_bytes: 0 };
  let uploading = false;
  let gone = false;

  const infoSlot = h('div', { style: { 'margin-bottom': '14px' } });
  const gridSlot = h('div');
  const upSlot = h('div', { class: 'sv-up' });

  const fileInput = h('input', {
    type: 'file', multiple: true, class: 'pp-sr-only',
    onChange: () => { const files = [...fileInput.files]; fileInput.value = ''; send(files); },
  });

  const drop = h('div', { class: 'sv-drop' },
    h('div', { style: { 'font-weight': '600' } }, 'Drop files here'),
    h('div', { class: 'pp-help' }, 'Images and video, as many at a time as you like. They upload one after the other.'),
    h('div', { class: 'pp-btns', style: { 'justify-content': 'center', 'margin-top': '12px' } },
      h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Choose files', onClick: () => fileInput.click() })),
    upSlot, fileInput);

  drop.addEventListener('dragover', (e) => {
    e.preventDefault();
    drop.classList.add('sv-drop--over');
  });
  drop.addEventListener('dragleave', () => drop.classList.remove('sv-drop--over'));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    drop.classList.remove('sv-drop--over');
    const files = [...(e.dataTransfer.files || [])];
    if (files.length) send(files);
  });

  fill(main,
    pageHead('Media', 'Images and video that the playlists draw from. The same file uploaded twice keeps one copy.',
      h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Upload', onClick: () => fileInput.click() })),
    infoSlot, drop, gridSlot);

  load();

  /* ------------------------------------------------------------------ load */

  async function load() {
    try {
      const out = await api('GET', '/api/admin/media');
      if (gone) return;
      media = out.media || [];
      totals = { files: out.files, bytes: out.bytes, max_bytes: out.max_bytes, free_bytes: out.free_bytes };
      paintInfo();
      paintGrid();
    } catch (err) {
      fill(gridSlot, banner({ kind: 'danger', title: 'The library did not load', body: errorText(err) }));
    }
  }

  function paintInfo() {
    const free = Number(totals.free_bytes) || 0;
    const used = Number(totals.bytes) || 0;
    // The bar shows how much of the space that this library could still take is
    // already taken. There is no quota, so the pair is "what is here" and "what
    // is left on the disk".
    const bar = progress(free + used > 0 ? used / (free + used) : 0, { brand: true });

    fill(infoSlot, card({
      body: [
        h('div', { class: 'pp-row', style: { 'justify-content': 'space-between' } },
          h('span', { class: 'pp-small' },
            h('b', { text: `${totals.files} ${totals.files === 1 ? 'file' : 'files'}` }),
            ' · ', fmtBytes(used), ' in the library'),
          h('span', { class: 'pp-small pp-muted', text: `${fmtBytes(free)} free on the disk` })),
        h('div', { style: { 'margin-top': '10px' } }, bar),
        h('div', { class: 'pp-help' },
          Number(totals.max_bytes) > 0
            ? `Up to ${fmtBytes(totals.max_bytes)} per file. `
            : 'There is no limit for one file: only the free space decides. ',
          'The store keeps 512 MB of the disk back, so a full disk never stops the fleet. ',
          h('a', { href: '#/health', text: 'Server health' }), ' has the rest of the numbers.'),
      ],
    }));
  }

  /* ---------------------------------------------------------------- upload */

  /* One file at a time. A browser that sent ten large files at once would fill
     its own send buffer and report progress that means nothing. */
  async function send(files) {
    if (uploading) { toast('An upload is already running.', 'danger'); return; }
    uploading = true;
    let duplicates = 0;
    let done = 0;
    try {
      for (const file of files) {
        const bar = progress(0, { brand: true });
        const pct = h('span', { class: 'pp-mono pp-muted', text: '0%' });
        const row = h('div', { class: 'sv-up__row' },
          h('div', { class: 'sv-up__name' },
            h('span', { class: 'pp-trunc', title: file.name, text: file.name }),
            h('span', null, pct, ' · ', fmtBytes(file.size))),
          bar);
        upSlot.append(row);
        try {
          const out = await upload('/api/admin/media', file, (frac) => {
            bar.set(frac);
            pct.textContent = `${Math.round(frac * 100)}%`;
          });
          if (out.duplicate) duplicates++;
          done++;
        } catch (err) {
          row.remove();
          toast(uploadError(err, file), 'danger');
          continue;
        }
        row.remove();
      }
    } finally {
      uploading = false;
    }
    if (done) {
      toast(duplicates
        ? `${done} uploaded. ${duplicates} ${duplicates === 1 ? 'was' : 'were'} already in the library, so the library kept one copy.`
        : `${done} ${done === 1 ? 'file' : 'files'} uploaded.`);
    }
    await load();
  }

  function uploadError(err, file) {
    if (err.status === 413) return `${file.name} is longer than this server takes.`;
    if (err.status === 507) return `${file.name} does not fit: the disk is nearly full.`;
    return `${file.name}: ${errorText(err)}`;
  }

  /* ------------------------------------------------------------ the grid */

  function paintGrid() {
    if (media.length === 0) {
      fill(gridSlot, h('div', { class: 'pp-empty' },
        h('div', { class: 'pp-empty__title', text: 'The library is empty' }),
        h('div', { class: 'pp-empty__body', text: 'Upload the images and the video that the playlists will use. Nothing is converted, so upload the file that you want on the screen.' })));
      return;
    }
    fill(gridSlot, h('div', { class: 'pp-grid' }, media.map(tile)));
  }

  function tile(m) {
    const kind = guessKind(m.orig_name);
    const used = m.playlists && m.playlists.length
      ? (m.playlists.length === 1 ? `In ${m.playlists[0]}` : `In ${m.playlists.length} playlists`)
      : 'Not used yet';
    const dims = m.width && m.height ? `${m.width}×${m.height}` : (kind === 'video' ? 'video' : '');

    return h('div', { class: 'pp-tile' },
      h('span', { style: { display: 'block', position: 'relative' } },
        preview(kind, m.has_thumb ? thumbURL(m.sha256) : null, { wide: true }),
        h('span', { class: 'pp-thumb__label' },
          h('span', { class: 'pp-kind pp-kind--chip', text: kind }))),
      h('div', { class: 'pp-tile__body' },
        h('div', { class: 'pp-tile__name', title: m.orig_name, text: m.orig_name }),
        h('div', { class: 'pp-tile__meta', text: [fmtBytes(m.size), dims].filter(Boolean).join(' · ') }),
        h('div', { class: 'pp-tile__use', text: used }),
        h('div', { class: 'pp-row', style: { 'margin-top': '8px', 'justify-content': 'space-between' } },
          h('button', { type: 'button', class: 'pp-btn pp-btn--sm', text: 'Details', onClick: () => details(m) }),
          h('button', {
            type: 'button', class: 'pp-btn pp-btn--icon', 'aria-label': `Delete ${m.orig_name}`,
            title: 'Delete', onClick: () => remove(m),
          }, icon('close', 14)))));
  }

  function details(m) {
    const kind = guessKind(m.orig_name);
    modal({
      title: m.orig_name,
      wide: true,
      body: h('div', null,
        h('div', { style: { 'max-width': '420px', 'margin-bottom': '14px' } },
          preview(kind, m.has_thumb ? thumbURL(m.sha256) : null, { wide: true })),
        h('div', { class: 'pp-facts' },
          row('Kind', kind),
          row('Type', m.mime || 'not known'),
          row('Size', fmtBytes(m.size)),
          row('Picture size', m.width && m.height ? `${m.width}×${m.height}` : 'not known'),
          row('Thumbnail', m.has_thumb ? 'yes' : 'no, so the screens and this page draw an icon'),
          row('Uploaded', fmtDate(m.uploaded_at)),
          row('Hash', h('span', { class: 'pp-mono', text: `${m.sha256.slice(0, 16)}…` })),
          row('In playlists', m.playlists && m.playlists.length ? m.playlists.join(', ') : 'none yet')),
        h('div', { class: 'pp-help' }, 'The hash is the name of the object. A screen that already holds these bytes under another name never downloads them again.')),
      actions: [{ label: 'Close', value: true }],
    });
  }

  function row(k, v) {
    return h('div', { class: 'pp-facts__row' },
      h('span', { class: 'pp-facts__k', text: k }),
      h('span', { class: 'pp-facts__v' }, v));
  }

  async function remove(m) {
    const inUse = m.playlists && m.playlists.length;
    const ok = await confirmDialog({
      title: `Delete ${m.orig_name}?`,
      body: inUse
        ? h('div', null, 'It is in ', h('b', { text: m.playlists.join(', ') }),
          '. A file that a playlist holds cannot go away: take it out of the playlist first.')
        : 'The object and its thumbnail go away. No playlist holds it.',
      confirm: 'Delete it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/admin/media/${encodeURIComponent(m.sha256)}`);
      toast(`${m.orig_name} is gone.`);
      await load();
    } catch (err) {
      if (err.status === 409) {
        const names = (err.payload && err.payload.playlists) || [];
        toast(names.length
          ? `It is still in ${names.join(', ')}. Take it out there first.`
          : 'A playlist still holds it.', 'danger');
        await load();
        return;
      }
      toast(errorText(err), 'danger');
    }
  }

  return {
    destroy() { gone = true; },
  };
}
