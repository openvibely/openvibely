package teststores_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Shared test helpers are ordinary Go packages so other packages' tests can
// import them. Keep those helpers out of the application's import graph.
func TestApplicationDoesNotImportTestHelpers(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path == filepath.Join(root, "internal", "testutil") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				const helpers = "github.com/openvibely/openvibely/internal/testutil"
				if imported == helpers || strings.HasPrefix(imported, helpers+"/") {
					t.Errorf("application file %s imports test helper %s", path, imported)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Application tests must select stores through teststores instead of importing
// an engine or reaching for the legacy connection-based test helpers.
func TestApplicationTestsUsePersistenceBoundary(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path == filepath.Join(root, "internal", "testutil") || path == filepath.Join(root, "internal", "persistence", "sqlite") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			aliases := map[string]bool{}
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if imported == "database/sql" || strings.HasPrefix(imported, "github.com/openvibely/openvibely/internal/persistence/sqlite") || imported == "github.com/openvibely/openvibely/internal/testutil/sqlitefixtures" {
					t.Errorf("application test %s imports backend %s", path, imported)
				}
				if imported == "github.com/openvibely/openvibely/internal/testutil" {
					name := "testutil"
					if spec.Name != nil {
						name = spec.Name.Name
					}
					aliases[name] = true
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if !ok || !aliases[qualifier.Name] {
					return true
				}
				if strings.Contains(selector.Sel.Name, "DB") || strings.Contains(selector.Sel.Name, "StatementCounting") {
					t.Errorf("application test %s uses raw backend helper %s", path, selector.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
