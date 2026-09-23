// node --test tests/device-admin
//
// The helpers of web/device-admin/util.js that decide an address. They import
// nothing, so node reads the file that ships.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { localMedia, frameURL } from '../../web/device-admin/util.js';

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
