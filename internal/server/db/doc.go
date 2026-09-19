// Package db holds the schema, the migrations and the queries of the fleet
// server.
//
// Why this package exists: the server keeps all its state, except the media
// files and the mirrored releases, in one SQLite file. This package is the only
// place that speaks SQL. The route packages (api, admin) call functions here,
// so a rule about the data lives in one place and a handler stays short.
//
// There is no ORM and no query builder. The queries are short, they are few,
// and plain SQL is the form that a reader can check against schema.sql.
//
// Two connection pools open the same file. All writes go through a pool of one
// connection, so two writers can never fight for the database lock. Reads use a
// pool of four connections. In write-ahead-log mode a reader does not block the
// writer and the writer does not block a reader, so the split is safe. The
// reason for the split is the integrity check: it reads the whole file and takes
// seconds, and with one shared connection it would stop every heartbeat until it
// finished. A single writer goroutine with a queue would give the same safety
// and more code, so the pool of one is what we use.
package db
