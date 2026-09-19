/* PortaPixel API client.
   Plain ES module. No dependencies.

   The device and the server both need the custom header on every call that
   changes state. That header is the CSRF guard (ARCHITECTURE.md section 6).
*/

/** Thrown by api() and upload(). It carries the HTTP status and, when the
    server sent one, the {error, fields} payload. */
export class ApiError extends Error {
  constructor(message, status, payload) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload || null;
    this.fields = (payload && payload.fields) || null;
  }
}

/** Fires on 401 so the app can show the login view. */
export const UNAUTHORIZED = 'pp:unauthorized';

function unauthorized(path) {
  dispatchEvent(new CustomEvent(UNAUTHORIZED, { detail: { path } }));
}

/** Call the JSON API. Sends and receives JSON. Non-GET calls carry the
    X-PortaPixel header. Returns the parsed body, or null for 204.
    Throws ApiError on any non-2xx answer. */
export async function api(method, path, body) {
  const head = { Accept: 'application/json' };
  const opts = { method: method.toUpperCase(), credentials: 'same-origin', headers: head };

  if (opts.method !== 'GET' && opts.method !== 'HEAD') {
    head['X-PortaPixel'] = '1';
    if (body !== undefined && body !== null) {
      head['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
  }

  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    throw new ApiError('The device did not answer. Check the network.', 0, null);
  }

  const payload = await readJson(res);

  if (res.status === 401) {
    unauthorized(path);
    throw new ApiError((payload && payload.error) || 'Please sign in again.', 401, payload);
  }
  if (!res.ok) {
    throw new ApiError((payload && payload.error) || `${res.status} ${res.statusText}`, res.status, payload);
  }
  return payload;
}

async function readJson(res) {
  if (res.status === 204) return null;
  const text = await res.text();
  if (!text) return null;
  try { return JSON.parse(text); } catch { return { error: text.slice(0, 400) }; }
}

/** Send one file as the raw request body, with progress.
    The name goes in X-Filename, so there is no multipart parsing on either
    end. onProgress(fraction, sentBytes, totalBytes) runs while it uploads.
    Returns the parsed answer. Throws ApiError.
    The returned promise carries abort() to stop the upload. */
export function upload(path, file, onProgress) {
  const xhr = new XMLHttpRequest();
  const p = new Promise((resolve, reject) => {
    xhr.open('POST', path, true);
    xhr.withCredentials = true;
    xhr.setRequestHeader('X-PortaPixel', '1');
    xhr.setRequestHeader('X-Filename', encodeURIComponent(file.name));
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.setRequestHeader('Accept', 'application/json');

    if (onProgress) {
      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) onProgress(e.loaded / e.total, e.loaded, e.total);
      });
    }

    xhr.addEventListener('load', () => {
      let payload = null;
      try { payload = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch { /* not JSON */ }
      if (xhr.status === 401) {
        unauthorized(path);
        reject(new ApiError('Please sign in again.', 401, payload));
        return;
      }
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new ApiError((payload && payload.error) || `Upload failed (${xhr.status})`, xhr.status, payload));
        return;
      }
      if (onProgress) onProgress(1, file.size, file.size);
      resolve(payload);
    });
    xhr.addEventListener('error', () => reject(new ApiError('The upload was cut off.', 0, null)));
    xhr.addEventListener('abort', () => reject(new ApiError('The upload was stopped.', 0, null)));

    xhr.send(file);
  });
  p.abort = () => xhr.abort();
  return p;
}

/** Open a server-sent-event stream. handlers maps an event name to a
    function; the special name "message" takes unnamed events and "error"
    takes stream errors. The browser reconnects on its own, so there is no
    retry loop here. Returns {close()}. */
export function sse(path, handlers = {}) {
  const src = new EventSource(path, { withCredentials: true });
  for (const [name, fn] of Object.entries(handlers)) {
    if (name === 'error') { src.addEventListener('error', fn); continue; }
    src.addEventListener(name, (e) => {
      let data = e.data;
      if (data) { try { data = JSON.parse(data); } catch { /* leave it as text */ } }
      fn(data, e);
    });
  }
  return { close: () => src.close(), source: src };
}
