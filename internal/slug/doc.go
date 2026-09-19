// Package slug makes a safe name from a name that a person typed.
//
// Why this package exists: three parts of PortaPixel need the same name rule.
// The device library makes a playlist directory name, netcfg makes a host name
// for /etc/hostname and for mDNS, and the fleet server makes the playlist name
// that the device turns into a directory under _fleet/. Each part had its own
// copy of the rule. Two copies of one rule drift apart, and then the directory on
// the card and the name in the browser are not the same name.
//
// The package is at internal/slug and not under internal/device, because the
// contract stops a server package from importing a device package.
//
// The rule is here. The tail of the name belongs to the caller: a directory name
// has a length limit, and a host name must never be empty.
package slug
