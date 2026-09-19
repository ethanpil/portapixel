# The fleet server

`portapixel-server` manages many screens from one place. It is not required:
one screen works fully on its own. Pair screens to a server only when you
run more than a few of them and want to set their playlists and schedules in
one place.

See `deploy/README.md` for how to install and run the server itself. This
page covers pairing, what the server manages, and the security notes that
matter for a fleet.

## The three ways a screen joins

### 1. An enrollment token

Make an enrollment token on the server's "Add screens" page. Put the token
block that the page shows into `portapixel.toml`, in the `[server]` table,
before the first boot of any number of cards. Each card then registers
itself under its own hardware ID at its first boot, and gets its own device
token back. All the cards can carry the same enrollment token and file,
because each one becomes a distinct, separate screen.

An enrollment token can pair a card at once, or make it wait for approval,
depending on how you made the token.

### 2. A pairing code

Leave `[server]` `token` empty and set only `url`. The screen then shows a
six-character code on its fallback screen and on its own Settings page. Open
the server's Screens page, find the code, and approve it.

### 3. The pairing card on the screen

Open the screen's own web UI, go to Settings, and type the server's address
into the pairing card there. With no token, this starts the pairing-code
flow above. With a token, the screen pairs at once.

## What the fleet manages

While a device is paired, the server manages exactly these settings:

- the playlists,
- the default playlist,
- the schedule rules,
- the screen's on and off times.

**Everything else stays local to the device.** This includes rotation,
audio, the network settings, `video_mode`, the transition and its length,
the nightly restart time, and whether updates install automatically.
Pushing a hardware setting to every screen in a fleet is how you brick a
screen that nobody is standing in front of.

The device's own Playlists and Schedule pages turn read-only while it is
paired, and say which server manages them. Unpair the device to take local
control back.

## Unpair

Open the device's Settings page and unpair it, or remove the screen from the
server's Screens page. Either action revokes the device's token. The
device's playlists and schedule pages become editable again, and the fleet
content it already downloaded stays on the media partition, unscheduled,
until you make a local playlist that uses it.

## Hardware repair, in plain words

A device derives its ID from its hardware: the Raspberry Pi serial number,
or a similar hardware value on a PC. When you move a stick into a different
box, the derived ID changes, but the stick still carries the same pairing
token.

The server treats this as a hardware repair, not as a new screen (D21). The
screen keeps its name, its pairing and its settings. The server shows a
notice that says the hardware ID changed, and one click confirms it and
retires the old ID.

**A true clone is different.** A clone is two separate boxes that both hold
a copy of one token and both call in at the same time. The server cannot
tell which box is the real screen, so it flags a conflict and changes
nothing on its own. Resolving the conflict revokes the shared token. Both
boxes then have to ask to join again, and you see which one comes back.

## Commands

The server can queue a command for a screen: reboot, restart the browser,
turn the screen on, turn the screen off, or look for new files. A screen
takes its queued commands at its next check-in.

## Approved releases and the release mirror

Screens paired to a server only ever install the version that an
administrator approves on the server's Versions page. The server downloads
that release from the public release page, checks its signature, and serves
it to every paired screen from its own mirror. See `docs/updates.md` for how
the health check and rollback work.

### Closed networks

When the server itself cannot reach the public release page, upload a
release bundle on the Versions page instead. The server checks every
signature in the bundle before it keeps a file from it.

A screen on a closed network with no path to the server at all can still
take an update: drop a signed release bundle into the `_update/` directory
of its media partition from a laptop (D52). The device applies it through
the same signature check, health check and rollback path as a downloaded
update.

## Security notes

- **Use HTTPS, or keep the fleet on a private network.** A screen sends its
  token on every call. Over plain HTTP, anybody on the path between the
  screen and the server can read that token. The device warns you when its
  server address is `http://` and is not a local address.
- **`trusted_proxies`.** Behind a reverse proxy, every request the server
  sees comes from the address of the proxy, not the real screen or admin.
  Name the proxy's address in `trusted_proxies` in `server.toml`, and the
  server then reads the real client address from the `X-Forwarded-For`
  header, and the real scheme from `X-Forwarded-Proto`, for that proxy only.
  Leave the list empty when no proxy sits in front of the server.
- **The first-run admin password.** The server makes its own admin password
  the first time it runs, and prints it once to its log. The server keeps
  only a hash of it, so read it from the log at that first run and record it
  somewhere safe; the server cannot show it to you again.
