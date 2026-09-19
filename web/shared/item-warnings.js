/* PortaPixel item warnings.
   Plain ES module. No dependencies.

   Two jobs:
     1. Find out which video formats a machine decodes (plan D12).
     2. Turn a playlist item plus those findings into plain sentences.

   Read this before you trust the result: probeCapabilities() with no argument
   probes the browser you are looking at, not the screen. The device reports
   its own findings in /api/status. Always pass that report in. The probe is
   only a fallback for a screen that has not checked in yet.
*/

const CODECS = {
  h264: {
    label: 'H.264',
    1080: 'video/mp4; codecs="avc1.640028"',
    2160: 'video/mp4; codecs="avc1.640033"',
  },
  hevc: {
    label: 'HEVC',
    1080: 'video/mp4; codecs="hvc1.1.6.L120.B0"',
    2160: 'video/mp4; codecs="hvc1.1.6.L153.B0"',
  },
  vp9: {
    label: 'VP9',
    1080: 'video/webm; codecs="vp09.00.10.08"',
    2160: 'video/webm; codecs="vp09.00.10.08"',
  },
  av1: {
    label: 'AV1',
    1080: 'video/mp4; codecs="av01.0.08M.08"',
    2160: 'video/mp4; codecs="av01.0.12M.08"',
  },
};

const SIZES = { 1080: [1920, 1080], 2160: [3840, 2160] };

/* The extensions that the player knows. Keep this set the same as imageExt and
   videoExt in internal/playlist/kind.go: a file that Go calls unknown is a file
   that the player skips, and this module must say so. */
const KNOWN_EXT = new Set([
  'jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'bmp', 'svg',
  'mp4', 'm4v', 'mov', 'webm', 'mkv', 'ogv',
]);

/** Find out what a machine decodes.
    Pass the object the device reported in /api/status to use that instead:
    this function prefers a device report over its own probe every time.
    Returns {source, tier, codecs: {name: {1080: r, 2160: r}}} where each r is
    {supported, smooth, powerEfficient}. */
export async function probeCapabilities(reported) {
  if (reported && reported.codecs) return normalize(reported, 'device');

  const codecs = {};
  const video = document.createElement('video');
  const mc = navigator.mediaCapabilities;

  for (const [name, spec] of Object.entries(CODECS)) {
    codecs[name] = {};
    for (const height of [1080, 2160]) {
      const type = spec[height];
      const [w, hgt] = SIZES[height];
      let r = null;
      if (mc && mc.decodingInfo) {
        try {
          const info = await mc.decodingInfo({
            type: 'file',
            video: { contentType: type, width: w, height: hgt, bitrate: height === 2160 ? 20e6 : 6e6, framerate: 30 },
          });
          r = { supported: !!info.supported, smooth: !!info.smooth, powerEfficient: !!info.powerEfficient };
        } catch { /* fall through to canPlayType */ }
      }
      if (!r) {
        const can = video.canPlayType(type);
        // canPlayType says nothing about hardware decode. Treat "probably" as
        // playable and leave the hardware answer unknown.
        r = { supported: can === 'probably' || can === 'maybe', smooth: can === 'probably', powerEfficient: null };
      }
      codecs[name][height] = r;
    }
  }
  return normalize({ codecs }, 'browser');
}

function normalize(caps, source) {
  const out = { source: caps.source || source, tier: caps.tier || null, codecs: {} };
  for (const name of Object.keys(CODECS)) {
    const got = caps.codecs[name] || {};
    out.codecs[name] = {
      1080: fix(got[1080] ?? got['1080']),
      2160: fix(got[2160] ?? got['2160']),
    };
  }
  return out;
}

function fix(r) {
  if (r === true) return { supported: true, smooth: true, powerEfficient: true };
  if (r === false || r === null || r === undefined) return { supported: false, smooth: false, powerEfficient: false };
  return { supported: !!r.supported, smooth: !!r.smooth, powerEfficient: r.powerEfficient ?? null };
}

/** Which codec an item most likely holds. Uses item.codec when the device
    told us, otherwise a guess from the file name. */
function codecOf(item) {
  const c = String(item.codec || '').toLowerCase();
  if (c.includes('hvc') || c.includes('hev') || c.includes('h265') || c.includes('hevc')) return 'hevc';
  if (c.includes('av01') || c === 'av1') return 'av1';
  if (c.includes('vp9') || c.includes('vp09')) return 'vp9';
  if (c.includes('avc') || c.includes('h264') || c.includes('h.264')) return 'h264';
  const n = nameOf(item).toLowerCase();
  if (/(hevc|h265|h\.265|x265)/.test(n)) return 'hevc';
  if (/av1/.test(n)) return 'av1';
  if (/vp9/.test(n)) return 'vp9';
  return null;
}

function nameOf(item) {
  return String(item.name || item.file || item.url || '');
}

/* The extension, with no length limit. Go takes everything after the last full
   stop, so a bound here would call a file good that the player skips. */
function extOf(item) {
  const n = nameOf(item).split(/[?#]/)[0];
  const m = /\.([A-Za-z0-9]+)$/.exec(n);
  return m ? m[1].toLowerCase() : '';
}

/** True when the item is roughly 4K or bigger. */
function isUhd(item) {
  if (Number(item.height) >= 1800 || Number(item.width) >= 3200) return true;
  return /(4k|2160|uhd)/i.test(nameOf(item));
}

/* The lead-in of each warning. A warning about a codec and a warning about a
   file that the player cannot read are not the same kind of problem, and one
   shared prefix made the second one read as a performance note. */
export const PREFIX = {
  playback: 'Might not play smoothly here.',
  skipped: 'This item gets skipped.',
  timing: 'This item has no time on screen.',
  size: 'Slower than it needs to be.',
};

/** Sentences to show under one playlist item. Returns an array of
    {prefix, text}; an empty array means the item is fine.
      item: {kind, name|file|url, duration, width, height, size, codec}
      caps: the object from probeCapabilities()
      tier: "low" or "high" — the device performance tier
      opts: {mixed} — true when the playlist holds more than a URL item
             {extra} — sentences that the host page found itself, for example a
                       file that the device reports as missing. Each one is a
                       string or a {prefix, text}.
             {machine} — the words for the machine that the warning is about.
                       The device UI leaves it out and gets "this box" or "this
                       browser"; the fleet UI passes "some screens", because the
                       report it gives is the whole fleet at its worst. */
export function warningsFor(item, caps, tier, opts = {}) {
  const out = [];
  const push = (prefix, text) => out.push({ prefix, text });
  if (!item) return out;
  const kind = String(item.kind || '').toLowerCase();
  const machine = opts.machine
    || (caps && caps.source === 'device' ? 'this box' : 'this browser');
  const effTier = tier || (caps && caps.tier) || null;

  if (kind === 'video') {
    const codec = codecOf(item);
    const uhd = isUhd(item);
    const band = uhd ? 2160 : 1080;
    // A device may report a plain true or false per band, so coerce it.
    const raw = codec && caps && caps.codecs && caps.codecs[codec] ? caps.codecs[codec][band] : undefined;
    const r = raw === undefined ? null : fix(raw);

    if (uhd && effTier === 'low') {
      push(PREFIX.playback, "It's a 4K file and this screen is set up as low-power. A 1080p copy will look identical here and play cleanly.");
    } else if (codec === 'hevc' && r && !r.supported) {
      push(PREFIX.playback, caps.source === 'device'
        ? "It's in HEVC, which this box does not decode. An H.264 copy plays cleanly."
        : "It's in HEVC, which this browser cannot decode, so the screen probably cannot either. An H.264 copy is the safe choice.");
    } else if (r && r.supported && r.powerEfficient === false) {
      push(PREFIX.playback, `${uhd ? "It's a 4K file" : 'It is'} in a newer format than ${machine} decodes in hardware. A 1080p H.264 copy will look identical on this screen and play cleanly.`);
    } else if (uhd && r && !r.supported) {
      push(PREFIX.playback, 'It is a 4K file and nothing here reports that it can decode it. A 1080p copy is the safe choice.');
    }
  }

  if (kind === 'url' && opts.mixed && !Number(item.duration)) {
    push(PREFIX.timing, 'Give it a time on screen, or the loop stops on this page.');
  }

  if (kind === 'image') {
    const size = Number(item.size) || 0;
    const px = (Number(item.width) || 0) * (Number(item.height) || 0);
    if (size > 12 * 1024 * 1024 || px > 40e6) {
      push(PREFIX.size, 'The image file is very large. It takes a moment to decode every time round the loop; a 1920x1080 copy shows the same thing.');
    }
  }

  if (kind !== 'url' && nameOf(item)) {
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
