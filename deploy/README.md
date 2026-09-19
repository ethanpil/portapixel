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
| `server.toml` | The listen address, the public URL, the password hash, the certificate paths, the repository, the trusted proxies. |
| `portapixel.db` | The screens, groups, playlists, times, media rows, commands and releases. |
| `media/` | The media files, named by the SHA-256 of their bytes. |
| `thumbs/` | The thumbnails. |
| `releases/` | The mirrored release files of the approved version. |
| `ops.log` | What the server did. |

A backup of `server.toml` and `portapixel.db` rebuilds the fleet. The media files
are large, so back them up as files.

## The first run

The first run makes `server.toml`. It generates the admin password and prints it
one time. Read it from the log and record it. The server keeps only the hash of
the password, so it cannot print the password again.

To set another password later:

```sh
echo "a long password" | portapixel-server set-password --data /var/lib/portapixel-server
```

The Settings page of the admin UI does the same thing.

## The public URL, and how to reach the UI the first time

`public_url` is the address that a browser and a screen use, for example
`https://signage.example.com`. The server takes the Host header allowlist of the
admin UI from it (D46).

**While `public_url` is empty, the admin UI answers `localhost` and `127.0.0.1`
only.** A request with any other `Host` header gets 421. That is the safe default
against DNS rebinding, and it is also the one thing that surprises a new
operator: a browser on another machine cannot reach the UI yet.

So set the address before you open the UI from another machine. There are three
ways, and each one works before the first login:

- Docker: `PORTAPIXEL_PUBLIC_URL` in the compose file. The example sets it.
- A package install: `public_url` in `server.toml`, then start the server again.
- Neither: open the UI on the host itself at `http://localhost:8080/`, or make an
  SSH port forward, for example `ssh -L 8080:127.0.0.1:8080 thehost`.

A change of `public_url` in the admin UI needs no restart. The allowlist comes
from a function that runs on every request.

The device API, `/api/v1/`, takes no allowlist. A screen carries a token and no
cookie, so the allowlist would protect nothing there. It would also stop a screen
that uses an address the server does not know.

## Path 1: Docker

```sh
cd deploy
# Set PORTAPIXEL_PUBLIC_URL in docker-compose.yml first.
docker compose up -d
docker compose logs portapixel-server        # the password is in here
```

Then open the address that you set.

To update, pull the image tag again and start the container again (D28). The
volume keeps everything.

The compose file gives the container 15 s to stop. The server stops gracefully in
8 s, so that a download of a large video can finish.

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
`/etc/conf.d/portapixel-server`. Set `public_url` in
`/var/lib/portapixel-server/server.toml`, then start the service again.

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
`/var/lib/portapixel-server`. Set `public_url` in the `server.toml` of that
directory, then start the unit again.

## The environment

Three variables replace a value of `server.toml`. The environment wins over the
file, and the server never writes such a value back into the file.

| Variable | What it sets |
|---|---|
| `PORTAPIXEL_PUBLIC_URL` | `public_url`. It must start with `http://` or `https://`. |
| `PORTAPIXEL_LISTEN` | `listen`, for example `:8080`. |
| `PORTAPIXEL_TRUSTED_PROXIES` | `trusted_proxies`, as a comma-separated list. |

## HTTPS

A screen sends its token in every call. Over plain HTTP anybody on the path can
read it. So:

- On a closed network, plain HTTP is acceptable.
- Over the internet, use HTTPS. Put a reverse proxy in front, or set `tls_cert`
  and `tls_key` in `server.toml` to the two files and start the server again.

There is no automatic certificate in this release. You must supply a certificate
or a reverse proxy.

## A reverse proxy in front

Behind a proxy, every request arrives from the address of the proxy. Then one
rate-limit bucket holds the whole fleet: one card that loops on a revoked token
stops enrollment for every screen, and five wrong logins from anywhere lock the
admin out. The device rows also all show the address of the proxy.

So name the proxy in `trusted_proxies`. For a peer inside that list the server
reads the client address from `X-Forwarded-For` and the scheme from
`X-Forwarded-Proto`. For every other peer it ignores the two headers, because a
caller that reaches the server directly can put anything in them.

Leave the list empty when no proxy is in front.

## Add screens

The Enrollment page makes an invite token and shows a `[server]` block. Paste the
block into `portapixel.toml` on any number of cards before the first boot. Each
screen then registers itself under its own hardware ID (D25). A screen can also
show a six-character code that you approve on the Screens page.

A code waits 24 hours. After that the screen shows a new code, and at most 200
screens can wait at one time.

## Firewall

Open the listen port to the network that the screens are on. The server needs
outward access to `api.github.com` for the release list, and nothing else. A
server with no outward access still works: upload a release bundle on the
Versions page instead (D28).
