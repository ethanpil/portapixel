# Troubleshooting

This page lists common problems, what causes each one, and what to do about
it.

## Where to look first

- **The ops log.** Open Activity in the web UI, or read `/var/lib/portapixel/ops.log`
  over SSH. Each line of the player starts with `player.`.
- **The status.** Open `http://<address>/api/status`. `player_state` is
  `running`, `starting`, `stopped`, `waiting-for-display` or `disabled`.
  `video_output` is `gpu` or `drm`. `hwdec` is the video decoder.
- **The player log.** Read `/run/portapixel/player.log` over SSH, as root. It
  holds the output of mpv. It lives in memory, and it starts again at each
  start of the player. It is 1 MiB at most.
- **The restart button.** The Dashboard has a "Restart player" button. The
  screen goes black for a few seconds and comes back on the same item.

## Symptoms

| Symptom | Likely cause | What to do |
|---|---|---|
| The screen stays black | The player did not start, or it crashed. The display can also have no signal or a mode that it does not support. | Connect over SSH. Read the status, then the ops log, then `/run/portapixel/player.log`. A line `player.start.fail` or `player.exit` names the reason. |
| The screen stays black and `player_state` is `waiting-for-display` | The device sees no connected display. This is a wait, not a fault. | Check the HDMI cable and the display. The picture comes back when a display appears. The wait does not count toward a reboot. |
| The screen stays black, but `player_state` is `running` | `display.video_mode` names a mode that the display does not have. mpv runs, and no file can show. The ops log can hold `player.item.fail` and `player.playlist.fail` lines, and `/run/portapixel/player.log` holds the error of mpv. | Empty `video_mode` in `portapixel.toml`, or set a mode that the display lists. The player then restarts. |
| Text shows on the screen between items, or when the player restarts | The text console shows through. The kernel command line must hide it. | Read `/proc/cmdline` over SSH. It must hold `quiet`, `vt.global_cursor_default=0`, `consoleblank=0` and `logo.nologo`. Put the options back in the boot configuration if they are not there. |
| The player restarts again and again | mpv ends, does not answer, or stops a video or an image. The watchdog restarts it. Too many counted restarts inside `restart_window` reboot the device. The default is four in one hour. | Read `player.exit` and `player.restart` in the ops log. Then read `player.log`. Set `display.video_output` to `drm` and try again. A file that crashes mpv must leave the playlist. On a very slow device, raise `watchdog.heartbeat_timeout`. |
| The device reboots by itself | The watchdog counted too many player restarts in `restart_window`. | Read `player.reboot` in the ops log. Fix the cause of the restarts. To stop the reboots while you look, set `watchdog.restarts_before_reboot` to `0`. |
| A video drops frames or stutters | The decoder is too slow. The video is large, or its format decodes in software. | Read `now_playing.dropped_frames` and `hwdec` in `/api/status`. `hwdec` of `no` means software decode. On a Raspberry Pi, use H.264. A Pi 4 and a Pi 5 decode HEVC in software. Match the size of the video to the screen. |
| A video plays in software on a Pi, and `hwdec` shows `no` | The Pi decoder module did not load, or the video is not H.264. A Pi 5 has no H.264 decoder. | Check the format of the video. On a Pi Zero 2 W, 3 and 4, look for `bcm2835` in the output of `lsmod` over SSH. See the release checklist. |
| A transition looks coarse, or the screen shows a cut | A slow device draws only a few steps. Or a step failed, and the script made a cut. | Read `player.transition.fault` in the ops log. Use `cut`, a wipe or a push, or a longer `transition_ms`. Set `display.video_output` to `gpu` if the driver supports it. |
| Ken Burns does nothing | Ken Burns needs the `gpu` video output. The device uses `drm`. | Read `player.kenburns.off` in the ops log. Check `video_output` in `/api/status`. Set `display.video_output` to `gpu` on a device with an OpenGL driver. |
| The crossfade from a video does not move | `playback.motion` is `off`, or `auto` is off on this board, or the guard switched motion off after dropped frames. | Read `player.motion.off` in the ops log. See `docs/settings.md`. Set `playback.motion` to `on` only on a device that you tested. |
| A file does not play | The file is damaged, or mpv cannot decode it. The player skips it. | Read `player.item.fail` in the ops log. It names the file and the reason. Convert the file to H.264 video in an MP4 container, or remove it. |
| The dashboard says "No content yet" | The active playlist has no items that this device can play. | Open Playlists and check that the playlist holds files with a supported extension. See `docs/content.md`. The screen shows the fallback screen with the address of the device. |
| The dashboard names a playlist problem | One or more items point at a file that is not there, or a file kind that PortaPixel does not play. | Open Playlists. The item with the problem carries its own warning. Fix or remove that item. |
| There is no sound | The wrong sound card, a muted video, or a volume of 0. | Check `mute` on the item and `audio.volume`. Try `audio.output` set to `hdmi`, `analog` or `usb`. A change of the output restarts the player. A mixer fault shows as the warning `audio-apply-failed`. |
| The screen does not turn off or on at the set time | `display.power_method` does not match the display, or the display ignores the command. CEC and DPMS have no check on real hardware yet. | Read the `power.` lines in the ops log. Try `display.power_method` set to `cec` or `dpms`. Test your own display before you rely on it. |
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
| You cannot find the logs | System logs stay in memory by default, so they do not survive a reboot. | Turn on `[logging]` `persist` in Settings while you look for the fault, then turn it off again: it adds wear to the stick. The ops log always writes to the stick, regardless of this setting. |
