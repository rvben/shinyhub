package httpbuf

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Every reverse proxy in the codebase is built through ReverseProxy so it
// draws its copy buffers from the shared pool. A direct
// httputil.NewSingleHostReverseProxy call or a httputil.ReverseProxy literal
// anywhere else silently reintroduces a 32 KiB allocation per response.
func TestReverseProxiesAreBuiltThroughThisPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var offenders []string
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "httpbuf" || d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if isHTTPUtil(node.X) && node.Sel.Name == "NewSingleHostReverseProxy" {
						offenders = append(offenders, fset.Position(node.Pos()).String())
					}
				case *ast.CompositeLit:
					if sel, ok := node.Type.(*ast.SelectorExpr); ok && isHTTPUtil(sel.X) && sel.Sel.Name == "ReverseProxy" {
						offenders = append(offenders, fset.Position(node.Pos()).String())
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d reverse proxy construction(s) outside internal/httpbuf; use httpbuf.ReverseProxy so responses copy through the shared buffer pool:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func isHTTPUtil(x ast.Expr) bool {
	ident, ok := x.(*ast.Ident)
	return ok && ident.Name == "httputil"
}
