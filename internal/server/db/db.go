package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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
var migrations = []func(*tx) error{
	func(tx *tx) error {
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
	w pool
	// r takes every read. See the package comment for why the two are separate.
	r pool
	// path is the file, for the size report of the health page.
	path string
	// now gives the time. A test replaces it.
	now func() time.Time
	// queries counts the statements. See pool.
	queries *atomic.Int64
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

// writeParams is what the write pool adds.
//
// BEGIN IMMEDIATE takes the write lock at the start of the transaction. Several of
// our write transactions read a row and then write it, and a plain BEGIN takes a
// read snapshot first. A second writer that commits in that moment makes the write
// fail with SQLITE_BUSY_SNAPSHOT, and busy_timeout does NOT cover that case: the
// transaction would have to roll back to see the newer snapshot, so a wait cannot
// help. With IMMEDIATE the wait happens at BEGIN, where busy_timeout does work.
//
// The read pool must not take it: a read would then wait for the writer.
const writeParams = "&_txlock=immediate"

// Open opens the database at path and brings the schema up to date.
func Open(path string) (*DB, error) {
	w, err := sql.Open("sqlite", path+openParams+writeParams)
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

	counter := &atomic.Int64{}
	d := &DB{
		w:       pool{db: w, queries: counter},
		r:       pool{db: r, queries: counter},
		path:    path,
		now:     time.Now,
		queries: counter,
	}
	if err := d.migrate(); err != nil {
		d.Close()
		return nil, err
	}
	// A file that an older development build made has the right version number and
	// the wrong columns. Every query would then fail with a raw SQL error, which
	// says nothing that an operator can act on.
	if err := d.checkSchema(); err != nil {
		d.Close()
		return nil, err
	}
	// A mirror runs in a goroutine. A process that stopped in the middle of one
	// left a row that says "working", and nothing else would ever clear it.
	if err := d.resetWorkingMirrors(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// requiredColumns names one column of each table that this build needs and that an
// older development build did not have. A file that holds the version number of the
// schema and not its columns comes from that time.
//
// From release 1 on this check answers "never", because a migration is then
// append-only and the version number says the truth. Until then it turns a raw SQL
// error into one sentence that says what to do.
var requiredColumns = map[string][]string{
	"devices":             {"ever_paired", "pending_hardware_id", "pending_hardware_at"},
	"pending_enrollments": {"claim_hash", "pairing_code", "collides_with", "approved_at"},
	"commands":            {"deliveries", "expired"},
	"releases":            {"mirror_state"},
}

// ErrOldSchema says that the data directory comes from an older development build.
var ErrOldSchema = errors.New("this data directory was made by an older development build; " +
	"delete portapixel.db or use a new --data directory")

// checkSchema reports if the file holds the columns that this build needs.
func (d *DB) checkSchema() error {
	for table, columns := range requiredColumns {
		have, err := d.columnsOf(table)
		if err != nil {
			return err
		}
		for _, name := range columns {
			if !have[name] {
				return fmt.Errorf("%w (the table %s has no column %s)", ErrOldSchema, table, name)
			}
		}
	}
	return nil
}

// columnsOf names the columns of one table.
func (d *DB) columnsOf(table string) (map[string]bool, error) {
	// The table name is one of our own constants, so there is nothing here that a
	// request could reach.
	rows, err := d.r.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var (
			cid           int
			name, kind    string
			notNull, prim int
			def           sql.NullString
		)
		if err := rows.Scan(&cid, &name, &kind, &notNull, &def, &prim); err != nil {
			return nil, err
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w (there is no table %s)", ErrOldSchema, table)
	}
	return out, nil
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
