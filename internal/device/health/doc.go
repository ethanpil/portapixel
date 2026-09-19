// Package health builds the status report of the device.
//
// Why this package exists: /api/status, the fleet heartbeat, the fallback screen
// and the admin dashboard all show the same facts, so the facts are gathered in
// one place and in one struct (manifest.Status). A second reader of /proc/meminfo
// would be a second set of small mistakes.
//
// Everything comes from files, and every file root is a parameter. On a machine
// that is not Linux the numbers are zero and the report still builds, so the
// tests and the daemon both run on a Windows development machine.
//
// The warnings list is the loud part of the product. A device with the default
// web password, the default root password or no time zone works, but it is not
// finished, and the admin UI says so until the person fixes it (D22, D23, D40).
package health
