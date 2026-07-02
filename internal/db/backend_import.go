package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrTargetNotEmpty is returned by ImportFrom when the destination already holds
// data, so a one-time migration never clobbers an existing deployment.
var ErrTargetNotEmpty = errors.New("target database is not empty")

// ImportFrom copies every data table from src into the receiver, preserving row
// IDs and referential integrity, in a single transaction. It is the engine
// behind a one-time SQLite->Postgres backend migration: the receiver must be a
// freshly migrated, empty Postgres store, and src the existing SQLite store.
//
// Correctness is driven by the TARGET's real column types (queried at runtime),
// so a value SQLite stores as an int epoch or a text timestamp is coerced to the
// timestamptz the target expects. FK triggers are disabled for the load (the
// source is already FK-consistent), and per-table id sequences are reset
// afterward so subsequent inserts do not collide with migrated IDs. Any error
// rolls the whole migration back. Returns per-table copied-row counts.
func (dst *Store) ImportFrom(src *Store) (map[string]int, error) {
	if dst.d.rebind("?") == "?" {
		return nil, errors.New("import target must be Postgres")
	}

	tables, err := srcTables(src)
	if err != nil {
		return nil, err
	}

	// Refuse a non-empty target on the core tables so an existing deployment is
	// never overwritten.
	for _, t := range []string{"users", "apps"} {
		var exists bool
		if err := dst.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + quoteIdent(t) + `)`).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check target %s: %w", t, err)
		}
		if exists {
			return nil, fmt.Errorf("%w (found rows in %s)", ErrTargetNotEmpty, t)
		}
	}

	tx, err := dst.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Disable FK triggers for the bulk load; the source is already consistent,
	// so out-of-order inserts are safe. Scoped to this transaction.
	if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
		return nil, fmt.Errorf("disable fk triggers (target user must be able to set session_replication_role): %w", err)
	}

	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		n, err := copyTable(src, tx, dst, table)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", table, err)
		}
		counts[table] = n
	}

	// Reset id sequences so future inserts start above the migrated max id.
	for _, table := range tables {
		if err := resetIDSequence(tx, table); err != nil {
			return nil, fmt.Errorf("reset sequence %s: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return counts, nil
}

// srcTables lists the source SQLite data tables to copy, excluding the schema
// ledger (the target maintains its own) and SQLite internals.
func srcTables(src *Store) ([]string, error) {
	rows, err := src.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list source tables (source must be SQLite): %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// copyTable copies one table from src into the target transaction, coercing each
// value to the target column's type. It copies only columns present in BOTH
// schemas (their intersection), so a benign column difference does not abort the
// migration.
func copyTable(src *Store, tx *boundTx, dst *Store, table string) (int, error) {
	targetTypes, err := dst.columnTypes(table)
	if err != nil {
		return 0, err
	}

	rows, err := src.DB().Query(`SELECT * FROM ` + quoteIdent(table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	srcCols, err := rows.Columns()
	if err != nil {
		return 0, err
	}

	// Columns to copy: those present in both source and target, in target order.
	var cols []string
	for _, c := range srcCols {
		if _, ok := targetTypes[c]; ok {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return 0, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		quoteIdent(table), strings.Join(quoted, ","), placeholders)

	n := 0
	for rows.Next() {
		scan := make([]any, len(srcCols))
		ptrs := make([]any, len(srcCols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, err
		}
		bySrcCol := make(map[string]any, len(srcCols))
		for i, c := range srcCols {
			bySrcCol[c] = scan[i]
		}
		args := make([]any, len(cols))
		for i, c := range cols {
			v, cerr := coerceForColumn(bySrcCol[c], targetTypes[c])
			if cerr != nil {
				return 0, fmt.Errorf("column %s: %w", c, cerr)
			}
			args[i] = v
		}
		if _, err := tx.Exec(insert, args...); err != nil {
			return 0, err
		}
		n++
	}
	return n, rows.Err()
}

// columnTypes returns the target's column name -> SQL data_type for a table.
func (dst *Store) columnTypes(table string) (map[string]string, error) {
	rows, err := dst.db.Query(
		`SELECT column_name, data_type FROM information_schema.columns WHERE table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("column types for %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out[name] = typ
	}
	return out, rows.Err()
}

// coerceForColumn converts a value read from SQLite into the form the target
// column expects. Two mismatches exist in this schema: timestamps (SQLite stores
// int epochs or text; the target uses timestamptz) and booleans (SQLite stores
// 0/1 integers; the target uses BOOLEAN, e.g. apps.identity_headers). Everything
// else (ints, floats, text, bytea, null) round-trips unchanged.
func coerceForColumn(v any, pgType string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch {
	case strings.Contains(pgType, "timestamp"):
		switch t := v.(type) {
		case time.Time:
			return t, nil
		case int64:
			return time.Unix(t, 0).UTC(), nil
		case string:
			if parsed, ok := parseSQLiteTime(t); ok {
				return parsed, nil
			}
			return nil, fmt.Errorf("unparseable timestamp %q", t)
		}
	case pgType == "boolean":
		switch b := v.(type) {
		case bool:
			return b, nil
		case int64:
			return b != 0, nil
		case string:
			return b == "1" || strings.EqualFold(b, "true"), nil
		}
	}
	return v, nil
}

// resetIDSequence advances a table's id sequence past the migrated max id, so
// the next natural insert does not collide. No-op for tables without an id
// serial (composite/natural keys) - checked first so the MAX(id) subquery is
// never parsed against a table that has no id column.
func resetIDSequence(tx *boundTx, table string) error {
	// pg_get_serial_sequence errors (not returns NULL) if the column is absent,
	// so confirm an id column exists first.
	var hasID bool
	if err := tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = ? AND column_name = 'id')`,
		table).Scan(&hasID); err != nil {
		return err
	}
	if !hasID {
		return nil
	}
	var seq sql.NullString
	if err := tx.QueryRow(`SELECT pg_get_serial_sequence(?, 'id')`, table).Scan(&seq); err != nil {
		return err
	}
	if !seq.Valid {
		return nil
	}
	_, err := tx.Exec(fmt.Sprintf(
		`SELECT setval('%s', GREATEST((SELECT COALESCE(MAX(id), 0) FROM %s), 1))`,
		seq.String, quoteIdent(table)))
	return err
}

// quoteIdent double-quotes a SQL identifier (table/column) so reserved words
// such as the "key" column survive on both dialects.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
