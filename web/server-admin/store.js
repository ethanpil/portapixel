/* The fleet poller.
   One timer for the whole UI: the sidebar totals and the fleet list read the
   same answer, so a page change never makes a second timer.

   GET /api/admin/devices gives the rows and the totals in one call, which is
   why there is nothing else to poll here.
*/

import { api } from '/shared/api.js';

/** Make the poller. It starts when start() is called and it keeps the last
    answer, so a page that mounts between two ticks has data at once. */
export function createStore(intervalMs = 10000) {
  const listeners = new Set();
  let devices = [];
  let totals = null;
  let error = null;
  let loaded = false;
  let timer = 0;
  let inFlight = null;

  function emit() {
    // A copy of the set: a listener may unsubscribe while we call it.
    for (const fn of [...listeners]) {
      try { fn({ devices, totals, error, loaded }); } catch (e) { console.error(e); }
    }
  }

  function tick() {
    if (inFlight) return inFlight;
    inFlight = api('GET', '/api/admin/devices')
      .then((out) => {
        devices = (out && out.devices) || [];
        totals = (out && out.totals) || null;
        error = null;
        loaded = true;
      }, (e) => { error = e; })
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
    /** Read the fleet now. Every page calls this after a change. */
    refresh: tick,
    get devices() { return devices; },
    get totals() { return totals; },
    get error() { return error; },
    get loaded() { return loaded; },
    /** One screen from the last answer, or null. */
    device(id) { return devices.find((d) => d.id === id) || null; },
    /** Take every new answer. The function runs at once when one is in hand.
        Returns the function that stops it. */
    subscribe(fn) {
      listeners.add(fn);
      if (loaded || error) { try { fn({ devices, totals, error, loaded }); } catch (e) { console.error(e); } }
      return () => listeners.delete(fn);
    },
  };
}
