package fsaccess_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// r3 task 1.5, design D3: only internal/executor (and fsaccess itself) may
// write to a source. The guard type-checks every non-test package of the
// module against the compiler's export data and fails on any use of
// fsaccess.AsWriter or fsaccess.Writer, any call (or method value) of a
// Writer method (RenameNoReplace, Mkdir, Rmdir, Sync, r4's CreateExclusive
// and Unlink, and r5's SetModTime) on a value implementing Writer, and any
// type assertion that gives a Dir one of those methods.

// writeAllowed reports whether pkg may write: internal/executor and the
// fsaccess packages, with their subpackages.
func writeAllowed(pkg string) bool {
	for _, p := range []string{"precious/internal/executor", "precious/internal/fsaccess"} {
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

// writeGuard holds the fsaccess objects the guard looks for.
type writeGuard struct {
	asWriter *types.Func
	writer   *types.TypeName
	iface    *types.Interface // Writer's
	dir      *types.Interface // Dir's
}

func newWriteGuard(t *testing.T, imp types.Importer) writeGuard {
	t.Helper()
	pkg, err := imp.Import("precious/internal/fsaccess")
	if err != nil {
		t.Fatal(err)
	}
	g := writeGuard{}
	var ok bool
	if g.asWriter, ok = pkg.Scope().Lookup("AsWriter").(*types.Func); !ok {
		t.Fatal("fsaccess.AsWriter not found")
	}
	if g.writer, ok = pkg.Scope().Lookup("Writer").(*types.TypeName); !ok {
		t.Fatal("fsaccess.Writer not found")
	}
	g.iface = g.writer.Type().Underlying().(*types.Interface)
	g.dir = pkg.Scope().Lookup("Dir").Type().Underlying().(*types.Interface)
	return g
}

// writerMethod reports whether T has a method with the name and signature
// of one of Writer's.
func (g writeGuard) writerMethod(T types.Type) bool {
	for m := range g.iface.Methods() {
		obj, _, _ := types.LookupFieldOrMethod(T, true, nil, m.Name())
		if f, ok := obj.(*types.Func); ok && types.Identical(f.Type(), m.Type()) {
			return true
		}
	}
	return false
}

// violations lists every forbidden write reference in files, type-checked
// into info.
func (g writeGuard) violations(fset *token.FileSet, files []*ast.File, info *types.Info) []string {
	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), what))
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				switch info.Uses[n] {
				case g.asWriter:
					report(n, "uses fsaccess.AsWriter")
				case g.writer:
					report(n, "names fsaccess.Writer")
				}
			case *ast.SelectorExpr:
				sel, ok := info.Selections[n]
				if !ok || sel.Kind() == types.FieldVal || !slices.ContainsFunc(slices.Collect(g.iface.Methods()),
					func(m *types.Func) bool { return m.Name() == n.Sel.Name }) {
					return true
				}
				if recv := sel.Recv(); types.Implements(recv, g.iface) || types.Implements(types.NewPointer(recv), g.iface) {
					report(n, "calls the Writer method "+n.Sel.Name)
				}
			case *ast.TypeAssertExpr:
				if n.Type == nil {
					return true
				}
				x, asserted := info.TypeOf(n.X), info.TypeOf(n.Type)
				if x != nil && asserted != nil && types.Implements(x, g.dir) && g.writerMethod(asserted) {
					report(n, "asserts a Dir to a type with a Writer method")
				}
			}
			return true
		})
	}
	return out
}

func TestOnlyExecutorWrites(t *testing.T) {
	type listed struct {
		ImportPath, Dir, Export string
		GoFiles                 []string
		Standard                bool
	}
	cmd := exec.Command("go", "list", "-deps", "-export", "-json=ImportPath,Dir,Export,GoFiles,Standard", "./...")
	cmd.Dir = filepath.Join("..", "..")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.Bytes())
	}
	exports := map[string]string{}
	var module []listed
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listed
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		exports[p.ImportPath] = p.Export
		if !p.Standard && (p.ImportPath == "precious" || strings.HasPrefix(p.ImportPath, "precious/")) {
			module = append(module, p)
		}
	}

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	g := newWriteGuard(t, imp)
	check := func(path string, files []*ast.File) []string {
		info := &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		if _, err := (&types.Config{Importer: imp}).Check(path, fset, files, info); err != nil {
			t.Fatalf("type-check %s: %v", path, err)
		}
		return g.violations(fset, files, info)
	}

	// The guard finds each kind of write, and nothing in look-alikes.
	parse := func(src string) []*ast.File {
		f, err := parser.ParseFile(fset, "fixture.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return []*ast.File{f}
	}
	bad := check("precious/internal/fixture/bad", parse(`package bad
import ("time"; "precious/internal/fsaccess")
func f(d fsaccess.Dir, w fsaccess.Writer) {
	if v, ok := fsaccess.AsWriter(d); ok { _ = v.Sync() }
	_ = w.RenameNoReplace(nil, d, nil)
	mk := w.Mkdir
	_ = mk
	_ = d.(interface{ Rmdir([]byte) error }).Rmdir(nil)
	_ = w.CreateExclusive(nil, nil)
	un := w.Unlink
	_ = un
	_ = d.(interface{ Unlink([]byte) error })
	_ = w.SetModTime(nil, time.Time{})
	_ = d.(interface{ SetModTime([]byte, time.Time) error })
}`))
	if len(bad) != 11 {
		t.Errorf("fixture violations = %q, want 11 (AsWriter, Writer, Sync, RenameNoReplace, Mkdir, the Rmdir assertion, "+
			"CreateExclusive, Unlink, the Unlink assertion, SetModTime, the SetModTime assertion)", bad)
	}
	if good := check("precious/internal/fixture/good", parse(`package good
import ("io"; "os"; "time"; "precious/internal/fsaccess")
func f(d fsaccess.Dir, w io.Writer, file *os.File) error {
	_ = os.Mkdir("x", 0o700)
	if s, ok := w.(interface{ Sync() error }); ok { _ = s.Sync() }
	if u, ok := w.(interface{ Unlink([]byte) error }); ok { _ = u.Unlink(nil) }
	if m, ok := w.(interface{ SetModTime([]byte, time.Time) error }); ok { _ = m.SetModTime(nil, time.Time{}) }
	_ = d.Self()
	return file.Sync()
}`)); len(good) != 0 {
		t.Errorf("look-alike violations = %q, want none", good)
	}

	var found []string
	for _, p := range module {
		if writeAllowed(p.ImportPath) || len(p.GoFiles) == 0 {
			continue
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		found = append(found, check(p.ImportPath, files)...)
	}
	if len(module) < 20 {
		t.Fatalf("checked %d module packages; go list found too few", len(module))
	}
	for _, v := range found {
		t.Errorf("only internal/executor may write to a source: %s", v)
	}
}
