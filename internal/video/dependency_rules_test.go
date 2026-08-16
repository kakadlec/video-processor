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

const internalPackagePrefix = "video-processor/internal/"

func TestDomainAndApplicationPackages_DoNotImportForbiddenDependencies(t *testing.T) {
	for _, directory := range []string{"domain", "application"} {
		checkPackageDependencies(t, directory)
	}
}

func TestIsForbiddenImport_DetectsCrossContextImportsGenerically(t *testing.T) {
	tests := []struct {
		importPath string
		want       bool
	}{
		{"video-processor/internal/video/domain", false},
		{"video-processor/internal/video/application", false},
		{"video-processor/internal/identity/domain", true},
		{"video-processor/internal/notification/application", true},
		{"video-processor/internal/future-context", true},
		{"time", false},
	}
	for _, test := range tests {
		t.Run(test.importPath, func(t *testing.T) {
			if got := isForbiddenImport(test.importPath); got != test.want {
				t.Fatalf("isForbiddenImport(%q) = %v, want %v", test.importPath, got, test.want)
			}
		})
	}
}

func checkPackageDependencies(t *testing.T, directory string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatalf("failed to glob %s: %v", directory, err)
	}
	if len(files) == 0 {
		t.Fatalf("no .go files found in %q; dependency check would pass vacuously", directory)
	}

	fileSet := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}
		for _, imported := range file.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if isForbiddenImport(importPath) {
				t.Errorf("%s imports %q, which %s packages must not depend on", path, importPath, directory)
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
	if !strings.HasPrefix(importPath, internalPackagePrefix) {
		return false
	}
	relativePath := strings.TrimPrefix(importPath, internalPackagePrefix)
	contextName, _, _ := strings.Cut(relativePath, "/")
	return contextName != "video"
}
