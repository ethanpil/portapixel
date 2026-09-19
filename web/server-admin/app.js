/* PortaPixel control server admin UI.
   The page that manages a fleet of screens.

   This file holds the login view, the shell, the router and the guard that
   stops a page change while a form holds unsaved work. Each page lives in
   pages/ and gets the same small context object.
*/

import {
  h, fill, renderShell, route, confirmDialog, closeModal, statusDot, setText,
} from '/shared/ui.js';
import { api, UNAUTHORIZED } from '/shared/api.js';
import { createStore } from './store.js';
import * as screens from './pages/screens.js';
import * as screen from './pages/screen.js';
import * as addScreens from './pages/add-screens.js';
import * as groups from './pages/groups.js';
import * as playlists from './pages/playlists.js';
import * as media from './pages/media.js';
import * as versions from './pages/versions.js';
import * as health from './pages/health.js';
import * as settings from './pages/settings.js';

const NAV = [
  { id: 'screens', label: 'Screens' },
  { id: 'groups', label: 'Groups and times' },
  { id: 'playlists', label: 'Playlists' },
  { id: 'media', label: 'Media' },
  { id: 'versions', label: 'Versions' },
  { id: 'health', label: 'Server health' },
  { id: 'settings', label: 'Settings' },
];

const root = document.getElementById('root');
const store = createStore();

let shell = null;
let router = null;
let page = null;          // the mounted page, or null
let at = '';              // the route pattern that is on the screen
let atHash = '';          // the address of the page that is on the screen
let guard = null;         // () => true while the page holds unsaved work
let goingBack = false;    // true while we put the hash back after a guard stop
let signedIn = false;
let session = {};         // the answer of GET /api/admin/session

/* The context that every page gets. It is small on purpose: a page reads the
   fleet from the store, and a page with a form holds the guard. Every link
   between pages is a plain href, so there is nothing else to hand over. */
const ctx = {
  store,
  /** The server name, the version, the public URL and the weak-password flag. */
  get session() { return session; },
  /** Hold the page. `fn` gives true while there is something unsaved. */
  setGuard(fn) { guard = fn; },
  clearGuard() { guard = null; },
  /** Read the session again. The settings page calls it after a save, so the
      top bar and the password banner follow. */
  async reloadSession() {
    try {
      session = await api('GET', '/api/admin/session');
      paintSession();
    } catch { /* the page keeps the values it has */ }
  },
};

boot();

async function boot() {
  let note = '';
  try {
    session = await api('GET', '/api/admin/session');
  } catch (err) {
    // A 421, a 500 or a network fault is not "your session ended". Say which one
    // it was, or the admin retypes a password that was never wrong.
    session = {};
    note = err.status === 421
      ? 'This server does not answer to this host name. Open it at the address in its settings.'
      : `The server did not answer: ${err.message}`;
  }
  if (session.logged_in) start(); else showLogin(note);
}

/* ------------------------------------------------------------------- login */

function showLogin(note) {
  signedIn = false;
  guard = null;
  store.stop();
  closeModal();
  // Take the page down before the form goes up. A page that stayed mounted
  // would keep its own timers running and every one of them would ask again
  // and be refused again.
  if (page && page.destroy) page.destroy();
  page = null;

  const password = h('input', {
    class: 'pp-input', type: 'password', autocomplete: 'current-password',
    name: 'password', required: true, autofocus: true,
  });
  const error = h('div', { class: 'pp-error', hidden: true });
  const button = h('button', {
    class: 'pp-btn pp-btn--primary pp-btn--block',
    style: { 'margin-top': '18px' }, text: 'Sign in',
  });

  const form = h('form', {
    class: 'pp-login__card',
    onSubmit: async (e) => {
      e.preventDefault();
      button.disabled = true;
      error.hidden = true;
      try {
        session = await api('POST', '/api/admin/login', { password: password.value });
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
      h('span', { class: 'pp-brand__name', text: 'PortaPixel' }),
      h('span', { class: 'pp-brand__sub', text: 'Control' })),
    h('div', { class: 'pp-lead', style: { margin: '0 0 18px' } },
      session.server_name ? ['Sign in to ', h('b', { text: session.server_name })] : 'Sign in to this server'),
    note ? h('div', { class: 'pp-banner pp-banner--warn', style: { 'margin-bottom': '14px' } },
      h('div', { class: 'pp-banner__text' }, h('div', { class: 'pp-banner__body', text: note }))) : null,
    h('label', { class: 'pp-label pp-field' }, 'Password', password),
    error,
    button,
    h('div', { class: 'pp-help', text: 'One administrator. The first run printed the password once; you can change it on the Settings page.' }));

  fill(root, h('div', { class: 'pp-login' }, form));
  password.focus();
}

/* ------------------------------------------------------------------- shell */

const hostEl = h('span', { class: 'pp-mono pp-muted' });
const totalsEl = h('div');

function start() {
  signedIn = true;

  // A second sign-in puts the shell that is already built back on the screen.
  // Building it again would add a second router, and then every page change
  // would mount two pages.
  if (shell) {
    fill(root, shell.el);
    paintSession();
    store.start();
    router.refresh();
    return;
  }

  shell = renderShell({
    brandSub: 'Control',
    subtitle: [hostEl],
    tools: [
      h('span', { class: 'pp-small pp-muted pp-hide-sm', text: 'Signed in as admin' }),
      h('button', {
        type: 'button', class: 'pp-btn pp-btn--sm', text: 'Sign out',
        onClick: async () => {
          try { await api('POST', '/api/admin/logout'); } catch { /* the view goes back anyway */ }
          showLogin();
        },
      }),
    ],
    nav: NAV,
    footer: [totalsEl],
    wide: true,
  });
  fill(root, shell.el);
  paintSession();

  store.subscribe(paintTotals);
  store.start();

  if (!location.hash || location.hash === '#' || location.hash === '#/') {
    // Put the route in the address before the router reads it, so the sidebar
    // marks the page that is on the screen.
    history.replaceState(null, '', '#/screens');
  }

  router = route({
    '': enter('screens', screens),
    screens: enter('screens', screens),
    // This key comes before the one with the colon, so "add" is a page and not
    // the ID of a screen.
    'screens/add': enter('screens/add', addScreens),
    'screens/:id': enter('screens/:id', screen),
    groups: enter('groups', groups),
    playlists: enter('playlists', playlists),
    media: enter('media', media),
    versions: enter('versions', versions),
    health: enter('health', health),
    settings: enter('settings', settings),
  });
}

/* The top bar names the host that this page manages, and the title says which
   server it is: an admin with two fleets keeps two tabs open. */
function paintSession() {
  const host = hostName(session.public_url);
  setText(hostEl, host || location.host);
  document.title = session.server_name ? `${session.server_name} — PortaPixel Control` : 'PortaPixel Control';
}

function hostName(url) {
  if (!url) return '';
  try { return new URL(url).host; } catch { return ''; }
}

/* The sidebar footer is the fleet in four lines. It comes from the same poll as
   the fleet list, so the two never disagree. */
function paintTotals({ totals }) {
  if (!totals) return;
  const line = (kind, n, word) => h('div', { class: 'pp-status', style: { gap: '6px' } },
    statusDot(kind), h('span', { text: `${n} ${word}` }));
  fill(totalsEl,
    h('div', { text: `${totals.screens} ${totals.screens === 1 ? 'screen' : 'screens'}` }),
    line('ok', totals.checked_in, 'checked in'),
    line('quiet', totals.quiet, 'quiet'),
    line('alert', totals.needs_a_look, 'needs a look'));
}

/* Mount one page. The guard runs first: a page with unsaved work puts the
   address back and asks before it lets the user leave. */
function enter(name, module) {
  return (arg) => {
    if (goingBack) { goingBack = false; return; }
    /* Two different screens both match the pattern "screens/:id", so a guard that
       compared the pattern let unsaved rules go without a word. Compare the
       address instead. */
    if (guard && guard() && location.hash !== atHash) { askToLeave(); return; }

    closeModal();
    if (page && page.destroy) page.destroy();
    page = null;
    guard = null;
    at = name;
    atHash = location.hash;
    fill(shell.main);
    scrollTo(0, 0);
    page = module.mount(shell.main, ctx, arg) || null;
  };
}

function askToLeave() {
  const wanted = location.hash;
  goingBack = true;
  location.hash = atHash;
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
