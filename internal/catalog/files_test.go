package catalog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// One entry per file, and the file is named after the entry. Go is happy to
// compile a package-level var nobody reads, so an entry file left out of the
// Templates index would vanish from the catalogue without a word — that is the
// failure this guards.

// entryFiles parses the package directory and returns entry ID -> file name for
// every package-level `var x = Template{...}`.
func entryFiles(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the catalogue package: %v", err)
	}

	out := map[string]string{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.CompositeLit)
					if !ok {
						continue
					}
					name, ok := lit.Type.(*ast.Ident)
					if !ok || name.Name != "Template" {
						continue
					}
					if id := litField(lit, "ID"); id != "" {
						out[id] = filepath.Base(path)
					}
				}
			}
		}
	}
	return out
}

func litField(lit *ast.CompositeLit, field string) string {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != field {
			continue
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok {
			v, err := strconv.Unquote(bl.Value)
			if err == nil {
				return v
			}
		}
	}
	return ""
}

func TestEveryEntryFileIsInTheIndex(t *testing.T) {
	files := entryFiles(t)
	if len(files) == 0 {
		t.Fatal("found no entry files; the parser is not seeing the package")
	}

	indexed := map[string]bool{}
	for _, e := range Templates {
		indexed[e.ID] = true
	}

	for id, file := range files {
		if !indexed[id] {
			t.Errorf("%s defines entry %q, which Templates never lists — it will not appear in the catalogue", file, id)
		}
	}
	for id := range indexed {
		if _, ok := files[id]; !ok {
			t.Errorf("Templates lists %q but no file defines it as a package-level var", id)
		}
	}
	if len(files) != len(Templates) {
		t.Errorf("%d entry files for %d indexed templates", len(files), len(Templates))
	}
}

func TestEntryFileIsNamedAfterItsEntry(t *testing.T) {
	for id, file := range entryFiles(t) {
		if want := id + ".go"; file != want {
			t.Errorf("entry %q lives in %s; name it %s so it can be found by name", id, file, want)
		}
	}
}
