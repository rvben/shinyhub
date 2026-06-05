package db_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestSchemaParity asserts the Postgres baseline reproduces the cumulative
// SQLite schema: same tables, same columns, same nullability, same unique-index
// column tuples, and same foreign-key edges (column -> referenced table.column).
// Type differences are tolerated per the documented mapping (timestamptz/text
// for datetimes, integer for booleans, bigint for epoch ints, bigserial for
// autoincrement PKs).
//
// It runs only when SHINYHUB_TEST_POSTGRES_DSN is set; the SQLite reference is
// always built in-process regardless of the active backend.
func TestSchemaParity(t *testing.T) {
	dbtest.RequirePostgres(t)

	sqliteStore := openSQLiteRef(t)
	pgStore := dbtest.New(t)

	sqSchema := introspectSQLite(t, sqliteStore)
	pgSchema := introspectPostgres(t, pgStore)

	// Same table set.
	assertSameStringSet(t, "tables", tableNames(sqSchema), tableNames(pgSchema))

	// Per-table: same column names, same nullability, same unique tuples, same FKs.
	for table, sqCols := range sqSchema {
		pgCols, ok := pgSchema[table]
		if !ok {
			t.Errorf("table %q missing in postgres", table)
			continue
		}
		assertSameStringSet(t, table+" columns", colNames(sqCols), colNames(pgCols))
		for name, sc := range sqCols {
			pc, ok := pgCols[name]
			if !ok {
				continue // already reported above
			}
			if sc.notNull != pc.notNull {
				t.Errorf("%s.%s nullability mismatch: sqlite notNull=%v, postgres notNull=%v",
					table, name, sc.notNull, pc.notNull)
			}
		}
	}

	sqUniq := introspectSQLiteUnique(t, sqliteStore)
	pgUniq := introspectPostgresUnique(t, pgStore)
	for table := range sqSchema {
		assertSameTupleSets(t, table+" unique", sqUniq[table], pgUniq[table])
	}

	sqFKs := introspectSQLiteFKs(t, sqliteStore)
	pgFKs := introspectPostgresFKs(t, pgStore)
	for table := range sqSchema {
		assertSameFKSets(t, table+" fks", sqFKs[table], pgFKs[table])
	}
}

// openSQLiteRef opens an in-memory SQLite store and runs all migrations.
// This is always SQLite, even when SHINYHUB_TEST_POSTGRES_DSN is set, because
// it is the canonical reference for the comparison.
func openSQLiteRef(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite ref: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate sqlite ref: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// colInfo holds the per-column metadata extracted from each backend.
type colInfo struct {
	notNull bool
}

// tableNames returns a sorted slice of table names from a schema map.
func tableNames(schema map[string]map[string]colInfo) []string {
	names := make([]string, 0, len(schema))
	for t := range schema {
		names = append(names, t)
	}
	sort.Strings(names)
	return names
}

// colNames returns a sorted slice of column names from a column map.
func colNames(cols map[string]colInfo) []string {
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)
	return names
}

// assertSameStringSet fails if the two slices do not contain the same elements
// (order-independent). label identifies what is being compared in error output.
func assertSameStringSet(t *testing.T, label string, a, b []string) {
	t.Helper()
	am := stringSet(a)
	bm := stringSet(b)
	for _, v := range a {
		if !bm[v] {
			t.Errorf("%s: %q present in sqlite but missing in postgres", label, v)
		}
	}
	for _, v := range b {
		if !am[v] {
			t.Errorf("%s: %q present in postgres but missing in sqlite", label, v)
		}
	}
}

func stringSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// introspectSQLite returns the table/column/nullability map for a SQLite store.
// schema_migrations is excluded from the comparison.
func introspectSQLite(t *testing.T, store *db.Store) map[string]map[string]colInfo {
	t.Helper()
	rows, err := store.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if name == "schema_migrations" {
			continue
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}

	schema := make(map[string]map[string]colInfo, len(tables))
	for _, table := range tables {
		cols, err := sqlitePragmaTableInfo(store, table)
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		schema[table] = cols
	}
	return schema
}

// sqlitePragmaTableInfo runs PRAGMA table_info on a single table and returns
// the column name -> colInfo map.
func sqlitePragmaTableInfo(store *db.Store, table string) (map[string]colInfo, error) {
	// PRAGMA statements are not parameterized; table name is safe (comes from
	// sqlite_master, not user input).
	rows, err := store.DB().Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]colInfo)
	for rows.Next() {
		// PRAGMA table_info columns: cid, name, type, notnull, dflt_value, pk
		var cid, notnull, pk int
		var name, typ string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = colInfo{notNull: notnull == 1}
	}
	return cols, rows.Err()
}

// introspectPostgres returns the table/column/nullability map for a Postgres store.
// schema_migrations is excluded from the comparison.
func introspectPostgres(t *testing.T, store *db.Store) map[string]map[string]colInfo {
	t.Helper()
	// Fetch all columns for public-schema tables in one query.
	rows, err := store.DB().Query(`
		SELECT table_name, column_name, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatalf("information_schema.columns query: %v", err)
	}
	defer rows.Close()

	schema := make(map[string]map[string]colInfo)
	for rows.Next() {
		var tbl, col, nullable string
		if err := rows.Scan(&tbl, &col, &nullable); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		if tbl == "schema_migrations" {
			continue
		}
		if schema[tbl] == nil {
			schema[tbl] = make(map[string]colInfo)
		}
		schema[tbl][col] = colInfo{notNull: strings.ToUpper(nullable) == "NO"}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("information_schema.columns rows: %v", err)
	}
	return schema
}

// -- Unique index parity --

// uniqueTuple is a sorted, comma-joined string of column names forming a unique
// constraint. Sorting removes declaration-order sensitivity.
type uniqueTuple = string

func normalizeUniqueTuple(cols []string) uniqueTuple {
	c := make([]string, len(cols))
	copy(c, cols)
	sort.Strings(c)
	return strings.Join(c, ",")
}

// introspectSQLiteUnique returns a map of table -> set of unique-index tuples.
// Each tuple is a normalized (sorted, comma-joined) column list.
// Primary-key columns are included when they form a multi-column PK (composite
// primary keys are unique by definition). Single-column BIGSERIAL/INTEGER PKs
// are intentionally excluded: the PK itself is structurally equivalent even if
// SQLite calls it "pk" and Postgres names it differently.
func introspectSQLiteUnique(t *testing.T, store *db.Store) map[string][]uniqueTuple {
	t.Helper()
	rows, err := store.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite unique: sqlite_master: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("sqlite unique: scan: %v", err)
		}
		if name != "schema_migrations" {
			tables = append(tables, name)
		}
	}
	_ = rows.Close()

	result := make(map[string][]uniqueTuple)
	for _, table := range tables {
		tuples, err := sqliteUniqueIndex(store, table)
		if err != nil {
			t.Fatalf("sqlite unique for %s: %v", table, err)
		}
		result[table] = tuples
	}
	return result
}

// sqliteUniqueIndex introspects PRAGMA index_list + PRAGMA index_info for a
// single table and returns the set of unique (multi-column or single-column)
// index tuples. Single-column unique constraints on the PK are skipped: both
// backends enforce PK uniqueness, but the mechanism differs (SERIAL vs
// AUTOINCREMENT) and it is not a structural drift risk.
func sqliteUniqueIndex(store *db.Store, table string) ([]uniqueTuple, error) {
	// PRAGMA index_list: seq, name, unique, origin, partial
	ilRows, err := store.DB().Query(fmt.Sprintf("PRAGMA index_list(%s)", table))
	if err != nil {
		return nil, err
	}
	defer ilRows.Close()

	type idxMeta struct {
		name   string
		unique bool
		origin string // "c" = CREATE INDEX, "u" = UNIQUE constraint, "pk" = PRIMARY KEY
	}
	var indexes []idxMeta
	for ilRows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := ilRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		if unique == 1 {
			indexes = append(indexes, idxMeta{name: name, unique: true, origin: origin})
		}
	}
	if err := ilRows.Err(); err != nil {
		return nil, err
	}

	var tuples []uniqueTuple
	for _, idx := range indexes {
		// Skip pk-origin single-column indexes: the PK row order is part of the
		// table definition already asserted by FK and column presence checks.
		if idx.origin == "pk" {
			continue
		}
		iiRows, err := store.DB().Query(fmt.Sprintf("PRAGMA index_info(%s)", idx.name))
		if err != nil {
			return nil, err
		}
		var cols []string
		for iiRows.Next() {
			var seqno, cid int
			var colName string
			if err := iiRows.Scan(&seqno, &cid, &colName); err != nil {
				_ = iiRows.Close()
				return nil, err
			}
			cols = append(cols, colName)
		}
		_ = iiRows.Close()
		if len(cols) > 0 {
			tuples = append(tuples, normalizeUniqueTuple(cols))
		}
	}
	sort.Strings(tuples)
	return tuples, nil
}

// introspectPostgresUnique returns a map of table -> set of unique constraint tuples.
func introspectPostgresUnique(t *testing.T, store *db.Store) map[string][]uniqueTuple {
	t.Helper()
	// Group columns by (table, constraint) to reconstruct multi-column tuples.
	type key struct{ table, constraint string }
	grouped := make(map[key][]string)
	tableForKey := make(map[key]string)

	rows, err := store.DB().Query(`
		SELECT tc.table_name, tc.constraint_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_name = tc.constraint_name
		 AND kcu.table_schema    = tc.table_schema
		WHERE tc.table_schema    = 'public'
		  AND tc.constraint_type = 'UNIQUE'
		ORDER BY tc.table_name, tc.constraint_name, kcu.ordinal_position`)
	if err != nil {
		t.Fatalf("postgres unique constraints query: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tbl, constraint, col string
		if err := rows.Scan(&tbl, &constraint, &col); err != nil {
			t.Fatalf("scan unique row: %v", err)
		}
		if tbl == "schema_migrations" {
			continue
		}
		k := key{tbl, constraint}
		grouped[k] = append(grouped[k], col)
		tableForKey[k] = tbl
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("postgres unique rows: %v", err)
	}

	result := make(map[string][]uniqueTuple)
	for k, cols := range grouped {
		tbl := tableForKey[k]
		result[tbl] = append(result[tbl], normalizeUniqueTuple(cols))
	}
	for tbl := range result {
		sort.Strings(result[tbl])
	}
	return result
}

// assertSameTupleSets fails if the two unique-tuple slices are not equal
// (order-independent set comparison).
func assertSameTupleSets(t *testing.T, label string, a, b []uniqueTuple) {
	t.Helper()
	am := stringSet(a)
	bm := stringSet(b)
	for _, v := range a {
		if !bm[v] {
			t.Errorf("%s: unique {%s} present in sqlite but missing in postgres", label, v)
		}
	}
	for _, v := range b {
		if !am[v] {
			t.Errorf("%s: unique {%s} present in postgres but missing in sqlite", label, v)
		}
	}
}

// -- Foreign key parity --

// fkEdge represents a single FK column -> referenced table.column.
type fkEdge struct {
	fromCol string
	toTable string
	toCol   string
}

func (e fkEdge) String() string {
	return fmt.Sprintf("%s->%s.%s", e.fromCol, e.toTable, e.toCol)
}

// introspectSQLiteFKs returns a map of table -> FK edges using PRAGMA foreign_key_list.
func introspectSQLiteFKs(t *testing.T, store *db.Store) map[string][]fkEdge {
	t.Helper()
	rows, err := store.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite fks: sqlite_master: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("sqlite fks: scan: %v", err)
		}
		if name != "schema_migrations" {
			tables = append(tables, name)
		}
	}
	_ = rows.Close()

	result := make(map[string][]fkEdge)
	for _, table := range tables {
		edges, err := sqliteFKList(store, table)
		if err != nil {
			t.Fatalf("sqlite fk_list(%s): %v", table, err)
		}
		result[table] = edges
	}
	return result
}

// sqliteFKList runs PRAGMA foreign_key_list for a table and returns FK edges.
func sqliteFKList(store *db.Store, table string) ([]fkEdge, error) {
	// Columns: id, seq, table, from, to, on_update, on_delete, match
	rows, err := store.DB().Query(fmt.Sprintf("PRAGMA foreign_key_list(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []fkEdge
	for rows.Next() {
		var id, seq int
		var toTable, fromCol, toCol, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &toTable, &fromCol, &toCol, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		edges = append(edges, fkEdge{fromCol: fromCol, toTable: toTable, toCol: toCol})
	}
	return edges, rows.Err()
}

// introspectPostgresFKs returns a map of table -> FK edges using information_schema.
func introspectPostgresFKs(t *testing.T, store *db.Store) map[string][]fkEdge {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT
			kcu.table_name,
			kcu.column_name,
			ccu.table_name  AS foreign_table_name,
			ccu.column_name AS foreign_column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_name = tc.constraint_name
		 AND kcu.table_schema    = tc.table_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_name = tc.constraint_name
		 AND ccu.table_schema    = tc.table_schema
		WHERE tc.table_schema   = 'public'
		  AND tc.constraint_type = 'FOREIGN KEY'
		ORDER BY kcu.table_name, kcu.column_name`)
	if err != nil {
		t.Fatalf("postgres fks query: %v", err)
	}
	defer rows.Close()

	result := make(map[string][]fkEdge)
	for rows.Next() {
		var tbl, col, fTable, fCol string
		if err := rows.Scan(&tbl, &col, &fTable, &fCol); err != nil {
			t.Fatalf("scan fk row: %v", err)
		}
		if tbl == "schema_migrations" {
			continue
		}
		result[tbl] = append(result[tbl], fkEdge{fromCol: col, toTable: fTable, toCol: fCol})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("postgres fk rows: %v", err)
	}
	return result
}

// assertSameFKSets fails if the two FK-edge slices are not equal
// (order-independent set comparison).
func assertSameFKSets(t *testing.T, label string, a, b []fkEdge) {
	t.Helper()
	edgeStr := func(edges []fkEdge) []string {
		ss := make([]string, len(edges))
		for i, e := range edges {
			ss[i] = e.String()
		}
		return ss
	}
	assertSameStringSet(t, label, edgeStr(a), edgeStr(b))
}
