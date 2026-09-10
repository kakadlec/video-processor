package logging_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePrefix   = "video-processor/internal/"
	platformPrefix = "video-processor/internal/platform/"
)

// TestPackageImportsNoBoundedContext enforces the placement rule this package
// exists under: internal/platform holds cross-cutting plumbing that no bounded
// context owns.
//
// A copy of the test internal/platform/rabbitmq carries rather than a shared
// helper: it parses ".", so it is per-directory, and a new platform package is
// unenforced until it carries its own. This is exactly the package where an
// internal/notification/... import would be tempting, since the values whose
// disclosure the change forecloses live there.
func TestPackageImportsNoBoundedContext(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				target, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: unquote import %s: %v", path, imp.Path.Value, err)
				}
				if !strings.HasPrefix(target, modulePrefix) {
					continue
				}
				if !strings.HasPrefix(target, platformPrefix) {
					t.Errorf("%s imports %s: internal/platform may not import a bounded context", path, target)
				}
			}
		}
	}
}

// TestPackageNamesNoContextEntities guards the other half of the same rule:
// the split that keeps this package generic is undone just as effectively by
// moving a name back in as by adding an import.
func TestPackageNamesNoContextEntities(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	forbidden := []string{"video.jobs", "video_job."}
	for _, pkg := range pkgs {
		for path := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, name := range forbidden {
				if strings.Contains(string(src), name) {
					t.Errorf("%s contains %q: bounded-context names belong in that context, not in internal/platform", path, name)
				}
			}
		}
	}
}

// TestPackageImportsNoHTTPFramework holds the other placement claim decision 4
// makes about this package specifically: the gin access-log middleware is one
// thin copy per HTTP composition root, and nothing under internal/ imports the
// framework today.
func TestPackageImportsNoHTTPFramework(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				target, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: unquote import %s: %v", path, imp.Path.Value, err)
				}
				if strings.Contains(target, "gin-gonic/gin") {
					t.Errorf("%s imports %s: the HTTP framework belongs in the composition roots, not in internal/platform/logging", path, target)
				}
			}
		}
	}
}
