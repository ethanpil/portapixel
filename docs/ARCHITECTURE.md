# PortaPixel architecture contract

This file is the contract between packages. The plan (`portapixel-dev-plan.md`) gives the
intent. This file gives the names, paths and wire formats. Change this file first, then
change the code.

Language rule: write all comments, documents and commit messages in ASD-STE100 Simplified
Technical English. Use short sentences. Use the active voice. Use one word for one thing.

## 1. Module and dependencies

Module: `github.com/ethanpil/portapixel`. Go 1.25. `CGO_ENABLED=0` always.

Permitted dependencies. Each new dependency needs a rationale line here.

| Dependency | Rationale |
|---|---|
| `github.com/BurntSushi/toml` | Parse TOML. We do not use it to write TOML. |
| `modernc.org/sqlite` | Pure-Go SQLite for the server. No cgo. |
| `aead.dev/minisign` | Verify release signatures (D47). |
| `golang.org/x/crypto` | bcrypt for the server admin password. |
| `golang.org/x/net/websocket` | CDP client for navigation rung 1. The standard library has no WebSocket client. |
| `golang.org/x/sys` | Indirect, through minisign. |
| `github.com/hashicorp/mdns` | mDNS announce (D20). |
| `github.com/skip2/go-qrcode` | QR code on the fallback screen (D18). |

The standard library does all other work. Use `net/http` with Go 1.22 pattern routes
(`mux.HandleFunc("PUT /api/playlists/{name}", ...)`). Use `log/slog` for logs.

## 2. Package map

```
cmd/portapixeld/          device entry point. Subcommands, flags, wiring only.
cmd/portapixel-server/    server entry point. Flags and wiring only.
web/embed.go              package web. go:embed of player, device-admin, server-admin, shared.

internal/version          Version string, set with -ldflags. Embedded minisign public key.
internal/fsutil           WriteFileAtomic (stage, fsync, rename, fsync dir). FreeBytes.
internal/opslog           Bounded event log. 1200 lines trim to 1000.
internal/config           portapixel.toml model, defaults, Validate, Render, Load with shadow.
internal/playlist         playlist.toml model, Parse, Render, item kind detection.
internal/manifest         Fleet wire types (section 5). Shared by device and server.
internal/store            SHA-256 object store paths, HashFile, resumable Range download.
internal/sigverify        minisign verify of a file against the embedded key.
internal/updater          A/B release swap, health gate, rollback. Device and server use it.
internal/httpguard        Host allowlist, CSRF header check, session store, login limiter.

internal/device/identity  Device ID derivation and repair semantics (D21).
internal/device/library   Scan media root, parse playlists, hash cache, item warnings.
internal/device/scheduler Rule evaluation each minute, clock-sync gate (D17, D40).
internal/device/browser   cage + Chromium supervisor, navigation ladder, watchdog, URL items, kiosk mode.
internal/device/power     CEC or DPMS, screen schedule.
internal/device/syncer    Fleet client: enroll, poll, download, fleet playlists, commands.
internal/device/mdns      Announce.
internal/device/health    Status struct for /api/status.
internal/device/netcfg    Render wpa_supplicant.conf and /etc/network/interfaces.
internal/device/installer install-to-disk (D54).
internal/device/httpd     Routes, handlers, SSE hub. No business logic.

internal/server/db        Schema, migrations, queries. Single writer.
internal/server/api       Device-facing /api/v1 routes.
internal/server/admin     Admin-facing /api/admin routes, sessions.
internal/server/media     Content-addressed media store, thumbnails.
internal/server/releases  GitHub release list, mirror, bundle upload.
```

Rules:

- `internal/device/*` and `internal/server/*` must not import each other.
- `httpd`, `api` and `admin` hold routes only. Logic lives in the other packages.
- Each package has a `doc.go` with a rationale comment: why the package exists.
- A bad input file must never cause a panic. It causes a skipped item and an ops log line.

## 3. Device paths

| Name | Image install | On-box install | Flag |
|---|---|---|---|
| Media root | `/media/ppmedia` | `/var/lib/portapixel/media` | `--media` |
| State dir | `/var/lib/portapixel` | same | `--state` |
| Run dir | `/run/portapixel` | same | `--run` |
| Release root | `/opt/portapixel` | same | `--releases` |

Files in the media root: `portapixel.toml`, `<playlist>/playlist.toml`, `_fleet/media/`,
`_fleet/<playlist>/playlist.toml`, `_update/`. A directory name that starts with `_` is
never a local playlist.

Files in the state dir: `ops.log`, `portapixel.toml.lkg` (shadow config, D38),
`state.json` (stored device ID, device token, server-assigned data, bad releases),
`hashcache.json`, `.provisioned`, `.root-default-hash`.

`state.json` holds the per-device fleet token. The TOML holds only the token that the user
typed. This keeps a flashed card clonable (D25).

## 4. Device daemon command line

```
portapixeld run [flags]          the daemon (default when no subcommand is given)
portapixeld selftest             parse embedded assets and templates, exit 0 or 1
portapixeld render-net           write wpa_supplicant.conf and interfaces from the TOML
portapixeld provision            first boot steps 2, 4, 5 of plan section 14 (idempotent)
portapixeld install-to-disk DEV  D54
portapixeld version
```

Shell does partition work. Go does all TOML work. No shell script parses TOML.

## 5. Fleet wire format (`internal/manifest`)

All bodies are JSON. Device calls carry `Authorization: Bearer <device token>`, except
`enroll`.

```go
type EnrollRequest struct {
    DeviceID   string `json:"device_id"`   // px-xxxxxxxx
    HardwareID string `json:"hardware_id"` // full SHA-256 hex of the hardware source
    Name       string `json:"name"`
    Token      string `json:"token"`       // enrollment token, device token, or "" for code pairing
    Version    string `json:"version"`
}
type EnrollResponse struct {
    Status      string `json:"status"`       // "paired" | "pending"
    DeviceToken string `json:"device_token"` // set when paired
    PairingCode string `json:"pairing_code"` // set when pending by code; 6 chars, no 0/O/1/I
    ClaimSecret string `json:"claim_secret"` // device keeps it; sends it again to poll a pending enroll
}
```

A pending device calls `enroll` again with the same `ClaimSecret` in `token` until the
status is `paired`.

```go
type Manifest struct {
    ServerName      string      `json:"server_name"`
    PollSeconds     int         `json:"poll_seconds"`
    Playlists       []Playlist  `json:"playlists"`
    DefaultPlaylist string      `json:"default_playlist"`
    Schedule        []Rule      `json:"schedule"`
    Media           []MediaRef  `json:"media"`
    Commands        []Command   `json:"commands"`
    Screen          *ScreenRule `json:"screen,omitempty"`
    Release         *ReleaseRef `json:"release,omitempty"`
}
type Playlist struct {
    Name       string `json:"name"` // directory-safe slug
    Title      string `json:"title"`
    Transition string `json:"transition,omitempty"`
    Shuffle    *bool  `json:"shuffle,omitempty"`
    Items      []Item `json:"items"`
}
type Item struct {
    SHA256         string `json:"sha256,omitempty"` // media item
    URL            string `json:"url,omitempty"`    // url item
    Duration       int    `json:"duration,omitempty"`
    Mute           bool   `json:"mute,omitempty"`
    MaxDuration    int    `json:"max_duration,omitempty"`
    RefreshSeconds int    `json:"refresh_seconds,omitempty"`
}
type Rule struct {
    Playlist string   `json:"playlist"`
    Days     []string `json:"days"`  // "mon".."sun"; empty = all days
    Start    string   `json:"start"` // "HH:MM"
    End      string   `json:"end"`
}
type MediaRef struct {
    SHA256 string `json:"sha256"`
    Size   int64  `json:"size"`
    Name   string `json:"name"`
    URL    string `json:"url"` // path relative to the server base URL
}
type Command struct {
    ID   int64             `json:"id"`
    Type string            `json:"type"` // reboot | restart-browser | screen-on | screen-off | rescan | update
    Args map[string]string `json:"args,omitempty"`
}
type ScreenRule struct {
    OnTime  string   `json:"on_time"`
    OffTime string   `json:"off_time"`
    Days    []string `json:"days"`
}
type ReleaseRef struct {
    Version string `json:"version"`
    BaseURL string `json:"base_url"` // mirror path; files: portapixeld-<arch>, .minisig, SHA256SUMS
}
type Heartbeat struct {
    DeviceID   string  `json:"device_id"`
    HardwareID string  `json:"hardware_id"`
    Name       string  `json:"name"`
    Version    string  `json:"version"`
    Status     Status  `json:"status"`
    Acks       []int64 `json:"acks"`
    SyncError  string  `json:"sync_error,omitempty"` // for example "needs 4.2 GB, has 1.1 GB"
}
```

`Status` is the same struct that the device serves at `/api/status` (plan section 8,
`health`). It lives in `internal/manifest` so the two ends share it.

The device stores fleet objects at `_fleet/media/<first 8 hex of sha>-<safe name>`.

## 6. Local API rules (D46)

- Host allowlist: `localhost`, `127.0.0.1`, each local IP, `<name>.local`,
  `portapixel-<last4>.local`, each with or without the port.
- Session cookie `pp_session`: `HttpOnly`, `SameSite=Strict`, path `/`.
- Each request with a method other than GET or HEAD must carry `X-PortaPixel: 1`.
- `/api/player/*` accepts loopback peers only and needs `?k=<boot secret>` or the
  `X-PortaPixel-Player` header with the same value. The secret is 32 random hex chars made
  at daemon start. The browser opens `/player?k=<secret>`.
- `/api/status` needs no session. It includes `pairing_code` for loopback peers only.
- The server uses the same package with an allowlist made from `public_url`.

## 7. Navigation ladder (`internal/device/browser`)

The display stack is Chromium in kiosk mode inside `cage` (see CONTEXT.md section 3).
The daemon starts one process tree as the `kiosk` user:

```
cage -s -- chromium --kiosk --ozone-platform=wayland \
  --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 \
  --autoplay-policy=no-user-gesture-required \
  --user-data-dir=<tmpfs>/profile --disk-cache-dir=<tmpfs>/cache \
  --no-first-run --noerrdialogs --disable-infobars \
  --disable-session-crashed-bubble --disable-features=Translate \
  --password-store=basic <url>
```

Rotation and `video_mode` go through `wlr-randr` in the cage session. They rotate all
content, external pages included. The browser command and its flags live in ONE place,
`internal/device/browser/command.go`.

```go
type Navigator interface {
    Name() string                                  // "cdp" | "relaunch"
    Start(ctx context.Context, url string) error   // browser up and on url
    Navigate(ctx context.Context, url string) error
    Reload(ctx context.Context) error
    CurrentURL(ctx context.Context) (string, error) // relaunch rung returns the last url if the process lives
    Alive() bool
    Stop() error
}
```

The ladder has two rungs. Rung 1 is the Chrome DevTools Protocol (CDP) on the loopback
port: `GET /json` to find the page target, then `Page.navigate`, `Page.reload` and
`Runtime.evaluate("location.href")` on its WebSocket. Rung 2 starts the browser again with
the target URL as its argument. The supervisor tries rung 1 at start and falls to rung 2
when CDP does not answer. It writes the chosen rung to the ops log. Tests must exercise
both rungs: CDP against a stub HTTP and WebSocket server, relaunch against a real stub
process.

## 7a. Player protocol

The browser opens `/player?k=<secret>[&resume=<index>]`. The SPA sends the secret in the
`X-PortaPixel-Player` header on each call. EventSource cannot set a header, so the SSE
URL carries `?k=<secret>`.

`GET /api/player/manifest`:

```json
{
  "fallback": false,
  "tier": "high",
  "playlist": {
    "name": "default", "title": "Lobby loop",
    "transition": "crossfade", "transition_ms": 500, "shuffle": false,
    "items": [
      {"index": 0, "kind": "image", "name": "welcome.jpg", "src": "/media/default/welcome.jpg", "duration": 15},
      {"index": 1, "kind": "video", "name": "promo.mp4", "src": "/media/default/promo.mp4", "mute": false, "max_duration": 0},
      {"index": 2, "kind": "url", "name": "https://dash.example.com/board", "url": "https://dash.example.com/board", "duration": 60, "refresh_seconds": 300}
    ]
  }
}
```

`fallback: true` means no playable content. Then `playlist` is null and the SPA shows the
fallback screen from `/api/status` and `/api/player/qr.svg`. The daemon applies defaults
(`image_duration`, playlist overrides) before it sends the manifest. The daemon does the
shuffle. `index` is the position in the list that the SPA received. `src` for a fleet
item is `/media/_fleet/media/<object>`.

`POST /api/player/heartbeat`, every 5 s:

```json
{"playlist": "default", "index": 1, "name": "promo.mp4", "kind": "video",
 "state": "playing", "frames": 18211, "position": 12.4}
```

`state` is `playing`, `fallback` or `handoff`. `frames` is a requestAnimationFrame
counter that only grows in one page life (D45). A new page starts at 0: the watchdog
must read a lower value as a reset, not as a stall. An optional `"note"` string gives one
line when something needs attention, for example a skipped item. The daemon writes a new
note to the ops log. The reply is `{"ok": true}`.

`POST /api/player/url-item` with `{"index": 2}`: the SPA stops. The daemon navigates to
the URL, waits for the dwell time, then opens `/player?k=...&resume=3`. A reply of
`{"skip": true}` means the URL is not reachable (D19). Then the SPA goes to the next item.

`GET /api/player/events` (SSE). Events: `playlist` (get the manifest again and start at
item 0), `grace` (the daemon wants to restart the browser; the SPA calls
`POST /api/player/ready` at the next item boundary), `reload` (reload the page now).

Kiosk mode (D42): when the active playlist is one URL item, the daemon keeps the browser
on that URL. The SPA does not run.

## 8. Web assets

No build step. No npm. The file in the repository is the file that ships.
`web/shared/` holds `pp.css` (the one stylesheet, made from the wireframe design tokens),
`fonts/` (IBM Plex, local files), `playlist-editor.js`, `item-warnings.js`, `api.js` and
`ui.js` (toast, modal, table helpers). The two admin UIs import them by URL path
`/shared/`. There is no Bootstrap (see CONTEXT.md section 3).

## 9. Test rule

Each package with logic has table tests. `go vet ./...`, `gofmt -l .` and `go test ./...`
must pass on Windows and Linux. Guard Linux-only code with build tags or runtime checks so
the tests run on a Windows developer machine.
