/* Sending a command to one screen or to a whole group.

   Nothing here reaches out to a screen. A command waits in a queue and the
   screen picks it up at its next check-in, so every message in this file says
   "at the next check-in" and never "now".
*/

import {
  h, toast, modal, confirmDialog, errorText,
} from '/shared/ui.js';
import { api } from '/shared/api.js';
import { COMMANDS, GROUP_COMMANDS, renameRefusal } from './util.js';

/** Ask, then queue one command for one screen. Returns true when it is queued.
    screenName is the name that the screen has now. waiting is the name that a
    queued rename waits with, or ''. */
export async function sendToScreen(deviceId, screenName, type, waiting = '') {
  const cmd = COMMANDS.find((c) => c.type === type);
  if (!cmd) return false;
  if (type === 'rename') return renameScreen(deviceId, screenName, cmd, waiting);
  const ok = await confirmDialog({
    title: `${cmd.label} on ${screenName}?`,
    body: h('div', null,
      h('div', { text: cmd.body }),
      h('div', { style: { 'margin-top': '8px' } },
        'The command waits in a queue. The screen takes it at its next check-in.')),
    confirm: cmd.label,
    kind: type === 'reboot' ? 'danger' : 'primary',
  });
  if (!ok) return false;
  try {
    await api('POST', `/api/admin/devices/${encodeURIComponent(deviceId)}/commands`, { type, args: {} });
    toast(`${cmd.label} is queued for ${screenName}.`);
    return true;
  } catch (err) {
    toast(errorText(err), 'danger');
    return false;
  }
}

/* The rename command takes the new name. The device owns its name, so this
   server does not change the row. The screen takes the command at its next
   check-in. It reports the new name in the same check-in. */
async function renameScreen(deviceId, screenName, cmd, waiting) {
  // 63 is manifest.MaxNameLength: the mDNS name comes from it.
  const input = h('input', {
    class: 'pp-input', type: 'text', value: waiting || screenName || '', maxlength: '63', autofocus: true,
  });
  const ok = await modal({
    title: `Rename ${screenName}`,
    body: h('div', null,
      h('label', { class: 'pp-label' }, 'New name', input),
      h('div', { class: 'pp-help', text: `${cmd.body} The screen also uses the name on its idle screen and for its address on the local network.` })),
    actions: [{ label: 'Cancel', value: false }, { label: 'Queue it', value: true, kind: 'primary' }],
    onOpen: (dialog, buttons) => {
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); buttons[1].click(); }
      });
    },
  });
  if (!ok) return false;
  const name = input.value.trim();
  const refusal = renameRefusal(name, screenName, waiting);
  if (refusal) {
    toast(refusal);
    return false;
  }
  try {
    await api('POST', `/api/admin/devices/${encodeURIComponent(deviceId)}/commands`, { type: 'rename', args: { name } });
    toast(`The new name is queued for ${screenName}. The screen takes it at its next check-in.`);
    return true;
  } catch (err) {
    const named = (err.fields || []).map((f) => f.message).join(' ');
    toast(named || errorText(err), 'danger');
    return false;
  }
}

/** Pick a group and a command, then queue it for every screen of that group.
    Rename is not in the list: one name belongs to one screen. */
export async function sendToGroup(groups, groupId) {
  const usable = (groups || []).filter((g) => g.devices > 0);
  if (usable.length === 0) {
    toast('No group holds a screen yet.', 'danger');
    return false;
  }
  const groupSelect = h('select', { class: 'pp-select' },
    usable.map((g) => h('option', {
      value: String(g.id),
      text: `${g.name} — ${g.devices} ${g.devices === 1 ? 'screen' : 'screens'}`,
    })));
  groupSelect.value = String(groupId || usable[0].id);

  const typeSelect = h('select', { class: 'pp-select' },
    GROUP_COMMANDS.map((c) => h('option', { value: c.type, text: c.label })));

  const note = h('div', { class: 'pp-help' });
  const paint = () => {
    const g = usable.find((x) => String(x.id) === groupSelect.value);
    const cmd = GROUP_COMMANDS.find((c) => c.type === typeSelect.value);
    note.textContent = g && cmd
      ? `${g.devices} ${g.devices === 1 ? 'screen takes' : 'screens take'} this at the next check-in. ${cmd.body}`
      : '';
  };
  groupSelect.addEventListener('change', paint);
  typeSelect.addEventListener('change', paint);
  paint();

  const go = await modal({
    title: 'Send a command to a group',
    body: h('div', null,
      h('div', { class: 'pp-fields' },
        h('label', { class: 'pp-label pp-field' }, 'Group', groupSelect),
        h('label', { class: 'pp-label pp-field' }, 'Command', typeSelect)),
      note),
    actions: [{ label: 'Cancel', value: false }, { label: 'Queue it', value: true, kind: 'primary' }],
  });
  if (!go) return false;

  const id = Number(groupSelect.value);
  try {
    const out = await api('POST', `/api/admin/groups/${id}/commands`, { type: typeSelect.value, args: {} });
    toast(`Queued for ${out.queued} ${out.queued === 1 ? 'screen' : 'screens'}.`);
    return true;
  } catch (err) {
    toast(errorText(err), 'danger');
    return false;
  }
}
