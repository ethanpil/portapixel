// Package identity finds the device identity and keeps the device state file.
//
// Why this package exists: a PortaPixel card can be cloned, and a device can
// get a new mainboard. Both events change what the hardware says about itself,
// and both must have a defined result (D21). The rules live here, in one place,
// and not in the daemon:
//
//   - The identity comes from the hardware at each boot. Three sources, in
//     order: the Raspberry Pi serial number, the DMI product UUID, the first
//     permanent MAC address of a physical network interface.
//   - state.json in the state directory holds the identity that the device used
//     last, together with the fleet token and the other data that must survive a
//     write to the media partition.
//   - A stored identity that is different from the derived identity is field
//     repair, not cloning: the device takes the new identity, keeps everything
//     else, and raises a flag for the next fleet heartbeat. The server catches a
//     true clone, because two hardware identities then present one token.
//
// Every source is a file, and the root of the file tree is a parameter. The
// tests therefore run on a Windows development machine.
package identity
