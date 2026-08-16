package video_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenImportPrefixes = []string{
	"net/http",
	"database/sql",
	"github.com/gin-gonic",
	"github.com/golang-jwt",
	"github.com/jackc",
	"video-processor/internal/video/infrastructure",
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
		t.Fatalf("no .go files found in %q", dir)
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
			if isForbiddenImport(importPath) {
				t.Errorf("%s imports %q, which %s packages must not depend on", path, importPath, dir)
			}
		}
	}
}

func isForbiddenImport(importPath string) bool {
	for _, forbidden := range forbiddenImportPrefixes {
		if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
			return true
		}
	}

	const internalPrefix = "video-processor/internal/"
	if !strings.HasPrefix(importPath, internalPrefix) {
		return false
	}
	remainder := strings.TrimPrefix(importPath, internalPrefix)
	contextName, _, _ := strings.Cut(remainder, "/")
	return contextName != "video"
}

func TestIsForbiddenImport_DetectsAnySiblingContext(t *testing.T) {
	tests := []struct {
		importPath string
		want       bool
	}{
		{"video-processor/internal/video/domain", false},
		{"video-processor/internal/video/application", false},
		{"video-processor/internal/identity/domain", true},
		{"video-processor/internal/notification/application", true},
		{"video-processor/internal/video/infrastructure/postgres", true},
		{"net/http", true},
		{"context", false},
	}

	for _, tt := range tests {
		t.Run(tt.importPath, func(t *testing.T) {
			if got := isForbiddenImport(tt.importPath); got != tt.want {
				t.Fatalf("isForbiddenImport(%q) = %v, want %v", tt.importPath, got, tt.want)
			}
		})
	}
}
