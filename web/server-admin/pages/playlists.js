/* Playlists: built here, then fetched by the screens that use them.

   The editor is the shared one, so the control server and a single device never
   drift apart. This file gives it the server media source: the library is the
   media page, an upload goes into the library first, and the impact strip says
   how many screens a save reaches.
*/

import {
  h, fill, toast, banner, modal, confirmDialog, fmtBytes,
} from '/shared/ui.js';
import { api, upload } from '/shared/api.js';
import { mountPlaylistEditor } from '/shared/playlist-editor.js';
import { warningsFor } from '/shared/item-warnings.js';
import {
  card, pageHead, errorText, thumbURL, parseStatus, guessKind, screenHref,
} from '../util.js';

/* The playlist that is open. It lives in the module, so a trip to the media page
   and back comes back to the same playlist. */
let pickedId = 0;

export function mount(main, ctx) {
  let playlists = [];
  let library = [];
  let editor = null;
  let gone = false;

  const side = h('div', { class: 'sv-split__side' });
  const panel = h('div', { class: 'sv-split__main' });
  const errorSlot = h('div');

  fill(main,
    pageHead('Playlists',
      'Built here, then downloaded by the screens that use them. It is the same editor that the screens have.',
      h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New playlist', onClick: createPlaylist })),
    errorSlot,
    h('div', { class: 'sv-split' }, side, panel));

  load(true);

  /* ------------------------------------------------------------------ load */

  async function load(reopen) {
    try {
      const [gotPlaylists, gotMedia] = await Promise.all([
        api('GET', '/api/admin/playlists'),
        api('GET', '/api/admin/media'),
      ]);
      if (gone) return;
      playlists = (gotPlaylists.playlists || []).map((p) => ({ ...p, items: p.items || [] }));
      library = gotMedia.media || [];
      if (!playlists.some((p) => p.id === pickedId)) pickedId = (playlists[0] && playlists[0].id) || 0;
      renderList();
      if (reopen) openPicked();
    } catch (err) {
      fill(errorSlot, banner({ kind: 'danger', title: 'The playlists did not load', body: errorText(err) }));
    }
  }

  function picked() { return playlists.find((p) => p.id === pickedId) || null; }

  /* ------------------------------------------------------------- the list */

  function renderList() {
    const rows = playlists.map((p) => h('button', {
      type: 'button', class: 'sv-pick', 'aria-current': String(p.id === pickedId),
      onClick: () => pick(p.id),
    },
      h('div', { class: 'sv-pick__name' },
        h('span', { text: p.title }),
        h('span', { class: 'sv-pick__count', text: String(p.devices) })),
      h('div', { class: 'sv-pick__meta', text: summary(p) })));

    fill(side, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-card__head' },
        h('div', { class: 'pp-h2', text: 'Playlists' }),
        h('div', { class: 'pp-card__meta', text: 'screens' })),
      rows.length ? rows : h('div', { class: 'pp-card__body' },
        h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'No playlists yet.' })),
      h('div', { class: 'pp-card__foot' },
        h('button', { type: 'button', class: 'pp-btn pp-btn--ghost', text: '+ New playlist', onClick: createPlaylist }))));
  }

  function summary(p) {
    const n = p.items.length;
    const bits = [n === 0 ? 'empty' : `${n} ${n === 1 ? 'item' : 'items'}`];
    bits.push(p.devices === 0
      ? 'no screen gets this'
      : `${p.devices} ${p.devices === 1 ? 'screen gets' : 'screens get'} this`);
    return bits.join(' · ');
  }

  async function pick(id) {
    if (id === pickedId) return;
    if (editor && editor.isDirty() && !(await confirmDialog({
      title: 'Leave without saving?',
      body: 'This playlist holds changes that are not saved yet. Leaving drops them.',
      confirm: 'Leave anyway', cancel: 'Stay here', kind: 'danger',
    }))) return;
    pickedId = id;
    renderList();
    openPicked();
  }

  /* ----------------------------------------------------------- the editor */

  function openPicked() {
    if (editor) { editor.destroy(); editor = null; }
    const p = picked();
    if (!p) {
      fill(panel, card({
        body: h('div', { class: 'pp-empty', style: { margin: '0' } },
          h('div', { class: 'pp-empty__title', text: 'No playlist is open' }),
          h('div', { class: 'pp-empty__body', text: 'A playlist is a list of files and web pages. Groups and screens point at one, and every screen that uses it downloads what it needs.' }),
          h('div', { class: 'pp-empty__actions' },
            h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'New playlist', onClick: createPlaylist }))),
      }));
      return;
    }

    const slot = h('div');
    fill(panel,
      h('div', { class: 'pp-row', style: { 'justify-content': 'space-between', 'margin-bottom': '12px' } },
        h('div', { class: 'pp-small pp-muted' }, 'The screens make a folder called ', h('span', { class: 'pp-mono', text: p.name })),
        h('div', { class: 'pp-btns' },
          h('button', { type: 'button', class: 'pp-btn pp-btn--sm', text: 'Rename', onClick: () => renamePlaylist(p) }),
          h('button', { type: 'button', class: 'pp-btn pp-btn--sm pp-btn--danger-outline', text: 'Delete', onClick: () => deletePlaylist(p) }))),
      slot);

    const bytes = p.items.reduce((sum, it) => sum + itemBytes(it), 0);
    editor = mountPlaylistEditor(slot, {
      // The editor's name field is the title that a person reads. The slug is
      // what the screens use as a folder name, and only Rename changes it.
      playlist: {
        name: p.title,
        transition: p.transition || '',
        shuffle: p.shuffle === undefined ? null : p.shuffle,
        items: p.items.map(forEditor),
      },
      capabilities: {
        // The server holds the playlist in its database. There is no file with
        // hand-typed comments to lose, so there is nothing to warn about.
        commentLossWarning: false,
        decode: fleetCodecs(),
        tier: fleetTier(),
        impact: {
          text: h('span', null, reachWords(p), ' · ', fmtBytes(bytes), ' of media'),
          note: 'Changes reach a screen at its next check-in.',
        },
        saveLabel: p.devices
          ? `Save — ${p.devices} ${p.devices === 1 ? 'screen fetches' : 'screens fetch'} the changes`
          : 'Save playlist',
        warnPrefix: 'Some screens may struggle with it.',
        warnAction: { label: 'Which ones?', onClick: whichScreens },
      },
      mediaSource: {
        list: () => library.map(forEditor),
        thumbUrl: (it) => (it.sha256 && hasThumb(it.sha256) ? thumbURL(it.sha256) : null),
        upload: uploadToLibrary,
      },
      onSave: (edited) => save(p, edited),
      onDirty: (dirty) => ctx.setGuard(() => dirty),
    });
  }

  function reachWords(p) {
    if (!p.devices) return h('span', null, 'No screen uses this yet');
    return h('span', null,
      h('strong', { text: `${p.devices} ${p.devices === 1 ? 'screen' : 'screens'}` }),
      p.devices === 1 ? ' gets this' : ' get this');
  }

  function hasThumb(sha) {
    const m = library.find((x) => x.sha256 === sha);
    return !!(m && m.has_thumb);
  }

  function itemBytes(it) {
    const m = library.find((x) => x.sha256 === it.sha256);
    return m ? Number(m.size) || 0 : 0;
  }

  /* One library object or one saved item, in the shape that the editor takes.
     The extra fields (size, width, height) are what the warnings read, and the
     editor keeps every field that it does not own. */
  function forEditor(raw) {
    const m = raw.sha256 ? library.find((x) => x.sha256 === raw.sha256) : null;
    const name = raw.name || raw.orig_name || raw.url || '';
    const out = {
      name,
      kind: raw.kind || guessKind(name),
      duration: Number(raw.duration) || null,
    };
    if (raw.url) {
      out.url = raw.url;
      out.refresh_seconds = Number(raw.refresh_seconds) || null;
    } else {
      out.sha256 = raw.sha256;
      out.max_duration = Number(raw.max_duration) || null;
      if (out.kind === 'video') out.mute = !!raw.mute;
      if (m) {
        out.size = m.size;
        out.width = m.width;
        out.height = m.height;
        if (m.has_thumb) out.thumb = thumbURL(m.sha256);
      }
    }
    return out;
  }

  /* An upload from inside the editor goes into the library first. Then the item
     points at the object by its hash, like every other item. */
  async function uploadToLibrary(file, onProgress) {
    const out = await upload('/api/admin/media', file, onProgress);
    const m = out.media;
    if (!library.some((x) => x.sha256 === m.sha256)) library.push(m);
    if (out.duplicate) toast(`${file.name} was already in the library as ${m.orig_name}.`);
    return forEditor({ sha256: m.sha256, name: m.orig_name });
  }

  async function save(p, edited) {
    const title = (edited.name || '').trim() || p.title;
    const body = {
      name: p.name,          // keep the slug; Rename is the way to change it
      title,
      transition: edited.transition || '',
      shuffle: edited.shuffle === null ? null : !!edited.shuffle,
      items: edited.items.map(forServer).filter(Boolean),
    };
    let saved;
    try {
      saved = await api('PUT', `/api/admin/playlists/${p.id}`, body);
    } catch (err) {
      // The editor shows what this throws, so the message must be the whole
      // story: a 422 carries one line for each item that is wrong.
      throw new Error(errorText(err));
    }
    const n = saved.devices || 0;
    toast(n
      ? `Saved — ${n} ${n === 1 ? 'screen fetches' : 'screens fetch'} the changes at the next check-in.`
      : 'Saved. No screen uses this playlist yet.');
    await load(false);
    // The editor marks itself clean after this promise, so the redraw waits for
    // the next turn of the event loop. A redraw inside the promise would leave
    // the editor with a clean copy of the old playlist and a "not saved yet"
    // line under a playlist that is saved.
    setTimeout(() => {
      if (!gone && picked()) openPicked();
    }, 0);
    ctx.clearGuard();
  }

  /* The body of a save. An item carries a hash or a URL and never both, and a
     file item always carries its name: the extension is what says image or
     video, on this server and on every screen. */
  function forServer(it) {
    if (it.url) {
      return {
        url: it.url,
        name: it.name || it.url,
        duration: Number(it.duration) || 0,
        refresh_seconds: Number(it.refresh_seconds) || 0,
      };
    }
    if (!it.sha256) return null;
    return {
      sha256: it.sha256,
      name: it.name,
      duration: Number(it.duration) || 0,
      mute: !!it.mute,
      max_duration: Number(it.max_duration) || 0,
    };
  }

  /* --------------------------------------------------- the fleet warnings */

  /* The decode report of the whole fleet, at its worst: a format counts as
     supported only when every screen decodes it. The playlist editor then shows
     one warning, and "Which ones?" names the screens.

     There is no route for this. The warnings are client-side work against the
     last report of each screen, which is what the API notes say. */
  function fleetCodecs() {
    const reports = screenCodecs();
    if (reports.length === 0) return null;
    const out = { source: 'device', tier: fleetTier(), codecs: {} };
    for (const name of ['h264', 'hevc', 'vp9', 'av1']) {
      out.codecs[name] = {};
      for (const band of ['1080', '2160']) {
        let supported = true;
        let smooth = true;
        let efficient = true;
        for (const r of reports) {
          const got = (r.codecs[name] || {})[band];
          if (!got || !got.supported) supported = false;
          if (!got || !got.smooth) smooth = false;
          if (!got || got.powerEfficient === false) efficient = false;
        }
        out.codecs[name][band] = { supported, smooth, powerEfficient: efficient };
      }
    }
    return out;
  }

  function fleetTier() {
    return ctx.store.devices.some((d) => parseStatus(d).tier === 'low') ? 'low' : 'high';
  }

  /* One entry for each screen that reported its codecs. */
  function screenCodecs() {
    const out = [];
    for (const d of ctx.store.devices) {
      const st = parseStatus(d);
      if (st.codecs && Object.keys(st.codecs).length) {
        out.push({ device: d, codecs: st.codecs, tier: st.tier || null });
      }
    }
    return out;
  }

  /* The screens that would have trouble with one item. Each screen is tested
     against its own report, so the list is exact and not a guess. */
  function whichScreens(item) {
    const hits = [];
    for (const r of screenCodecs()) {
      const caps = { source: 'device', tier: r.tier, codecs: r.codecs };
      const why = warningsFor(item, caps, r.tier, { mixed: true });
      // A warning is {prefix, text}. An older shared module gave a plain string.
      if (why.length) hits.push({ device: r.device, why: why[0].text || String(why[0]) });
    }
    modal({
      title: `Which screens struggle with ${item.name || 'this item'}?`,
      wide: true,
      body: hits.length
        ? h('div', null,
          h('div', { class: 'pp-help', style: { 'margin-top': '0' } },
            'Each screen is measured against what it reported about its own hardware.'),
          h('div', { class: 'pp-facts' }, hits.map((hit) => h('div', { class: 'pp-facts__row' },
            h('span', { class: 'pp-facts__k' },
              h('a', { href: screenHref(hit.device.id), text: hit.device.name || hit.device.id })),
            h('span', { class: 'pp-facts__v', style: { 'font-family': 'inherit', 'max-width': '60%' }, text: hit.why })))))
        : h('div', { class: 'pp-help' },
          'No screen reported a problem with it. The warning comes from the file itself, so a screen that has not checked in yet may still struggle.'),
      actions: [{ label: 'Close', value: true }],
    });
  }

  /* ------------------------------------------------------- make and remove */

  async function createPlaylist() {
    const input = h('input', { class: 'pp-input', type: 'text', autofocus: true, maxlength: '60' });
    const ok = await modal({
      title: 'New playlist',
      body: h('div', null,
        h('label', { class: 'pp-label' }, 'Name', input),
        h('div', { class: 'pp-help', text: 'The screens make a folder of this name, with letters, numbers and hyphens. Add the first item after it is made.' })),
      actions: [{ label: 'Cancel', value: false }, { label: 'Make it', value: true, kind: 'primary' }],
      onOpen: (dialog, buttons) => {
        input.addEventListener('keydown', (e) => {
          if (e.key === 'Enter') { e.preventDefault(); buttons[1].click(); }
        });
      },
    });
    if (!ok) return;
    const title = input.value.trim();
    if (!title) return;
    // A playlist needs one item or more, so the first one comes from the
    // library. A new list with nothing in it is a 422 on items.
    const first = library[0];
    if (!first) {
      toast('Upload something on the Media page first: a playlist needs one item.', 'danger');
      return;
    }
    try {
      const made = await api('POST', '/api/admin/playlists', {
        title, transition: '',
        items: [{ sha256: first.sha256, name: first.orig_name, duration: 10 }],
      });
      pickedId = made.id;
      toast(`${title} is ready. It starts with ${first.orig_name}.`);
      await load(true);
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function renamePlaylist(p) {
    const titleIn = h('input', { class: 'pp-input', type: 'text', value: p.title, autofocus: true, maxlength: '60' });
    const slugIn = h('input', { class: 'pp-input pp-input--mono', type: 'text', value: p.name, maxlength: '60' });
    const ok = await modal({
      title: 'Rename this playlist',
      body: h('div', null,
        h('label', { class: 'pp-label' }, 'Name', titleIn),
        h('label', { class: 'pp-label', style: { 'margin-top': '14px' } }, 'Folder on the screens', slugIn),
        h('div', { class: 'pp-help', text: 'Leave the folder empty and the server makes one from the name. Every screen that uses this playlist writes the new folder at its next check-in.' })),
      actions: [{ label: 'Cancel', value: false }, { label: 'Rename it', value: true, kind: 'primary' }],
    });
    if (!ok) return;
    try {
      await api('PUT', `/api/admin/playlists/${p.id}`, {
        name: slugIn.value.trim(),
        title: titleIn.value.trim() || p.title,
        transition: p.transition || '',
        shuffle: p.shuffle === undefined ? null : p.shuffle,
        items: p.items.map(forEditor).map(forServer).filter(Boolean),
      });
      toast('Renamed.');
      await load(true);
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function deletePlaylist(p) {
    const ok = await confirmDialog({
      title: `Delete ${p.title}?`,
      body: h('div', null,
        p.devices
          ? `${p.devices} ${p.devices === 1 ? 'screen uses' : 'screens use'} it, so this will not work until they point somewhere else. `
          : 'No screen uses it. ',
        'The files stay in the library; only the list goes away.'),
      confirm: 'Delete it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/admin/playlists/${p.id}`);
      toast(`${p.title} is gone.`);
      pickedId = 0;
      ctx.clearGuard();
      await load(true);
    } catch (err) {
      if (err.status === 409) {
        toast('A group, a screen or a time rule still uses it. Point those at another playlist first.', 'danger');
        return;
      }
      toast(errorText(err), 'danger');
    }
  }

  return {
    destroy() {
      gone = true;
      if (editor) editor.destroy();
    },
  };
}
