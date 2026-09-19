// Package media holds the content-addressed media store of the fleet server.
//
// Why this package exists: an upload is the one path of the server that moves
// gigabytes, so it needs care that a route handler must not hold. The store
// names every object by the SHA-256 of its bytes, which gives deduplication for
// nothing: two uploads of one file are one object (D27). An upload streams to a
// temporary file in the directory that will hold the object, and the rename at
// the end is the commit. Nothing is ever held in memory as a whole, so a 4 GB
// video costs the same memory as a small picture.
//
// The store also makes the thumbnails. It uses the image packages of the
// standard library and a scaler of its own, so the server needs no image
// dependency (D27).
package media
