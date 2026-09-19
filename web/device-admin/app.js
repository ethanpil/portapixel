/* PortaPixel device admin UI.
   The page that one screen serves on its own network.

   This file holds the login view, the shell, the router and the guard that
   stops a page change while a form holds unsaved work. Each page lives in
   pages/ and gets the same small context object.
*/

import { h, fill, renderShell, route, confirmDialog, closeModal } from '/shared/ui.js';
import { api, UNAUTHORIZED } from '/shared/api.js';
import { createStore } from './store.js';
import * as dashboard from './pages/dashboard.js';
import * as playlists from './pages/playlists.js';
import * as schedule from './pages/schedule.js';
import * as settings from './pages/settings.js';
import * as activity from './pages/activity.js';
import * as about from './pages/about.js';

const NAV = [
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'playlists', label: 'Playlists' },
  { id: 'schedule', label: 'Schedule' },
  { id: 'settings', label: 'Settings' },
  { id: 'activity', label: 'Activity' },
  { id: 'about', label: 'About' },
];

const PAGES = { dashboard, playlists, schedule, settings, activity, about };

const root = document.getElementById('root');
const store = createStore();

let shell = null;
let router = null;
let page = null;          // the mounted page, or null
let at = 'dashboard';     // the page that is on the screen
let guard = null;         // () => true while the page holds unsaved work
let goingBack = false;    // true while we put the hash back after a guard stop
let signedIn = false;

/* The context that every page gets. It is small on purpose: a page reads the
   status from the store, and a page with a form holds the guard. Every link
   between pages is a plain href, so there is nothing else to hand over. */
const ctx = {
  store,
  /** Hold the page. `fn` gives true while there is something unsaved. */
  setGuard(fn) { guard = fn; },
  clearGuard() { guard = null; },
};

boot();

async function boot() {
  let authenticated = false;
  let note = '';
  try {
    authenticated = !!(await api('GET', '/api/session')).authenticated;
  } catch (err) {
    // A 421 or a network fault is not "your session ended". Say which one it was.
    authenticated = false;
    if (err.status !== 401) {
      note = err.status === 421
        ? 'This device does not answer to this address. Use its name or its IP address on the network.'
        : `The device did not answer: ${err.message}`;
    }
  }
  if (authenticated) start(); else showLogin(note);
}

/* ------------------------------------------------------------------- login */

async function showLogin(note) {
  signedIn = false;
  guard = null;
  store.stop();
  closeModal();
  // Take the page down before the form goes up. A page that stayed mounted
  // would keep its own timers running and every one of them would ask again
  // and be refused again.
  if (page && page.destroy) page.destroy();
  page = null;

  // /api/status needs no session, so the card can name the device that the
  // user is signing in to.
  let where = '';
  try {
    const s = await api('GET', '/api/status');
    where = s.mdns_name || s.name || '';
  } catch { /* the card works without it */ }

  const password = h('input', {
    class: 'pp-input', type: 'password', autocomplete: 'current-password',
    name: 'password', required: true, autofocus: true,
  });
  const error = h('div', { class: 'pp-error', hidden: true });
  const button = h('button', { class: 'pp-btn pp-btn--primary pp-btn--block', style: { 'margin-top': '18px' }, text: 'Sign in' });

  const form = h('form', {
    class: 'pp-login__card',
    onSubmit: async (e) => {
      e.preventDefault();
      button.disabled = true;
      error.hidden = true;
      try {
        await api('POST', '/api/login', { password: password.value });
        start();
      } catch (err) {
        error.textContent = err.message || 'That did not work.';
        error.hidden = false;
        password.value = '';
        password.focus();
      } finally {
        button.disabled = false;
      }
    },
  },
    h('div', { class: 'pp-login__brand' },
      h('span', { class: 'pp-brand__mark' }),
      h('span', { class: 'pp-brand__name', text: 'PortaPixel' })),
    h('div', { class: 'pp-lead', style: { margin: '0 0 18px' } },
      where ? ['Sign in to ', h('span', { class: 'pp-mono', text: where })] : 'Sign in to this screen'),
    note ? h('div', { class: 'pp-banner pp-banner--warn', style: { 'margin-bottom': '14px' } },
      h('div', { class: 'pp-banner__text' }, h('div', { class: 'pp-banner__body', text: note }))) : null,
    h('label', { class: 'pp-label pp-field' }, 'Password', password),
    error,
    button,
    h('div', { class: 'pp-help', text: 'One administrator. The password lives in the settings file on the stick.' }));

  fill(root, h('div', { class: 'pp-login' }, form));
  password.focus();
}

/* ------------------------------------------------------------------- shell */

function start() {
  signedIn = true;

  // A second sign-in puts the shell that is already built back on the screen.
  // Building it again would add a second router, and then every page change
  // would mount two pages.
  if (shell) {
    fill(root, shell.el);
    store.start();
    router.refresh();
    return;
  }

  const nameEl = h('span');
  const idEl = h('span', { class: 'pp-mono pp-muted' });
  const versionEl = h('div', { class: 'pp-mono' });
  const hardwareEl = h('div');

  shell = renderShell({
    subtitle: [nameEl, idEl],
    tools: [h('button', {
      type: 'button', class: 'pp-btn pp-btn--sm', text: 'Sign out',
      onClick: async () => {
        try { await api('POST', '/api/logout'); } catch { /* the view goes back anyway */ }
        showLogin();
      },
    })],
    nav: NAV,
    footer: [versionEl, hardwareEl],
  });
  fill(root, shell.el);

  // The name, the version and the hardware line come from the same report that
  // the pages read.
  store.subscribe((status) => {
    if (!status) return;
    nameEl.textContent = status.name || 'This screen';
    idEl.textContent = status.device_id || '';
    versionEl.textContent = status.version ? `v${status.version}` : '';
    const bits = [status.arch, status.tier === 'low' ? 'low-power' : 'full-power'].filter(Boolean);
    hardwareEl.textContent = bits.join(' · ');
    document.title = status.name ? `${status.name} — PortaPixel` : 'PortaPixel';
  });
  store.start();

  if (!location.hash || location.hash === '#' || location.hash === '#/') {
    // Put the route in the address before the router reads it, so the sidebar
    // marks the page that is on the screen.
    history.replaceState(null, '', '#/dashboard');
  }

  router = route({
    '': enter('dashboard'),
    dashboard: enter('dashboard'),
    playlists: enter('playlists'),
    schedule: enter('schedule'),
    settings: enter('settings'),
    activity: enter('activity'),
    about: enter('about'),
  });
}

/* Mount one page. The guard runs first: a page with unsaved work puts the
   address back and asks before it lets the user leave. */
function enter(name) {
  return () => {
    if (goingBack) { goingBack = false; return; }
    if (guard && guard() && name !== at) { askToLeave(); return; }

    closeModal();
    if (page && page.destroy) page.destroy();
    page = null;
    guard = null;
    at = name;
    fill(shell.main);
    scrollTo(0, 0);
    page = PAGES[name].mount(shell.main, ctx) || null;
  };
}

function askToLeave() {
  const wanted = location.hash;
  goingBack = true;
  location.hash = `#/${at}`;
  confirmDialog({
    title: 'Leave without saving?',
    body: 'This page holds changes that are not saved yet. Leaving drops them.',
    confirm: 'Leave anyway',
    cancel: 'Stay here',
    kind: 'danger',
  }).then((ok) => {
    if (!ok) return;
    guard = null;
    location.hash = wanted;
  });
}

/* A reload or a closed tab is the other way out of an unsaved page. */
addEventListener('beforeunload', (e) => {
  if (guard && guard()) { e.preventDefault(); e.returnValue = ''; }
});

/* The session ended while the page was open: stop the poller and ask again. */
addEventListener(UNAUTHORIZED, () => {
  if (!signedIn) return;
  showLogin('The session ended. Sign in again.');
});
