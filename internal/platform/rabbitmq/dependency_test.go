package rabbitmq_test

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
// exists under: internal/platform holds connection/lifecycle plumbing that no
// bounded context owns.
//
// Written as an allow-list on internal/platform/ rather than as a deny-list
// naming identity and video: ddd-architecture already names a Notification
// context for Phase 7, and a deny-list would silently permit importing it the
// day internal/notification/ appears.
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
			src, err := readFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, name := range forbidden {
				if strings.Contains(src, name) {
					t.Errorf("%s contains %q: bounded-context names belong in that context, not in internal/platform", path, name)
				}
			}
		}
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
