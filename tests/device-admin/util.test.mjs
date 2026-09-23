// node --test tests/device-admin
//
// The helpers of web/device-admin/util.js that decide an address. They import
// nothing, so node reads the file that ships.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { localMedia, frameURL, playingItem, nameToSave } from '../../web/device-admin/util.js';

// A paired device plays fleet objects from /media/_fleet/media/<object>. The first
// guard took only /media/<playlist>/<file>, so every fleet item showed the
// placeholder on the dashboard.
test('localMedia takes the two shapes that the daemon serves', () => {
  for (const good of [
    '/media/default/welcome.jpg',
    '/media/lobby-loop/promo%20clip.mp4',
    '/media/_fleet/media/55efb67e-lab2-teal.png',
    '/media/_fleet/media/55efb67e-caf%C3%A9%20menu.jpg',
    '/media/a/(draft)~v2@home.png',
  ]) {
    assert.equal(localMedia(good), good, good);
  }
});

test('localMedia refuses every other address', () => {
  for (const bad of [
    '', null, undefined,
    '//evil.example.com/media/a/b.jpg',
    'https://evil.example.com/media/a/b.jpg',
    '/media/a.jpg',
    '/media//b.jpg',
    '/media/a//b.jpg',
    '/media/a/b/c.jpg',
    '/media/_fleet/media/',
    '/media/_fleet/other/x.jpg',
    '/media/_fleet/media/x/y.jpg',
    '/media/../portapixel.toml',
    '/media/a/..',
    '/media/%2e%2e/secret.jpg',
    '/media/_fleet/media/%2E%2E',
    '/media/a/b%2fc.jpg',
    '/media/a/b%5cc.jpg',
    '/media/a\\..\\x.jpg',
    '/media/a/b.jpg?x=1',
    '/media/a/b.jpg#t=9',
    '/media/a/b c.jpg',
    '/media/a/%zz.jpg',
    '/api/status',
  ]) {
    assert.equal(localMedia(bad), null, String(bad));
  }
});

test('frameURL parks a video half a second in', () => {
  assert.equal(frameURL('/media/_fleet/media/aabbccdd-clip.mp4'), '/media/_fleet/media/aabbccdd-clip.mp4#t=0.5');
});

// The index of the player is a place in the list that the daemon sent it: the
// daemon shuffles that list and leaves out a missing file. items[np.index] showed
// the picture of another file under the name of the file on the screen.
test('playingItem finds the item by its name and not by the index', () => {
  const items = [
    { name: 'teal.png', sha256: 'a'.repeat(64) },
    { name: 'clip.mp4', sha256: 'b'.repeat(64) },
    { name: 'menu.jpg' },
  ];
  // Shuffled: the player reports place 2 for clip.mp4.
  assert.deepEqual(playingItem(items, { index: 2, item: 'clip.mp4' }), { item: items[1], index: 1 });
  assert.deepEqual(playingItem(items, { index: 0, item: 'clip.mp4', sha256: 'b'.repeat(64) }), { item: items[1], index: 1 });
  // A file with no hash yet still matches by its name.
  assert.deepEqual(playingItem(items, { index: 0, item: 'menu.jpg', sha256: 'c'.repeat(64) }), { item: items[2], index: 2 });
});

test('playingItem gives no place when the match is not certain', () => {
  const twice = [{ name: 'a.jpg' }, { name: 'b.jpg' }, { name: 'a.jpg' }];
  assert.deepEqual(playingItem(twice, { index: 2, item: 'a.jpg' }), { item: twice[0], index: -1 });
  const none = { item: null, index: -1 };
  // Another file with the same name but another hash is not the file.
  assert.deepEqual(playingItem([{ name: 'a.jpg', sha256: 'a'.repeat(64) }], { index: 0, item: 'a.jpg', sha256: 'b'.repeat(64) }), none);
  assert.deepEqual(playingItem([{ name: 'a.jpg' }], { index: 0, item: 'gone.jpg' }), none);
  assert.deepEqual(playingItem([{ name: 'a.jpg' }], null), none);
  assert.deepEqual(playingItem(undefined, { index: 0, item: 'a.jpg' }), none);
});

// A rename from the fleet server arrives while the Settings page is open. A save
// of another field must not put the old name back.
test('nameToSave keeps a rename that arrived while the page was open', () => {
  assert.equal(nameToSave('Lobby', 'Lobby', 'Front desk'), 'Front desk');
  // The person typed a name: it wins.
  assert.equal(nameToSave('Back office', 'Lobby', 'Front desk'), 'Back office');
  assert.equal(nameToSave('Lobby', 'Lobby', 'Lobby'), 'Lobby');
  assert.equal(nameToSave('Lobby', 'Lobby', undefined), 'Lobby');
});
