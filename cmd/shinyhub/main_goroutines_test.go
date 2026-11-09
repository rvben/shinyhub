package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// exemptMarker is the comment that admits a bare go statement in the server's
// startup and ownership code. It must say how that goroutine is joined before
// the store closes, since nothing else will.
const exemptMarker = "rungroup:exempt"

// runServe's background work runs as services of its run group, which joins
// each one within a named stop budget before the store closes. A bare go
// statement skips that join: it can outlive runServe's return, or still be
// using the store when the deferred Close runs. The few goroutines with their
// own join (the HTTP serve, the elector and its watchdog, the ownership-epoch
// loops) say so with an exemption comment on the line above.
func TestRunServeStartsBackgroundWorkThroughTheRunGroup(t *testing.T) {
	for _, target := range []struct{ file, fn string }{
		{"main.go", "runServe"},
		{"owner_runtime.go", "run"},
	} {
		t.Run(target.file+":"+target.fn, func(t *testing.T) {
			checkGoStatementsAreJoined(t, target.file, target.fn)
		})
	}
}

func checkGoStatementsAreJoined(t *testing.T, path, fnName string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == fnName {
			fn = d
		}
	}
	if fn == nil {
		t.Fatalf("%s has no func %s", path, fnName)
	}

	exemptLines := map[int]string{}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if i := strings.Index(c.Text, exemptMarker); i >= 0 {
				exemptLines[fset.Position(c.End()).Line] = strings.TrimSpace(strings.TrimLeft(c.Text[i+len(exemptMarker):], ": "))
			}
		}
	}

	var seen int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		seen++
		line := fset.Position(stmt.Pos()).Line
		reason, ok := exemptLines[line-1]
		switch {
		case !ok:
			t.Errorf("%s:%d starts a goroutine outside the run group; use services.Go, or mark it %q with how it is joined", path, line, "// "+exemptMarker+": <join>")
		case reason == "":
			t.Errorf("%s:%d is exempt from the run group without saying how it is joined", path, line)
		}
		return true
	})
	// Each scanned function holds at least one exempt goroutine, so seeing
	// none means the scan is not looking at the code it is meant to guard.
	if seen == 0 {
		t.Fatalf("found no go statements in %s %s at all; the scan is not looking at the right function", path, fnName)
	}
}
