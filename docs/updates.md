# Updates

This page covers how `portapixeld` updates itself. It does not cover the
underlying Alpine Linux operating system; see "OS updates" at the bottom.

## How an update is applied

PortaPixel keeps two release directories side by side, `current` and
`previous`, and a symlink that points at the one that runs. An update:

1. Downloads the new release, or reads it from a sideload bundle.
2. Checks its minisign signature against the public key that this build was
   compiled with, and checks its SHA-256 checksum.
3. Puts the new release in its own directory and flips the `current`
   symlink to point at it.
4. Restarts the daemon.

The new daemon has two minutes to write a health marker: the web server up,
and the browser session started. When the marker does not appear in time,
the device flips `current` back to the previous release, restarts again, and
marks the failed release as bad so that it is never offered again.

**No update ever half-applies.** Either the new release comes up and proves
itself healthy, or the device is back on the release it started from.

## Why a build might say it cannot install updates

A release binary is signed with a private key that only the project's
release process holds. A build made without that key, for example a
development build, has no public key to check a signature against. The About
page then says "This build cannot install updates," and the update route
answers with that same sentence instead of an error. This is a state of the
build, not a fault of your request.

## Automatic updates

`[updates]` `auto` is `false` by default: you decide when a screen changes,
and you check the About page or the Versions page to install one. Turn
`auto` on and a standalone device checks the public release page on its own
and applies a newer release when it finds one. A device paired to a fleet
server only ever installs the version that an administrator approved on
that server, whether `auto` is on or off.

## Sideload an update from the stick

On a closed network, or when the network path to a release source is down,
drop a signed release bundle into the `_update/` directory of the media
partition from a laptop (D52). Pull the stick, copy the bundle in, eject it
properly, and put the stick back. The device applies the bundle through the
identical signature check, health check and rollback path as a downloaded
update. The bundle is removed after it applies, whether it succeeds or is
rolled back, so a reboot never re-applies it.

## Fleet-approved versions

A screen paired to a fleet server ignores the public release page and the
`auto` setting for version choice. It only ever installs the version that an
administrator approved on the server's Versions page, fetched from that
server's own release mirror. See `docs/fleet.md` for how the server mirrors
and approves releases.

## OS updates

Upgrading the underlying Alpine Linux release is not automated in this
version of PortaPixel (D29). Each release pins an exact Alpine release, so
the packages on a device never drift on their own. An OS upgrade path is
planned for a later version; for now, treat an OS upgrade as a full
reinstall from a newer release image.
