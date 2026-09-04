package notification_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenImportPrefixes lists import paths that domain and application
// packages must never depend on, per design.md's package-shape rule.
var forbiddenImportPrefixes = []string{
	"net/http",
	"database/sql",
	"github.com/gin-gonic",
	"github.com/golang-jwt",
	"github.com/jackc",
}

// ownContext is this bounded context's own directory name under internal/,
// used to distinguish "internal/notification/infrastructure" (forbidden for
// domain/application) from a sibling context's packages (also forbidden,
// detected generically below rather than via a hardcoded entry per context).
//
// The generic rule is what enforces this context's defining constraint:
// Notification may not import internal/video to learn what an event type is
// called, nor internal/identity for a UserID, which is why it declares both
// itself. A build that broke that rule should fail here.
const ownContext = "notification"

const internalPrefix = "video-processor/internal/"

func TestDomainAndApplicationPackages_DoNotImportForbiddenDependencies(t *testing.T) {
	// "application" joins this list when the use cases land; the package
	// does not exist yet and checkPackageDependencies deliberately fails on
	// a directory holding no Go files rather than passing vacuously.
	for _, dir := range []string{"domain"} {
		checkPackageDependencies(t, dir)
	}
}

func checkPackageDependencies(t *testing.T, dir string) {
	t.Helper()

	var entries []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk %s: %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no .go files found under %q — dependency check would pass vacuously", dir)
	}

	fset := token.NewFileSet()
	for _, path := range entries {
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if isForbiddenImport(importPath) {
				t.Errorf("%s imports %q, which %s packages must not depend on", path, importPath, dir)
			}
		}
	}
}

// isForbiddenImport is factored out of checkPackageDependencies so the
// generic cross-context detection (any video-processor/internal/<other>/...
// path where <other> isn't "notification") can be unit tested directly — its
// failure mode is silent (a bug just means the check passes vacuously
// forever), so TestIsForbiddenImport_DetectsAnySiblingContext below exercises
// it without needing a throwaway offending import file.
func isForbiddenImport(importPath string) bool {
	for _, forbidden := range forbiddenImportPrefixes {
		if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
			return true
		}
	}

	rest, ok := strings.CutPrefix(importPath, internalPrefix)
	if !ok {
		return false
	}
	context, _, _ := strings.Cut(rest, "/")
	if context != ownContext {
		return true
	}
	return strings.HasPrefix(rest, ownContext+"/infrastructure")
}

func TestIsForbiddenImport_DetectsAnySiblingContext(t *testing.T) {
	tests := []struct {
		importPath string
		want       bool
	}{
		{"video-processor/internal/notification/domain", false},
		{"video-processor/internal/notification/application", false},
		{"video-processor/internal/video/domain", true},
		{"video-processor/internal/video/infrastructure/postgres", true},
		{"video-processor/internal/identity/domain", true},
		{"video-processor/internal/identity/application", true},
		{"video-processor/internal/notification/infrastructure", true},
		{"video-processor/internal/notification/infrastructure/postgres", true},
		{"net/http", true},
		{"database/sql", true},
		{"context", false},
		{"time", false},
		{"net/url", false},
	}

	for _, tt := range tests {
		t.Run(tt.importPath, func(t *testing.T) {
			if got := isForbiddenImport(tt.importPath); got != tt.want {
				t.Fatalf("isForbiddenImport(%q) = %v, want %v", tt.importPath, got, tt.want)
			}
		})
	}
}
