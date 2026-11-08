package db

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// boundDB wraps *sql.DB and rebinds `?` placeholders to the active dialect on
// every query. Store.db is a *boundDB, so the ~132 query methods keep writing
// SQLite-style `?` and work unchanged on both backends.
type boundDB struct {
	real *sql.DB
	d    dialect

	// observer, when set via Store.ObserveQueries, receives the rebound SQL
	// text of every Exec/Query/QueryRow call issued directly through this
	// boundDB (not through a boundTx or boundConn, which serve the write
	// path and are never used for listing queries). nil by default; reading
	// an atomic pointer costs one load, so the hot path pays nothing when no
	// test has installed an observer.
	observer atomic.Pointer[func(string)]
}

// observe reports the rebound SQL text to the currently installed observer,
// if any. It is a no-op (a single atomic load) when ObserveQueries has never
// been called.
func (b *boundDB) observe(query string) {
	if fn := b.observer.Load(); fn != nil {
		(*fn)(query)
	}
}

func (b *boundDB) Exec(query string, args ...any) (sql.Result, error) {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.Exec(rebound, args...)
}
func (b *boundDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.ExecContext(ctx, rebound, args...)
}
func (b *boundDB) Query(query string, args ...any) (*sql.Rows, error) {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.Query(rebound, args...)
}
func (b *boundDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.QueryContext(ctx, rebound, args...)
}
func (b *boundDB) QueryRow(query string, args ...any) *sql.Row {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.QueryRow(rebound, args...)
}
func (b *boundDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	rebound := b.d.rebind(query)
	b.observe(rebound)
	return b.real.QueryRowContext(ctx, rebound, args...)
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
	conn *sql.Conn
	d    dialect
	ctx  context.Context
	// done is set once the transaction has ended either way and the dedicated
	// connection has gone back to the pool, so a deferred Rollback after
	// Commit is a no-op instead of a statement on a released connection.
	done bool
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

// Commit ends the transaction and releases the connection. A COMMIT that fails
// (the context is done, or SQLite reports BUSY while a checkpoint holds the
// lock) leaves the transaction open on the connection, and the driver does not
// reset sessions on reuse, so the next borrower would inherit it and its own
// BEGIN IMMEDIATE would fail. Roll back explicitly before releasing, on a
// context that cannot already be cancelled, and report the COMMIT error.
func (b *boundConn) Commit() error {
	if b.done {
		return sql.ErrTxDone
	}
	_, err := b.conn.ExecContext(b.ctx, "COMMIT")
	if err != nil {
		_, _ = b.conn.ExecContext(context.WithoutCancel(b.ctx), "ROLLBACK")
	}
	b.done = true
	_ = b.conn.Close()
	return err
}

// Rollback aborts the transaction and releases the connection. After Commit
// (successful or not) the transaction has already ended, so it returns nil.
func (b *boundConn) Rollback() error {
	if b.done {
		return nil
	}
	b.done = true
	_, err := b.conn.ExecContext(context.WithoutCancel(b.ctx), "ROLLBACK")
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
