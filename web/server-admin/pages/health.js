/* Server health: what this host has and where it is tight.

   Every line is a plain sentence and a number. There is no PHP under this
   server, so there is no upload cap, no post limit and no time limit to report:
   the free space and the reserve are what decide (D27).
*/

import {
  h, fill, toast, banner, statusDot, progress, factList, fmtBytes,
  fmtDuration, fmtAgo, card, pageHead, errorText,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { fmtClock, isNever } from '../util.js';

export function mount(main, ctx) {
  let view = null;
  let checking = false;
  let gone = false;

  const body = h('div');

  fill(main,
    pageHead('Server health',
      'What this host has and where it is tight. Worth a look before a big upload goes wrong.',
      h('button', { type: 'button', class: 'pp-btn', text: 'Read it again', onClick: () => load() })),
    body);

  load();

  async function load() {
    try {
      view = await api('GET', '/api/admin/health');
      if (gone) return;
      draw();
    } catch (err) {
      fill(body, banner({ kind: 'danger', title: 'The health page did not load', body: errorText(err) }));
    }
  }

  /* One row: a dot, a name, a note, and the number. */
  function row({ kind = 'ok', k, note, v }) {
    return h('div', { class: 'sv-health' },
      statusDot(kind),
      h('div', { class: 'sv-health__k' },
        h('div', null, k),
        note ? h('div', { class: 'sv-health__note' }, note) : null),
      h('div', { class: 'sv-health__v' }, v));
  }

  function draw() {
    const free = Number(view.disk_free_bytes) || 0;
    const total = Number(view.disk_total_bytes) || 0;
    const reserve = Number(view.reserve_bytes) || 0;
    const diskBar = progress(total ? (total - free) / total : 0, { brand: true });

    // A disk with less free space than the reserve takes no upload at all.
    const diskKind = free <= reserve ? 'alert' : (free < reserve * 4 ? 'busy' : 'ok');

    fill(body,
      h('div', { class: 'pp-cards' },
        h('div', { class: 'pp-stack', style: { flex: '1 1 460px', 'min-width': 'min(320px, 100%)' } },
          card({
            title: 'This host',
            body: [
              h('div', { class: 'pp-row', style: { 'justify-content': 'space-between' } },
                h('span', { class: 'pp-small', text: 'Space where the media live' }),
                h('span', { class: 'pp-small pp-mono', text: total ? `${fmtBytes(free)} free of ${fmtBytes(total)}` : 'not known' })),
              h('div', { style: { margin: '8px 0 14px' } }, diskBar),

              row({
                kind: diskKind,
                k: 'Room for an upload',
                note: free <= reserve
                  ? 'The free space is down to the reserve, so this server takes no more uploads. Make room, or move the data directory.'
                  : `The store keeps ${fmtBytes(reserve)} of the disk back, so a full disk never stops the fleet. An upload larger than the rest is refused before it starts.`,
                v: fmtBytes(Math.max(0, free - reserve)),
              }),
              row({
                kind: view.media_writable ? 'ok' : 'alert',
                k: 'The media store takes new files',
                note: view.media_writable
                  ? 'This is the answer of a real write into the store, not a guess from the permissions.'
                  : `A write into the store failed: ${view.media_error || 'no reason given'}`,
                v: view.media_writable ? 'yes' : 'no',
              }),
              row({
                k: 'Files in the library',
                note: 'One file is one object, named by the hash of its bytes.',
                v: `${view.media_files} · ${fmtBytes(view.media_bytes)}`,
              }),
              row({
                kind: integrityKind(),
                k: 'The database is whole',
                note: integrityNote(),
                v: integrityValue(),
              }),
              row({
                k: 'Size of the database',
                note: 'The write-ahead log is counted, because it can be larger than the database after a busy hour.',
                v: fmtBytes(view.database_bytes),
              }),
              row({
                kind: mirrorKind(),
                k: 'The release mirror',
                note: mirrorNote(),
                v: view.approved_version || 'nothing approved',
              }),
            ],
            foot: [
              h('span', { text: 'The integrity check reads the whole database, so it takes a few seconds.' }),
              checkBtn(),
            ],
          }),
          card({
            title: 'Backups',
            body: [
              h('div', { class: 'pp-small' },
                'Everything except the media files lives in one database file. Copy it somewhere safe and the fleet can be rebuilt.'),
              h('div', { class: 'pp-help' },
                'It is at ', h('span', { class: 'pp-mono', text: `${view.data_dir}/portapixel.db` }),
                '. Copy the ', h('span', { class: 'pp-mono', text: '-wal' }), ' file beside it as well, or stop the server first.'),
              h('div', { class: 'pp-help' },
                'The media files are the other half. They are under ',
                h('span', { class: 'pp-mono', text: `${view.data_dir}/media` }),
                ' and every one of them is named by its own hash.'),
            ],
          })),

        h('div', { class: 'pp-stack', style: { flex: '1 1 320px', 'min-width': 'min(300px, 100%)' } },
          card({
            title: 'Check-ins',
            body: factList([
              ['Screens', view.contacts.total],
              ['Called in the last hour', view.contacts.last_hour],
              ['Called in the last day', view.contacts.last_day],
              { k: 'Never called', v: view.contacts.never_seen, kind: view.contacts.never_seen ? 'warn' : null },
              { k: 'Waiting for approval', v: view.contacts.pending, kind: view.contacts.pending ? 'warn' : null },
              { k: 'Clone conflicts', v: view.contacts.conflict, kind: view.contacts.conflict ? 'danger' : null },
              ['Quietest screen', isNever(view.contacts.quietest_seen) ? 'none has called' : fmtAgo(view.contacts.quietest_seen)],
            ]),
          }),
          card({
            title: 'Versions in the fleet',
            body: versionRows(),
          }),
          card({
            title: 'This server',
            body: factList([
              ['Version', view.server_version],
              ['Built for', view.arch],
              ['Running for', fmtDuration(view.uptime_seconds)],
              ['Data directory', h('span', { class: 'pp-mono', style: { 'font-size': '11.5px' }, text: view.data_dir })],
              ['Release key', view.has_release_key ? 'yes' : 'no, so it cannot mirror a release'],
            ]),
          }))));
  }

  function versionRows() {
    const versions = Object.entries(view.contacts.versions || {});
    if (versions.length === 0) {
      return h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'No screen has reported a version yet.' });
    }
    versions.sort((a, b) => b[1] - a[1]);
    return [
      factList(versions.map(([v, n]) => [v || 'not known', `${n} ${n === 1 ? 'screen' : 'screens'}`])),
      h('div', { class: 'pp-help' },
        'The Versions page has the same numbers as a picture, with the release notes.'),
    ];
  }

  /* ---- the integrity check ---- */

  function integrityKind() {
    if (!view.integrity) return 'quiet';
    return view.integrity === 'ok' ? 'ok' : 'alert';
  }

  function integrityValue() {
    if (!view.integrity) return 'not checked';
    return view.integrity === 'ok' ? 'yes' : 'no';
  }

  function integrityNote() {
    if (!view.integrity) {
      return 'Nobody has run the check on this server yet. It is worth one run after a power cut or a full disk.';
    }
    const when = isNever(view.integrity_at) ? '' : ` Last run at ${fmtClock(view.integrity_at)}.`;
    if (view.integrity === 'ok') return `SQLite read every page and found nothing wrong.${when}`;
    return `SQLite reported: ${view.integrity}. Restore the last copy of the database file.${when}`;
  }

  function checkBtn() {
    const btn = h('button', {
      type: 'button', class: 'pp-btn', text: checking ? 'Checking…' : 'Run the check',
      disabled: checking,
      onClick: async () => {
        checking = true;
        draw();
        try {
          const out = await api('POST', '/api/admin/health/integrity');
          view.integrity = out.integrity;
          view.integrity_at = out.integrity_at;
          toast(out.integrity === 'ok' ? 'The database is whole.' : `SQLite reported: ${out.integrity}`,
            out.integrity === 'ok' ? 'info' : 'danger');
        } catch (err) {
          toast(errorText(err), 'danger');
        } finally {
          checking = false;
          if (!gone) draw();
        }
      },
    });
    return btn;
  }

  /* ---- the mirror ---- */

  function mirrorKind() {
    if (!view.approved_version) return 'quiet';
    if (view.mirror_state === 'failed') return 'alert';
    if (view.mirror_state === 'working') return 'busy';
    return view.mirror_state === 'done' ? 'ok' : 'busy';
  }

  function mirrorNote() {
    if (!view.has_release_key) {
      return 'This build has no release key, so it cannot check the signature of a release and never serves one.';
    }
    if (!view.approved_version) {
      return 'No version is approved, so no screen installs anything. Approve one on the Versions page.';
    }
    switch (view.mirror_state) {
      case 'done': return 'The binaries are here and their signatures check out. Screens fetch them from this server.';
      case 'working': return 'The server is downloading the binaries of the approved version.';
      case 'failed': return `The copy failed: ${view.mirror_error || 'no reason given'}. The Versions page can start it again.`;
      default: return 'The approved version is not on this server yet, so no screen can install it.';
    }
  }

  return {
    destroy() { gone = true; },
  };
}
