/* Sending a command to one screen or to a whole group.

   Nothing here reaches out to a screen. A command waits in a queue and the
   screen picks it up at its next check-in, so every message in this file says
   "at the next check-in" and never "now".
*/

import { h, toast, modal, confirmDialog } from '/shared/ui.js';
import { api } from '/shared/api.js';
import { COMMANDS, errorText } from './util.js';

/** Ask, then queue one command for one screen. Returns true when it is queued. */
export async function sendToScreen(deviceId, screenName, type) {
  const cmd = COMMANDS.find((c) => c.type === type);
  if (!cmd) return false;
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

/** Pick a group and a command, then queue it for every screen of that group. */
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
    COMMANDS.map((c) => h('option', { value: c.type, text: c.label })));

  const note = h('div', { class: 'pp-help' });
  const paint = () => {
    const g = usable.find((x) => String(x.id) === groupSelect.value);
    const cmd = COMMANDS.find((c) => c.type === typeSelect.value);
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
