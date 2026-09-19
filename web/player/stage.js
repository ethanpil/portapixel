/* PortaPixel player stage.

   The stage has two layers. The front layer shows the item that plays now.
   The back layer holds the item that comes next. A transition moves the two
   layers, then the layers change place.

   Two rules keep the memory flat for weeks of play:
     - One layer holds one media element. The stage removes the media of the
       old layer after each transition. This frees the video decoder.
     - Each element has an AbortController. The abort removes every listener
       of that element.

   The stage prepares one item at a time. The player must not start a second
   prepare before the first one ends.
*/

/** How long the stage waits for an image or a video to load. */
const LOAD_TIMEOUT_MS = 15000;

/** How long the stage waits for play() to start the video. A slow start is
    not an error. The player watches the video for a stall after this. */
const PLAY_TIMEOUT_MS = 5000;

/** Where each layer starts and ends for a push. Both layers move. */
const PUSH = {
  'push-left': { from: 'translateX(100%)', to: 'translateX(-100%)' },
  'push-right': { from: 'translateX(-100%)', to: 'translateX(100%)' },
  'push-up': { from: 'translateY(100%)', to: 'translateY(-100%)' },
  'push-down': { from: 'translateY(-100%)', to: 'translateY(100%)' },
};

/** Wait for ms milliseconds. */
export function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Reject when promise takes longer than ms. The loser of the race continues,
    but the caller throws away its result. */
function withTimeout(promise, ms, message) {
  let timer = 0;
  const limit = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(message)), ms);
  });
  return Promise.race([promise, limit]).finally(() => clearTimeout(timer));
}

/** Start a video. Return "ok", "slow" or "rejected". */
async function tryPlay(el) {
  let timer = 0;
  const slow = new Promise((resolve) => { timer = setTimeout(() => resolve('slow'), PLAY_TIMEOUT_MS); });
  const started = el.play().then(() => 'ok', () => 'rejected');
  const why = await Promise.race([started, slow]);
  clearTimeout(timer);
  return why;
}

export class Stage {
  constructor(root) {
    this.layers = [
      root.querySelector('[data-layer="a"]'),
      root.querySelector('[data-layer="b"]'),
    ];
    this.front = 0;
    /** The item that the front layer shows: {item, el, layer, ac} or null. */
    this.current = null;
  }

  get frontLayer() { return this.layers[this.front]; }
  get backLayer() { return this.layers[1 - this.front]; }

  /** Load one item into the back layer. Resolve with the prepared item.
      Throw when the media is bad or too slow. */
  async prepare(item, loop) {
    const layer = this.backLayer;
    layer.replaceChildren();
    const ac = new AbortController();
    const prepared = { item, layer, ac, el: null };

    if (item.kind === 'video') {
      const el = document.createElement('video');
      el.muted = true;                // the real mute comes at play time
      el.playsInline = true;
      el.preload = 'auto';
      el.loop = !!loop;               // one item alone must loop without a gap
      prepared.el = el;
      layer.append(el);
      const ready = new Promise((resolve, reject) => {
        el.addEventListener('canplay', () => resolve(), { once: true, signal: ac.signal });
        el.addEventListener('error', () => reject(new Error('the video did not load')), { once: true, signal: ac.signal });
      });
      el.src = item.src;
      try {
        await withTimeout(ready, LOAD_TIMEOUT_MS, 'the video took more than 15 s to load');
      } catch (e) {
        Stage.teardown(prepared);
        throw e;
      }
      return prepared;
    }

    const el = new Image();
    prepared.el = el;
    el.src = item.src;
    try {
      // decode() ends when the picture is ready to draw. A decode error and a
      // load error both reject.
      await withTimeout(el.decode(), LOAD_TIMEOUT_MS, 'the image took more than 15 s to load');
    } catch (e) {
      Stage.teardown(prepared);
      throw e;
    }
    layer.append(el);
    return prepared;
  }

  /** Run the transition to a prepared item, then free the old media.
      Return a note when something needed attention, else an empty string.
      Throw when a video refuses to play. */
  async present(prepared, transition, ms) {
    let note = '';
    try {
      note = await this.#play(prepared);
    } catch (e) {
      Stage.teardown(prepared);
      throw e;
    }

    const into = prepared.layer;
    const out = this.frontLayer;
    const push = PUSH[transition];
    const time = transition === 'cut' ? 0 : Math.max(0, ms | 0);

    // Start state. The reflow makes the browser use it before the change.
    into.style.transition = 'none';
    out.style.transition = 'none';
    into.style.zIndex = '2';
    out.style.zIndex = '1';
    into.style.opacity = push ? '1' : '0';
    into.style.transform = push ? push.from : 'none';
    out.style.opacity = '1';
    out.style.transform = 'none';
    void into.offsetWidth;

    // End state.
    const css = time > 0
      ? `opacity ${time}ms linear, transform ${time}ms cubic-bezier(0.4, 0, 0.2, 1)`
      : 'none';
    into.style.transition = css;
    out.style.transition = css;
    into.style.opacity = '1';
    into.style.transform = 'none';
    if (push) out.style.transform = push.to;
    else out.style.opacity = '0';

    if (time > 0) await sleep(time);

    // The old layer becomes the back layer. Put it back to the rest state and
    // free its media.
    out.style.transition = 'none';
    out.style.opacity = '0';
    out.style.transform = 'none';
    const old = this.current;
    this.front = 1 - this.front;
    this.current = prepared;
    Stage.teardown(old);
    return note;
  }

  /** Fade the front layer to black and free its media. D14 uses this on a low
      tier device, so that only one video decoder runs at a time. */
  async fadeOut(ms) {
    const out = this.frontLayer;
    const time = Math.max(0, ms | 0);
    out.style.transition = time > 0 ? `opacity ${time}ms linear` : 'none';
    out.style.opacity = '0';
    if (time > 0) await sleep(time);
    out.style.transition = 'none';
    out.style.transform = 'none';
    Stage.teardown(this.current);
    this.current = null;
  }

  /** Stop the sound but keep the picture. The player does this before it gives
      the browser to the daemon for a URL item. */
  pause() {
    const el = this.current && this.current.el;
    if (el && el.tagName === 'VIDEO') { try { el.pause(); } catch { /* it ended */ } }
  }

  /** Black screen. Free all media. */
  clear() {
    Stage.teardown(this.current);
    this.current = null;
    for (const layer of this.layers) {
      layer.replaceChildren();
      layer.style.transition = 'none';
      layer.style.opacity = '0';
      layer.style.transform = 'none';
    }
  }

  /** Start a video. An image needs no work.
      Unmuted autoplay is on in the browser (see ARCHITECTURE.md section 7).
      If the browser still says no, try again muted and report it.
      A slow start is not an error. The player watches for a stall. */
  async #play(prepared) {
    const { item, el } = prepared;
    if (item.kind !== 'video') return '';
    el.muted = !!item.mute;

    const first = await tryPlay(el);
    if (first === 'ok') return '';
    if (first === 'slow') return `play() was slow on ${item.name}`;
    if (el.muted) throw new Error('the video did not play');

    el.muted = true;
    const second = await tryPlay(el);
    if (second === 'rejected') throw new Error('the video did not play, muted too');
    return `the browser refused sound on ${item.name}; it plays muted`;
  }

  /** Free one prepared item. Safe with null and safe twice. */
  static teardown(prepared) {
    if (!prepared) return;
    prepared.ac.abort();
    const el = prepared.el;
    if (!el) return;
    if (el.tagName === 'VIDEO') {
      try { el.pause(); } catch { /* it never started */ }
      el.removeAttribute('src');
      el.load();                      // this frees the decoder
    } else {
      el.removeAttribute('src');
    }
    el.remove();
    prepared.el = null;
  }
}
