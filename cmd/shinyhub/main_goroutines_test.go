package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// exemptMarker is the comment that admits a bare go statement in runServe. It
// must say how that goroutine is joined before the store closes, since nothing
// else in runServe will.
const exemptMarker = "rungroup:exempt"

// runServe's background work runs as services of its run group, which joins
// each one within a named stop budget before the store closes. A bare go
// statement skips that join: it can outlive runServe's return, or still be
// using the store when the deferred Close runs. The few goroutines with their
// own join (the HTTP serve, the elector and its watchdog, the ownership-epoch
// loops) say so with an exemption comment on the line above.
func TestRunServeStartsBackgroundWorkThroughTheRunGroup(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	var runServe *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "runServe" {
			runServe = fn
		}
	}
	if runServe == nil {
		t.Fatal("main.go has no runServe")
	}

	exemptLines := map[int]string{}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if i := strings.Index(c.Text, exemptMarker); i >= 0 {
				exemptLines[fset.Position(c.End()).Line] = strings.TrimSpace(strings.TrimLeft(c.Text[i+len(exemptMarker):], ": "))
			}
		}
	}

	var bare, exempt int
	ast.Inspect(runServe.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		line := fset.Position(stmt.Pos()).Line
		reason, ok := exemptLines[line-1]
		switch {
		case !ok:
			bare++
			t.Errorf("main.go:%d starts a goroutine outside the run group; use services.Go, or mark it %q with how it is joined", line, "// "+exemptMarker+": <join>")
		case reason == "":
			t.Errorf("main.go:%d is exempt from the run group without saying how it is joined", line)
		default:
			exempt++
		}
		return true
	})
	// The check must have seen the exempt goroutines it is meant to tolerate,
	// or a parse that matched nothing would pass vacuously.
	if bare == 0 && exempt == 0 {
		t.Fatal("found no go statements in runServe at all; the scan is not looking at the right function")
	}
}
