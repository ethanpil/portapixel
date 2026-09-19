/* Add screens: the three ways a screen joins this fleet, and the tokens that
   make the first one work.

   A token value is shown one time, at the moment it is made. The server keeps
   only its hash, so this page says so loudly and offers a copy button.
*/

import {
  h, fill, toast, banner, confirmDialog, statusDot, fmtAgo, card, pageHead,
  errorText, cell,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { fmtDate, isNever, copyText } from '../util.js';

export function mount(main, ctx) {
  let tokens = [];
  let groups = [];
  let gone = false;

  const secretSlot = h('div');
  const listSlot = h('div');
  const formSlot = h('div');
  const pendingSlot = h('div');

  fill(main,
    h('a', { class: 'pp-back', href: '#/screens', text: '← All screens' }),
    pageHead('Add screens', 'A screen joins once and then checks in on its own. Nothing here reaches out to a screen.'),
    card({
      title: 'The three ways in',
      body: [
        h('div', { class: 'pp-small', style: { 'line-height': '1.6' } },
          h('div', null,
            h('b', { text: 'An enrollment token' }),
            ' goes into one line of the configuration file and then onto any number of cards. Each card registers itself under its own ID and gets a token of its own, so the cards stay identical and interchangeable.'),
          h('div', { style: { 'margin-top': '8px' } },
            h('b', { text: 'A pairing code' }),
            ' is the way in with no file to edit: a screen with only this address shows six characters, and you let it in from the Screens page.'),
          h('div', { style: { 'margin-top': '8px' } },
            h('b', { text: 'A device token' }),
            ' is one token pasted onto one card by hand. It is the right choice for one screen and a slow way to do fifty.')),
      ],
    }),
    h('div', { style: { height: '14px' } }),
    pendingSlot,
    secretSlot,
    formSlot,
    h('div', { style: { height: '14px' } }),
    listSlot);

  /* ------------------------------------------------------------------ load */

  async function load() {
    try {
      const [gotTokens, gotGroups] = await Promise.all([
        api('GET', '/api/admin/tokens'),
        api('GET', '/api/admin/groups'),
      ]);
      if (gone) return;
      tokens = gotTokens.tokens || [];
      groups = gotGroups.groups || [];
      paintGroupSelect();
      paintList();
    } catch (err) {
      fill(listSlot, banner({ kind: 'danger', title: 'The tokens did not load', body: errorText(err) }));
    }
  }

  /* The screens that wait with a code. They are approved on the Screens page;
     this card is only a pointer, so the two places never disagree. */
  function paintPending({ devices }) {
    const waiting = (devices || []).filter((d) => d.state === 'pending');
    if (waiting.length === 0) { fill(pendingSlot); return; }
    fill(pendingSlot, banner({
      kind: 'paired',
      title: waiting.length === 1
        ? 'One screen is waiting to be let in'
        : `${waiting.length} screens are waiting to be let in`,
      body: h('div', null,
        waiting.map((d) => h('div', { class: 'pp-status', style: { 'margin-top': '4px' } },
          statusDot('alert'),
          h('span', { class: 'pp-mono', text: d.pending_code }),
          h('span', { text: `— ${d.name || d.id}, asked ${fmtAgo(d.created_at)}` })))),
      actions: [h('a', { class: 'pp-btn', href: '#/screens', text: 'Let them in' })],
    }));
  }

  /* --------------------------------------------------------------- the form */

  const nameInput = h('input', { class: 'pp-input', type: 'text', maxlength: '60', placeholder: 'the first batch' });
  const modeSelect = h('select', { class: 'pp-select' },
    h('option', { value: 'auto', text: 'Pair themselves' }),
    h('option', { value: 'pending', text: 'Wait for approval' }));
  const groupSelect = h('select', { class: 'pp-select' });
  const expiryInput = h('input', { class: 'pp-input', type: 'date' });
  const usesInput = h('input', { class: 'pp-input pp-input--mono', type: 'number', min: '0', step: '1', value: '0' });
  const modeHelp = h('div', { class: 'pp-help' });
  const makeBtn = h('button', { type: 'button', class: 'pp-btn pp-btn--primary', text: 'Make a token', onClick: makeToken });

  modeSelect.addEventListener('change', paintModeHelp);
  paintModeHelp();

  function paintModeHelp() {
    modeHelp.textContent = modeSelect.value === 'auto'
      ? 'A card with this token joins the fleet by itself at its first boot and starts playing what its group plays.'
      : 'A card with this token waits in the Screens list with a six-character code until you let it in. It keeps playing what it holds meanwhile.';
  }

  function drawForm() {
    fill(formSlot, card({
      title: 'Make an enrollment token',
      body: [
        h('div', { class: 'pp-fields' },
          h('label', { class: 'pp-label pp-field' }, 'A name for this batch', nameInput,
            h('div', { class: 'pp-help', text: 'Only for this list, so you can tell two batches apart.' })),
          h('label', { class: 'pp-label pp-field' }, 'The cards should', modeSelect)),
        modeHelp,
        h('div', { class: 'pp-fields', style: { 'margin-top': '14px' } },
          h('label', { class: 'pp-label pp-field' }, 'Put them in this group', groupSelect),
          h('label', { class: 'pp-label pp-field' }, 'Stop working after', expiryInput,
            h('div', { class: 'pp-help', text: 'Leave it empty for a token with no end.' })),
          h('label', { class: 'pp-label pp-field' }, 'How many cards', usesInput,
            h('div', { class: 'pp-help', text: '0 means no limit.' }))),
        h('div', { style: { 'margin-top': '16px' } }, makeBtn),
      ],
    }));
  }

  function paintGroupSelect() {
    fill(groupSelect,
      h('option', { value: '0', text: 'No group' }),
      groups.map((g) => h('option', { value: String(g.id), text: g.name })));
  }

  async function makeToken() {
    makeBtn.disabled = true;
    const body = {
      name: nameInput.value.trim() || 'a batch of cards',
      mode: modeSelect.value,
      group_id: Number(groupSelect.value),
      max_uses: Number(usesInput.value) || 0,
    };
    if (expiryInput.value) {
      // The field gives a date; the route takes a time. The end of that day is
      // what a person means by "stop working after this date".
      body.expires_at = `${expiryInput.value}T23:59:59Z`;
    }
    try {
      const made = await api('POST', '/api/admin/tokens', body);
      showSecret(made, body);
      nameInput.value = '';
      expiryInput.value = '';
      await load();
    } catch (err) {
      // A toast replaces the one before it, so every field goes in one sentence.
      const named = (err.fields || []).map((f) => f.message).join(' ');
      toast(named || errorText(err), 'danger');
    } finally {
      makeBtn.disabled = false;
    }
  }

  /* The one time that the token is on the screen. */
  function showSecret(made, body) {
    const copyBtn = (label, text) => h('button', {
      type: 'button', class: 'pp-btn pp-btn--sm', text: label,
      onClick: async () => {
        toast(await copyText(text) ? 'Copied.' : 'The browser would not copy it. Select it and copy it by hand.',
          'info');
      },
    });

    fill(secretSlot, h('div', { class: 'pp-card', style: { 'margin-bottom': '14px', 'border-color': 'var(--pp-brand-tint-border)' } },
      h('div', { class: 'pp-note', style: { 'font-weight': '600' } }, 'Copy this now: it is not shown again'),
      h('div', { class: 'pp-card__body' },
        h('div', { class: 'pp-small' },
          'The server keeps only a hash of the token, so this page cannot show it a second time. Make another token if this one gets away.'),
        h('div', { style: { 'margin-top': '14px' } },
          h('div', { class: 'pp-label' }, 'The token'),
          h('pre', { class: 'sv-secret', text: made.token }),
          h('div', { class: 'pp-btns', style: { 'margin-top': '8px' } }, copyBtn('Copy the token', made.token))),
        h('div', { style: { 'margin-top': '16px' } },
          h('div', { class: 'pp-label' }, 'Ready to paste into portapixel.toml on the card'),
          h('pre', { class: 'sv-secret', text: made.toml }),
          h('div', { class: 'pp-btns', style: { 'margin-top': '8px' } },
            copyBtn('Copy the block', made.toml),
            h('button', {
              type: 'button', class: 'pp-btn pp-btn--sm', text: 'Hide it',
              onClick: () => fill(secretSlot),
            }))),
        h('div', { class: 'pp-help' },
          body.mode === 'auto'
            ? 'Put that block in the file on as many cards as you like. Each one joins by itself at its first boot.'
            : 'Put that block in the file on as many cards as you like. Each one then waits in the Screens list with a code.'))));
    secretSlot.scrollIntoView({ block: 'nearest' });
  }

  /* -------------------------------------------------------------- the list */

  function paintList() {
    const inner = h('div', { class: 'pp-table__inner' });
    inner.append(h('div', { class: 'pp-table__head' },
      cell({ grow: true }, 'Batch'),
      cell({ width: '92px' }, 'Starts as'),
      cell({ width: '120px' }, 'Group'),
      cell({ width: '100px' }, 'Used'),
      cell({ width: '130px' }, 'Stops working'),
      cell({ width: '120px' }, '')));

    if (tokens.length === 0) {
      inner.append(h('div', { class: 'pp-empty' },
        h('div', { class: 'pp-empty__title', text: 'No enrollment tokens yet' }),
        h('div', { class: 'pp-empty__body', text: 'Make one above for a batch of cards, or let a screen show its own code and approve it from the Screens page.' })));
    } else {
      for (const t of tokens) inner.append(tokenRow(t));
    }

    fill(listSlot, h('div', { class: 'pp-card' },
      h('div', { class: 'pp-table' }, inner),
      h('div', { class: 'pp-card__foot' },
        h('span', { text: 'Revoking a token stops new cards. A card that already joined keeps its own token and stays in the fleet.' }))));
  }

  function tokenRow(t) {
    const group = groups.find((g) => g.id === t.group_id);
    const dead = t.revoked || (!isNever(t.expires_at) && Date.parse(t.expires_at) < Date.now())
      || (t.max_uses > 0 && t.uses >= t.max_uses);

    const actions = h('div', { class: 'pp-btns' });
    if (!t.revoked) {
      actions.append(h('button', {
        type: 'button', class: 'pp-btn pp-btn--sm', text: 'Revoke', onClick: () => revoke(t),
      }));
    }
    if (dead) {
      actions.append(h('button', {
        type: 'button', class: 'pp-btn pp-btn--sm pp-btn--danger-outline', text: 'Remove the row',
        onClick: () => remove(t),
      }));
    }

    return h('div', { class: 'pp-table__row' },
      cell({ grow: true }, h('span', null,
        h('span', { style: { display: 'block' }, text: t.name || 'a batch of cards' }),
        h('span', { class: 'pp-mono pp-muted', style: { 'font-size': '11.5px' }, text: `${t.prefix}… · made ${fmtDate(t.created_at)}` }))),
      cell({ width: '92px' }, h('span', { class: 'pp-small', text: t.mode === 'auto' ? 'paired' : 'waiting' })),
      cell({ width: '120px' }, h('span', { class: 'pp-small', text: group ? group.name : 'no group' })),
      cell({ width: '100px' }, h('span', { class: 'pp-mono', style: { 'font-size': '12px' }, text: t.max_uses ? `${t.uses} of ${t.max_uses}` : String(t.uses) })),
      cell({ width: '130px' }, h('span', { class: 'pp-small', text: expiryWords(t) })),
      cell({ width: '120px' }, actions));
  }

  function expiryWords(t) {
    if (t.revoked) return 'revoked';
    if (t.max_uses > 0 && t.uses >= t.max_uses) return 'all used up';
    if (isNever(t.expires_at)) return 'no end';
    const when = Date.parse(t.expires_at);
    return when < Date.now() ? `ended ${fmtDate(t.expires_at)}` : fmtDate(t.expires_at);
  }

  async function revoke(t) {
    const ok = await confirmDialog({
      title: `Revoke ${t.name || 'this token'}?`,
      body: h('div', null,
        h('div', null, 'No new card can join with it. Cards that already joined keep their own tokens and stay in the fleet.'),
        t.uses ? h('div', { style: { 'margin-top': '8px' } },
          `${t.uses} ${t.uses === 1 ? 'card has' : 'cards have'} used it so far. `,
          h('a', { href: '#/screens', text: 'The Screens page' }), ' lists them.') : null),
      confirm: 'Revoke it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('POST', `/api/admin/tokens/${t.id}/revoke`);
      toast('The token is revoked.');
      await load();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  async function remove(t) {
    const ok = await confirmDialog({
      title: 'Remove this row?',
      body: 'The token is already dead, so nothing changes for any screen. Only the row goes away.',
      confirm: 'Remove it',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await api('DELETE', `/api/admin/tokens/${t.id}`);
      toast('The row is gone.');
      await load();
    } catch (err) {
      toast(errorText(err), 'danger');
    }
  }

  /* The page starts here, after every part above is built. */
  drawForm();
  load();
  const unsubscribe = ctx.store.subscribe(paintPending);

  return {
    destroy() {
      gone = true;
      unsubscribe();
    },
  };
}
