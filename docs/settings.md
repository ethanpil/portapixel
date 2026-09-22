# Settings: portapixel.toml

Every setting of a device lives in one file, `portapixel.toml`, at the root
of the media partition. Edit it from the web UI, or by hand from any
computer. This page lists every key: its default value, what it means, and
when a change of it takes effect.

**Takes effect** has three values:

- **live** — the daemon uses the new value at once.
- **browser** — the on-screen browser must restart. The screen goes black
  for a few seconds and comes back with the new value.
- **reboot** — the whole device must restart.

The web UI tells you which of the three applies after each save, and offers
a reboot button when one is needed.

## `[device]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `name` | `"PortaPixel"` | any text | The display name. The mDNS address uses it, in lower case. | live |
| `id` | derived at each boot | — | The device ID, from the hardware. An edit here does nothing. | — |
| `timezone` | `"UTC"` | an IANA name, for example `"America/New_York"` | The zone that schedule rules use. The web UI nags until you set it. | live |
| `tier` | `"auto"` | `auto`, `low`, `high` | `low` turns on zram swap and degrades video-to-video transitions. | reboot |

## `[network]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `mode` | `"dhcp"` | `dhcp`, `static` | How the device gets an address. | reboot |
| `address` | empty | an IP with a prefix, for example `"192.168.1.50/24"` | Required when `mode` is `static`. | reboot |
| `gateway` | empty | an IP address | The router address, for a static setup. | reboot |
| `dns` | empty list | a list of IP addresses | The name servers, for a static setup. | reboot |
| `wifi_ssid` | empty | any text | The WiFi network name. Empty means a wired network. | reboot |
| `wifi_psk` | empty | any text | The WiFi password. | reboot |
| `wifi_country` | empty | two letters, for example `"US"` | The radio region. Empty leaves out the setting, and some radios then show fewer channels. | reboot |

A change to any `[network]` key needs a reboot, so that a live edit never
breaks the connection that you are using to make it.

## `[display]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `rotation` | `0` | `0`, `90`, `180`, `270` | Turns the picture, including web page items. | browser |
| `video_mode` | empty | for example `"1920x1080@60"` | Forces the output mode. Leave it empty to trust the display. Set it only when a display or an HDMI splitter reports the wrong mode. | browser |
| `power_method` | `"auto"` | `auto`, `cec`, `dpms`, `none` | How the device turns the screen on and off. | live |
| `on_time` | empty | `HH:MM` | The screen turns on at this time. Set both `on_time` and `off_time`, or set neither. | live |
| `off_time` | empty | `HH:MM` | The screen turns off at this time. | live |
| `power_days` | empty list | day names, for example `["mon","tue"]` | The days that the schedule above applies to. Empty means every day. | live |

### Screen power: CEC and DPMS

`power_method: auto` tries CEC first. CEC needs a `/dev/cec*` device and a
display that answers the CEC probe; a television on HDMI then goes to
standby, which is what a person calls "off." When CEC does not answer, the
device falls back to DPMS, the monitor's own power-saving mode.

**Neither path has a real-hardware check yet.** CONTEXT.md marks CEC and DPMS
as untested outside QEMU. Test the screen power behaviour of your own display
before you rely on it for an unattended install.

The device turns the display off before it stops the browser, and turns the
browser on before it turns the display back on. A browser that stopped first
would take the display driver with it, and the screen would stay on all
night.

## `[audio]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `output` | `"auto"` | `auto`, `hdmi`, `analog`, `usb` | Which sound card ALSA uses. `auto` leaves the choice to ALSA. | browser |
| `volume` | `100` | `0` to `100` | The output volume. | live |

The device picks the card by a name pattern in `/proc/asound/cards`: `hdmi`
takes the first card whose name holds HDMI, `usb` the first that holds USB,
and `analog` the first that holds neither. It writes the number of that card
into `/etc/asound.conf`. `auto` writes no file, and ALSA then uses card 0.

The browser reads the default card when it starts, so a change of `output`
restarts the browser. The volume goes to the ALSA mixer at once.

**Not proven on hardware.** No real sound card has answered this yet. When
the mixer refuses the volume, the status carries the warning
`audio-apply-failed` and the event log names the control that refused.

## `[playback]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `default_playlist` | `"default"` | a playlist name | Plays when no schedule rule matches, and while the clock is not yet synced. | live |
| `transition` | `"crossfade"` | `crossfade`, `push-left`, `push-right`, `push-up`, `push-down`, `cut` | The transition between items, unless a playlist sets its own. | live |
| `transition_ms` | `500` | `0` or more | The length of the transition, in milliseconds. | live |
| `image_duration` | `10` | `1` or more | Seconds that an image with no duration of its own stays on the screen. | live |
| `shuffle` | `false` | `true`, `false` | Plays each playlist's items in a new order each time it starts. | live |
| `nightly_restart` | `"03:30"` | `HH:MM`, or empty to turn it off | The daily browser restart, which clears memory that a long-running browser has collected. | live |

## `[watchdog]`

The recovery ladder of the browser (D30). The player reports in every 5
seconds. Silence means that the page is dead, so the device restarts the
browser. Restarts that repeat mean that a restart is not the answer, so the
device reboots.

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `enabled` | `true` | `true`, `false` | `false` stops the whole ladder: a frozen page then stays on the screen until a person acts. A browser that dies still starts again. | live |
| `heartbeat_timeout` | `30` | `10` to `600` seconds | The silence from the player that restarts the browser. | live |
| `restarts_before_reboot` | `4` | `0` to `20` | How many browser restarts inside `restart_window` reboot the device. `0` means that the device never reboots on its own. | live |
| `restart_window` | `60` | `1` to `1440` minutes | The length of that window. | live |

The daily browser restart is not here: it is `playback.nightly_restart`,
because it is a time of day and not a threshold. A restart that a person or
the daily job asks for never counts toward the reboot step, and neither does
a display that is not plugged in (D44).

## `[[schedule]]`

Each `[[schedule]]` block is one rule: a playlist, the days it applies to,
and a start and end time. See `docs/content.md` for the rules of how a rule
matches. A change to the schedule takes effect live.

## `[server]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `url` | empty | an address, for example `"https://signage.example.com"` | The fleet server. Empty keeps the device standalone. | live |
| `token` | empty | an enrollment token or a device token | Pairs the device with the server above. Leave it empty to pair by code in the web UI instead. | live |
| `poll_seconds` | `60` | `10` or more | How often the device checks in with the fleet server, when the server does not name its own interval. | live |

See `docs/fleet.md` for how pairing works and what a fleet server manages.

## `[web]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `port` | `80` | `1` to `65535` | The port of the web UI and the API. | reboot |
| `password` | `"portapixel"` | any text, 8 characters or more when you set it from the web UI | The web UI password. Change it: the device nags until you do. | live |

## `[ssh]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `enabled` | `true` | `true`, `false` | Turns remote terminal access on or off. The root password is set at the first boot and is separate from this file; see `docs/troubleshooting.md`. | reboot |

## `[updates]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `auto` | `false` | `true`, `false` | Installs an approved update on its own. `false` is the safe default: you decide when a screen changes. | live |

## `[logging]`

| Key | Default | Values | What it does | Takes effect |
|---|---|---|---|---|
| `persist` | `false` | `true`, `false` | Writes the system log to the stick instead of memory. Turn this on only while chasing a fault that must survive a reboot: it adds wear to the stick. | reboot |

## The shadow copy and field repair

After every good read of `portapixel.toml`, the daemon copies it to
`portapixel.toml.lkg` in its own state directory, on the system partition.
This is the shadow copy (D38).

When `portapixel.toml` on the media partition is missing, or the daemon
cannot parse it as TOML, the daemon boots from the shadow copy instead, and
the web UI shows a loud warning. **Save the settings once from the web UI,
and a good file goes back onto the media partition.**

When the file parses but holds one or more bad values, the daemon repairs
the file field by field: every value that you wrote stays as it is, and each
bad field alone takes its default value. **One bad value never costs you the
rest of the file.** The web UI shows which fields were repaired.

A repaired file is never written to the shadow copy in its place: the
shadow copy always holds the last file that parsed and validated cleanly.
