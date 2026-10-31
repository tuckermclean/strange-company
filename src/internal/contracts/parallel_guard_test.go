package contracts_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// serialTests lists top-level tests allowed to stay serial, with the reason.
// Every test in this package works in its own treeCopy (t.TempDir) and only
// reads the real repository tree, so the list is empty. A test that must use
// t.Setenv, os.Chdir, run `make contracts` or write outputs in the real
// repository tree, or rely on other process-global state belongs here, with
// its reason.
var serialTests = map[string]string{}

// TestContractsTests_AreParallel keeps the slow contracts tests parallel (the
// package would otherwise exceed the go test timeout). Operator-approved
// testProtection exception, 2026-10-28.
func TestContractsTests_AreParallel(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found: %v", err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" || fn.Body == nil {
				continue
			}
			seen++
			if reason, ok := serialTests[fn.Name.Name]; ok {
				if reason == "" {
					t.Errorf("%s: serial exception needs a reason", fn.Name.Name)
				}
				continue
			}
			if !firstIsParallel(fn) {
				t.Errorf("%s (%s): the first statement must be t.Parallel(), or list the test in serialTests with a reason",
					fn.Name.Name, name)
			}
		}
	}
	if seen < 40 {
		t.Errorf("found only %d top-level tests; the guard is not looking at the package", seen)
	}
}

func firstIsParallel(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) == 0 {
		return false
	}
	es, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "t"
}
