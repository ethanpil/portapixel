# Install the PortaPixel fleet server

The fleet server manages many screens: playlists, times, media, remote commands
and the approved version. A screen plays without it. The server makes a fleet of
screens one job instead of many (plan section 12).

The server is one static binary. It needs no database server, no web server and
no run time. It runs anywhere a process stays alive: a virtual machine, a NAS, a
Docker host or a Raspberry Pi.

Every screen calls the server. The server never calls a screen. So the server
needs one open port and the screens need none.

## What the server keeps

Everything is in one data directory, `/var/lib/portapixel-server` by default:

| Name | What it holds |
|---|---|
| `server.toml` | The listen address, the public URL, the password hash, the certificate paths, the repository, the poll interval. |
| `portapixel.db` | The screens, groups, playlists, times, media rows, commands and releases. |
| `media/` | The media files, named by the SHA-256 of their bytes. |
| `thumbs/` | The thumbnails. |
| `releases/` | The mirrored release files of the approved version. |
| `ops.log` | What the server did. |

A backup of `server.toml` and `portapixel.db` rebuilds the fleet. The media files
are large, so back them up as files.

## The first run

The first run makes `server.toml`, generates the admin password and prints it one
time. Read it from the log and write it down: the server keeps only the hash of
it, so it cannot print it again.

To set another password later:

```sh
echo "a long password" | portapixel-server set-password --data /var/lib/portapixel-server
```

The Settings page of the admin UI does the same thing.

## Path 1: Docker

```sh
cd deploy
docker compose up -d
docker compose logs portapixel-server        # the password is in here
```

Open `http://<this host>:8080/`. Then set the public URL on the Settings page to
the address that the screens use, and restart the container.

To update, pull the image tag again and start the container again (D28). The
volume keeps everything.

## Path 2: Alpine with OpenRC

```sh
install -m 755 portapixel-server /usr/local/bin/
install -m 755 portapixel-server.initd /etc/init.d/portapixel-server
install -m 644 portapixel-server.confd /etc/conf.d/portapixel-server
addgroup -S portapixel
adduser -S -D -H -G portapixel portapixel
install -d -o portapixel -g portapixel -m 750 /var/lib/portapixel-server
rc-update add portapixel-server default
rc-service portapixel-server start
grep -A4 "first run" /var/log/portapixel-server.log
```

Change the data directory or the listen address in
`/etc/conf.d/portapixel-server`.

## Path 3: systemd

```sh
install -m 755 portapixel-server /usr/local/bin/
install -m 644 portapixel-server.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now portapixel-server
journalctl -u portapixel-server | grep -A4 "first run"
```

The unit uses `DynamicUser=yes` and `StateDirectory=`, so there is no account to
make and no directory to own. The state directory is
`/var/lib/portapixel-server`.

## HTTPS

A screen sends its token in every call. Over plain HTTP anybody on the path can
read it. So:

- On a closed network, plain HTTP is acceptable.
- Over the internet, use HTTPS. Put a reverse proxy in front, or set `tls_cert`
  and `tls_key` in `server.toml` to the two files and restart.

There is no automatic certificate in this release. Bring a certificate or bring a
proxy.

## The public URL

`public_url` is the address that a browser and a screen use, for example
`https://signage.example.com`. The server takes the Host header allowlist of the
admin UI from it (D46). While it is empty, the admin UI answers on `localhost`
and `127.0.0.1` only, so a fresh server is not open to a name that somebody else
controls.

The device API, `/api/v1/`, takes no allowlist: a screen carries a token and no
cookie, so the allowlist would protect nothing there and would stop a screen that
uses an address the server does not know.

## Add screens

The Enrollment page makes an invite token and shows a `[server]` block. Paste the
block into `portapixel.toml` on any number of cards before the first boot, and
each screen registers itself under its own hardware ID (D25). A screen can also
show a six-character code that you approve on the Screens page.

## Firewall

Open the listen port to the network that the screens are on. The server needs
outward access to `api.github.com` for the release list, and nothing else. A
server with no outward access still works: upload a release bundle on the
Versions page instead (D28).
