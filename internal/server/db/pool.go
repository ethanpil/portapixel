package db

import (
	"database/sql"
	"sync/atomic"
)

// pool is one connection pool of the database with a counter on it.
//
// Why the counter exists: the cost of one device poll is a contract of this
// package. A fleet of 200 screens with a 50-item loop costs about 10000 queries a
// minute if the count grows with the number of items, and every one of them goes to
// one SQLite file that also takes every heartbeat. A test reads Queries and holds a
// budget, so a helper that asks for one row at a time cannot come back unnoticed.
//
// The counter is one atomic add for each statement. That is nothing beside the
// statement itself.
type pool struct {
	db      *sql.DB
	queries *atomic.Int64
}

func (p pool) Query(query string, args ...any) (*sql.Rows, error) {
	p.queries.Add(1)
	return p.db.Query(query, args...)
}

func (p pool) QueryRow(query string, args ...any) *sql.Row {
	p.queries.Add(1)
	return p.db.QueryRow(query, args...)
}

func (p pool) Exec(query string, args ...any) (sql.Result, error) {
	p.queries.Add(1)
	return p.db.Exec(query, args...)
}

// Begin starts a transaction whose statements the counter also sees.
func (p pool) Begin() (*tx, error) {
	p.queries.Add(1)
	inner, err := p.db.Begin()
	if err != nil {
		return nil, err
	}
	return &tx{tx: inner, queries: p.queries}, nil
}

func (p pool) Close() error { return p.db.Close() }

// tx is one transaction with the counter on it.
type tx struct {
	tx      *sql.Tx
	queries *atomic.Int64
}

func (t *tx) Query(query string, args ...any) (*sql.Rows, error) {
	t.queries.Add(1)
	return t.tx.Query(query, args...)
}

func (t *tx) QueryRow(query string, args ...any) *sql.Row {
	t.queries.Add(1)
	return t.tx.QueryRow(query, args...)
}

func (t *tx) Exec(query string, args ...any) (sql.Result, error) {
	t.queries.Add(1)
	return t.tx.Exec(query, args...)
}

func (t *tx) Commit() error   { return t.tx.Commit() }
func (t *tx) Rollback() error { return t.tx.Rollback() }

// Queries gives the number of statements that the database ran since it opened.
// Only a test reads it.
func (d *DB) Queries() int64 { return d.queries.Load() }
