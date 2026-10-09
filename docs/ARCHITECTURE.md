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
| `golang.org/x/sys` | The DRM ioctls of the DPMS screen power (`internal/device/power`). |
| `golang.org/x/image` | The text of the fallback screen: OpenType and Go fonts (`internal/device/fallback`, D18). |
| `golang.org/x/text` | Indirect, a dependency of `golang.org/x/image`. |
| `golang.org/x/net` | Indirect, through `miekg/dns`. |
| `github.com/hashicorp/mdns` | mDNS announce (D20). |
| `github.com/miekg/dns` | Indirect, through `hashicorp/mdns`. |
| `github.com/skip2/go-qrcode` | QR code on the fallback screen (D18). |

The standard library does all other work. Use `net/http` with Go 1.22 pattern routes
(`mux.HandleFunc("PUT /api/playlists/{name}", ...)`). Use `log/slog` for logs.

## 2. Package map

```
cmd/portapixeld/          device entry point. Subcommands, flags, wiring only.
cmd/portapixel-server/    server entry point. Flags and wiring only.
web/embed.go              package web. go:embed of device-admin, server-admin, shared.

internal/version          Version string and minisign public key, both set with -ldflags.
internal/rnd              Hex(n): the one random-text helper. Session tokens, a staging
                          directory name.
internal/fleet            The rules of a device call to a fleet server: ResolveURL (a
                          server-relative address becomes a whole address on the paired
                          host), DropBearerOffHost, NewClient, ContextUntil. The sync
                          client and the updater both import it.
internal/fsutil           WriteFileAtomic (stage, fsync, rename, fsync dir). FreeBytes.
internal/opslog           Bounded event log. 1200 lines trim to 1000.
internal/config           portapixel.toml model, defaults, Validate, Render, Load with shadow.
internal/playlist         playlist.toml model, Parse, Render, item kind detection.
internal/manifest         Fleet wire types (section 5). Shared by device and server.
internal/store            SHA-256 object store paths, HashFile, resumable Range download.
internal/sigverify        minisign verify of a file against the embedded key.
internal/updater          A/B release swap, minisign verify, health gate, rollback, sideload.
                          Device and server use it. No device specifics in it.
internal/httpguard        Host allowlist, CSRF header check, session store, login limiter.
internal/slug             One safe-name rule. A playlist directory, a host name and a
                          server playlist name share it.

internal/device/identity  Device ID derivation and repair semantics (D21).
internal/device/library   Scan media root, parse playlists, hash cache, item warnings.
internal/device/scheduler Rule evaluation each minute, clock-sync gate (D17, D40).
internal/device/player    mpv supervisor: command line, JSON IPC, playlist, transition
                          script, fallback screen, watchdog (section 7).
internal/device/fallback  Draw the fallback screen as a PNG (D18).
internal/device/power     CEC or DPMS, screen schedule, manual override (D31).
internal/device/syncer    Fleet client: enroll, poll, download, fleet playlists, commands.
internal/device/mdns      Announce _http._tcp as <name>.local (D20).
internal/device/health    Status struct for /api/status.
internal/device/netcfg    Render wpa_supplicant.conf and /etc/network/interfaces.
internal/device/installer install-to-disk: disk list, GPT clone, media copy, boot bits (D54).
internal/device/httpd     Routes, handlers, the SSE hub of the install progress. No
                          business logic.

internal/server          New(deps) http.Handler: the one route stack of the server.
internal/server/db        Schema, migrations, queries. Single writer.
internal/server/httpjson  The JSON answer shapes and the client-address rule.
internal/server/api       Device-facing /api/v1 routes.
internal/server/admin     Admin-facing /api/admin routes, sessions.
internal/server/media     Content-addressed media store, thumbnails, orphan sweep.
internal/server/releases  GitHub release list, mirror, bundle upload.
```

Rules:

- `internal/device/*` and `internal/server/*` must not import each other. A rule
  that the two ends share lives in a package of its own, for example
  `internal/slug` or `internal/manifest`.
- `httpd`, `api` and `admin` hold routes only. Logic lives in the other packages.
- The route stack of the server is built in one place, `internal/server.New`. The
  command and the route tests both call it, so a guard cannot be in one and not in
  the other.
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
typed. This keeps a flashed card clonable (D25). The fleet fields of `state.json` are
`device_token`, `claim_secret`, `pairing_code`, `pending_since`, `server_url`, `fleet` (the
last applied manifest with no commands) and `commands` (the last 100 command IDs that ran).

The fleet client keeps the whole last manifest and not only a hash of it, for two jobs in
one field: the poll compares the new manifest with it, so an answer that did not change
writes nothing at all; and the daemon hands the schedule to the scheduler at start, so a
paired device uses the fleet rules before its first poll answers.

Files in the run dir: `health/<version>.ok` (the health marker of an update) and the files
of the player (section 7).

The staging directories of a sync are `_fleet/.staging-<random>` and
`_fleet/.trash-<random>`. A name that starts with a full stop is never a playlist, so a
power cut in the middle of a swap leaves nothing that the library reads.

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
status is `paired`. The server keeps only the SHA-256 of that secret. A `pending`
answer with no `claim_secret` is a protocol fault: the device cannot poll with it.

One 401 ends a pairing, and only that one: the answer of the fleet API for a token
that this server does not hold, which carries `{"error":"...","code":"token-revoked"}`
(`httpjson.Revoked`). The device drops its token for that answer alone. Any other 401,
403 or 407 comes from something between the device and the server — a proxy, a captive
portal, a web application firewall — and is a network fault: the device backs off, says
so in `sync_error` and keeps the pairing.

The server gives the `token-revoked` answer only when the lookup of the token ends in
`db.ErrNotFound`. A request with no `Authorization` header gets a 401 with no code. Any other
error of the lookup, such as a busy or damaged database, gets a 500. A fault of the server
must never make a screen drop its pairing.

Every address that the manifest names (`media[].url`, `release.base_url`) is resolved
by `fleet.ResolveURL` against the address of the pairing. The result must be on that
scheme, host and port, and under its path, or the device leaves the object or the
release out. A manifest is not trusted input, even from a paired server.

Enrollment rules of the server (D25). A request that waits lives in its own table,
so it can never change a screen that works:

1. An enroll request never changes a `devices` row that was ever paired, except
   through rule 2.
2. A request with a valid auto-mode enrollment token, the device ID of a paired row
   and the stored `hardware_id` of that row gets a new device token. That is the
   reflashed card: the card lost its token and the box is the same box.
3. Every other request that names a paired row waits for the admin, with
   `collides_with` set. A request with no row and an auto token pairs at once.
4. A request that waits longer than 24 hours goes away. At most 200 wait at a time.
5. Each request that makes a new pending row counts against its address: five new rows
   in one hour (`httpguard.NewPendingLimiter`). The sixth gets 429 and writes nothing.
   The route takes a place in the count first, and `db.Enroll` refuses a new row with
   `db.ErrQueueLimited` when `mayQueue` is false. A request that makes no row gives its
   place back. A poll with a good claim secret makes no row, so it does not count.

`POST /api/v1/enroll` and every other JSON route of `/api/v1` and `/api/admin` need
`Content-Type: application/json`. A form on another site cannot send that type
without a preflight, so the check keeps those routes off a cross-site request.

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
    Transition string `json:"transition,omitempty"` // a word of config.Transitions; empty = the device setting
    Shuffle    *bool  `json:"shuffle,omitempty"`
    KenBurns   bool   `json:"ken_burns,omitempty"` // a slow zoom and pan on each image
    Items      []Item `json:"items"`
}
type Item struct {
    SHA256      string `json:"sha256,omitempty"` // the media object
    Duration    int    `json:"duration,omitempty"`
    Mute        bool   `json:"mute,omitempty"`
    MaxDuration int    `json:"max_duration,omitempty"`
    // The transition INTO this item. Empty and 0 = the playlist, then the device.
    Transition   string `json:"transition,omitempty"` // a word of config.Transitions
    TransitionMS int    `json:"transition_ms,omitempty"`
}
type Rule struct {
    Playlist string   `json:"playlist"`
    Days     []string `json:"days"`  // "mon".."sun"; empty = all days
    Start    string   `json:"start"` // "HH:MM"; both times empty = the whole day
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
    Type string            `json:"type"` // reboot | restart-player | screen-on | screen-off | rescan | update | rename
    Args map[string]string `json:"args,omitempty"` // rename: {"name": "<new name>"}
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

`ReleaseRef.Version` and the `releases.version` column hold a version without the letter
"v". `version.Normalize` turns the tag `v1.5.0` into `1.5.0`, and the release list of the
server applies it to each tag. A device reports the same form, so the manifest gate and the
Versions page compare equal names. `releases.prerelease` (migration 5) holds the flag that
GitHub sets on a release. `GET /api/admin/releases` gives it as `prerelease`, and the Versions
page shows a badge and a warning at the approval. The server has no "latest" choice: only
the approved version goes out. `updater.CompareVersions` orders two pre-release suffixes by
semver: `rc.9` is older than `rc.10`, and both are older than the final release.

`Status` is the same struct that the device serves at `/api/status` (plan section 8,
`health`). It lives in `internal/manifest` so the two ends share it.

The content model. An item is an image or a video, and nothing else: there are no
web page items and no single-URL kiosk mode. `internal/playlist` holds the one
extension table (`Kind`, `MediaType`): images `jpg jpeg png gif webp avif bmp`, videos
`mp4 m4v mov webm mkv ogv`. The device library, `/media/` and the server media types
all use it. The server library takes the same files and no others. The upload route
answers 422 on the `name` field for any other extension. A playlist save refuses an item
whose media row has such a name. The manifest names the object by that name, and the
device reads the kind from its extension. A transition is a word of `config.Transitions`: `cut`, `fade`, `fade-white`,
`crossfade`, `wipe-*`, `push-*`, `slide-in-*`, `slide-out-*`, `zoom-out` and `split`, where `*`
is `left`, `right`, `up` or `down` (owner decisions of 2026-10-08). A fade goes through black
and a fade-white through white. A wipe, a push, a slide-in and a slide-out move in the
direction of their word: a push moves both items, a slide-in moves the new item over the old
one, and a slide-out moves the old item away. `zoom-out` shrinks the old item into the centre,
and `split` opens it from the centre like a barn door. `playback.transition_ms` is the length
of each transition except `cut`. The config, the playlist files, the syncer, the server
database, the playlist editor and the device settings page use this one list: a Go test and
`web/embed_test.go` hold the Lua and the JavaScript lists equal to it. The server schema
migration 2 removed the url items and changed each old transition word to `"fade"` in the
rows of that time. Those words are transitions again, and the migration stays as it is.

An item can name its own transition: `transition` and `transition_ms` in `playlist.toml`, in
the manifest item and in the editor ("Playlist default" is the first choice). They are the
transition INTO that item, from the item before it. For the first item, that is the last item,
because the list loops. Each value falls back by itself: the item, then the playlist, then the
device (`library.BuildManifest` applies the rule, so the player has none). A length with no word
changes the length only. The server keeps the two values in `playlist_items` (migration 3).

`ken_burns = true` in the `[playlist]` table of `playlist.toml` (and `Playlist.KenBurns` in the
manifest, and `playlists.ken_burns`, migration 4) asks for a slow zoom and pan on each image of
the playlist while it shows (section 7a). It is a choice of the playlist. The device has no
setting for it.

`rename` gives a screen a new display name. The device owns its name: mDNS, the host
name and the fallback screen use it. The device saves `device.name` through the save
path of the admin, and the heartbeat of the same poll reports it. The server writes
`devices.name` from the enroll request and from `Heartbeat.Name`. A heartbeat changes
the name only when its `hardware_id` is the stored one and the row has no conflict.
`POST /api/admin/devices/{id}/rename` queues this command. A new rename expires each
older rename of the device that has no acknowledgement. Both ends apply
`manifest.CleanName`, and `config.Validate` applies it to `device.name`: 1 to 63
characters (one DNS label), no control character, no line or paragraph separator,
no bidi control and no invisible character. ZWNJ and ZWJ are permitted. A group
command cannot be `rename`.

The device refuses a `rename` (it logs and acknowledges it) while its configuration
is not the file on PPMEDIA: a shadow copy, the defaults, a repaired file, or a hand
edit that it refused or took only in part. The save writes the whole file, so it
would remove the values of the person.

`Status.NowPlaying.SHA256` (`now_playing.sha256`) is the hash of the file on the
screen. The server finds its library object with it, because the device reports the
object name `<sha8>-<name>` and not the library name. It is empty for a local file
that the device did not hash yet.

`Acks` holds at most 100 command IDs. A command that the server delivered and that
no heartbeat acknowledged goes out again after 10 minutes, three times in all, and
then it is expired.

A `Rule` has `Start` and `End` both set or both empty. Both empty means the whole
day, and the device scheduler reads the pair the same way.

`Status.Warnings` is `[]Warning{Code, Message}`. The code is the contract; the message
is the sentence for a person. The codes are the constants of
`internal/manifest/status.go`: `default-web-password`, `default-root-password`,
`timezone-utc`, `clock-unsynced`, `config-shadow`, `config-repaired`,
`config-bad-edit`, `playlist-problem`, `hardware-changed`, `update-rolled-back`,
`server-insecure`.

The player fields of `Status`: `player_state` uses the State words of the player
supervisor (`stopped`, `starting`, `running`, `waiting-for-display`, `disabled`).
`video_output` is `"gpu"` or `"drm"`. `hwdec` is the decoder of the current video, `"no"`
for software decoding, and `""` when no video plays. It is also `""` while a video plays
that ends in a moving crossfade: mpv gives no `hwdec-current` when its filter graph feeds
the output (section 7a). `now_playing.dropped_frames` counts
the frames of the current video that the player dropped. An empty value means that the
player did not report it. `Status` has no device tier and no codec report.

`hardware_id` is NOT in `Status`: it is a secret between the device and its server.
`Status.HardwareChanged` is a bool.

The device stores fleet objects at `_fleet/media/<first 8 hex of sha>-<safe name>`.

## 6. Local API rules (D46)

- Host allowlist: `localhost`, `127.0.0.1`, each local IP, `<name>.local`,
  `portapixel-<last4>.local`, each with or without the port.
- Session cookie `pp_session`: `HttpOnly`, `SameSite=Strict`, path `/`.
- Each request with a method other than GET or HEAD must carry `X-PortaPixel: 1`.
- `/api/status` needs no session. It includes `pairing_code` for loopback peers only.
- `/media/` needs no session. It serves image and video files only, for the previews of
  the admin UI. mpv reads the files directly.
- `GET /licenses` needs no session. A licence list is a public document (D33).
- `PUT /api/config` answers `{applied, changes:[{field, class}]}`. A class is `live`,
  `player` (a new player process applies the value) or `reboot`. `applied` is the
  highest class of the save (`config.ChangeClass`).
- The server uses the same package with an allowlist made from `public_url`.

### 6a. The v0.2 device routes

```
GET    /api/media/{playlist}       {files:[{name,size,kind,src,in_playlist}]}
POST   /api/update/check           {current, available?, source?, notes?, blocked?}
POST   /api/update/apply           {ok:true}; the work runs in the background
GET    /api/disks                  {disks:[{device,model,size_bytes,removable,too_small}], error?}
POST   /api/install-to-disk        {device, confirm} -> {ok:true}. confirm must equal device.
GET    /api/install-to-disk/events SSE: "progress" {phase,percent,message}, "done" {ok,error?,instruction?}
GET    /licenses                   text/plain, the licence list
```

### 6b. The v0.3 pairing routes

```
GET    /api/pair    {status: "unpaired"|"pending"|"paired", server_url, server_name?,
                     pairing_code?, last_sync?, sync_error?, insecure?}
POST   /api/pair    {url, token?} -> the same object. An empty token starts the code
                    pairing. The route saves url and token in portapixel.toml and enrolls
                    at once; it answers when the server answered.
DELETE /api/pair    {ok:true}. It forgets the token and the claim secret, takes [server]
                    url and token out of the TOML, and keeps the cached objects.
```

`pairing_code` is in `GET /api/pair` because that route needs a session. `/api/status`
gives the code to loopback callers only (D46). The fallback screen gets it inside the
daemon.

`insecure` is true when the address is `http://` on a host that is not loopback and not a
private network. The status then also carries the warning `server-insecure`.

While the device is paired, these routes answer 403 with
`{"error":"managed by <server name>","fields":[{"field":"...","message":"..."}]}`:
playlist create, save, rename and delete; media upload and delete. `PUT /api/config`
answers the same 403 for a change of a managed field and 200 for every other field.

The managed set is exactly what the manifest carries (D48):
`playback.default_playlist`, `schedule`, `display.on_time`, `display.off_time`,
`display.power_days`. `server.url` and `server.token` are refused as well while the
device is paired: they change through `/api/pair` only. Everything else, including the
other `[playback]` fields and `updates.auto`, stays with the local admin. The table is
`internal/device/syncer.ManagedFields`, and `GET /api/pair` carries the same list in
`managed_fields` while the device is paired, so the admin UI holds no copy of it.

The refusal is in the mechanism and not only in the route: the write methods of
`internal/device/library` answer `ErrManaged` while the device is paired, and a hand
edit of `portapixel.toml` keeps the running value of a managed field and raises the
status warning `config-managed-ignored`.

`POST /api/pair` on a device that is already paired answers 409. The device token and
the address it belongs to are written together and cleared together, so a token never
travels to a server that did not give it.

`blocked` in the check answer is the reason that this build can install nothing, for
example a development build with no minisign public key. It is not an error: the route
answers 200 and the About page says the sentence.

The install stream replays its last event to a new subscriber. The admin UI sends the
POST and opens the stream after the answer, so the first events are already gone.

## 7. The player (`internal/device/player`)

The player is mpv 0.40. It shows the content directly on DRM/KMS: there is no compositor,
no seat manager, no X and no Wayland. The daemon starts one mpv process as the `kiosk`
account and controls it over JSON IPC (section 7a).

```
mpv --no-config --profile=fast --idle=yes --force-window=yes --keep-open=yes \
  --loop-playlist=inf --prefetch-playlist=yes \
  --input-ipc-server=<run>/player/mpv.sock --script=<run>/transitions.lua \
  --input-default-bindings=no --osc=no --ytdl=no --load-stats-overlay=no \
  --load-console=no --load-auto-profiles=no --load-select=no --load-commands=no \
  --load-positioning=no --hwdec=auto-safe|v4l2m2m-copy --ao=alsa --msg-level=all=warn \
  --gpu-shader-cache=no --vo=drm | --vo=gpu --gpu-context=drm \
  [--video-rotate=<display.rotation>] [--drm-mode=<display.video_mode>]
```

This block is a copy for the reader. `command.go` is the source, and the mpv command line
lives there and nowhere else. mpv refuses an option that it does not know, so the image
needs mpv 0.40 or later. The environment of mpv is `PATH`, `HOME=<run>/player` and
`MESA_SHADER_CACHE_DISABLE=true`. HOME is on the small tmpfs of the run directory, so no
shader cache goes there: not the one of mpv (`--gpu-shader-cache=no`) and not the one of
Mesa.

`--player-cmd` or `PORTAPIXEL_PLAYER_CMD` replaces the program. Its own arguments come
after the arguments of the daemon, and mpv uses the last value of an option. An override
keeps the environment of the daemon, because a desktop needs `DISPLAY` or
`WAYLAND_DISPLAY`. The value `none` switches the player off (`player_state: "disabled"`).

The files of the player in the run directory (a tmpfs):

| File | Owner | What |
|---|---|---|
| `player/` | kiosk, 0700 | The only directory that mpv can write. It holds `mpv.sock`, and it is `HOME`. |
| `transitions.lua` | root, 0644 | The transition script. The daemon writes it from the binary at each start. |
| `fallback.png` | root, 0644 | The fallback screen (D18). The daemon writes it atomically. |
| `player.log` | root, 0640 | The output of mpv. 1 MiB at most, new at each start. |

The video output comes from `display.video_output`. `auto` takes `gpu` when a graphics card
has a hardware OpenGL driver in Mesa: `vc4`, `v3d`, `i915`, `xe`, `amdgpu`, `radeon` or
`nouveau` (the name can end in `-drm`). Each other driver gets `drm`. virtio_gpu, bochs and
simpledrm have no GPU, and vo=gpu there is llvmpipe. The daemon reads the `DRIVER=` line of
`/sys/class/drm/card*/device/uevent`. `status.video_output` names the output that runs.

The hardware decoder comes from the board. The daemon reads `/proc/device-tree/model` one
time. A name that starts with `Raspberry Pi` gives `--hwdec=v4l2m2m-copy`; every other
board gives `--hwdec=auto-safe`. auto-safe tries only the decoders of the mpv whitelist
(`video/decode/vd_lavc.c`): d3d11va, dxva2-copy, nvdec, vaapi, vulkan, vdpau-copy, drm,
drm-copy, mediacodec-copy and videotoolbox, with their `-copy` forms. `v4l2m2m` is not in
that list, and the H.264 decoder of a Pi Zero 2 W, 3 and 4 is a V4L2 memory-to-memory
device. `v4l2m2m-copy` is FFmpeg's `h264_v4l2m2m` with the frames in memory, which works
with vo=drm, vo=gpu and the moving crossfade. A Pi 5 has no H.264 decoder. The HEVC
decoder of a Pi 4 and a Pi 5 needs the V4L2 request API (`drm` and `drm-copy`), and the
FFmpeg of Alpine aarch64 does not have it, so every Pi gets `v4l2m2m-copy`. mpv falls back
to software decoding by itself when a hardware decoder fails.

On a Pi Zero 2 W, 3 and 4 each video item also gets `vd-lavc-o=num_capture_buffers=8`
(section 7a): `h264_v4l2m2m` takes 20 capture buffers from the CMA area by default, about
3.1 MB each at 1080p, and a 512 MB Pi has a CMA area of 128 MB. The option is per file and
not on the command line, because each decoder that does not know it writes an error line.

The exit reason in the ops log (`player.exit`) is the last line of the output of mpv that is
not noise. The noise is the lines of the hardware decoder probe of auto-safe (vaapi, Vulkan,
VDPAU, at each video on a device that does not have them), the two lines of the DRM output
with no TTY, and the line of a decoder that does not know the buffer option.

The kiosk account. mpv parses files from removable media and from the network, so it never
runs as root. The kernel makes the first process that opens a DRM primary node the DRM
master, also without root. mpv gets the groups `video` (`/dev/dri/card*`) and `audio`
(`/dev/snd`). Alpine has no `render` group: eudev gives `/dev/dri/renderD*` to `video` with
mode 0666, so the render node of vo=gpu (a Pi 4 renders on v3d) needs nothing more. Proven
on the pp-zero lab VM on 2026-10-08: vo=drm and vo=gpu as `kiosk`.

Rotation goes to `--video-rotate`, and `display.video_mode` goes to `--drm-mode`. A change
of `display.rotation`, `display.video_mode`, `display.video_output` or `audio.output`
restarts mpv (change class `player`). A `video_mode` that the display does not have does
not stop mpv. No file can show, and mpv goes idle. The ops log can hold `player.item.fail` and
`player.playlist.fail` lines, and `player.log` holds the error of mpv.

The watchdog ladder (plan 3.3). The thresholds come from `[watchdog]` (D30). T is
`heartbeat_timeout`.

| Fault | Rule |
|---|---|
| mpv ends | Start again at once. An mpv that ends before its first picture waits 5 s, doubled up to 60 s. |
| The IPC does not answer | No socket T after the start, or a request with no answer for T. |
| A video does not move | The `time-pos` of the video does not change for T. |
| An image does not go on | The same image for its duration plus T. One image alone is not checked. |

Each fault above is a counted restart. `restarts_before_reboot` counted restarts inside
`restart_window` reboot the device. A restart from a person, from a setting or from the
nightly job does not count. After a fault, the new mpv starts at the item after the item
on the screen. After a restart from a person, it starts at the same item.

The supervisor measures each duration with `time.Now`, which has a monotonic reading. A device
with no RTC gets a step of its wall clock, of hours or days, at the first sync of the clock,
while mpv already runs. That step is not a stall. `Options.Local` gives a time of day (the
nightly restart, the clock of the fallback screen) in the zone of the device, and it is never
used to measure a duration.

No display is a wait and never a fault (D44). The daemon reads `/sys/class/drm/*/status`
every 5 s. While nothing is connected, the period doubles up to 60 s, mpv stops, and
`player_state` is `waiting-for-display`. An mpv that ends on its own makes the daemon read
the connectors at once. If nothing is connected, the ending is part of the wait and it does
not count.

The nightly restart (`playback.nightly_restart`) waits for the end of the item on the
screen, for 60 s at most. A screen schedule that has the screen off at that time skips
it. The fallback screen and a playlist of one item have no item boundary, so the restart
is immediate.

Screen power. `power.Controller` stops mpv first and then switches the display off.
`Suspend` returns when the process has ended, so the DRM device is free. Going on, the
display comes first and mpv second.

The health marker of an update (`<run>/health/<version>.ok`): the daemon writes it after
the first picture of mpv, content or the fallback screen. A device that waits for a
display, a device with the screen off and a player that is off are also up.

## 7a. Player protocol: JSON IPC with mpv

The daemon connects to `<run>/player/mpv.sock`. Each request is one line
`{"command": [...], "request_id": N}`, and mpv answers the requests in order. The client
uses the standard library only.

At the connect the daemon observes four properties:

| ID | Property | Use |
|---|---|---|
| 1 | `idle-active` | `true` after a file started: no item of the list could play. |
| 2 | `hwdec-current` | `status.hwdec` while a video plays. `no` is software decoding. |
| 3 | `user-data/pptr/fault` | A fault of `transitions.lua`. The daemon writes it to the ops log. |
| 4 | `user-data/pptr/moved` | The result of a moving crossfade: `{count, dropped, frames, failed}`. The guard reads it. |

At the connect, and at each playlist change, the daemon also sets
`user-data/pptr/motion` to `"yes"` or `"no"` (the moving crossfade, below), before it loads
the list.

The events that it reads are `start-file`, `playback-restart` (an item shows its first
frame), `end-file` (`reason`, `file_error`, `playlist_entry_id`) and `property-change`.

The manifest (`library.PlayerManifest`) goes to the player in the same process. The daemon
applies the defaults (`image_duration`, the playlist overrides) and does the shuffle. The
player gives mpv the whole list, and mpv reads each file directly from the media root:

```
["loadfile", "<path of item 0>", "replace", -1, {<per-file options>}]
["loadfile", "<path of item 1>", "append", -1, {<per-file options>}]
...
```

A restart starts the list at the resume item. mpv loops the list, so the order stays the
order of the playlist. The per-file options:

| Option | Item | Value |
|---|---|---|
| `script-opts` | each | `pptr-kind=<transition into the next item>,pptr-ms=<its length>`, and `pptr-kb=<seconds>` for an image with Ken Burns |
| `image-display-duration` | image | the duration; `inf` for one image alone |
| `mute` | video | `yes` or `no`, from the `mute` of the item |
| `end` | video | `max_duration`, when it is set |
| `vd-lavc-o` | video | `num_capture_buffers=8` on a Pi Zero 2 W, 3 and 4 (section 7) |
| `loop-file` | video | `inf` for one video alone |

The script works when an item ends, so each entry carries the transition INTO the next item
of the list (`fileOptions`): the item after it, or the first item for the last entry. The list
is in play order, also after a restart that starts it at the resume item. The manifest has the
defaults applied already (item, then playlist, then device), so the player reads two fields.
`pptr-kb` is set only when the playlist asks for Ken Burns, the output is `gpu`, and the item
is an image that has an end (one image alone has `image-display-duration=inf`). On `drm`, the
daemon writes one ops log line (`player.kenburns.off`) when it loads the playlist.

`transitions.lua` sets more options on the file itself (`file-local-options/...`) for a
moving crossfade: `lavfi-complex`, `end` and `hwdec` on the item that ends, and `start` on
the item that comes next.

A schedule change, a library change or a playback setting gives a new manifest. The
daemon replaces the list only when the manifest is different. No playable content gives
the fallback screen:

```
["loadfile", "<run>/fallback.png", "replace", -1,
 {"image-display-duration": "inf", "script-opts": "pptr-kind=cut,pptr-ms=0"}]
```

The daemon draws the fallback screen at the size of the display: `display.video_mode`, else
the first mode of the connected connector, else 1920x1080, turned for a rotation of 90 or
270. It draws it again for new data, for a new minute and every 3 minutes for the burn-in
shift. When no item of a list can play, mpv goes idle. The fallback screen then shows, and
the daemon tries the list again after 5 minutes.

Every 2 s the daemon asks for `playlist-pos`, and for a video also `time-pos`,
`frame-drop-count` and `decoder-frame-drop-count`. At each `playback-restart` it asks for
`playlist-pos` and the two counters again. `now_playing.index` is the index in the
manifest, and `now_playing.since` is the time of the first frame.
`now_playing.dropped_frames` is the sum of the two counters since that frame. mpv starts
the counters again at each file.

The transition script `transitions.lua` is mpv Lua with the LuaJIT FFI. When an item ends,
its `on_unload` hook covers the screen with a copy (`screenshot-raw window bgra`, OSD
overlay 62). The next item loads under the copy. At its `playback-restart`, a 20 ms timer
moves the copy away. The script uses `pptr-kind` and `pptr-ms` of the item that ends (the
transition into the next item) and `pptr-kb` of the item on the screen. vo=drm has no
screenshot of its own and mpv stretches the frame to the window, so on vo=drm the script
scales the copy back into the video rectangle (`osd-dimensions`) and makes the bars black.
An item that shows no frame (it did not load) keeps the copy of the last good frame for
the next item.

| Kind | Effect |
|---|---|
| `cut` | No copy. |
| `fade` | Dip to black. The copy goes dark (a pass of LuaJIT over the copy). Then a black square of 256 x 256 pixels, scaled to the screen, takes its place in overlay 62 and goes clear over the next item. One overlay at a time. |
| `fade-white` | The same, through white. |
| `crossfade` | The copy goes clear over the next item. With motion, both items move (below). |
| `wipe-left`, `-right`, `-up`, `-down` | The copy gets smaller; its edge moves in the direction of the word. |
| `push-left`, `-right`, `-up`, `-down` | The copy moves out in the direction of the word, and `video-pan-x` or `video-pan-y` moves the next item in behind it. The script moves the next item first. |
| `slide-in-left`, `-right`, `-up`, `-down` | The next item moves in over the copy, which stays where it is. The copy gets the crop of the wipe, and `video-pan` moves the next item as in the push. |
| `slide-out-left`, `-right`, `-up`, `-down` | The copy moves out in the direction of the word and the next item stays where it is. The cheapest kind: only the position of the overlay changes. |
| `zoom-out` | The copy gets smaller toward the centre of the screen (the destination size of the overlay). |
| `split` | The copy is two halves in two overlays (62 and 63). They move apart from the centre. The two overlays do not touch, and they draw right on `drm` and on `gpu`. Stacked overlays are still wrong on `drm`. |

Only `fade` and `fade-white` change pixels. Each other kind moves, crops or scales an overlay.
The square of the dip is not one pixel: vo=gpu puts a clear border of one pixel around each
bitmap and smooths the scaled bitmap into it, so a bitmap of one pixel made the dip half clear
at the edges of the screen.

Each fault of the copy ends in a cut: a failed copy, an overlay that fails, a Lua error, or a
next item that does not show in 5 s. With no LuaJIT FFI, each transition is a cut.

Ken Burns. An image with `pptr-kb` zooms and pans slowly while it shows. After the transition
into the image is over (so it never uses `video-pan` together with a push or a slide-in), a
timer sets `video-zoom`, `video-pan-x` and `video-pan-y` every 100 ms from the clock, over the
rest of the time of the image. The zoom goes in from 1 to 1.12. It starts at the plain picture,
which is what the transition showed, so nothing jumps when the transition ends. The drift goes to
a random side and is 4 % of the picture at most, so the edge of the picture does not show. When
the image ends, the script puts the copy of the screen on top and sets zoom and pan back to 0
under it. A cut after Ken Burns holds the copy until the next item shows its first frame (the
kind `join`), or the next item would show with the zoom of the old one.

Ken Burns runs on vo=gpu only. On vo=drm (virtio_gpu, simpledrm, unknown drivers) mpv does a
`reconfig` of the output and scales the whole picture in software at each step of the zoom, and
`screenshot-raw window` there returns the frame with no zoom, so the copy of a transition would
jump. Measured on the pp-zero lab VM (1280x800, one image of 8 s, CPU of mpv as a part of one
core): vo=drm unthrottled 24 % at 10 steps in a second, 11 % at 4 and 6 % at 2; throttled
(3 ms of run in 20 ms, the Pi Zero 2 W proxy) 131 %, 63 % and 36 %. The VM has no GPU, so its
vo=gpu is llvmpipe and says little about a Pi: unthrottled 35 % at 10 steps in a second and
16 % at 4. Each step on a real GPU is a change of two properties and one redraw. The step stays
at 100 ms on vo=gpu, because the zoom of 12 % in 8 s then moves the picture by about 2 pixels
at each step.

The moving crossfade. `playback.motion` is `auto`, `on` or `off` (default `auto`, change
class `live`). `auto` is on for an x86_64 device and for a Raspberry Pi 5 or Compute
Module 5 (`/proc/device-tree/model`), and off for every other board. When
`user-data/pptr/motion` is `"yes"`, a `crossfade` from a video A to the next item B has
motion on both sides:

1. The `on_preloaded` hook of A adds the video of B as a track (`video-add <B> auto`) and
   sets a file-local `lavfi-complex` on A:
   `[vidB]scale=<A size>,setsar=1,format=yuva420p,fade=t=in:d=<d>:alpha=1,setpts=PTS-STARTPTS+<len-d>/TB[mix];[vidA][mix]overlay[vo]`.
   An image B gets `loop=loop=-1:size=1,fps=<A rate>` first. overlay passes the frames of A
   through with no work until the mix starts. (xfade takes 4:4:4 frames only and would
   convert each frame of A.)
2. A gets a file-local `end` at the end of the mix, and `hwdec` becomes `auto-copy` when it
   was `auto-safe`: the graph needs the frames in memory.
3. B starts at `start=<d>` (set in its `on_load` hook) under the copy, and the copy goes
   away at its first frame (the kind `join`). No part of B plays two times. A B with less
   than 0.1 s left after `<d>` (a clip as short as the mix) starts at 0 and plays in full.
4. The audio is the audio of A. mpv marks the other tracks of B `no_auto_select`.

The script makes a moving crossfade only when A is a video of the same shape as B, with no
rotation and square pixels, A has 1 s before the mix, and no moving crossfade failed in this
mpv. Else the crossfade uses the copy.

Faults. A graph that fails in its setup makes mpv refuse the item (`end-file` `error`). A
graph that stops in the mix ends the item early. mpv writes each fault of the graph with the
prefix `lavfi`, and the script listens to the error messages: an item that ends early with
such a message is a fault of the graph, and an item that ends early for another reason (a
file that does not decode, a file shorter than its duration) is not. For a fault of the
graph the script reports `{failed: true}`, makes no more moving crossfades in this mpv, and
uses the copy. When A showed no frame, the script plays A again with no graph. B is never
skipped, and the other transitions go on.

The guard. After each moving crossfade the script sets `user-data/pptr/moved` to its
dropped frames (`frame-drop-count` plus `decoder-frame-drop-count` over the mix) and the
frames of the mix. With `playback.motion = "auto"`, when a crossfade failed or dropped more
than a quarter of its frames, the daemon sets `user-data/pptr/motion` to `"no"` until the
daemon stops (also through a restart of mpv) and writes one ops log line,
`player.motion.off`. `"on"` is the choice of the owner: the daemon writes
`player.motion.slow` (at most once an hour) and keeps the moving crossfade on. A new value
of `playback.motion` clears the guard.

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
