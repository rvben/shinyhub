package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// actionsRequiringDetail names the audit actions whose row is not
// self-explanatory. Each is recorded from more than one code path, or destroys
// the record its resource_id points at, so the Detail column is the only place
// the distinguishing fact can live:
//
//   - login and create_user happen through the local form, the CLI, and each
//     SSO provider, and read identically without the provider and grant.
//   - delete_user and delete_token destroy the row their resource_id names, so
//     nothing can resolve it afterwards.
//   - login_failed is the difference between somebody guessing usernames and
//     somebody guessing one account's password.
//
// A new provider or a new deletion path is exactly where this is forgotten, and
// nothing else notices: the row is written, the request succeeds, and the gap
// only surfaces when an operator is reading the trail to answer a question.
var actionsRequiringDetail = map[string]bool{
	"login":        true,
	"login_failed": true,
	"create_user":  true,
	"delete_user":  true,
	"delete_token": true,
}

// TestAuditEventParams_RequiredActionsCarryDetail scans the package source for
// db.AuditEventParams literals and fails any listed action built without a
// Detail field.
//
// This reads source rather than behaviour because the gap it guards is an
// absent field, which no request can be made to exhibit: the handler returns
// its normal success and writes a row that simply says less than it should.
// Every one of these paths also has a behavioural test asserting what the
// Detail contains; this one exists so a sixth call site cannot be added without
// one.
func TestAuditEventParams_RequiredActionsCarryDetail(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isAuditEventParams(lit.Type) {
				return true
			}
			action, ok := literalStringField(lit, "Action")
			if !ok || !actionsRequiringDetail[action] {
				return true
			}
			checked++
			if !hasField(lit, "Detail") {
				t.Errorf("%s: audit action %q is recorded without a Detail; %s",
					fset.Position(lit.Pos()), action, whyDetailMatters(action))
			}
			return true
		})
	}

	// Positive control. A scan that matches nothing reports every action as
	// compliant, which is indistinguishable from a codebase that is. The count
	// is a floor rather than an exact number so that adding a call site does not
	// fail the build for the wrong reason.
	if checked < 8 {
		t.Fatalf("the scan found only %d audit sites for the listed actions, which is too few to be a real result; the parser or the type match is broken", checked)
	}
}

func whyDetailMatters(action string) string {
	switch action {
	case "login", "create_user":
		return "several sign-in paths record this same action, so without provider and grant they are indistinguishable"
	case "delete_user", "delete_token":
		return "the row this names is destroyed, so nothing can resolve resource_id afterwards"
	case "login_failed":
		return "an unknown username and a wrong password call for different responses"
	}
	return "it is recorded from more than one path"
}

func isAuditEventParams(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "AuditEventParams" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "db"
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

func literalStringField(lit *ast.CompositeLit, name string) (string, bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != name {
			continue
		}
		val, ok := kv.Value.(*ast.BasicLit)
		if !ok || val.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(val.Value)
		if err != nil {
			return "", false
		}
		return s, true
	}
	return "", false
}
