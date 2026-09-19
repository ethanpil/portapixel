/* About: what this build is, how it updates, and how to move it onto a disk.

   Two of the three cards talk to routes that ship in a later version. They are
   built now, and each one says plainly when the device answers that the route
   is not in this build.
*/

import {
  h, fill, toast, banner, badge, factList, progress, spinner,
  confirmDialog, typedConfirm, fmtBytes, fmtDuration,
  card, pageHead, setText, setShown, errorText,
} from '/shared/ui.js';
import { api, sse } from '/shared/api.js';
import { notInThisBuild } from '../util.js';

export function mount(main, ctx) {
  let gone = false;
  let stream = null;

  /* ---- what this build is ---- */

  const vVersion = h('span');
  const vImage = h('span');
  const vManifest = h('span', { class: 'pp-trunc', style: { 'max-width': '220px', display: 'inline-block' } });
  const vArch = h('span');
  const vTier = h('span');
  const vRung = h('span');
  const vID = h('span');
  const vSystem = h('span');
  const vStick = h('span');
  const vUptime = h('span');

  const facts = card({
    title: 'This build',
    body: [
      factList([
        ['Version', vVersion],
        ['Image version', vImage],
        ['Package manifest', vManifest],
        ['Hardware', vArch],
        ['Performance', vTier],
        ['Player control', vRung],
        ['Device ID', vID],
        ['Running for', vUptime],
        ['On the stick', vStick],
        ['Player', vSystem],
      ]),
      h('div', { class: 'pp-help', text: 'The image version and the hash of the package list are what make two builds comparable. Quote both of them in a bug report.' }),
    ],
  });

  /* ---- updates ---- */

  const updateText = h('div', { class: 'pp-help', style: { 'margin-top': '0' } });
  const updateNote = h('div');
  const checkBtn = h('button', { type: 'button', class: 'pp-btn', text: 'Check for updates', onClick: check });
  const applyBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Install it', hidden: true, onClick: installUpdate });
  let offered = null;   // {available, notes, source} from the last check

  const updates = card({
    title: 'Software updates',
    body: [updateText, updateNote],
    foot: [h('span'), h('div', { class: 'pp-btns' }, checkBtn, applyBtn)],
  });

  /* ---- licences ---- */

  const licences = card({
    title: 'Open source',
    body: [
      h('div', { text: 'PortaPixel is MIT licensed. It is built on Alpine Linux, Chromium and cage, all used as they ship.' }),
      h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
        h('a', { class: 'pp-btn', href: '/licenses', target: '_blank', rel: 'noopener', text: 'Licences of everything included' })),
      h('div', { class: 'pp-help' },
        'The same file is on the device at ',
        h('span', { class: 'pp-mono', text: '/usr/share/portapixel/LICENSES-THIRD-PARTY.md' }), '.'),
    ],
  });

  /* ---- install to disk ---- */

  const diskNote = h('div');
  const diskList = h('div');
  const diskBtn = h('button', { type: 'button', class: 'pp-btn', text: 'Look for disks', onClick: findDisks });
  const jobPhase = h('div', { class: 'pp-status', hidden: true });
  const jobText = h('span');
  const jobBar = progress(0, { brand: true });
  const jobWrap = h('div', { hidden: true, style: { 'margin-top': '12px' } }, jobBar);
  const jobDone = h('div');

  const install = card({
    title: 'Install onto an internal disk',
    body: [
      h('div', { class: 'pp-help', style: { 'margin-top': '0' } },
        'This copies the whole running system onto a disk in this machine and grows the media partition to fill it. The stick keeps working; nothing on it is changed.'),
      diskNote,
      diskList,
      h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } }, diskBtn),
      jobPhase,
      jobWrap,
      jobDone,
    ],
  });

  fill(main,
    pageHead('About', 'Version, hardware, and where the licences live.'),
    h('div', { class: 'pp-stack' },
      h('div', { class: 'pp-cards' }, facts, h('div', { class: 'pp-stack', style: { flex: '1 1 420px', 'min-width': 'min(300px, 100%)' } }, updates, licences)),
      install));

  jobPhase.append(spinner('Installing'), jobText);

  const unsubscribe = ctx.store.subscribe(showStatus);

  function showStatus(status) {
    if (!status || gone) return;
    setText(vVersion, status.version || '—');
    setText(vImage, status.image_version || 'not an image build');
    const hash = status.package_manifest_hash || '';
    setText(vManifest, hash ? hash.slice(0, 16) : '—');
    vManifest.title = hash;
    setText(vArch, status.arch || '—');
    setText(vTier, status.tier === 'low' ? 'low-power' : 'full-power');
    setText(vRung, status.navigation_rung || '—');
    setText(vID, status.device_id || '—');
    setText(vUptime, fmtDuration(status.uptime_seconds));
    const total = Number(status.media_total_bytes) || 0;
    setText(vStick, total ? `${fmtBytes(status.media_free_bytes)} free of ${fmtBytes(total)}` : '—');
    setText(vSystem, `${status.browser_state || 'unknown'}${status.display_connected ? '' : ', no display'}`);
    renderUpdate(status);
  }

  /* --------------------------------------------------------------- updates */

  function renderUpdate(status) {
    const u = status.update || {};
    const auto = 'Automatic updates are off unless you turn them on in Settings, so nothing changes until you say so.';
    switch (u.state) {
      case 'checking':
        setText(updateText, 'Checking with the release page…');
        break;
      case 'downloading':
      case 'verifying':
        setText(updateText, `Getting ${u.available || 'the new version'} ready. The screen keeps playing.`);
        break;
      case 'applying':
        setText(updateText, 'Putting the new version in place.');
        break;
      case 'restarting':
        setText(updateText, `${u.available} is in place. The device restarts now and has two minutes to come up; it puts ${status.version} back by itself if it does not.`);
        break;
      case 'rolled-back':
        setText(updateText, u.error || `An update did not come up, so the device went back to ${status.version}. It never tries that release again.`);
        break;
      case 'failed':
        setText(updateText, `The last update did not work: ${u.error || 'no reason was given'}. The device stayed on ${status.version}.`);
        break;
      default:
        setText(updateText, u.available
          ? `Running ${status.version}. ${u.available} is available. ${auto}`
          : `Running ${status.version}. ${auto}`);
    }
    const version = (offered && offered.available) || u.available || '';
    setShown(applyBtn, !!version);
    setText(applyBtn, version ? `Install ${version}` : 'Install it');
  }

  async function check() {
    checkBtn.disabled = true;
    setText(checkBtn, 'Checking…');
    fill(updateNote);
    try {
      const out = await api('POST', '/api/update/check');
      offered = out;
      let note;
      if (out.available) {
        note = banner({
          kind: 'info',
          title: `${out.available} is available`,
          body: h('div', null, out.notes || 'No release note came with it.',
            out.source ? h('div', { class: 'pp-small pp-muted', style: { 'margin-top': '6px' }, text: `from the ${out.source} release page` }) : null),
        });
      } else if (out.blocked) {
        /* A build with no signing key can never install a release, so it never
           offers one. A development build is in that state. */
        note = banner({
          kind: 'warn',
          title: 'This build cannot install updates',
          body: out.blocked,
        });
      } else {
        note = h('div', { class: 'pp-help', text: `${out.current} is the newest release. Last checked just now.` });
      }
      fill(updateNote, note);
      if (ctx.store.status) renderUpdate(ctx.store.status);
      ctx.store.refresh();
    } catch (err) {
      fill(updateNote, notInThisBuild(err) ? notYet('Checking for updates') : banner({ kind: 'danger', title: 'The check did not work', body: errorText(err) }));
    } finally {
      checkBtn.disabled = false;
      setText(checkBtn, 'Check for updates');
    }
  }

  async function installUpdate() {
    const version = (offered && offered.available) || (ctx.store.status && ctx.store.status.update.available) || 'the new version';
    const ok = await confirmDialog({
      title: `Install ${version}?`,
      body: 'The device gets the release, checks its signature and swaps it in. It reboots at the end, and it puts the old version back by itself if the new one does not come up.',
      confirm: 'Install it',
    });
    if (!ok) return;
    applyBtn.disabled = true;
    try {
      await api('POST', '/api/update/apply');
      toast('The update is on its way. The screen keeps playing until the reboot.');
      ctx.store.refresh();
    } catch (err) {
      fill(updateNote, notInThisBuild(err) ? notYet('Installing an update from this page') : banner({ kind: 'danger', title: 'The update did not start', body: errorText(err) }));
    } finally {
      applyBtn.disabled = false;
    }
  }

  /* -------------------------------------------------------- install to disk */

  async function findDisks() {
    diskBtn.disabled = true;
    fill(diskNote);
    try {
      const out = await api('GET', '/api/disks');
      const disks = out.disks || [];
      fill(diskList, disks.length
        ? disks.map((d) => diskRow(d))
        : h('div', { class: 'pp-help', text: out.error
          ? `This machine cannot install onto a disk: ${out.error}`
          : 'No other disk is in this machine. Nothing to install onto.' }));
    } catch (err) {
      fill(diskList);
      fill(diskNote, notInThisBuild(err) ? notYet('Installing onto a disk') : banner({ kind: 'danger', title: 'The disks did not load', body: errorText(err) }));
    } finally {
      diskBtn.disabled = false;
    }
  }

  function diskRow(d) {
    const blocked = !!d.too_small;
    return h('div', { class: 'dv-disk' },
      h('div', { class: 'dv-disk__name' },
        h('div', null, h('span', { class: 'pp-mono', text: d.device }), '  ', h('span', { text: d.model || '' })),
        h('div', { class: 'pp-small pp-muted pp-mono', text: fmtBytes(d.size_bytes) })),
      h('div', { class: 'pp-row' },
        d.removable ? badge('removable', 'warn') : null,
        blocked ? badge('too small', 'danger') : null),
      h('button', {
        type: 'button', class: 'pp-btn', disabled: blocked,
        text: 'Install onto this disk', onClick: () => startInstall(d),
      }));
  }

  async function startInstall(d) {
    const ok = await typedConfirm({
      title: 'Install onto this disk?',
      body: h('div', null,
        'Everything on ', h('span', { class: 'pp-mono', text: d.device }),
        ' is erased and the running system is written over it. ',
        d.removable ? 'This disk says it is removable, so read the name twice. ' : '',
        'There is no undo.'),
      expect: d.device,
      label: 'Type the name of the disk, ',
      confirm: 'Erase it and install',
    });
    if (!ok) return;

    fill(jobDone);
    setShown(jobPhase, true);
    setShown(jobWrap, true);
    setText(jobText, 'Starting…');
    jobBar.set(0);
    diskBtn.disabled = true;

    try {
      // The device path, character for character. The device checks it again, so
      // a request from a script passes the same test as a person (D54).
      await api('POST', '/api/install-to-disk', { device: d.device, confirm: d.device });
    } catch (err) {
      setShown(jobPhase, false);
      setShown(jobWrap, false);
      diskBtn.disabled = false;
      fill(jobDone, notInThisBuild(err) ? notYet('Installing onto a disk') : banner({ kind: 'danger', title: 'The install did not start', body: errorText(err) }));
      return;
    }
    /* The typed confirmation and the POST are two waits. A person who left this
       page in that time must not get a stream that nothing ever closes. */
    if (gone) return;
    watchInstall(d);
  }

  function watchInstall(d) {
    if (stream) stream.close();
    stream = sse('/api/install-to-disk/events', {
      progress: (data) => {
        if (!data) return;
        setText(jobText, `${data.phase || 'working'}${data.message ? ` — ${data.message}` : ''}`);
        jobBar.set((Number(data.percent) || 0) / 100);
      },
      done: (data) => {
        stream.close();
        stream = null;
        setShown(jobPhase, false);
        diskBtn.disabled = false;
        if (data && data.ok) {
          jobBar.set(1);
          fill(jobDone, banner({
            kind: 'paired',
            title: 'The disk is ready',
            body: h('div', null,
              h('div', null, 'Now do these three things, in this order:'),
              h('ol', { style: { margin: '6px 0 0', 'padding-left': '18px', 'line-height': '1.7' } },
                h('li', { text: 'Power the machine off.' }),
                h('li', { text: 'Take the USB stick out.' }),
                h('li', null, 'Start the machine again and let it boot from ', h('span', { class: 'pp-mono', text: d.device }), '.')),
              h('div', { style: { 'margin-top': '8px' }, text: 'Both the stick and the disk now carry the same partition labels, so taking the stick out is what tells the machine which one to use.' })),
          }));
        } else {
          setShown(jobWrap, false);
          fill(jobDone, banner({
            kind: 'danger',
            title: 'The install stopped',
            body: (data && data.error) || 'The device gave no reason. The stick is untouched and the screen keeps playing.',
          }));
        }
      },
      error: () => {
        // The browser reconnects on its own. Say nothing until the stream ends
        // for good: a reconnect in the middle is not a fault.
        setText(jobText, 'Waiting for the device…');
      },
    });
  }

  function notYet(what) {
    return banner({
      kind: 'info',
      title: `${what} is not in this build yet`,
      body: 'The device answers this route with "not implemented yet". It arrives in a later version; nothing here is broken.',
    });
  }

  return {
    destroy() {
      gone = true;
      unsubscribe();
      if (stream) stream.close();
    },
  };
}
