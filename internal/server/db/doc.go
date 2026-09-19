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
// Migrations: from release 1 on, a change appends one entry to the list in db.go
// and never edits an old one. Nothing is released yet, so schema.sql still changes
// in place, and Open then refuses a file that an older development build made. It
// answers with one sentence that says what to do and never with a raw SQL error.
// See requiredColumns.
//
// Two connection pools open the same file. All writes go through a pool of one
// connection, so two writers can never fight for the database lock. Reads use a
// pool of four connections. In write-ahead-log mode a reader does not block the
// writer and the writer does not block a reader, so the split is safe.
//
// The integrity check is the reason for the split. It reads the whole file and it
// takes seconds. With one shared connection it would stop every heartbeat until it
// finished. A single writer goroutine with a queue would give the same safety and
// more code, so the pool of one is what we use.
//
// Each pool counts its statements. See pool.go: the cost of one device poll is a
// contract of this package, and a test holds a budget for it.
package db
