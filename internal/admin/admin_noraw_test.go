package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// responseWriters are the calls whose arguments reach the client.
var responseWriters = map[string]bool{
	"writeError":                        true,
	"writeAdminAPIErrorHertz":           true,
	"writeAdminAPIValidationErrorHertz": true,
	"WriteError":                        true, // httpx.WriteError
}

// rawErrorViolations returns "file:line" for every response-writing call in src
// whose arguments contain a `.Error()` call, i.e. text of an internal error that
// would reach the client (FR-022, B15). Calls of c.JSON / c.String count too.
func rawErrorViolations(fset *token.FileSet, file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !writesResponse(call) {
			return true
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(m ast.Node) bool {
				inner, ok := m.(*ast.CallExpr)
				if !ok || len(inner.Args) != 0 {
					return true
				}
				if sel, ok := inner.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
					out = append(out, fset.Position(inner.Pos()).String())
				}
				return true
			})
		}
		return true
	})
	return out
}

func writesResponse(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return responseWriters[fn.Name]
	case *ast.SelectorExpr:
		if responseWriters[fn.Sel.Name] {
			return true
		}
		// c.JSON(...) / c.String(...) on the request context
		if id, ok := fn.X.(*ast.Ident); ok && id.Name == "c" && (fn.Sel.Name == "JSON" || fn.Sel.Name == "String") {
			return true
		}
	}
	return false
}

func TestNoRawErrorInResponses(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		violations = append(violations, rawErrorViolations(fset, f)...)
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d files; the glob is broken", scanned)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("FR-022: internal error text reaches the client at %s (log it with the request id and send a fixed message)", v)
	}
}

// The scanner must really see the patterns it exists to forbid.
func TestNoRawErrorScannerFlagsKnownBadPatterns(t *testing.T) {
	const src = `package p
func h(c *Ctx, err error) {
	writeAdminAPIErrorHertz(c, 500, err.Error())
	writeAdminAPIErrorHertz(c, 500, "Failed: "+err.Error())
	writeAdminAPIValidationErrorHertz(c, []string{err.Error()})
	writeError(c, 500, "internal_error", err.Error())
	httpx.WriteError(c, 500, "internal_error", err.Error())
	c.JSON(500, map[string]string{"error": err.Error()})
	c.String(500, err.Error())
}
func fine(c *Ctx, err error) {
	logHandlerError(c, "what", err)
	writeAdminAPIErrorHertz(c, 500, "Failed to do it")
	errs = append(errs, err.Error())
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bad.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := rawErrorViolations(fset, f)
	if len(got) != 7 {
		t.Errorf("flagged %d calls, want 7: %v", len(got), got)
	}
	for _, v := range got {
		line, _ := strconv.Atoi(strings.Split(v, ":")[1])
		if line > 9 {
			t.Errorf("flagged an innocent call at %s", v)
		}
	}
}

// R6: every route group of the admin API is the literal /admin/api/v1 so that
// cmd/llmsgen keeps extracting the route table and no group is left on a legacy prefix.
func TestRouteGroupsAreV1(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Group" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			path, _ := strconv.Unquote(lit.Value)
			if strings.HasPrefix(path, "/admin/api") {
				found++
				if path != "/admin/api/v1" {
					t.Errorf("%s: route group %q must be the literal /admin/api/v1", fset.Position(call.Pos()), path)
				}
			}
			return true
		})
	}
	if found < 8 {
		t.Errorf("found only %d /admin/api groups; the scan is broken", found)
	}
}
