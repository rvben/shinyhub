package db

import (
	"context"
	"database/sql"
)

// boundDB wraps *sql.DB and rebinds `?` placeholders to the active dialect on
// every query. Store.db is a *boundDB, so the ~132 query methods keep writing
// SQLite-style `?` and work unchanged on both backends.
type boundDB struct {
	real *sql.DB
	d    dialect
}

func (b *boundDB) Exec(query string, args ...any) (sql.Result, error) {
	return b.real.Exec(b.d.rebind(query), args...)
}
func (b *boundDB) Query(query string, args ...any) (*sql.Rows, error) {
	return b.real.Query(b.d.rebind(query), args...)
}
func (b *boundDB) QueryRow(query string, args ...any) *sql.Row {
	return b.real.QueryRow(b.d.rebind(query), args...)
}
func (b *boundDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return b.real.QueryRowContext(ctx, b.d.rebind(query), args...)
}
func (b *boundDB) Begin() (*boundTx, error) {
	tx, err := b.real.Begin()
	if err != nil {
		return nil, err
	}
	return &boundTx{tx: tx, d: b.d}, nil
}
func (b *boundDB) PingContext(ctx context.Context) error { return b.real.PingContext(ctx) }
func (b *boundDB) Close() error                          { return b.real.Close() }

// boundTx wraps *sql.Tx with the same rebinding behavior.
type boundTx struct {
	tx *sql.Tx
	d  dialect
}

func (b *boundTx) Exec(query string, args ...any) (sql.Result, error) {
	return b.tx.Exec(b.d.rebind(query), args...)
}
func (b *boundTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return b.tx.ExecContext(ctx, b.d.rebind(query), args...)
}
func (b *boundTx) Query(query string, args ...any) (*sql.Rows, error) {
	return b.tx.Query(b.d.rebind(query), args...)
}
func (b *boundTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return b.tx.QueryContext(ctx, b.d.rebind(query), args...)
}
func (b *boundTx) QueryRow(query string, args ...any) *sql.Row {
	return b.tx.QueryRow(b.d.rebind(query), args...)
}
func (b *boundTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return b.tx.QueryRowContext(ctx, b.d.rebind(query), args...)
}
func (b *boundTx) Commit() error   { return b.tx.Commit() }
func (b *boundTx) Rollback() error { return b.tx.Rollback() }

// writeTx is the serialized-write handle returned by dialect.beginWrite. Both
// the Postgres boundTx and the SQLite dedicated-connection boundConn satisfy it,
// so GrantSharedData and first-login provisioning are written once.
type writeTx interface {
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Commit() error
	Rollback() error
}

// boundConn wraps a dedicated *sql.Conn on which BEGIN IMMEDIATE was issued
// out-of-band. It rebinds `?` and turns Commit/Rollback into COMMIT/ROLLBACK
// plus releasing the connection back to the pool. database/sql does not let you
// mix a manual BEGIN IMMEDIATE with *sql.Tx, so the dedicated-conn model is what
// preserves the original SQLite eager-write-lock semantics exactly.
//
// ctx is the transaction's own context (callers pass a long-lived context such
// as context.Background(), not an HTTP request context, since it governs COMMIT).
type boundConn struct {
	conn      *sql.Conn
	d         dialect
	ctx       context.Context
	committed bool
}

func (b *boundConn) Exec(query string, args ...any) (sql.Result, error) {
	return b.conn.ExecContext(b.ctx, b.d.rebind(query), args...)
}
func (b *boundConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return b.conn.ExecContext(ctx, b.d.rebind(query), args...)
}
func (b *boundConn) Query(query string, args ...any) (*sql.Rows, error) {
	return b.conn.QueryContext(b.ctx, b.d.rebind(query), args...)
}
func (b *boundConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return b.conn.QueryContext(ctx, b.d.rebind(query), args...)
}
func (b *boundConn) QueryRow(query string, args ...any) *sql.Row {
	return b.conn.QueryRowContext(b.ctx, b.d.rebind(query), args...)
}
func (b *boundConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return b.conn.QueryRowContext(ctx, b.d.rebind(query), args...)
}
func (b *boundConn) Commit() error {
	_, err := b.conn.ExecContext(b.ctx, "COMMIT")
	b.committed = true
	_ = b.conn.Close()
	return err
}
func (b *boundConn) Rollback() error {
	if b.committed {
		return nil
	}
	_, err := b.conn.ExecContext(b.ctx, "ROLLBACK")
	_ = b.conn.Close()
	return err
}

// beginImmediateSQLite grabs a dedicated connection and issues BEGIN IMMEDIATE,
// taking the write lock up front so two readers cannot both pass a read-then-
// write check before either writes. Returns a boundConn (a writeTx).
func beginImmediateSQLite(ctx context.Context, db *sql.DB, d dialect) (writeTx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &boundConn{conn: conn, d: d, ctx: ctx}, nil
}
