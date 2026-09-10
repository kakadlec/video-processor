package logging_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"video-processor/internal/platform/logging"
)

const loggingImportPath = "video-processor/internal/platform/logging"

// TestTheCanonicalServiceSet pins the set itself: the binary names, which are
// also the deployment's own service names, with no cmd/ prefix.
func TestTheCanonicalServiceSet(t *testing.T) {
	want := []string{"identity-api", "video-api", "notification-api", "worker", "notifier"}

	if got := logging.Services(); !slices.Equal(got, want) {
		t.Errorf("Services() = %v, want %v", got, want)
	}
}

// TestEveryCompositionRootNamesItsCanonicalService is the other half: a set
// nothing refers to would let a root spell its own service name and drift from
// the spec's scenarios, the output-shape test, and the filters an operator
// writes.
func TestEveryCompositionRootNamesItsCanonicalService(t *testing.T) {
	roots := map[string]string{
		"identity-api":     "ServiceIdentityAPI",
		"video-api":        "ServiceVideoAPI",
		"notification-api": "ServiceNotificationAPI",
		"worker":           "ServiceWorker",
		"notifier":         "ServiceNotifier",
	}

	for root, constant := range roots {
		dir := filepath.Join("..", "..", "..", "cmd", root)
		if !rootNamesConstant(t, dir, constant) {
			t.Errorf("no non-test source under cmd/%s names logging.%s; the service value comes from the canonical set and nowhere else", root, constant)
		}
	}
}

func rootNamesConstant(t *testing.T, dir, constant string) bool {
	t.Helper()

	fset := token.NewFileSet()
	parsed, found := 0, false

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Same skip as the source walk: an entry that vanished under the
			// walk names no constant, and the parsed count below still fails a
			// walk that read nothing.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		parsed++

		local := ""
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) != loggingImportPath {
				continue
			}
			local = "logging"
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
		if local == "" {
			return nil
		}

		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != constant {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == local {
				found = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if parsed == 0 {
		t.Fatalf("no non-test Go file was parsed under %s; this check is passing vacuously", dir)
	}
	return found
}
