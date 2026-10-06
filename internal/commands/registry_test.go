package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The dispatcher registers exactly the command names this package exports
// (every exported string constant is one); other commands come from
// Register, and registering a name twice panics.
func TestRegistry(t *testing.T) {
	h := New(Options{})
	exported := slices.Sorted(maps.Keys(exportedStringConsts(t)))
	if registered := slices.Sorted(maps.Keys(h.decoders)); !slices.Equal(registered, exported) {
		t.Fatalf("New registers %v, want the exported command names %v", registered, exported)
	}

	h.Register(enqueueTest, decodeEnqueue)
	if h.decoders[enqueueTest] == nil {
		t.Fatalf("Register did not install %s", enqueueTest)
	}
	for _, name := range []string{CancelJob, enqueueTest} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering %q a second time did not panic", name)
				}
			}()
			h.Register(name, decodeEnqueue)
		}()
	}
	if other := New(Options{}); other.decoders[enqueueTest] != nil {
		t.Error("a registration leaked into another handler")
	}
}

// A name nobody registered answers 404 not_found, before the
// Idempotency-Key is checked, and records nothing.
func TestUnknownCommandNotFound(t *testing.T) {
	e := newEnv(t, nil)
	for _, key := range []string{"k1", ""} {
		if r := e.post(t, "no-such-command", key, `{}`); r.status != http.StatusNotFound || r.errCode() != "not_found" {
			t.Errorf("unknown command with key %q = %d %s, want 404 not_found", key, r.status, r.raw)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM command_requests`); n != 0 {
		t.Errorf("%d command requests recorded for an unknown command", n)
	}
}

// exportedStringConsts maps the value of every exported string constant
// declared in the package's non-test files to its name. Two constants with
// one value are an error.
func exportedStringConsts(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !id.IsExported() || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					if prev, dup := out[value]; dup {
						t.Errorf("constants %s and %s are both %q", prev, id.Name, value)
					}
					out[value] = id.Name
				}
			}
		}
	}
	return out
}
