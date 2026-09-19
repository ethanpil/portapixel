/* Settings: the few values that belong to the server itself.

   One administrator, one site. Everything about content lives on the other
   pages; this page holds the name, the address, the check-in interval and the
   password.
*/

import { h, fill, toast, banner, modal } from '/shared/ui.js';
import { api } from '/shared/api.js';
import { card, pageHead, errorText, setText } from '../util.js';

export function mount(main, ctx) {
  let settings = null;
  let dirty = false;
  let gone = false;

  const body = h('div', { class: 'pp-stack' });
  const banners = h('div');

  fill(main,
    pageHead('Settings',
      'One administrator, one site. Screens hold their own copy of everything, so this server going down never blanks a screen.'),
    banners, body);

  load();

  async function load() {
    try {
      settings = await api('GET', '/api/admin/settings');
      if (gone) return;
      draw();
    } catch (err) {
      fill(body, banner({ kind: 'danger', title: 'The settings did not load', body: errorText(err) }));
    }
  }

  /* --------------------------------------------------------------- the form */

  const nameInput = h('input', { class: 'pp-input', type: 'text', maxlength: '80', onInput: touch });
  const urlInput = h('input', { class: 'pp-input pp-input--mono', type: 'url', placeholder: 'https://signage.example.com', onInput: touch });
  const pollInput = h('input', { class: 'pp-input pp-input--mono', type: 'number', min: '5', max: '86400', onInput: touch });
  const pollHelp = h('div', { class: 'pp-help' });
  const quietHelp = h('div', { class: 'pp-help' });
  const dirtyNote = h('span');
  const saveBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Save settings', disabled: true, onClick: save });
  const discardBtn = h('button', { type: 'button', class: 'pp-btn', text: 'Discard', disabled: true, onClick: () => { paintForm(); } });

  function draw() {
    paintBanners();
    fill(body,
      card({
        title: 'Who can get in',
        body: passwordCard(),
      }),
      card({
        title: 'This site',
        body: [
          h('label', { class: 'pp-label pp-field' }, 'Name', nameInput,
            h('div', { class: 'pp-help', text: 'It goes in the top bar and in the title of this page. Screens report it back in their own pages.' })),
          h('label', { class: 'pp-label pp-field' }, 'The address that screens use', urlInput,
            h('div', { class: 'pp-help' },
              'Every screen holds this address, and the server only answers a browser that asks for this host name. ',
              h('b', { text: 'A change here needs a restart' }), ', because the allowlist is built when the server starts.')),
        ],
      }),
      card({
        title: 'How often screens check in',
        body: [
          h('label', { class: 'pp-label pp-field', style: { 'max-width': '200px' } }, 'Every (seconds)', pollInput),
          pollHelp,
          quietHelp,
        ],
      }),
      card({
        title: 'New screens',
        body: [
          h('div', { class: 'pp-small' },
            'There is no switch here. Each enrollment token says what the cards made with it do: a token in ',
            h('b', { text: 'auto' }), ' mode lets a card pair itself, and a token in ',
            h('b', { text: 'wait for approval' }), ' mode makes it wait in the list with a code.'),
          h('div', { class: 'pp-help', text: 'That is finer than one setting for the whole server: one batch of cards can pair itself while another batch waits.' }),
          h('div', { class: 'pp-btns', style: { 'margin-top': '12px' } },
            h('a', { class: 'pp-btn', href: '#/screens/add', text: 'Add screens' })),
        ],
      }),
      card({
        title: 'Licences',
        body: [
          h('div', { class: 'pp-small' }, 'PortaPixel is MIT licensed. This server is one Go binary with the admin pages inside it.'),
          h('div', { class: 'pp-help' },
            'The licences of everything that is built in are in the file ',
            h('span', { class: 'pp-mono', text: 'LICENSES-THIRD-PARTY.md' }),
            ' of the release, beside the binary.'),
        ],
      }),
      h('div', { class: 'pp-savebar' },
        h('span', null, 'The name and the interval take effect at once. ', dirtyNote),
        h('div', { class: 'pp-btns' }, discardBtn, saveBtn)));

    paintForm();
  }

  function paintBanners() {
    fill(banners, ctx.session.weak_password ? banner({
      kind: 'danger',
      title: 'Change the administrator password',
      body: 'This server still uses the password that its first run made and printed. Anybody who has that line of output, or who finds it in a log, can change what every screen shows.',
    }) : null);
  }

  function paintForm() {
    nameInput.value = settings.server_name || '';
    urlInput.value = settings.public_url || '';
    pollInput.value = String(settings.default_poll_seconds || 60);
    dirty = false;
    touch();
  }

  function touch() {
    dirty = nameInput.value !== (settings.server_name || '')
      || urlInput.value !== (settings.public_url || '')
      || Number(pollInput.value) !== Number(settings.default_poll_seconds);
    saveBtn.disabled = !dirty;
    discardBtn.disabled = !dirty;
    setText(dirtyNote, dirty ? 'Not saved yet.' : '');
    ctx.setGuard(() => dirty);
    paintMath();
  }

  /* The sentence that the wireframe computes from the other fields. */
  function paintMath() {
    const seconds = Number(pollInput.value) || 0;
    const screens = (ctx.store.totals && ctx.store.totals.screens) || 0;
    if (seconds < 5 || seconds > 86400) {
      setText(pollHelp, 'It must be between 5 and 86400 seconds.');
    } else if (screens === 0) {
      setText(pollHelp, 'No screen has joined yet. Every screen will ask this server for its manifest on this interval.');
    } else {
      const rate = screens / seconds;
      setText(pollHelp, `${screens} ${screens === 1 ? 'screen' : 'screens'} at ${seconds} seconds is about ${rate.toFixed(rate < 1 ? 2 : 1)} check-ins a second. Stretch the interval if this host is small.`);
    }
    const quiet = Math.round(seconds * 2.5);
    setText(quietHelp, seconds >= 5
      ? `A screen counts as quiet when it has not called for ${quiet} seconds, which is two and a half intervals. That one rule decides the grey dots, so there is no second setting for it.`
      : '');
  }

  async function save() {
    saveBtn.disabled = true;
    clearFieldErrors();
    try {
      const out = await api('PUT', '/api/admin/settings', {
        server_name: nameInput.value.trim(),
        public_url: urlInput.value.trim(),
        default_poll_seconds: Number(pollInput.value),
      });
      settings = out.settings || settings;
      dirty = false;
      ctx.clearGuard();
      await ctx.reloadSession();
      paintForm();
      toast(out.restart_needed
        ? 'Saved. The new address needs a restart of the server before a browser can use it.'
        : 'Settings saved.');
      if (out.restart_needed) {
        modal({
          title: 'Restart the server for the new address',
          body: h('div', null,
            h('div', null, 'The list of host names that this server answers is built when it starts, so the new address does not work yet.'),
            h('div', { style: { 'margin-top': '8px' } }, 'Screens are not affected: each one keeps the address that it already holds, and it keeps playing either way.')),
          actions: [{ label: 'All right', value: true, kind: 'primary' }],
        });
      }
    } catch (err) {
      if (err.fields) showFieldErrors(err.fields);
      else toast(errorText(err), 'danger');
      touch();
    }
  }

  function clearFieldErrors() {
    for (const el of [nameInput, urlInput, pollInput]) {
      el.classList.remove('sv-input--bad');
      if (el.errorSlot) { el.errorSlot.remove(); el.errorSlot = null; }
    }
  }

  function showFieldErrors(list) {
    const map = { server_name: nameInput, public_url: urlInput, default_poll_seconds: pollInput };
    for (const f of list) {
      const el = map[f.field];
      if (!el) { toast(`${f.field}: ${f.message}`, 'danger'); continue; }
      el.classList.add('sv-input--bad');
      el.errorSlot = h('div', { class: 'pp-error', text: f.message });
      el.after(el.errorSlot);
    }
  }

  /* ------------------------------------------------------------- password */

  function passwordCard() {
    const current = h('input', { class: 'pp-input', type: 'password', autocomplete: 'current-password' });
    const next = h('input', { class: 'pp-input', type: 'password', autocomplete: 'new-password', placeholder: 'at least 8 characters' });
    const again = h('input', { class: 'pp-input', type: 'password', autocomplete: 'new-password' });
    const error = h('div', { class: 'pp-error', hidden: true });
    const button = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Set the password' });

    const fail = (message) => {
      error.textContent = message;
      error.hidden = false;
    };

    button.addEventListener('click', async () => {
      error.hidden = true;
      if (!current.value) { fail('Type the password that you use now.'); current.focus(); return; }
      if (next.value.length < 8) { fail('The new password needs eight characters or more.'); next.focus(); return; }
      if (next.value !== again.value) { fail('The two new passwords are not the same.'); again.focus(); return; }
      button.disabled = true;
      // The route that sets a password does not ask for the old one, so the page
      // checks it with a sign-in first. A wrong guess costs one of the five
      // attempts a minute that the limiter allows.
      //
      // This one call goes through fetch and not through api(): a 401 from api()
      // fires the unauthorized event, and a wrong guess here would then throw the
      // admin out of a session that is still good.
      let checked = 0;
      try {
        const res = await fetch('/api/admin/login', {
          method: 'POST', credentials: 'same-origin',
          headers: { 'X-PortaPixel': '1', 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ password: current.value }),
        });
        checked = res.status;
      } catch {
        checked = 0;
      }
      if (checked !== 200) {
        button.disabled = false;
        fail(checked === 429
          ? 'Too many attempts from this address. Wait a minute.'
          : 'That is not the password that you use now.');
        current.focus();
        return;
      }
      try {
        await api('POST', '/api/admin/password', { password: next.value });
        current.value = next.value = again.value = '';
        toast('The password is set. The session stays open.');
        await ctx.reloadSession();
        paintBanners();
      } catch (err) {
        fail(errorText(err));
      } finally {
        button.disabled = false;
      }
    });

    return [
      h('div', { class: 'pp-help', style: { 'margin-top': '0' }, text: 'There is one account, called admin. A new password takes effect at once and this browser stays signed in.' }),
      h('label', { class: 'pp-label pp-field', style: { 'max-width': '320px' } }, 'The password you use now', current),
      h('div', { class: 'pp-fields', style: { 'margin-top': '14px' } },
        h('label', { class: 'pp-label pp-field' }, 'New password', next),
        h('label', { class: 'pp-label pp-field' }, 'The same one again', again)),
      error,
      h('div', { style: { 'margin-top': '14px' } }, button),
    ];
  }

  return {
    destroy() { gone = true; },
  };
}
