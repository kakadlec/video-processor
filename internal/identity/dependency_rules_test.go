package identity_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenImportPrefixes lists import paths that the domain and
// application packages must never depend on, per design.md's package-shape
// rule (openspec/changes/archive/.../implement-identity-authentication-from-scratch):
// domain/application stay independent of HTTP, SQL drivers, and JWT
// libraries, and must not import infrastructure adapters directly.
var forbiddenImportPrefixes = []string{
	"net/http",
	"database/sql",
	"github.com/gin-gonic",
	"github.com/golang-jwt",
	"github.com/jackc",
	"video-processor/internal/identity/infrastructure",
}

func TestDomainAndApplicationPackages_DoNotImportForbiddenDependencies(t *testing.T) {
	for _, dir := range []string{"domain", "application"} {
		checkPackageDependencies(t, dir)
	}
}

func checkPackageDependencies(t *testing.T, dir string) {
	t.Helper()

	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("failed to glob %s: %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no .go files found in %q — dependency check would pass vacuously", dir)
	}

	fset := token.NewFileSet()
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImportPrefixes {
				if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
					t.Errorf("%s imports %q, which %s packages must not depend on", path, importPath, dir)
				}
			}
		}
	}
}
