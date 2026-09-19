# Releasing PortaPixel

This document is for the person who cuts a release. It says what to press, what
each job proves, how long a run takes, and what to do when a job fails.

The release runs in GitHub Actions. Two workflows do the work:

| File | When it runs | What it does |
|---|---|---|
| `.github/workflows/lint.yml` | every push and every pull request | shell lint, Go lint, the race detector, the web asset gate |
| `.github/workflows/build.yml` | manual start, or a pushed tag `v*` | the whole release: binaries, images, smoke tests, the GitHub Release |

## 1. One value

The release tag is the one value of a release. `VERSION` is the tag with no
leading `v`:

```
tag v0.1.0  ->  VERSION 0.1.0
```

`VERSION` goes into the binary with `-ldflags -X`, into the image, into the
release directory `/opt/portapixel/releases/0.1.0/`, and into the health marker
`health/0.1.0.ok`. The `settings` job proves with `tests/ci/tagcheck` that
`internal/updater.NormalizeVersion` makes the same value from the tag. Two values
here would roll every good release back and mark it bad for ever (D47).

Never edit the version anywhere else. The tag is the only place.

## 2. Cut a release

1. Put the changes at the top of `CHANGELOG.md`. The release note is the top
   section of that file, so a good entry is a good release note.
2. Check off `docs/release-checklist.md` on real hardware.
3. Start the workflow:

```sh
gh workflow run build.yml \
  -f release_tag=v0.1.0 \
  -f alpine_release=3.23.2 \
  -f draft=true \
  -f prerelease=false
```

4. Watch the run:

```sh
gh run watch "$(gh run list --workflow=build.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```

5. Read the draft release, then publish it in the GitHub web interface, or with:

```sh
gh release edit v0.1.0 --draft=false
```

A tag push (`git tag v0.1.0 && git push origin v0.1.0`) starts the same pipeline
with `alpine_release` 3.23.2 and `draft` true.

### The inputs

| Input | Default | What it means |
|---|---|---|
| `release_tag` | `v0.0.0-dev` | the tag, and with it `VERSION`. It must start with `v` and a number. |
| `alpine_release` | `3.23.2` | an EXACT Alpine release. Never a branch: a branch moves and two builds of one release would differ (D50). |
| `draft` | `true` | a draft release is not public. Keep it true until you read the release. |
| `prerelease` | `false` | a prerelease is public and is not offered to a device, because the updater asks for the newest full release. |

`draft` and `prerelease` also decide the `latest` tag of the server container
image: only a release that is neither gets it.

## 3. Signing: the owner does this one time

The device holds the minisign public key inside the binary and refuses every
release that the matching secret key did not sign (D47). Until the key is set, a
release carries no signature and **no device from that release can ever install
an update**: the release note says so in capitals.

**The key must never change after release 1.** A device in the field holds the
public key of the release that it runs. A new key makes every device in the field
refuse every later release, and only a person with a USB stick can fix that
(D47). Keep the secret key in a password manager AND in one offline copy.

Run these commands yourself. Do not give the secret key to anybody, and do not
put it in a file inside the repository (`.gitignore` already refuses `*.key`).

```sh
# 1. Make the key pair. Choose a strong password when it asks.
minisign -G -p minisign.pub -s minisign.key

# 2. The secret key goes in a repository SECRET.
gh secret set MINISIGN_SECRET_KEY < minisign.key

# 3. The password goes in a second secret. Leave this out for a key with no
#    password.
gh secret set MINISIGN_PASSWORD

# 4. The public key goes in a repository VARIABLE. Give the BASE64 LINE only,
#    without the "untrusted comment" line: -ldflags -X cannot carry a newline.
gh variable set MINISIGN_PUBLIC_KEY --body "$(tail -n 1 minisign.pub)"

# 5. Put minisign.pub and minisign.key in your password manager, then remove
#    them from this machine.
rm -f minisign.key
```

Check what is set (the value of a secret is never readable again):

```sh
gh secret list
gh variable list
```

The pipeline holds these rules:

- A secret key with an empty `MINISIGN_PUBLIC_KEY` **fails** the run. Such a
  release would carry signatures that no device could check.
- Every signature that CI makes is verified with the public key **before**
  anything is published. A key pair that does not match stops the run.
- The secret key is written to a file under `/dev/shm` with `umask 077` and is
  never printed.
- The signatures are PREHASHED (`minisign -S -H`). `internal/sigverify` refuses a
  legacy signature, because a check of one would have to hold a whole release
  binary in the memory of a device with 512 MB.
- All four binaries are signed. The fleet server self-updates through the same
  updater.

## 4. What each job proves

| Job | It proves |
|---|---|
| `settings` | the tag gives one version, `internal/updater` reads the same value, and the signing settings agree |
| `lint` | shellcheck and busybox ash read every shipped script, `gofmt`, `go vet`, `go mod tidy`, `go test -race`, the cross-compile of both commands, and that no web file asks another host for a file |
| `build` (amd64, arm64) | both commands build with no C compiler, the web assets are inside the binary (`selftest`), and the binary prints the version of the tag |
| `sign` | one `SHA256SUMS` in the format that the updater parses, and a signature for each binary that verifies with the public key |
| `images` (x86_64, aarch64) | `os/build-image.sh` makes a bootable image from the exact Alpine release, PPROOT leaves room for a second release, the initramfs finds USB, SD, eMMC, NVMe and SATA (D53), and the package manifest is there (D50) |
| `smoke-x86` (bios, uefi) | the x86_64 image boots under SeaBIOS and under OVMF, `/api/status` answers on port 80, and the browser session comes up |
| `smoke-arm` | the aarch64 image holds a daemon that starts and passes `selftest` under qemu-user, the display packages are in it, the overlay is in place, and PPBOOT holds every Pi firmware file |
| `smoke-update` | the A/B update path with the real binary and the real health gate: a signed release is installed and swapped, the daemon writes `<VERSION>.ok`, a release with no marker is rolled back and marked bad, and an unsigned release, a release with another key, a legacy signature and a changed binary are all refused before the flip |
| `server` | the server route tests pass, the Docker image builds for both architectures, it goes to GHCR, and it runs and answers |
| `smoke-vanilla-install` | `os/install.sh` puts PortaPixel on a stock Alpine box, live, as root, and the box answers after a reboot (D51). It is NOT blocking: it boots two virtual machines and it is the longest job |
| `release` | every deliverable is present, and one GitHub Release holds them |

`smoke-x86`, `smoke-arm` and `smoke-update` are blocking. The `release` job runs
only after `lint`, `sign`, `images`, the three smoke gates and `server` pass.

## 5. What a release holds

Two `.img.gz` images, two `portapixeld-<arch>` binaries, two
`portapixel-server-<arch>` binaries, `SHA256SUMS`, a `.minisig` for each binary
when the key is set, a package manifest for each image, `install.sh`,
`LICENSES-THIRD-PARTY.md` and `portapixel-server-deploy.tar.gz`.

The updater of a device downloads exactly three of these:
`portapixeld-<arch>`, `portapixeld-<arch>.minisig` and `SHA256SUMS`. Do not
rename them. The repository must stay public, because a device asks GitHub with
no account (D34).

`actions/upload-artifact` is never the channel of a deliverable. It always makes
a zip, and a device downloads single files. The workflow uses it only to move
files between jobs.

## 6. How long a run takes

| Job | Time |
|---|---|
| `settings` | under 1 minute |
| `lint` | 2 to 4 minutes |
| `build` | 2 minutes for each architecture |
| `sign` | under 1 minute |
| `images` | 6 to 12 minutes for each architecture |
| `smoke-x86` | 5 to 10 minutes for each firmware, with KVM |
| `smoke-arm` | 3 to 6 minutes |
| `smoke-update` | 3 to 6 minutes |
| `server` | 6 to 12 minutes |
| `smoke-vanilla-install` | 30 to 60 minutes |
| `release` | 2 to 5 minutes |

The whole run is about 35 to 50 minutes, because the jobs run beside each other.
The vanilla install job is the long one and it does not block the release.

Two runs never overlap: the `concurrency` group `portapixel-release` makes the
second run wait.

## 7. When a job fails

Read the log first:

```sh
gh run view <run-id> --log-failed
```

| Job | What to look at |
|---|---|
| `lint` | The shell job runs in a real Alpine container, so a fault there is a fault of the script and not of your machine. `go test -race` runs on Linux; the development machine is Windows and has no C compiler. |
| `build` | A version that does not match the tag stops the job with both values in the message. |
| `sign` | "the signature does not verify with MINISIGN_PUBLIC_KEY" means the secret and the variable are not a pair. Do not publish that run. |
| `images` | A size gate that fails wants a package out of `os/packages.list` or a bigger `P2_MB`. A missing module means the `mkinitfs` feature list in `os/install.sh` (D53). A missing Pi boot file means `linux-rpi` moved its files again. |
| `smoke-x86` | "no login prompt" is the boot chain. "/api/status did not answer" is the daemon; the log holds the serial output. "browser_state never running" is the browser: read `/var/cache/kiosk/browser.log` on the guest with `tests/qemu/boot-dev.sh`. |
| `smoke-arm` | This job mounts the image. A missing file is named. It cannot boot the image: only a real Pi proves a Pi boot. |
| `smoke-update` | This gate uses its own key pair, so it fails for a code reason and never for a key reason. |
| `server` | The Docker image is built by `deploy/Dockerfile`. A container that does not answer prints its own log in the job. |
| `smoke-vanilla-install` | It is not blocking. A fault is a fault of `os/install.sh` in on-box mode. It boots two virtual machines, so read the serial output from the start. |

A failed run publishes nothing. Start a new run with the same tag after the fix:
the tag is made by the release step, so a run that never got there left no tag.

To take a draft release away again:

```sh
gh release delete v0.1.0 --yes
git push --delete origin v0.1.0   # only if the release step made the tag
```

## 8. After the release

- Move `docs/release-checklist.md` results into the release notes or the pull
  request.
- Write the release in `CHANGELOG.md` under its own heading, so the next release
  note starts clean.
- A device with auto-update off asks for the release on the About page. A fleet
  device gets it only after the server approves it (D28).
