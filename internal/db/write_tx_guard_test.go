package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

// transactionEntryPoints are the only files allowed to call Begin or BeginTx
// on a database handle. Every other file starts a write transaction through
// dialect.beginWrite and a read-only snapshot through Store.beginReadSnapshot.
// On SQLite a deferred transaction that reads before it writes takes its read
// snapshot at the first SELECT; once any other connection commits, the first
// write fails with SQLITE_BUSY_SNAPSHOT (or a plain SQLITE_BUSY the busy
// handler is never consulted for), so the operation is lost instead of waiting.
// beginWrite opens with BEGIN IMMEDIATE so the lock is held before any read.
var transactionEntryPoints = map[string]bool{
	"bound.go":   true,
	"dialect.go": true,
}

func TestTransactionsStartOnlyInEntryPoints(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("list package directory: %v", err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || transactionEntryPoints[name] {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "Begin" || sel.Sel.Name == "BeginTx" {
				offenders = append(offenders, fset.Position(sel.Sel.Pos()).String())
			}
			return true
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("%d transaction(s) started outside %v; use s.d.beginWrite (writes) or s.beginReadSnapshot (read-only):\n  %s",
			len(offenders), slices.Sorted(maps.Keys(transactionEntryPoints)), strings.Join(offenders, "\n  "))
	}
}
