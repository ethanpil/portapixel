// node --test tests/server-admin/*.test.mjs
//
// The helpers of web/server-admin/util.js. The module imports /shared/ui.js by
// its URL path, as the browser does. The resolve hook below maps that path to
// web/shared/, so node reads the files that ship.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const web = new URL('../../web/', import.meta.url);
registerHooks({
  resolve(specifier, context, next) {
    if (specifier.startsWith('/shared/')) return next(new URL(`.${specifier}`, web).href, context);
    return next(specifier, context);
  },
});

/* The smallest DOM that the h() builder of ui.js needs. An element records its
   tag, its attributes, its properties and its children. */
class Node {}
class Element extends Node {
  constructor(tag) {
    super();
    this.tag = tag;
    this.attrs = {};
    this.children = [];
    this.style = { setProperty: (k, v) => { this.attrs[`style:${k}`] = v; } };
    this.dataset = {};
    this.listeners = {};
  }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  replaceWith(node) { this.replacedBy = node; }
  append(...c) { this.children.push(...c); }
  replaceChildren() { this.children = []; }
}
globalThis.Node = Node;
globalThis.document = {
  createElement: (tag) => new Element(tag),
  createElementNS: (ns, tag) => new Element(tag),
  createTextNode: (text) => Object.assign(new Node(), { text }),
};

const util = await import('../../web/server-admin/util.js');

const teal = { sha256: '55efb67e'.padEnd(64, '0'), orig_name: 'lab2-teal.png', has_thumb: true };
const clip = { sha256: 'aabbccdd'.padEnd(64, '1'), orig_name: 'promo.mp4', has_thumb: false };
const library = util.mediaIndex([teal, clip]);

// A screen reports the name of its own copy, <sha8>-<name>, which is never the
// name of the upload. The first UI matched the two names and never found one.
test('playingMedia finds the object by the hash that the screen reports', () => {
  assert.equal(util.playingMedia({ item: '55efb67e-lab2-teal.png', sha256: teal.sha256 }, library), teal);
  assert.equal(util.playingMedia({ item: 'aabbccdd-promo.mp4', sha256: clip.sha256 }, library), clip);
  assert.equal(util.playingMedia({ item: 'lab2-teal.png', sha256: 'f'.repeat(64) }, library), null,
    'a hash that the library does not hold must not fall back to the name');
});

test('playingMedia takes the name only when the report has no hash', () => {
  assert.equal(util.playingMedia({ item: 'lab2-teal.png' }, library), teal);
  assert.equal(util.playingMedia({ item: '55efb67e-lab2-teal.png' }, library), null);
  assert.equal(util.playingMedia(null, library), null);
  assert.equal(util.playingMedia({ item: 'lab2-teal.png' }, null), null);
});

test('fileURL names the object route of the admin API', () => {
  assert.equal(util.fileURL(clip.sha256), `/api/admin/media/${clip.sha256}/file`);
});

test('mediaPreview draws the first frame of a video only when asked', () => {
  const frame = util.mediaPreview('video', clip, { wide: true, frame: true });
  const video = frame.children[0];
  assert.equal(video.tag, 'video');
  assert.equal(video.attrs.src, `/api/admin/media/${clip.sha256}/file#t=0.5`);
  assert.equal(video.attrs.preload, 'metadata');
  assert.equal(video.attrs.playsinline, '');
  assert.equal(video.muted, true);

  // A file that the browser cannot decode gives the icon of its kind.
  video.listeners.error();
  assert.ok(frame.replacedBy, 'a video that fails must give way to the icon');
  assert.notEqual(frame.replacedBy.children[0].tag, 'video');

  // The list of screens keeps the icon: no video element, so no request.
  const icon = util.mediaPreview('video', clip, { wide: true });
  assert.notEqual(icon.children[0].tag, 'video');

  // A thumbnail wins, and an object that is not there gives the icon.
  const thumb = util.mediaPreview('image', teal, { frame: true });
  assert.equal(thumb.children[0].tag, 'img');
  assert.equal(thumb.children[0].attrs.src, util.thumbURL(teal.sha256));
  assert.notEqual(util.mediaPreview('video', null, { frame: true }).children[0].tag, 'video');
});

test('rename is a command of one screen and never of a group', () => {
  assert.ok(util.COMMANDS.some((c) => c.type === 'rename'));
  assert.ok(!util.GROUP_COMMANDS.some((c) => c.type === 'rename'));
  assert.equal(util.GROUP_COMMANDS.length, util.COMMANDS.length - 1);
});

test('commandLabel names the new name of a rename', () => {
  assert.equal(util.commandLabel({ type: 'rename', args: { name: 'Front desk' } }), 'Rename the screen to "Front desk"');
  assert.equal(util.commandLabel({ type: 'reboot' }), 'Reboot');
  assert.equal(util.commandLabel({ type: 'something-new' }), 'something-new');
});

test('waitingName waits until the screen reports the new name', () => {
  const rename = (state, name) => ({ type: 'rename', state, args: { name } });
  const device = { name: 'Lobby' };
  assert.equal(util.waitingName(device, [rename('queued', 'Front desk')]), 'Front desk');
  assert.equal(util.waitingName(device, [rename('delivered', 'Front desk')]), 'Front desk');
  assert.equal(util.waitingName({ name: 'Front desk' }, [rename('acked', 'Front desk')]), '');
  // Acknowledged with the old name: the screen refused it. Expired: it never arrived.
  assert.equal(util.waitingName(device, [rename('acked', 'Front desk')]), '');
  assert.equal(util.waitingName(device, [rename('expired', 'Front desk')]), '');
  // The newest rename counts; other commands do not.
  assert.equal(util.waitingName(device, [{ type: 'reboot', state: 'queued' }, rename('queued', 'B'), rename('queued', 'A')]), 'B');
  assert.equal(util.waitingName(device, []), '');
  assert.equal(util.waitingName(device, undefined), '');
});
