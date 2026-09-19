/* The status poller.
   One timer for the whole UI: every page reads the same report, so a page
   change never makes a second timer and never makes a second request.
*/

import { api } from '/shared/api.js';

/** Make the poller. It starts when start() is called and it keeps the last
    report, so a page that mounts between two ticks has data at once. */
export function createStore(intervalMs = 5000) {
  const listeners = new Set();
  let status = null;
  let error = null;
  let timer = 0;
  let inFlight = null;

  function emit() {
    // A copy of the set: a listener may unsubscribe while we call it.
    for (const fn of [...listeners]) {
      try { fn(status, error); } catch (e) { console.error(e); }
    }
  }

  function tick() {
    if (inFlight) return inFlight;
    inFlight = api('GET', '/api/status')
      .then((s) => { status = s; error = null; }, (e) => { error = e; })
      .then(() => { inFlight = null; emit(); });
    return inFlight;
  }

  return {
    start() {
      if (timer) return;
      timer = setInterval(tick, intervalMs);
      tick();
    },
    stop() {
      clearInterval(timer);
      timer = 0;
    },
    /** Read the status now. Every page calls this after a command. */
    refresh: tick,
    get status() { return status; },
    get error() { return error; },
    /** Take every new report. The function runs at once when one is in hand.
        Returns the function that stops it. */
    subscribe(fn) {
      listeners.add(fn);
      if (status || error) { try { fn(status, error); } catch (e) { console.error(e); } }
      return () => listeners.delete(fn);
    },
  };
}
