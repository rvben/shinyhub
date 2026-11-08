package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoJSONIsBuiltWithSprintf fails on any fmt.Sprintf whose format string is
// a JSON object template.
//
// %q writes Go escapes, not JSON ones: a control byte becomes \x01 or \a and
// invalid UTF-8 becomes \xff, all of which encoding/json rejects. One such byte
// in a schedule name or a slug turns the whole audit row into a string nothing
// downstream can decode. The inputs are ASCII-constrained today; this keeps the
// encoding correct without depending on that, by requiring db.AuditDetail.
func TestNoJSONIsBuiltWithSprintf(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	sprintfs := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Sprintf" {
					return true
				}
				if pkgID, ok := sel.X.(*ast.Ident); !ok || pkgID.Name != "fmt" {
					return true
				}
				sprintfs++
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				format, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if strings.HasPrefix(strings.TrimSpace(format), "{") {
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d: JSON built with fmt.Sprintf(%s); use db.AuditDetail so every value is JSON-escaped",
						filepath.Base(path), pos.Line, lit.Value)
				}
				return true
			})
		}
	}

	// A scan that matched nothing would pass forever, so prove it sees the
	// package's Sprintf calls.
	if sprintfs < 50 {
		t.Fatalf("only %d fmt.Sprintf calls found; the scan is not matching the code it guards", sprintfs)
	}
}
