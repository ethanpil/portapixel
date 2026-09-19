# Troubleshooting

This page lists common problems, what causes each one, and what to do about
it.

| Symptom | Likely cause | What to do |
|---|---|---|
| The screen stays black | The browser did not start, or it crashed. | Connect over SSH. Read `/var/cache/kiosk/browser.log` first. Then read the ops log: open Activity in the web UI, or read `/var/lib/portapixel/ops.log`. Run `wlr-randr` to check that a display mode was set. |
| The dashboard says "No content yet" | The active playlist has no items that this device can play. | Open Playlists and check that the playlist holds files with a supported extension. See `docs/content.md`. |
| The dashboard names a playlist problem | One or more items point at a missing file, or a file kind that PortaPixel does not play. | Open Playlists. The item with the problem carries its own warning. Fix or remove that item. |
| No network address, or you cannot find the device | The network is not connected, or `portapixel.toml` holds the wrong WiFi details. | Connect a screen and read the address shown on it. If there is no address, check the network cable, or edit `portapixel.toml` on the media partition from another computer. See `docs/install.md`. |
| Scheduled playlists do not switch | The clock has not synced with a time server yet. | Wait for the network to reach a time server. The dashboard shows a clock warning while this is true, and the default playlist plays meanwhile. |
| The device shows "config from backup" | `portapixel.toml` on the stick is missing, or the daemon cannot parse it. | Open Settings and save once. This writes a good file back onto the stick. See `docs/settings.md`. |
| The device shows "bad value repaired" | One or more keys in `portapixel.toml` held a value the daemon could not accept. | Open Settings and check the fields the warning names. Every other value in the file was kept as you wrote it. |
| A screen waits with a pairing code forever | Nobody has approved the code on the fleet server, or the code expired after 24 hours. | Open the server's Screens page and approve the waiting screen. A code that expired shows a new one; approve that one instead. |
| The fleet dashboard shows "needs X GB, has Y GB" | The playlist that the server assigned to this screen is larger than the free space on its media partition, even after clearing unused files. | Remove something from the playlist, or move the screen to a card with more free space. The screen keeps playing what it already has. |
| The web UI signs you out with a 401 that names something other than PortaPixel | A proxy or a captive portal between your browser and the device is answering instead of the device. | Check that you are on the intended network, and that no proxy sits between your browser and the device. |
| An update is refused | The build has no release key, the release is already marked bad, or the release is older than the one that runs. | Open About and read the message. A build with no release key cannot install any update; use a build made by the release process. |
| `install-to-disk` refuses a target | The target is the disk the machine booted from, or the target is smaller than the stick. It can also mean the typed confirmation did not match the device path. | Pick a different disk, or type the device path again exactly as shown. |
| The device does not shut down cleanly on a hypervisor stop, or a UPS signal | `acpid` is not running, or the machine has no ACPI power button at all. | Check that `acpid` runs (`rc-service acpid status`). A Raspberry Pi has no ACPI power button; this is expected there. |
| You need SSH access, or the network is down | SSH is on by default. When the network itself is the problem, use the serial console instead. | Connect over SSH with the root password. For a machine with no working network, connect a serial cable and use the console. |
| You forgot the web password | The password is a plain key in `portapixel.toml`. | Pull the stick, open `portapixel.toml` from another computer, and set `password` under `[web]` to a new value. Put the stick back and boot. |
| You want to wipe a device back to its first-boot state | PortaPixel keeps no separate factory-reset command. | Flash a fresh release image onto the stick. This is the same first step as a new install; see `docs/install.md`. |
| You cannot find the logs | System logs stay in memory by default, so they do not survive a reboot. | Turn on `[logging]` `persist` in Settings while you chase the fault, then turn it off again: it adds wear to the stick. The ops log always writes to the stick, regardless of this setting. |
| Chromium makes a network connection you did not expect | Chromium contacts `www.google.com` and `accounts.google.com` one time at each browser start. This is a measured, known behaviour with no PortaPixel flag that stops it. | If this matters for your network, block those two addresses at your firewall. No other outbound connection was measured from an idle screen. |
