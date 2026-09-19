// Package store holds the rules of the content-addressed media store.
//
// Why this package exists: the device and the server must agree on the name of
// an object and on its hash, and a media download must survive a dropped
// connection. exFAT has no hard links, so the store is a flat directory of
// files named <first 8 hex of sha>-<safe name> (D24). A download writes a .part
// file, continues it with a Range request after an interruption, and only
// renames the file to its true name after the SHA-256 matches.
package store
