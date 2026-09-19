// Package library reads the media root and holds the playlists.
//
// Why this package exists: the media partition is the user interface of a
// PortaPixel device. A person can pull the card out, drop files on it from a
// laptop, hand-edit a playlist.toml, and put the card back. The library turns
// that free-form directory tree into the one data structure that the player, the
// admin UI and the fleet client read. It must never trust what it finds:
//
//   - A playlist.toml that does not parse is skipped. It becomes a line in the
//     ops log and an entry in Problems, so the admin UI can show the fault. It
//     never stops the scan and it never panics.
//   - A file that an item names but that does not exist is marked Missing. The
//     player leaves it out; the admin UI shows it.
//   - A directory name that starts with an underscore is never a local playlist.
//     _fleet/<name>/ is the exception: the fleet client writes playlists there
//     and they play only while the device is paired.
//
// The device computes the hash in the background. The SHA-256 of every file goes
// into a cache with the path, the size and the modification time as its key, so a
// rescan costs nothing. The first hash of a 1 GB video takes seconds. It happens
// in a goroutine after the scan: playback must never wait for a checksum.
//
// The snapshot is a value. Callers get a copy, so a rescan can never change the
// list under a handler that is halfway through it.
package library
