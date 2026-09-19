// Package playlist reads and writes playlist.toml.
//
// Why this package exists: a playlist file is hand-editable, so it can hold any
// text that a person can type (D15). One package parses it, says what is wrong
// with it in words, and writes it again in the canonical shape. A bad file gives
// an error, never a panic: the device skips the playlist, writes a line in the
// ops log, and keeps playing.
//
// The package also decides if an item is an image, a video or a URL, because
// the player, the admin UI and the fleet client must all get the same answer.
package playlist
