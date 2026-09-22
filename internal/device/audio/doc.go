// Package audio applies the [audio] table of portapixel.toml (D11).
//
// Why this package exists: the two keys of that table were inert. The API
// reported them as applied, and nothing on the device read them. The sound came
// from whatever card ALSA picked and at whatever volume the card held.
//
// D11 is direct ALSA: no PulseAudio and no PipeWire daemon. So there are exactly
// two things to do.
//
//   - The volume goes to the mixer with amixer of alsa-utils. It takes effect at
//     once.
//   - The output goes to /etc/asound.conf, which names the default card of ALSA.
//     Chromium reads the default device when it STARTS, so a change of the output
//     is of the class "browser" in internal/config: the browser must restart
//     before a person hears the new card (internal/config/change.go).
//
// The card comes from a name pattern in /proc/asound/cards: "hdmi" takes the
// first card whose text holds HDMI, "usb" takes the first card whose text holds
// USB, and "analog" takes the first card that holds neither. "auto" writes no
// file at all and leaves the default of ALSA, which is card 0.
//
// NOT VERIFIED ON HARDWARE. No real sound card has ever answered these calls.
// The exact card match is the part that a real machine can prove wrong: a driver
// that names its HDMI output another word would send the sound to the wrong
// card. The release checklist holds the check (docs/release-checklist.md).
//
// Every program call and every path is an option, so the tests need no Linux, no
// sound card and no root.
package audio
