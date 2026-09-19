package db

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the pure-Go SQLite driver: no cgo (D7)
)

//go:embed schema.sql
var files embed.FS

// migrations holds one entry for each schema version. Entry 0 makes the schema
// at version 1. A later change appends one entry and never edits an old one.
//
// The runner is small on purpose: this is a single-admin homelab server, so a
// migration tool with its own file format and command line would be more code
// than the thing it manages.
var migrations = []func(*sql.Tx) error{
	func(tx *sql.Tx) error {
		data, err := files.ReadFile("schema.sql")
		if err != nil {
			return err
		}
		_, err = tx.Exec(string(data))
		return err
	},
}

// DB is the database of the fleet server.
type DB struct {
	// w takes every write. Its pool holds one connection, so two writers never
	// fight for the lock of the database file.
	w *sql.DB
	// r takes every read. See the package comment for why the two are separate.
	r *sql.DB
	// path is the file, for the size report of the health page.
	path string
	// now gives the time. A test replaces it.
	now func() time.Time
}

// openParams are the pragmas of each connection.
//
// The write-ahead log lets a reader and the writer work at the same time. The
// busy timeout of five seconds covers the moment when SQLite moves the log into
// the database file. Foreign keys are off in SQLite unless a connection turns
// them on, and the schema needs them: a deleted playlist must take its items
// with it.
const openParams = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)"

// Open opens the database at path and brings the schema up to date.
func Open(path string) (*DB, error) {
	w, err := sql.Open("sqlite", path+openParams)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	// The one write connection stays open: a reopen would have to set the
	// pragmas again, and the write-ahead log file would be removed and remade.
	w.SetConnMaxIdleTime(0)

	r, err := sql.Open("sqlite", path+openParams)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	r.SetMaxOpenConns(4)

	d := &DB{w: w, r: r, path: path, now: time.Now}
	if err := d.migrate(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close closes both pools.
func (d *DB) Close() error {
	err := d.w.Close()
	if err2 := d.r.Close(); err == nil {
		err = err2
	}
	return err
}

// Path gives the file name of the database.
func (d *DB) Path() string { return d.path }

// migrate applies every migration that the file has not seen. PRAGMA
// user_version holds the number of applied migrations. It costs no table.
func (d *DB) migrate() error {
	var have int
	if err := d.w.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	if have > len(migrations) {
		return fmt.Errorf("the database is at schema version %d and this build knows %d; use a newer build", have, len(migrations))
	}
	for i := have; i < len(migrations); i++ {
		tx, err := d.w.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if err := migrations[i](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no parameter, and the value is an integer of our own, so
		// there is nothing here that a request could reach.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("mark migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// IntegrityCheck runs PRAGMA integrity_check. It reads the whole file, so it
// takes seconds on a large database. It goes to the read pool: the heartbeat
// path must keep working while it runs.
func (d *DB) IntegrityCheck() (string, error) {
	rows, err := d.r.Query("PRAGMA integrity_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "; "), nil
}

// stamp gives the time in the form that the tables hold.
func (d *DB) stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTime reads a time value of a table. An empty or bad value gives the zero
// time, which every caller reads as "never".
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// joinDays and splitDays move a day list between the table and the wire. The
// table holds "mon,tue"; the wire holds ["mon","tue"].
func joinDays(days []string) string { return strings.Join(days, ",") }

func splitDays(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// nullInt64 turns a zero into NULL. The schema uses NULL for "no group" and
// "no playlist", because a foreign key cannot point at row 0.
func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// intFromNull reads a column that can be NULL.
func intFromNull(v sql.NullInt64) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}
