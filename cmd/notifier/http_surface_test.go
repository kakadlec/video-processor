package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// httpFrameworkImportPrefixes lists the import paths that carry a router and
// its listener. gin is the only HTTP framework in this module's graph, so it
// is the only one a change could reach for without also editing go.mod --
// which is a diff a reviewer sees. The prefix form is alias-immune, unlike
// the selector rule below.
var httpFrameworkImportPrefixes = []string{"github.com/gin-gonic"}

// serverNamesByImportPath holds, per standard-library package, the exported
// names that construct or run an HTTP server. Everything else in net/http
// stays allowed on purpose -- see the note on http.Client below.
var serverNamesByImportPath = map[string]map[string]bool{
	"net/http": {
		"Server":            true,
		"ListenAndServe":    true,
		"ListenAndServeTLS": true,
		"Serve":             true,
		"ServeTLS":          true,
		"ServeMux":          true,
		"NewServeMux":       true,
		"DefaultServeMux":   true,
		"Handle":            true,
		"HandleFunc":        true,
		"FileServer":        true,
	},
	"net/http/httptest": {
		"NewServer":          true,
		"NewUnstartedServer": true,
		"NewTLSServer":       true,
	},
}

// The notifier serves nothing, by requirement rather than by omission:
// container-image says the worker and the notifier SHALL each expose no port
// at all, which is why add-health-and-readiness-endpoints gave /health and
// /ready to the three HTTP services and to neither of these two.
//
// This test is deliberately stronger than that requirement, rather than a
// mis-transcription of it. Exposing no port is observable at run time and
// only of the process as configured; constructing no server and importing no
// HTTP framework is a claim about source, which no behavioural test can hold
// -- a listener wired here would pass every other test in this package.
//
// It does not ban net/http outright. An http.Client is not an http.Server:
// the webhook deliverer's client lives in the Notification context's own
// infrastructure, not in this root, so no client is built here today -- but
// a blanket import ban would also forbid the day one belongs here, and would
// then be weakened to allow it, which is the same as not having the test at
// all.
// What that costs is that a raw net.Listen is out of scope; this pins the
// HTTP half of "no port", which is the half a copy from one of the three API
// composition roots would introduce.
//
// Same idiom as TestOnlyTheIdentityServiceConstructsATokenIssuer, and one
// deliberate copy per package rather than one cross-root scan: package main
// cannot import package main, and the failure belongs in the package that
// introduced the listener.
func TestTheNotifierConstructsNoHTTPServer(t *testing.T) {
	findings, parsed := scanForAnHTTPServerSurface(t, thisPackageDir(t))

	// Vacuity guards. The walk skips an entry that vanished under it, and it
	// resolves its own root, so without these a root that no longer exists
	// would report the rule clean while checking nothing.
	if len(parsed) == 0 {
		t.Fatal("no non-test Go file was parsed under cmd/notifier; this scan is passing vacuously")
	}
	if !slices.Contains(parsed, "main.go") {
		t.Fatalf("parsed %v, which does not include main.go: this scan walked a tree other than cmd/notifier", parsed)
	}

	for _, finding := range findings {
		t.Errorf("%s: cmd/notifier must construct no HTTP server and import no HTTP framework", finding)
	}
}

// thisPackageDir resolves the directory holding this test's own source file,
// which is this package's directory by construction. The working directory
// is deliberately not used: this package has no TestMain today, but
// cmd/worker's chdirs to the repository root so its tests resolve temp/
// where the binary does, and a walk root taken from the working directory
// would there cover the whole repository -- every API composition root
// included -- reporting findings belonging to somebody else. The two copies
// resolve their root the same way so they cannot drift into disagreeing
// about what they scan.
func thisPackageDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve this test file's own path; the scan has no root to walk")
	}
	return filepath.Dir(file)
}

// scanForAnHTTPServerSurface parses every non-test Go file under dir and
// returns one finding per denied construction, each positioned at the file
// and line that holds it, alongside the base names of the files it parsed.
func scanForAnHTTPServerSurface(t *testing.T, dir string) (findings, parsed []string) {
	t.Helper()

	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The hardening PR #251 applied to the two existing source
			// walkers: internal/video/infrastructure/ffmpeg's tests create
			// and delete directories inside the repository tree while
			// `go test ./...` runs packages in parallel, and a walker that
			// returned this error verbatim flaked. The root below is this
			// package's own directory, and cmd/notifier writes nothing to
			// disk at all, so the skip is a standing rule rather than a fix
			// for a race this tree has. An entry that vanished under the
			// walk holds no server construction,
			// and the guards above still fail a walk that skipped too much.
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
			if errors.Is(parseErr, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		parsed = append(parsed, filepath.Base(path))
		findings = append(findings, httpServerSurfaceIn(fset, file)...)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk %s: %v", dir, err)
	}
	return findings, parsed
}

func httpServerSurfaceIn(fset *token.FileSet, file *ast.File) []string {
	findings := make([]string, 0)

	// deniedByIdentifier maps the identifier this file refers to a scanned
	// package by -- its alias where it has one -- to that package's denied
	// names, so an aliased import is judged like a plain one.
	deniedByIdentifier := make(map[string]map[string]bool)
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		for _, prefix := range httpFrameworkImportPrefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				findings = append(findings, fmt.Sprintf("%s imports %s, an HTTP framework", fset.Position(spec.Pos()), path))
			}
		}

		denied, scanned := serverNamesByImportPath[path]
		if !scanned {
			continue
		}
		identifier := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			identifier = spec.Name.Name
		}
		switch identifier {
		case "_":
			// A blank import runs init and names nothing.
		case ".":
			// A dot import puts the denied names in file scope, where the
			// selector walk below would never see them.
			findings = append(findings, fmt.Sprintf("%s dot-imports %s", fset.Position(spec.Pos()), path))
		default:
			deniedByIdentifier[identifier] = denied
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if deniedByIdentifier[identifier.Name][selector.Sel.Name] {
			findings = append(findings, fmt.Sprintf("%s names %s.%s", fset.Position(selector.Sel.Pos()), identifier.Name, selector.Sel.Name))
		}
		return true
	})

	return findings
}
