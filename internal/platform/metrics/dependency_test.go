package metrics_test

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
// A copy of the test internal/platform/logging and internal/platform/rabbitmq
// carry rather than a shared helper: each parses ".", so it is per-directory,
// and a new platform package is unenforced until it carries its own. The
// disclosure walk beside this file enforces nothing of the kind — it judges
// call sites, wherever they are, and has no opinion about where a family is
// declared.
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

// TestPackageNamesNoContextEntities guards the other half of the same rule,
// and here it is the half that does the work: a metric family is declared in
// the package that records it, so a fiapx_video_jobs_… family declared in
// this package is the mistake this test exists to catch. It costs no import
// to make — a family is a string — which is exactly why the name rule is
// needed beside the import one.
func TestPackageNamesNoContextEntities(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	forbidden := []string{"video.jobs", "video_job", "notification_", "identity_"}
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

// TestPackageImportsNoHTTPFramework holds the placement claim this package
// makes about the route table specifically: it crosses the boundary as a
// method and a path per entry, in a representation declared here, rather than
// as the framework's own route type. Taking that type would put the first
// import of the framework under internal/ into the shared package, for a
// value that is two strings.
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
					t.Errorf("%s imports %s: the HTTP framework belongs in the composition roots, not in internal/platform/metrics", path, target)
				}
			}
		}
	}
}
