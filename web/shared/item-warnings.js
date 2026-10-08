/* PortaPixel item warnings.
   Plain ES module. No dependencies.

   It turns one playlist item into plain sentences: a file that the player
   skips, and an image that is larger than it needs to be.
*/

/* The extensions that the player knows. Keep this set the same as imageExt and
   videoExt in internal/playlist/kind.go: a file that Go calls unknown is a file
   that the player skips, and this module must say so. */
const KNOWN_EXT = new Set([
  'jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'bmp',
  'mp4', 'm4v', 'mov', 'webm', 'mkv', 'ogv',
]);

function nameOf(item) {
  return String(item.name || item.file || '');
}

/* The extension, with no length limit. Go takes everything after the last full
   stop, so a bound here would call a file good that the player skips. */
function extOf(item) {
  const n = nameOf(item).split(/[?#]/)[0];
  const m = /\.([A-Za-z0-9]+)$/.exec(n);
  return m ? m[1].toLowerCase() : '';
}

/* The lead-in of each warning. A file that the player cannot read and an image
   that is slow to decode are not the same kind of problem, so each one has its
   own lead-in. */
export const PREFIX = {
  skipped: 'This item gets skipped.',
  size: 'Slower than it needs to be.',
};

/** Sentences to show under one playlist item. Returns an array of
    {prefix, text}; an empty array means the item is fine.
      item: {kind, name|file, width, height, size}
      opts: {extra} — sentences that the host page found itself, for example a
                      file that the device reports as missing. Each one is a
                      string or a {prefix, text}. */
export function warningsFor(item, opts = {}) {
  const out = [];
  const push = (prefix, text) => out.push({ prefix, text });
  if (!item) return out;
  const kind = String(item.kind || '').toLowerCase();

  if (kind === 'image') {
    const size = Number(item.size) || 0;
    const px = (Number(item.width) || 0) * (Number(item.height) || 0);
    if (size > 12 * 1024 * 1024 || px > 40e6) {
      push(PREFIX.size, 'The image file is very large. It takes a moment to decode every time round the loop; a 1920x1080 copy shows the same thing.');
    }
  }

  if (nameOf(item)) {
    const ext = extOf(item);
    if (!ext) {
      push(PREFIX.skipped, 'This file has no extension, so PortaPixel cannot tell what it holds.');
    } else if (!KNOWN_EXT.has(ext)) {
      push(PREFIX.skipped, `PortaPixel does not know the .${ext} format.`);
    }
  }

  /* What the host page found out for itself. The device reports a file that is
     not on the stick and a kind that the player does not know, and only the
     host page has that answer. */
  for (const extra of opts.extra || []) {
    if (!extra) continue;
    if (typeof extra === 'string') push(PREFIX.skipped, extra);
    else push(extra.prefix || PREFIX.skipped, extra.text || '');
  }

  return out;
}
