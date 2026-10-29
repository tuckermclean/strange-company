package installstate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// serialTests lists top-level tests allowed to stay serial, with the reason.
// The install harness is hermetic (own temp dir, in-memory S3 world and clock
// per test; no Setenv, Chdir or shared files), so the list is empty. A test
// that must use t.Setenv, process-global state, a fixed port, os.Args, the
// real clock or a re-exec'd helper process belongs here, with its reason.
var serialTests = map[string]string{}

// TestInstallStateTests_AreParallel keeps the slow install state tests parallel (the
// package would otherwise exceed the go test timeout). Copy of the install guard (operator-approved split, 2026-10-29;
// the original is an operator-approved testProtection exception, 2026-10-28).
func TestInstallStateTests_AreParallel(t *testing.T) {
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
	if seen < 15 {
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
