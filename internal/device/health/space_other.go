//go:build !linux

package health

// space has no meaning away from Linux. The device always runs Linux; another
// system is a development machine, where the free space of the media directory
// tells nobody anything. Zero is the honest answer, and the admin UI shows an
// empty bar.
func space(dir string) (free, total uint64) { return 0, 0 }
