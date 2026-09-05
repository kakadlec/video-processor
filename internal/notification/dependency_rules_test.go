package notification_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// forbiddenImportPrefixes lists import paths that carry a delivery mechanism
// into a package that should not know one exists. They are forbidden in
// domain and application packages, per design.md's package-shape rule, and
// deliberately allowed in infrastructure — an adapter's whole job is to name
// one of these.
var forbiddenImportPrefixes = []string{
	"net/http",
	"database/sql",
	"github.com/gin-gonic",
	"github.com/golang-jwt",
	"github.com/jackc",
}

// ownContext is this bounded context's own directory name under internal/,
// used to distinguish "internal/notification/infrastructure" (forbidden for
// domain/application) from a sibling context's packages (forbidden for
// every package here, detected generically below rather than via a
// hardcoded entry per context).
//
// The generic rule is what enforces this context's defining constraint:
// Notification may not import internal/video to learn what an event type is
// called, nor internal/identity for a UserID, which is why it declares both
// itself. A build that broke that rule should fail here.
const ownContext = "notification"

const internalPrefix = "video-processor/internal/"

// infrastructureDir is the one subtree where the delivery-mechanism
// allowances apply.
const infrastructureDir = "infrastructure"

// platformContext is internal/platform, which the generic sibling-context
// rule would otherwise refuse. It is not a bounded context: it holds the
// connection and lifecycle plumbing every context dials through, and its own
// tests forbid it containing a context name, so importing it cannot couple
// Notification to Video Processing. Only an infrastructure package may reach
// it — that is where a connection belongs, and domain and application are
// still held to naming no transport at all.
const platformContext = "platform"

// TestEveryPackage_DoesNotImportForbiddenDependencies walks the whole
// context rather than only domain and application.
//
// The widening is not tidiness. The cross-context prohibition is what makes
// this context declare its own event-type constants, its own UserID, and —
// as of add-notification-webhook-delivery — its own copy of the terminal
// wire contract. An infrastructure package is exactly where importing
// internal/video would be tempting, since it would delete a duplication the
// design deliberately keeps, so the rule has to reach there too.
func TestEveryPackage_DoesNotImportForbiddenDependencies(t *testing.T) {
	files, err := goFilesByPackageDir(".")
	if err != nil {
		t.Fatalf("failed to walk the context: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no packages found under the notification context — dependency check would pass vacuously")
	}

	dirs := make([]string, 0, len(files))
	for dir := range files {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)

	// Vacuity guards, one per class of package the rules distinguish. Without
	// them a refactor that renamed or moved a subtree would leave this test
	// passing while checking nothing it was written to check — the same
	// failure mode the file-count guard below already covers for one
	// directory.
	for _, required := range []string{"domain", "application"} {
		if !slices.Contains(dirs, required) {
			t.Fatalf("no %q package was visited; the rule it is meant to enforce is not being checked", required)
		}
	}
	if !slices.ContainsFunc(dirs, isInfrastructurePackage) {
		t.Fatal("no infrastructure package was visited; the allowances below are not being exercised")
	}

	fset := token.NewFileSet()
	for _, dir := range dirs {
		infrastructure := isInfrastructurePackage(dir)
		for _, path := range files[dir] {
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", path, err)
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if isForbiddenImport(importPath, infrastructure) {
					t.Errorf("%s imports %q, which %s packages must not depend on", path, importPath, dir)
				}
			}
		}
	}
}

// isInfrastructurePackage reports whether a package directory, relative to
// the context root, is under the infrastructure subtree.
func isInfrastructurePackage(dir string) bool {
	return dir == infrastructureDir || strings.HasPrefix(dir, infrastructureDir+"/")
}

// goFilesByPackageDir collects every non-test .go file under root, keyed by
// the directory it lives in. Directories holding no such file are absent
// rather than present and empty, so the context root — which holds only this
// test — does not appear.
func goFilesByPackageDir(root string) (map[string][]string, error) {
	files := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		files[dir] = append(files[dir], path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for dir, paths := range files {
		if len(paths) == 0 {
			delete(files, dir)
		}
	}
	return files, nil
}

// isForbiddenImport is factored out of the walk so the generic cross-context
// detection (any video-processor/internal/<other>/... path where <other>
// isn't "notification") can be unit tested directly — its failure mode is
// silent (a bug just means the check passes vacuously forever), so
// TestIsForbiddenImport_DetectsAnySiblingContext below exercises it without
// needing a throwaway offending import file.
//
// infrastructure relaxes exactly three things and nothing else: the
// delivery-mechanism prefixes, importing another package of this context's
// own infrastructure, and internal/platform. The cross-context prohibition
// is not relaxed, because it is the one rule an adapter has a motive to
// break.
func isForbiddenImport(importPath string, infrastructure bool) bool {
	if !infrastructure {
		for _, forbidden := range forbiddenImportPrefixes {
			if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
				return true
			}
		}
	}

	rest, ok := strings.CutPrefix(importPath, internalPrefix)
	if !ok {
		return false
	}
	context, _, _ := strings.Cut(rest, "/")
	if context == platformContext {
		return !infrastructure
	}
	if context != ownContext {
		return true
	}
	if infrastructure {
		return false
	}
	return strings.HasPrefix(rest, ownContext+"/"+infrastructureDir)
}

func TestIsForbiddenImport_DetectsAnySiblingContext(t *testing.T) {
	tests := []struct {
		importPath     string
		infrastructure bool
		want           bool
	}{
		{"video-processor/internal/notification/domain", false, false},
		{"video-processor/internal/notification/application", false, false},
		{"video-processor/internal/video/domain", false, true},
		{"video-processor/internal/video/infrastructure/postgres", false, true},
		{"video-processor/internal/identity/domain", false, true},
		{"video-processor/internal/identity/application", false, true},
		{"video-processor/internal/notification/infrastructure", false, true},
		{"video-processor/internal/notification/infrastructure/postgres", false, true},
		{"net/http", false, true},
		{"database/sql", false, true},
		{"context", false, false},
		{"time", false, false},
		{"net/url", false, false},
		{"video-processor/internal/platform/rabbitmq", false, true},

		// The same paths judged as an infrastructure package. The delivery
		// mechanisms and this context's own infrastructure become legal; a
		// sibling context does not.
		{"net/http", true, false},
		{"database/sql", true, false},
		{"github.com/jackc/pgx/v5/stdlib", true, false},
		{"video-processor/internal/notification/domain", true, false},
		{"video-processor/internal/notification/infrastructure/postgres", true, false},
		{"video-processor/internal/platform/rabbitmq", true, false},
		{"video-processor/internal/video/infrastructure/messaging", true, true},
		{"video-processor/internal/video/domain", true, true},
		{"video-processor/internal/identity/domain", true, true},
	}

	for _, tt := range tests {
		name := tt.importPath
		if tt.infrastructure {
			name += " (infrastructure)"
		}
		t.Run(name, func(t *testing.T) {
			if got := isForbiddenImport(tt.importPath, tt.infrastructure); got != tt.want {
				t.Fatalf("isForbiddenImport(%q, %v) = %v, want %v", tt.importPath, tt.infrastructure, got, tt.want)
			}
		})
	}
}

func TestIsInfrastructurePackage(t *testing.T) {
	tests := []struct {
		dir  string
		want bool
	}{
		{"domain", false},
		{"application", false},
		{"infrastructure", true},
		{"infrastructure/postgres", true},
		{"infrastructure/webhook", true},
		{"infrastructurely", false},
	}

	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			if got := isInfrastructurePackage(tt.dir); got != tt.want {
				t.Fatalf("isInfrastructurePackage(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}
