package lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const ruleNetcheck = "netcheck"

// netcheckSkipDirs are never scanned: fixtures, caches and build output.
var netcheckSkipDirs = map[string]bool{
	".git": true, ".cache": true, "out": true, "testdata": true, "node_modules": true, "vendor": true,
}

// httpDefaultUsers are net/http package-level helpers that go through http.DefaultClient.
var httpDefaultUsers = map[string]bool{"DefaultClient": true, "Get": true, "Head": true, "Post": true, "PostForm": true}

// networkNames are the first arguments of net.Dial-style calls; they are not addresses.
var networkNames = map[string]bool{
	"tcp": true, "tcp4": true, "tcp6": true, "udp": true, "udp4": true, "udp6": true,
	"unix": true, "unixgram": true, "unixpacket": true, "ip": true, "ip4": true, "ip6": true,
}

// NetCheck is the D5 analyzer of plan 52 section 9.4: outside test/e2e/** a _test.go file must not use
// http.DefaultClient (or the helpers that use it) and must not dial a non-loopback address.
func NetCheck(root string) (Result, error) {
	var res Result
	res.ran(ruleNetcheck)
	e2e := filepath.Join(root, "test", "e2e")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == e2e || (path != root && netcheckSkipDirs[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return netcheckFile(&res, path, filepath.ToSlash(rel))
	})
	return res, err
}

func netcheckFile(res *Result, path, rel string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil // unparsable files are the compiler's business
	}
	// Local names of the net/http import (aliases included); a dot or blank import has none.
	httpNames := map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != "net/http" {
			continue
		}
		name := "http"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		httpNames[name] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && httpNames[id.Name] && httpDefaultUsers[x.Sel.Name] {
				res.add(ruleNetcheck, rel, "line %d: http.%s uses http.DefaultClient; use an in-process httptest server and its Client", fset.Position(x.Pos()).Line, x.Sel.Name)
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasPrefix(sel.Sel.Name, "Dial") {
				return true
			}
			for _, a := range x.Args {
				lit, ok := a.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || networkNames[s] {
					continue
				}
				if !loopbackAddr(s) {
					res.add(ruleNetcheck, rel, "line %d: %s dials non-loopback address %q", fset.Position(x.Pos()).Line, sel.Sel.Name, s)
				}
				break
			}
		}
		return true
	})
	return nil
}

// loopbackAddr reports whether a host[:port] literal is a loopback address.
func loopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" { // ":port" listens or dials on the local host
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
