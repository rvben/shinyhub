package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryAuditEventCarriesDetail parses this package and fails on any
// logAuditEvent call whose db.AuditEventParams literal omits Detail.
//
// The defect it guards is quiet: an event with no detail still appears in the
// audit log and still looks like a complete record, so nothing signals that the
// row cannot answer what actually changed. "stop reports" does not say whether
// the app was running; "deploy reports" does not say which bundle shipped. That
// is only discoverable months later, by someone reading the trail during an
// incident, when the missing facts can no longer be recovered.
//
// The check is structural rather than a list of known handlers, because the way
// this regresses is a NEW handler being added without a Detail, which no
// enumeration written today would cover.
func TestEveryAuditEventCarriesDetail(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	seen := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "logAuditEvent" {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.CompositeLit)
					if !ok {
						continue
					}
					if !isAuditEventParams(lit.Type) {
						continue
					}
					seen++
					if !hasField(lit, "Detail") {
						pos := fset.Position(lit.Pos())
						t.Errorf("%s:%d: audit event is recorded with no Detail, so the trail cannot say what changed; add the facts that make this event self-explanatory",
							filepath.Base(path), pos.Line)
					}
				}
				return true
			})
		}
	}

	// A scan that silently matched nothing would report success forever, so the
	// harness proves it can see the call sites it is judging.
	if seen < 20 {
		t.Fatalf("only %d audit call sites found; the scan is not matching the code it is meant to guard", seen)
	}
}

func isAuditEventParams(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "db" && sel.Sel.Name == "AuditEventParams"
}

func hasField(lit *ast.CompositeLit, name string) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == name {
			return true
		}
	}
	return false
}
