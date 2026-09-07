package contracts_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contractsImportPath is what a package elsewhere in the repository would
// have to write to depend on this one.
const contractsImportPath = "video-processor/internal/contracts"

// TestThePackageDeclaresNothingOutsideItsTestFiles is the first half of the
// condition ddd-architecture attaches to this package's cross-context import
// permission. Without it "test-only" is a comment rather than a property, and
// the exemption would rest on nobody having added a helper here yet.
//
// The emptiness is what makes the package undependable: a package exporting
// no symbol cannot be imported for one, so there is no useful dependency to
// form. That is a stronger guarantee than a naming convention, and it costs
// nothing to hold — everything this package does lives in _test.go files.
//
// Parsed in full rather than with parser.ImportsOnly, which discards
// declarations: this check reads Decls, so the fast mode would report every
// file clean whatever it held.
func TestThePackageDeclaresNothingOutsideItsTestFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read the package directory: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", name, err)
		}
		if len(file.Decls) != 0 {
			t.Errorf("%s declares %d top-level item(s); this package may declare nothing outside its _test.go files, which is the condition its cross-context import permission rests on",
				name, len(file.Decls))
		}
	}

	// Vacuity guard. A walk finding no non-test file reports every file clean
	// while checking none, and doc.go carrying the package clause is exactly
	// the file that must be there.
	if scanned == 0 {
		t.Fatal("no non-test Go file was scanned in this package; the rule this enforces is not being checked")
	}
}

// TestNoPackageImportsThisOne is the second half of the condition, and it is
// not made redundant by the first. A package that exports nothing cannot be
// imported for a symbol, but it can still be imported blankly, and a blank
// import compiles. The spec's claim is that nothing depends on this package,
// not merely that nothing usefully can — two cheap assertions, one of them
// belt.
//
// Test files are walked too, unlike the dependency-rule walks elsewhere in
// the repository: this rule is about whether anything reaches the package at
// all, and an import in a _test.go file reaches it just as well.
func TestNoPackageImportsThisOne(t *testing.T) {
	// Relative to this package rather than to the repository root: there is
	// no TestMain here, deliberately, because the check above reads this
	// directory as ".".
	roots := []string{
		filepath.Join("..", "..", "cmd"),
		filepath.Join("..", "..", "internal"),
	}

	fset := token.NewFileSet()
	parsed := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Skipped because the rule is about every *other* package:
				// this package's own external test files are allowed to
				// name it, and one of them declares the path above.
				if filepath.Base(path) == "contracts" && filepath.Base(filepath.Dir(path)) == "internal" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			parsed++

			file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Fatalf("failed to parse %s: %v", path, parseErr)
			}
			for _, imp := range file.Imports {
				if strings.Trim(imp.Path.Value, `"`) == contractsImportPath {
					t.Errorf("%s imports %s; nothing may depend on the test-only package that holds the cross-context pins", path, contractsImportPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("failed to walk %s: %v", root, err)
		}
	}

	// Vacuity guard, for the failure this walk is most likely to have: a
	// moved or renamed tree leaves both roots empty and the test reports
	// clean.
	if parsed == 0 {
		t.Fatal("no Go file was parsed under cmd/ or internal/; this scan is passing vacuously")
	}
}
