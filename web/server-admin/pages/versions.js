/* Versions: which release the screens may install.

   Screens only ever install the version that is approved here, and they fetch it
   from this server's mirror. A screen that cannot come up on a new version puts
   itself back on the old one, so an approval is safe to try on one screen first.
*/

import {
  h, fill, toast, banner, modal, confirmDialog, progress, spinner, fmtBytes,
} from '/shared/ui.js';
import { api, upload } from '/shared/api.js';
import { card, pageHead, errorText, fmtDate } from '../util.js';

/* How often the page asks again while the mirror copies a release. */
const MIRROR_POLL_MS = 3000;

export function mount(main, ctx) {
  let view = null;
  let bundleVersion = '';
  let timer = 0;
  let gone = false;

  const noticesSlot = h('div');
  const fleetSlot = h('div', { style: { 'margin-bottom': '14px' } });
  const tableSlot = h('div');
  const bundleSlot = h('div');

  const bundleInput = h('input', {
    type: 'file', class: 'pp-sr-only', accept: '.zip,.tar.gz,.tgz',
    onChange: () => {
      const file = bundleInput.files[0];
      bundleInput.value = '';
      if (file) sendBundle(bundleVersion, file);
    },
  });

  const refreshBtn = h('button', {
    type: 'button', class: 'pp-btn', text: 'Ask the release page again', onClick: refresh,
  });

  fill(main,
    pageHead('Versions',
      'Screens only ever install the version that you approve here. A screen that cannot come up on a new version puts itself back on the old one.',
      refreshBtn),
    noticesSlot, fleetSlot, tableSlot, bundleSlot, bundleInput);

  load();

  /* ------------------------------------------------------------------ load */

  async function load() {
    try {
      view = await api('GET', '/api/admin/releases');
      if (gone) return;
      draw();
      // The mirror copies two binaries in the background, so the page asks again
      // until it is finished.
      clearTimeout(timer);
      if (view.mirroring) timer = setTimeout(load, MIRROR_POLL_MS);
    } catch (err) {
      fill(tableSlot, banner({ kind: 'danger', title: 'The versions did not load', body: errorText(err) }));
    }
  }

  async function refresh() {
    refreshBtn.disabled = true;
    try {
      view = await api('POST', '/api/admin/releases/refresh');
      draw();
      toast(view.list_error ? 'The release page did not answer.' : 'The list is up to date.',
        view.list_error ? 'danger' : 'info');
    } catch (err) {
      toast(errorText(err), 'danger');
    } finally {
      refreshBtn.disabled = false;
    }
  }

  /* --------------------------------------------------------------- notices */

  function draw() {
    const releases = view.releases || [];
    fill(noticesSlot,
      view.has_key === false ? banner({
        kind: 'warn',
        title: 'This build cannot mirror releases',
        body: 'It was built with no release key, so it cannot check the signature of a release and it never marks one as ready. Screens keep the version that they have. To update a fleet from this server, use a build that carries the key.',
      }) : null,
      view.list_error ? banner({
        kind: 'warn',
        title: 'The public release page did not answer',
        body: h('div', null,
          h('div', { class: 'pp-mono', style: { 'font-size': '12px' }, text: view.list_error }),
          h('div', { style: { 'margin-top': '6px' } },
            'The list below is what this server already knew. The approval and the mirror are local, so both still work, and a closed network can upload a release bundle instead.')),
      }) : null,
      view.mirroring ? h('div', { class: 'pp-banner pp-banner--info' },
        h('div', { class: 'pp-banner__text' },
          h('div', { class: 'pp-row' }, spinner('Copying a release'),
            h('div', null,
              h('div', { class: 'pp-banner__title', text: `Copying ${view.mirroring}` }),
              h('div', { class: 'pp-banner__body', text: 'The server downloads the binaries and checks their signatures. Screens wait until it is finished.' }))))) : null);

    paintFleet();
    paintTable(releases);
  }

  /* One bar for each version that the fleet runs. */
  function paintFleet() {
    const fleet = view.fleet || [];
    const total = fleet.reduce((sum, v) => sum + v.devices, 0);
    fill(fleetSlot, card({
      title: 'Where the fleet is',
      body: total === 0
        ? h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'No screen has reported a version yet.' })
        : fleet.map((v) => {
          const share = v.devices / total;
          const bar = progress(share, { brand: v.version === view.approved });
          return h('div', { class: 'sv-bar' },
            h('span', { class: 'sv-bar__v', text: v.version || 'not known' }),
            h('span', { class: 'sv-bar__track' }, bar),
            h('span', {
              class: 'sv-bar__n',
              text: `${Math.round(share * 100)}% · ${v.devices} ${v.devices === 1 ? 'screen' : 'screens'}`,
            }));
        }),
    }));
  }

  /* ----------------------------------------------------------- the releases */

  function paintTable(releases) {
    const inner = h('div', { class: 'pp-table__inner' });
    inner.append(h('div', { class: 'pp-table__head' },
      headCell('96px', 'Approved'),
      headCell('86px', 'Version'),
      headCell('120px', 'Released'),
      headCell('150px', 'On this server'),
      headCell(null, 'What changed')));

    if (releases.length === 0) {
      inner.append(h('div', { class: 'pp-empty' },
        h('div', { class: 'pp-empty__title', text: 'No releases are known' }),
        h('div', { class: 'pp-empty__body' },
          'This server has not read the public release page yet. Ask it again, or upload a release bundle on a network that cannot reach it.')));
    } else {
      for (const rel of releases) inner.append(releaseRow(rel));
    }

    fill(tableSlot, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-table' }, inner),
      h('div', { class: 'pp-card__foot' },
        h('span', { text: 'Read from the public release page. Nothing is pushed: a screen fetches the approved version on its own.' }),
        h('span', { class: 'pp-mono', text: view.approved ? `approved ${view.approved}` : 'nothing approved' }))));
  }

  function headCell(width, text) {
    const el = h('span', { class: width ? 'pp-cell' : 'pp-cell pp-cell--grow' }, text);
    if (width) { el.style.flex = 'none'; el.style.width = width; }
    return el;
  }

  function releaseRow(rel) {
    const radio = h('input', {
      type: 'radio', name: 'pp-approved', checked: rel.approved,
      disabled: view.has_key === false,
      'aria-label': `Approve ${rel.version}`,
      onChange: () => approve(rel),
    });

    const state = h('span', { class: 'pp-small' }, mirrorWords(rel));
    const actions = h('div', { class: 'pp-btns', style: { 'margin-top': '6px' } });
    if (rel.approved) {
      actions.append(h('button', {
        type: 'button', class: 'pp-btn pp-btn--ghost', text: 'Take the approval away',
        onClick: () => unapprove(rel),
      }));
    }
    if (rel.mirror_state === 'failed' && view.has_key !== false) {
      actions.append(h('button', {
        type: 'button', class: 'pp-btn pp-btn--sm', text: 'Copy it again',
        onClick: () => mirrorAgain(rel),
      }));
    }
    if (!rel.mirrored && view.has_key !== false) {
      actions.append(h('button', {
        type: 'button', class: 'pp-btn pp-btn--sm', text: 'Upload a bundle',
        onClick: () => { bundleVersion = rel.version; bundleInput.click(); },
      }));
    }

    const row = h('div', { class: `pp-table__row${rel.approved ? ' sv-approved' : ''}` },
      cell('96px', h('label', { class: 'pp-check' }, radio,
        h('span', { class: 'pp-small', text: rel.approved ? 'in use' : '' }))),
      cell('86px', h('span', { class: 'pp-mono', text: rel.version })),
      cell('120px', h('span', { class: 'pp-small pp-muted', text: fmtDate(rel.published_at) })),
      cell('150px', h('span', null, state, actions)),
      cell(null, h('span', { class: 'pp-small', text: rel.notes || 'No notes came with this release.' })));
    return row;
  }

  function cell(width, content) {
    const el = h('span', { class: width ? 'pp-cell' : 'pp-cell pp-cell--grow' }, content);
    if (width) { el.style.flex = 'none'; el.style.width = width; }
    else el.style.whiteSpace = 'normal';
    return el;
  }

  /* The mirror state in words. A screen installs a release only when it is both
     approved and copied onto this server. */
  function mirrorWords(rel) {
    if (view.mirroring === rel.version) return h('span', { class: 'pp-muted', text: 'copying it now…' });
    if (rel.mirrored) return h('span', { style: { color: 'var(--pp-brand)' }, text: 'ready for screens' });
    switch (rel.mirror_state) {
      case 'failed':
        return h('span', null,
          h('span', { style: { color: 'var(--pp-danger)' }, text: 'the copy failed' }),
          rel.mirror_error ? h('span', { class: 'pp-help', style: { 'margin-top': '2px' }, text: rel.mirror_error }) : null);
      case 'working':
        return h('span', { class: 'pp-muted', text: 'copying it now…' });
      default:
        return h('span', { class: 'pp-muted', text: 'not on this server yet' });
    }
  }

  /* ------------------------------------------------------------- the actions */

  async function approve(rel) {
    const fleet = (view.fleet || []).reduce((sum, v) => sum + v.devices, 0);
    const ok = await confirmDialog({
      title: `Approve ${rel.version}?`,
      body: h('div', null,
        h('div', null, `Every screen that checks in installs ${rel.version} on its own, ${fleet ? `all ${fleet} of them` : 'once there are screens'}.`),
        h('div', { style: { 'margin-top': '8px' } },
          'The server copies the release and checks its signature first. A screen that cannot come up on it puts itself back on the version that it had.'),
        h('div', { style: { 'margin-top': '8px' } },
          'Only one version is approved at a time, so this takes the approval off ',
          h('b', { text: view.approved || 'nothing' }), '.')),
      confirm: 'Approve it',
    });
    if (!ok) { draw(); return; }
    try {
      const out = await api('POST', `/api/admin/releases/${encodeURIComponent(rel.version)}/approve`);
      if (out.mirror_error) toast(`Approved, but the copy could not start: ${out.mirror_error}`, 'danger');
      else toast(`${rel.version} is approved. The server is copying it.`);
    } catch (err) {
      toast(errorText(err), 'danger');
    }
    await load();
  }

  async function unapprove(rel) {
    const ok = await confirmDialog({
      title: `Take the approval off ${rel.version}?`,
      body: 'No screen installs anything after this. Screens that already moved to it stay on it: this server never puts a screen back.',
      confirm: 'Take it away',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('POST', `/api/admin/releases/${encodeURIComponent(rel.version)}/unapprove`);
      toast('No version is approved now.');
    } catch (err) {
      toast(errorText(err), 'danger');
    }
    await load();
  }

  async function mirrorAgain(rel) {
    try {
      await api('POST', `/api/admin/releases/${encodeURIComponent(rel.version)}/mirror`);
      toast('The copy started again.');
    } catch (err) {
      toast(err.status === 409 ? `A copy already runs for ${view.mirroring}.` : errorText(err), 'danger');
    }
    await load();
  }

  /* A network that cannot reach the release page gets its binaries this way. The
     archive holds the two binaries, their signatures and SHA256SUMS. */
  async function sendBundle(version, file) {
    const bar = progress(0, { brand: true });
    const pct = h('span', { class: 'pp-mono pp-muted', text: '0%' });
    fill(bundleSlot, card({
      title: `Uploading a bundle for ${version}`,
      body: [
        h('div', { class: 'sv-up__name' },
          h('span', { class: 'pp-trunc', text: file.name }),
          h('span', null, pct, ' · ', fmtBytes(file.size))),
        bar,
        h('div', { class: 'pp-help', text: 'The server checks every signature in the archive before it keeps one file.' }),
      ],
    }));
    try {
      await upload(`/api/admin/releases/${encodeURIComponent(version)}/bundle`, file, (frac) => {
        bar.set(frac);
        pct.textContent = `${Math.round(frac * 100)}%`;
      });
      fill(bundleSlot);
      toast(`${version} came from the bundle and it verifies.`);
    } catch (err) {
      fill(bundleSlot);
      if (err.status === 412) {
        modal({
          title: 'This build has no release key',
          body: 'Without the key the server cannot check the signature of a release, so it will not keep one. Use a build that carries the key.',
          actions: [{ label: 'All right', value: true }],
        });
      } else {
        modal({
          title: 'The bundle was not accepted',
          body: h('div', null,
            h('div', { text: errorText(err) }),
            h('div', { style: { 'margin-top': '8px' } },
              'The archive must hold portapixeld-amd64 and portapixeld-arm64, a .minisig file for each one, and SHA256SUMS.')),
          actions: [{ label: 'All right', value: true }],
        });
      }
    }
    await load();
  }

  return {
    destroy() {
      gone = true;
      clearTimeout(timer);
    },
  };
}
