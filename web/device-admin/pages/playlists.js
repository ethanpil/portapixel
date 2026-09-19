/* Playlists: the folders on the stick and what is in them.

   The editor itself is the shared one, so the device and the control server
   never drift apart. This file gives it the device media source (an upload puts
   the file straight into the playlist folder) and the device rules: a save
   rewrites playlist.toml, and everything is read-only while a server manages
   this screen.
*/

import {
  h, fill, toast, banner, badge, modal, confirmDialog, fmtDuration,
} from '/shared/ui.js';
import { api, upload } from '/shared/api.js';
import { mountPlaylistEditor } from '/shared/playlist-editor.js';
import { probeCapabilities } from '/shared/item-warnings.js';
import { card, pageHead, errorText, guessKind, mediaURL } from '../util.js';

export function mount(main, ctx) {
  let snap = { playlists: [], problems: [], active: '', hashing: false };
  let caps = null;         // what this machine decodes (D12)
  let picked = null;       // the folder name of the playlist in the editor
  let editor = null;
  let paired = false;
  let gone = false;

  const banners = h('div');
  const side = h('div', { class: 'dv-split__side' });
  const panel = h('div', { class: 'dv-split__main' });
  const newBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New playlist', onClick: createPlaylist });

  fill(main,
    pageHead('Playlists',
      'Each playlist is a folder on the USB stick. You can edit them here, or drop files in from any computer.',
      h('div', { class: 'pp-btns' },
        h('button', { type: 'button', class: 'pp-btn', text: 'Look for new files', onClick: rescan }),
        newBtn)),
    banners,
    h('div', { class: 'dv-split' }, side, panel));

  ctx.setGuard(() => !!editor && editor.isDirty());

  const unsubscribe = ctx.store.subscribe((status) => {
    if (!status || gone) return;
    if (status.paired === paired && caps) return;
    paired = !!status.paired;
    if (caps) { renderBanners(); openPicked(); }
  });

  /* The capability probe looks at this browser, not at the screen. The device
     does not report its own codec findings yet, so this is the fallback that
     item-warnings.js documents, and the warnings say "this browser". */
  probeCapabilities().catch(() => null).then((c) => {
    if (gone) return;
    caps = c;
    load({ reopen: true });
  });

  /* ---------------------------------------------------------------- loading */

  async function load({ reopen = false } = {}) {
    try {
      snap = await api('GET', '/api/playlists');
    } catch (err) {
      fill(banners, banner({ kind: 'danger', title: 'The playlists did not load', body: errorText(err) }));
      return;
    }
    if (gone) return;
    // A playlist with no items comes back with a null item list, so give every
    // list a real array before anything counts it.
    snap.playlists = snap.playlists || [];
    snap.problems = snap.problems || [];
    for (const p of snap.playlists) p.items = p.items || [];

    if (!snap.playlists.some((p) => p.name === picked)) picked = null;
    if (!picked) picked = (snap.playlists[0] && snap.playlists[0].name) || null;
    renderBanners();
    renderList();
    if (reopen) openPicked();
  }

  function renderBanners() {
    const status = ctx.store.status || {};
    // A paired device gets its playlists from the server, so a new local one
    // would never play. The button that makes one goes with them.
    newBtn.disabled = paired;
    fill(banners,
      paired ? banner({
        kind: 'paired',
        title: `Managed by ${status.server_url || 'a control server'}`,
        body: 'Playlists and schedules come from the server, so they are read-only here. Display, sound and network settings are still yours. Unpairing hands control back to this page.',
        actions: [h('a', { class: 'pp-btn', href: '#/settings', text: 'Unpair on Settings' })],
      }) : null,
      snap.problems && snap.problems.length ? banner({
        kind: 'warn',
        title: snap.problems.length === 1 ? 'One playlist needs a look' : `${snap.problems.length} playlists need a look`,
        body: h('ul', { style: { margin: '4px 0 0', 'padding-left': '18px' } },
          snap.problems.map((p) => h('li', null,
            p.playlist ? [h('span', { class: 'pp-mono', text: p.playlist }), ' — '] : null, p.message))),
      }) : null);
  }

  /* ------------------------------------------------------------- the list */

  function renderList() {
    const rows = snap.playlists.map((p) => h('button', {
      type: 'button', class: 'dv-pick', 'aria-current': String(p.name === picked),
      onClick: () => pick(p.name),
    },
      h('div', { class: 'dv-pick__name' }, h('span', { text: p.title })),
      h('div', { class: 'dv-pick__meta', text: summary(p) }),
      h('div', { class: 'dv-pick__tags' },
        p.name === snap.active ? badge('playing now', 'brand') : null,
        p.kiosk ? badge('kiosk') : null,
        p.fleet ? badge('from the server', 'brand') : null)));

    fill(side, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-card__head' },
        h('div', { class: 'pp-h2', text: 'On the stick' }),
        snap.hashing ? h('span', { class: 'pp-card__meta', text: 'checking files' }) : null),
      rows.length ? rows : h('div', { class: 'pp-card__body' },
        h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'No playlists yet.' })),
      h('div', { class: 'pp-card__foot' },
        h('button', {
          type: 'button', class: 'pp-btn pp-btn--ghost', text: '+ New playlist',
          disabled: paired, onClick: createPlaylist,
        }))));
  }

  function summary(p) {
    if (p.kiosk) return '1 web page · kiosk';
    if (!p.items.length) return 'empty';
    let seconds = 0;
    let partial = false;
    for (const it of p.items) {
      const d = Number(it.duration) || Number(it.max_duration) || 0;
      if (d) seconds += d; else partial = true;
    }
    const count = `${p.items.length} ${p.items.length === 1 ? 'item' : 'items'}`;
    if (!seconds) return count;
    return `${count} · ${partial ? 'at least ' : ''}${fmtDuration(seconds)}`;
  }

  async function pick(name) {
    if (name === picked) return;
    if (editor && editor.isDirty() && !(await confirmDialog({
      title: 'Leave without saving?',
      body: 'This playlist holds changes that are not saved yet. Leaving drops them.',
      confirm: 'Leave anyway', cancel: 'Stay here', kind: 'danger',
    }))) return;
    picked = name;
    renderList();
    openPicked();
  }

  /* ------------------------------------------------------------ the editor */

  function openPicked() {
    if (editor) { editor.destroy(); editor = null; }

    const p = snap.playlists.find((x) => x.name === picked);
    if (!p) {
      fill(panel, card({
        body: h('div', { class: 'pp-empty', style: { margin: '0' } },
          h('div', { class: 'pp-empty__title', text: 'No playlist is open' }),
          h('div', { class: 'pp-empty__body', text: 'Make a playlist, or pull the stick and copy a folder of images onto it from any computer.' }),
          paired ? null : h('div', { class: 'pp-empty__actions' },
            h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New playlist', onClick: createPlaylist }))),
      }));
      return;
    }

    const readOnly = paired || p.fleet;
    const slot = h('div');

    fill(panel,
      h('div', { class: 'pp-row', style: { 'justify-content': 'space-between', 'margin-bottom': '12px' } },
        h('div', { class: 'pp-small pp-muted' }, 'Folder ', h('span', { class: 'pp-mono', text: p.name })),
        readOnly ? null : h('div', { class: 'pp-btns' },
          h('button', { type: 'button', class: 'pp-btn pp-btn--sm', text: 'Rename', onClick: () => renamePlaylist(p) }),
          h('button', { type: 'button', class: 'pp-btn pp-btn--sm pp-btn--danger-outline', text: 'Delete', onClick: () => deletePlaylist(p) }))),
      fileWarnings(p),
      slot);

    /* The name field of the editor is the title that people read. The folder
       name is what the API and the schedule rules use, and only Rename changes
       it, so it stays in this closure and never goes through the editor. */
    editor = mountPlaylistEditor(slot, {
      playlist: {
        name: p.title,
        transition: p.transition || '',
        shuffle: p.shuffle === undefined ? null : p.shuffle,
        items: p.items.map(forEditor),
      },
      readOnly,
      capabilities: {
        commentLossWarning: true,
        configFile: `${p.name}/playlist.toml`,
        folder: p.name,
        tier: (ctx.store.status && ctx.store.status.tier) || null,
        decode: caps,
      },
      mediaSource: {
        thumbUrl: (it) => (it.kind === 'image' && it.src ? it.src : null),
        upload: (file, onProgress) => upload(`/api/media/${encodeURIComponent(p.name)}`, file, onProgress)
          .then((r) => ({
            file: r.name, name: r.name, kind: guessKind(r.name),
            size: r.size, src: mediaURL(p.name, r.name),
          })),
      },
      onSave: (edited) => save(p, edited),
    });
  }

  function forEditor(it) {
    const out = {
      name: it.name, kind: it.kind, src: it.src, size: it.size,
      duration: Number(it.duration) || null,
      max_duration: Number(it.max_duration) || null,
      refresh_seconds: Number(it.refresh_seconds) || null,
    };
    if (it.url) out.url = it.url; else out.file = it.file;
    if (it.kind === 'video') out.mute = !!it.mute;
    return out;
  }

  /* The items go back in the shape that playlist.toml takes: a file item never
     carries a reload interval and a web page never carries a cap, and the
     device answers 422 for either of them. */
  function forDevice(it) {
    if (it.url) {
      return {
        url: it.url,
        duration: Number(it.duration) || 0,
        refresh_seconds: Number(it.refresh_seconds) || 0,
      };
    }
    return {
      file: it.file,
      duration: Number(it.duration) || 0,
      mute: !!it.mute,
      max_duration: Number(it.max_duration) || 0,
    };
  }

  async function save(p, edited) {
    const body = {
      title: (edited.name || '').trim() || p.title,
      transition: edited.transition || '',
      items: edited.items.filter((it) => it.file || it.url).map(forDevice),
    };
    if (edited.shuffle === true || edited.shuffle === false) body.shuffle = edited.shuffle;

    try {
      await api('PUT', `/api/playlists/${encodeURIComponent(p.name)}`, body);
    } catch (err) {
      // The editor shows what this throws, so the message must be the whole
      // story: a 422 carries one line for each item that is wrong.
      throw new Error(errorText(err));
    }
    toast('Playlist saved — the screen picks it up on the next item');
    await load();          // new titles and sizes, without taking the editor away
    ctx.store.refresh();
  }

  /* Warnings that the device itself found: a missing file, or a kind that the
     player does not know. They belong to the playlist, not to one field. */
  function fileWarnings(p) {
    const bad = p.items.filter((it) => it.warning);
    if (!bad.length) return null;
    return banner({
      kind: 'warn',
      title: bad.length === 1 ? 'One item needs a look' : `${bad.length} items need a look`,
      body: h('ul', { style: { margin: '4px 0 0', 'padding-left': '18px' } },
        bad.map((it) => h('li', null, h('span', { class: 'pp-mono', text: it.name }), ' — ', it.warning))),
    });
  }

  /* -------------------------------------------------------- make and remove */

  async function createPlaylist() {
    const title = await askName({
      title: 'New playlist',
      label: 'Name',
      help: 'A folder of this name is made on the stick. Letters, numbers and hyphens make the folder name.',
      confirm: 'Make it',
    });
    if (!title) return;
    try {
      const out = await api('POST', '/api/playlists', { title });
      picked = out.name;
      toast(`${title} is ready. Add files to it.`);
      await load({ reopen: true });
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function renamePlaylist(p) {
    const title = await askName({
      title: 'Rename this playlist',
      label: 'Name',
      value: p.title,
      help: 'The folder on the stick is renamed too. Schedule rules that name it are corrected.',
      confirm: 'Rename it',
    });
    if (!title) return;
    try {
      const out = await api('POST', `/api/playlists/${encodeURIComponent(p.name)}/rename`, { title });
      const fixed = out.name !== p.name ? await fixReferences(p.name, out.name) : false;
      picked = out.name;
      toast(fixed ? 'Renamed, and the schedule rules were corrected.' : 'Renamed.');
      await load({ reopen: true });
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function deletePlaylist(p) {
    const ok = await confirmDialog({
      title: `Delete ${p.title}?`,
      body: h('div', null,
        'This removes the folder ', h('span', { class: 'pp-mono', text: p.name }),
        ' and every file in it from the stick. It cannot be undone.',
        p.name === snap.active ? h('div', { style: { 'margin-top': '8px' } }, 'This playlist is on the screen now.') : null),
      confirm: 'Delete it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/playlists/${encodeURIComponent(p.name)}`);
      picked = null;
      toast(`${p.title} is gone.`);
      await warnAboutReferences(p.name);
      await load({ reopen: true });
      ctx.store.refresh();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* The configuration names playlists by folder name, so a rename has to
     correct the rules that point at the old name (the device says so in the
     answer of the rename call). */
  async function fixReferences(from, to) {
    try {
      const cfg = (await api('GET', '/api/config')).config;
      let touched = false;
      for (const rule of cfg.schedule || []) {
        if (rule.playlist === from) { rule.playlist = to; touched = true; }
      }
      if (cfg.playback.default_playlist === from) { cfg.playback.default_playlist = to; touched = true; }
      if (!touched) return false;
      await api('PUT', '/api/config', cfg);
      return true;
    } catch (err) {
      toast(`The rename worked, but the schedule rules still name the old folder: ${errorText(err)}`, 'danger');
      return false;
    }
  }

  async function warnAboutReferences(name) {
    try {
      const cfg = (await api('GET', '/api/config')).config;
      const inRules = (cfg.schedule || []).some((r) => r.playlist === name);
      const isDefault = cfg.playback.default_playlist === name;
      if (inRules || isDefault) {
        toast('A schedule rule or the default playlist still names it. Open Schedule.', 'danger');
      }
    } catch { /* the toast is a courtesy, not a step */ }
  }

  async function rescan() {
    try {
      const out = await api('POST', '/api/rescan');
      toast(`${out.playlists} ${out.playlists === 1 ? 'playlist' : 'playlists'} on the stick.`);
      await load();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* One text field in a dialog, for a new name and for a rename. */
  function askName({ title, label, help, value = '', confirm }) {
    const input = h('input', { class: 'pp-input', type: 'text', value, autofocus: true, maxlength: '60' });
    return modal({
      title,
      body: h('div', null, h('label', { class: 'pp-label' }, label, input), h('div', { class: 'pp-help', text: help })),
      actions: [{ label: 'Cancel', value: false }, { label: confirm, value: true, kind: 'primary' }],
      onOpen: (dialog, buttons) => {
        input.addEventListener('keydown', (e) => {
          if (e.key === 'Enter') { e.preventDefault(); buttons[1].click(); }
        });
      },
    }).then((ok) => (ok ? input.value.trim() : ''));
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
      if (editor) editor.destroy();
    },
  };
}
