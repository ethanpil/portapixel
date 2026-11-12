// Package hwwatchdog holds the hardware watchdog of the machine
// (/dev/watchdog, the setting watchdog.hardware).
//
// Why this package exists: the watchdog ladder of internal/device/player
// recovers a player that hangs. Nothing recovered a daemon that hangs, or a
// kernel that hangs. supervise-daemon restarts a daemon that ends, but not a
// daemon that stops and stays. The timer of the hardware resets the machine
// when nobody pets it for the timeout.
//
// The rules:
//
//   - "auto" is on for the appliance image and off for an on-box install. On a
//     host that a person owns, a reset that they did not ask for is worse than
//     a hang. The daemon learns the install mode from its --install-mode flag.
//   - The daemon asks for a timeout of 60 s. A driver that refuses it keeps its
//     own timeout (the Pi bcm2835 driver has 15 s at most), and the daemon reads
//     that value back. It pets at a third of the timeout.
//   - The pet proves that the daemon lives. It does not prove that the player
//     lives: the ladder of the player does that job.
//   - A clean stop writes the magic character 'V' and closes the device, so
//     "rc-service portapixeld stop" does not reset the machine. A crash leaves
//     the timer armed. supervise-daemon starts the daemon again in seconds, and
//     the new daemon opens the device again and pets it.
//   - A missing or busy device is a warning in the status, never a fault of the
//     start. EBUSY means that another program (for example the watchdog of
//     busybox) holds the device.
//
// The ioctl code is for Linux only. Other systems get errors.ErrUnsupported,
// so a development machine runs with no watchdog and no warning. The tests use
// a fake Device.
package hwwatchdog
