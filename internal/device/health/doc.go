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
// The warnings list is what the product says out loud. A device with the default
// web password, the default root password or no time zone plays its content. But
// it is not finished. The admin UI shows the warning until the person makes the
// change (D22, D23, D40).
package health
