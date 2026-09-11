package logging_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The tree this walk reads, relative to this package:
// internal/platform/logging is three levels below the repository root.
var walkRoots = []string{
	filepath.Join("..", "..", "..", "cmd"),
	filepath.Join("..", "..", "..", "internal"),
}

const (
	repositoryPrefix = ".." + string(filepath.Separator) + ".." + string(filepath.Separator) + ".." + string(filepath.Separator)

	slogImportPath = "log/slog"
	logImportPath  = "log"
	fmtImportPath  = "fmt"
	osImportPath   = "os"
	ginImportPath  = "github.com/gin-gonic/gin"
)

// messageArgument names the calls that emit a record, mapped to the index of
// their message argument. slog's package-level functions and the methods of
// *slog.Logger share these signatures, so one table covers both forms.
var messageArgument = map[string]int{
	"Debug":        0,
	"Info":         0,
	"Warn":         0,
	"Error":        0,
	"DebugContext": 1,
	"InfoContext":  1,
	"WarnContext":  1,
	"ErrorContext": 1,
	"Log":          2,
	"LogAttrs":     2,
}

// bindingCall attaches attributes to a logger for every subsequent record
// rather than emitting one. Its arguments are all attributes, so a rule
// phrased as "arguments after the message" does not reach it at all — and a
// value bound there is the wider leak of the two. WithGroup is deliberately
// absent: its signature is WithGroup(name string), so it binds no value.
const bindingCall = "With"

// The typed attribute constructors a log call may build a field from.
var permittedAttribute = map[string]bool{
	"String":   true,
	"Int":      true,
	"Int64":    true,
	"Bool":     true,
	"Duration": true,
	"Time":     true,
}

// Constructors that take a value of arbitrary type. Denied by name wherever
// they appear, not only in a receiver this walk recognized: that is what keeps
// the rule from resting on the naming of a logger variable.
var forbiddenAttribute = map[string]bool{
	"Any":        true,
	"Group":      true,
	"AnyValue":   true,
	"GroupValue": true,
}

type findings struct {
	files    int
	logCalls int

	untypedAttribute   []string
	unstructuredOutput []string
	assembledMessage   []string
}

var scanRepositoryOnce = sync.OnceValues(scanRepository)

func repositoryFindings(t *testing.T) findings {
	t.Helper()

	found, err := scanRepositoryOnce()
	if err != nil {
		t.Fatalf("%v", err)
	}
	// Vacuity guard. The walk below skips an entry that vanished under it, so
	// without this a tree that moved — or a skip that swallowed everything —
	// would report every rule clean while checking nothing.
	if found.files == 0 {
		t.Fatal("no non-test Go file was parsed under cmd/ or internal/; this walk is passing vacuously")
	}
	return found
}

func scanRepository() (findings, error) {
	found := findings{}
	fset := token.NewFileSet()

	for _, root := range walkRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// The race #251 fixed for the two existing source walkers:
				// internal/video/infrastructure/ffmpeg's tests create and
				// remove temp/<jobID> under their own package directory while
				// `go test ./...` runs this package in parallel. An entry that
				// is gone holds no log call, and the count above still fails a
				// walk that skipped too much.
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
			found.files++
			newFileScan(fset, file, &found).run()
			return nil
		})
		if err != nil {
			return findings{}, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	return found, nil
}

type fileScan struct {
	fset  *token.FileSet
	file  *ast.File
	found *findings

	// imports maps the identifier a file refers to a package by, to that
	// package's import path.
	imports map[string]string
	// loggers and notLoggers hold the identifiers and field names this file
	// declares whose type settles whether a call on them is a log call. A
	// receiver in neither set is treated as a logger: an unrecognized name
	// must fail the rule loudly rather than escape it silently.
	loggers    map[string]bool
	notLoggers map[string]bool
	// streamAliases holds the identifiers this file assigns a standard
	// stream to, so a destination written as a name is judged like the
	// selector it was assigned from.
	streamAliases map[string]bool
}

func newFileScan(fset *token.FileSet, file *ast.File, found *findings) *fileScan {
	scan := &fileScan{
		fset:          fset,
		file:          file,
		found:         found,
		imports:       map[string]string{},
		loggers:       map[string]bool{},
		notLoggers:    map[string]bool{},
		streamAliases: map[string]bool{},
	}
	scan.collectImports()
	scan.collectTypeHints()
	scan.collectStreamAliases()
	return scan
}

func (s *fileScan) collectImports() {
	for _, imp := range s.file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		s.imports[name] = path
	}
}

func (s *fileScan) collectTypeHints() {
	classify := func(names []*ast.Ident, typ ast.Expr) {
		rendered := types.ExprString(typ)
		switch {
		case strings.HasSuffix(rendered, "slog.Logger"):
			for _, name := range names {
				s.loggers[name.Name] = true
			}
		// gin.Context carries Error and Errors, and the recovery middleware
		// calls c.Error(err) on a broken connection. Without this it would be
		// read as a log call with a non-literal message.
		case strings.HasSuffix(rendered, "gin.Context"):
			for _, name := range names {
				s.notLoggers[name.Name] = true
			}
		}
	}

	ast.Inspect(s.file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.Field:
			if n.Type != nil {
				classify(n.Names, n.Type)
			}
		case *ast.ValueSpec:
			if n.Type != nil {
				classify(n.Names, n.Type)
			}
		}
		return true
	})
}

// collectStreamAliases resolves the identifiers this file assigns a standard
// stream to, so `out := os.Stdout` followed by `fmt.Fprintln(out, v)` is the
// output path it plainly is. Resolution is by provenance rather than by type:
// os.Stdout is an *os.File, a type every other file this repository opens
// shares, so type information cannot tell the standard stream from any of
// them and only the assignment that produced the value can.
//
// Collected in the constructor rather than during the walk, so a call site
// appearing before the assignment that names its destination is judged the
// same as one appearing after it.
func (s *fileScan) collectStreamAliases() {
	assigned := map[string][]ast.Expr{}
	pair := func(targets, values []ast.Expr) {
		// Unequal lengths are a multi-value call, where no value expression
		// pairs with a name.
		if len(targets) != len(values) {
			return
		}
		for i, target := range targets {
			if ident, ok := target.(*ast.Ident); ok && ident.Name != "_" {
				assigned[ident.Name] = append(assigned[ident.Name], values[i])
			}
		}
	}

	ast.Inspect(s.file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			pair(n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			if len(n.Values) == 0 {
				return true
			}
			targets := make([]ast.Expr, 0, len(n.Names))
			for _, name := range n.Names {
				targets = append(targets, name)
			}
			pair(targets, n.Values)
		}
		return true
	})

	// A name assigned a standard stream anywhere in the file counts as one
	// everywhere in it, matching this walk's posture elsewhere: a name it
	// cannot settle must fail the rule loudly rather than escape it.
	var resolves func(name string, seen map[string]bool) bool
	resolves = func(name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, value := range assigned[name] {
			switch expr := unparenthesize(value).(type) {
			case *ast.SelectorExpr:
				if s.isStandardStreamSelector(expr) {
					return true
				}
			case *ast.Ident:
				if resolves(expr.Name, seen) {
					return true
				}
			}
		}
		return false
	}
	for name := range assigned {
		if resolves(name, map[string]bool{}) {
			s.streamAliases[name] = true
		}
	}
}

func unparenthesize(expr ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = parenthesized.X
	}
}

func (s *fileScan) run() {
	ast.Inspect(s.file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			s.checkCall(call)
		}
		return true
	})
	s.checkImports()
}

// checkImports holds the first half of the no-unstructured-output rule. It is
// an import ban rather than a list of function names because six of the calls
// this change replaces are made on a *log.Logger instance, which no
// enumeration of log.Print/log.Fatal reaches, and a later log.New would
// reintroduce the whole path while such a check stayed green.
func (s *fileScan) checkImports() {
	for _, imp := range s.file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != logImportPath {
			continue
		}
		s.found.unstructuredOutput = append(s.found.unstructuredOutput,
			fmt.Sprintf("%s: imports the standard %q package", s.position(imp.Pos()), logImportPath))
	}
}

func (s *fileScan) checkCall(call *ast.CallExpr) {
	s.checkUnstructuredOutput(call)
	s.checkForbiddenAttribute(call)

	name, ok := s.logCall(call)
	if !ok {
		return
	}

	if name == bindingCall {
		for _, arg := range call.Args {
			s.checkAttribute(arg, "bound to a logger")
		}
		return
	}

	s.found.logCalls++

	index := messageArgument[name]
	if len(call.Args) <= index {
		s.found.assembledMessage = append(s.found.assembledMessage,
			fmt.Sprintf("%s: %s has no message argument", s.position(call.Pos()), name))
		return
	}
	if lit, isLiteral := call.Args[index].(*ast.BasicLit); !isLiteral || lit.Kind != token.STRING {
		s.found.assembledMessage = append(s.found.assembledMessage,
			fmt.Sprintf("%s: message is %s, not a string literal", s.position(call.Args[index].Pos()), types.ExprString(call.Args[index])))
	}
	if call.Ellipsis.IsValid() {
		s.found.untypedAttribute = append(s.found.untypedAttribute,
			fmt.Sprintf("%s: attributes are spread from a slice, so their types cannot be read here", s.position(call.Pos())))
	}
	for _, arg := range call.Args[index+1:] {
		s.checkAttribute(arg, "passed after the message")
	}
}

func (s *fileScan) checkAttribute(arg ast.Expr, where string) {
	if s.isPermittedAttribute(arg) {
		return
	}
	s.found.untypedAttribute = append(s.found.untypedAttribute,
		fmt.Sprintf("%s: %s: %s is not one of slog.String/Int/Int64/Bool/Duration/Time", s.position(arg.Pos()), where, types.ExprString(arg)))
}

func (s *fileScan) isPermittedAttribute(arg ast.Expr) bool {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return s.imports[ident.Name] == slogImportPath && permittedAttribute[sel.Sel.Name]
}

// checkForbiddenAttribute denies the arbitrary-value constructors wherever
// they are named, independently of whether the call they feed was recognized
// as a log call.
func (s *fileScan) checkForbiddenAttribute(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	if s.imports[ident.Name] != slogImportPath || !forbiddenAttribute[sel.Sel.Name] {
		return
	}
	s.found.untypedAttribute = append(s.found.untypedAttribute,
		fmt.Sprintf("%s: slog.%s takes a value of arbitrary type", s.position(call.Pos()), sel.Sel.Name))
}

func (s *fileScan) checkUnstructuredOutput(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || s.imports[ident.Name] != fmtImportPath {
		return
	}

	switch sel.Sel.Name {
	case "Print", "Printf", "Println":
		s.found.unstructuredOutput = append(s.found.unstructuredOutput,
			fmt.Sprintf("%s: fmt.%s writes to standard output", s.position(call.Pos()), sel.Sel.Name))
	case "Fprint", "Fprintf", "Fprintln":
		// The destination decides. internal/notification/infrastructure/smtp
		// assembles the mail message with fmt.Fprintf into a strings.Builder,
		// and a check on the function name alone rejects every one of those.
		if len(call.Args) == 0 || !s.isStandardStream(call.Args[0]) {
			return
		}
		s.found.unstructuredOutput = append(s.found.unstructuredOutput,
			fmt.Sprintf("%s: fmt.%s writes to %s", s.position(call.Pos()), sel.Sel.Name, types.ExprString(call.Args[0])))
	}
}

func (s *fileScan) isStandardStream(arg ast.Expr) bool {
	switch destination := unparenthesize(arg).(type) {
	case *ast.SelectorExpr:
		return s.isStandardStreamSelector(destination)
	case *ast.Ident:
		return s.streamAliases[destination.Name]
	}
	return false
}

func (s *fileScan) isStandardStreamSelector(sel *ast.SelectorExpr) bool {
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch s.imports[ident.Name] {
	case osImportPath:
		return sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr"
	case ginImportPath:
		return sel.Sel.Name == "DefaultWriter" || sel.Sel.Name == "DefaultErrorWriter"
	}
	return false
}

// logCall reports whether this call emits a record or binds attributes to a
// logger, and under which name.
func (s *fileScan) logCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name := sel.Sel.Name
	if _, emitting := messageArgument[name]; !emitting && name != bindingCall {
		return "", false
	}
	// err.Error() and its kind: a call that emits a record always carries at
	// least a message.
	if len(call.Args) == 0 {
		return "", false
	}

	switch receiver := sel.X.(type) {
	case *ast.Ident:
		if path, isPackage := s.imports[receiver.Name]; isPackage && !s.loggers[receiver.Name] {
			// A package-level function of some other package — http.Error and
			// its kind — is not a log call.
			return name, path == slogImportPath
		}
		return name, !s.notLoggers[receiver.Name]
	case *ast.SelectorExpr:
		// A field: uc.logger.Info(...).
		return name, !s.notLoggers[receiver.Sel.Name]
	default:
		// A call or an index: slog.Default().Info(...), base.With(...).Info(...).
		return name, true
	}
}

func (s *fileScan) position(pos token.Pos) string {
	position := s.fset.Position(pos)
	return fmt.Sprintf("%s:%d:%d", filepath.ToSlash(strings.TrimPrefix(position.Filename, repositoryPrefix)), position.Line, position.Column)
}

// TestNoLogCallPassesAnUntypedAttribute enforces the rule that makes passing a
// domain value to the logger structurally impossible rather than merely
// discouraged: every argument a log call passes after its message, and every
// argument to the call that binds attributes to a logger, is a typed scalar
// constructor. It rejects slog.Any, slog.Value, a LogValuer implementation,
// and slog's loosely-typed alternating key-and-value form, whose value
// position takes any type and is therefore the identical hole.
//
// It does not constrain which logging function is called: Info/Warn/Error
// accept typed attributes directly, so slog.LogAttrs is permitted rather than
// required.
func TestNoLogCallPassesAnUntypedAttribute(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.untypedAttribute {
		t.Errorf("%s", finding)
	}
}

// TestNoUnstructuredOutputPathRemains holds the other source-level scenario:
// no import of the standard log package at all, and no fmt call writing to
// standard output or standard error. fmt.Errorf and fmt.Sprintf produce values
// and are untouched.
func TestNoUnstructuredOutputPathRemains(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.unstructuredOutput {
		t.Errorf("%s", finding)
	}
}

// TestEveryLogMessageIsAStringLiteral keeps the migration from landing the
// defect it exists to remove: a formatted or concatenated message satisfies
// every rule about attributes and every call-site count while putting the
// identifier straight back inside the message.
func TestEveryLogMessageIsAStringLiteral(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.assembledMessage {
		t.Errorf("%s", finding)
	}
}

// TestTheSourceWalkReadsTheRepository reports what the three rules above were
// applied to. The recognized-call count is the reconciliation the migration
// needs: a log call made on a receiver this walk fails to recognize would
// leave all three rules quietly checking less than they claim.
func TestTheSourceWalkReadsTheRepository(t *testing.T) {
	found := repositoryFindings(t)
	t.Logf("parsed %d non-test files under cmd/ and internal/, recognizing %d emitting log call(s)", found.files, found.logCalls)
}

// scanSource applies the three rules to one synthetic file, so the walker's
// own behaviour can be exercised on the forms the repository does not contain
// yet.
func scanSource(t *testing.T, src string) findings {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	found := findings{}
	newFileScan(fset, file, &found).run()
	return found
}

// TestTheWalkJudgesEachFormItMustJudge covers the walker itself. Three of
// these are the cases where a careless implementation is wrong in the
// dangerous direction — a destination-blind fmt.Fprintf rule rejects the
// eleven calls that assemble the mail message, an alternating key-and-value
// pair is the same hole as an arbitrary-value attribute, and gin's
// c.Error(err) is not a log call. The alias cases carry the same tension
// from both sides: a destination named by an identifier still has to be
// judged by what was assigned to it, and judging it by its name alone would
// reject the builder those eleven calls write into.
func TestTheWalkJudgesEachFormItMustJudge(t *testing.T) {
	cases := []struct {
		name               string
		src                string
		untypedAttribute   int
		unstructuredOutput int
		assembledMessage   int
		logCalls           int
	}{
		{
			name: "a typed attribute passed after the message",
			src: `package p
import "log/slog"
func f(id string) { slog.Info("job accepted", slog.String("job_id", id)) }`,
			logCalls: 1,
		},
		{
			name: "the alternating key-and-value form",
			src: `package p
import "log/slog"
func f(id string) { slog.Info("job accepted", "job_id", id) }`,
			untypedAttribute: 2,
			logCalls:         1,
		},
		{
			name: "attributes bound to a logger, and a group opened on it",
			src: `package p
import "log/slog"
func f(id string) *slog.Logger { return slog.Default().With(slog.String("job_id", id)).WithGroup("lease") }`,
		},
		{
			name: "LogAttrs, which is permitted rather than required",
			src: `package p
import (
	"context"
	"log/slog"
)
func f(ctx context.Context, n int) { slog.LogAttrs(ctx, slog.LevelInfo, "frames extracted", slog.Int("frames", n)) }`,
			logCalls: 1,
		},
		{
			name: "a message assembled by formatting",
			src: `package p
import (
	"fmt"
	"log/slog"
)
func f(id string) { slog.Info(fmt.Sprintf("job %s accepted", id)) }`,
			assembledMessage: 1,
			logCalls:         1,
		},
		{
			name: "fmt.Fprintf into a builder, which is not output",
			src: `package p
import (
	"fmt"
	"strings"
)
func f(v string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "To: %s\r\n", v)
	return b.String()
}`,
		},
		{
			name: "fmt.Fprintln to standard error, which is",
			src: `package p
import (
	"fmt"
	"os"
)
func f(v string) { fmt.Fprintln(os.Stderr, v) }`,
			unstructuredOutput: 1,
		},
		{
			name: "fmt.Fprintln to a name the file assigned standard output",
			src: `package p
import (
	"fmt"
	"os"
)
func f(v string) {
	out := os.Stdout
	fmt.Fprintln(out, v)
}`,
			unstructuredOutput: 1,
		},
		{
			name: "a standard stream reached through a second name",
			src: `package p
import (
	"fmt"
	"os"
)
func f(v string) {
	var first = os.Stderr
	second := first
	fmt.Fprintf(second, "%s\n", v)
}`,
			unstructuredOutput: 1,
		},
		{
			name: "a builder reached through a name, which is still not output",
			src: `package p
import (
	"fmt"
	"strings"
)
func f(v string) string {
	var b strings.Builder
	w := &b
	fmt.Fprintf(w, "To: %s\r\n", v)
	return b.String()
}`,
		},
		{
			name: "fmt.Println",
			src: `package p
import "fmt"
func f() { fmt.Println("starting") }`,
			unstructuredOutput: 1,
		},
		{
			name: "an instance of the standard logger, which no name-based rule reaches",
			src: `package p
import "log"
func f(w *log.Logger, id string) { w.Printf("job %s accepted", id) }`,
			unstructuredOutput: 1,
		},
		{
			name: "gin's c.Error, which is not a log call",
			src: `package p
import "github.com/gin-gonic/gin"
func f(c *gin.Context, err error) { c.Error(err) }`,
		},
		{
			name: "a logger whose variable name says nothing about logging",
			src: `package p
import "log/slog"
func f(v any) {
	x := slog.Default()
	x.Info("something happened", slog.Any("value", v))
}`,
			untypedAttribute: 2,
			logCalls:         1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := scanSource(t, tc.src)
			if len(found.untypedAttribute) != tc.untypedAttribute {
				t.Errorf("untyped attributes = %v, want %d", found.untypedAttribute, tc.untypedAttribute)
			}
			if len(found.unstructuredOutput) != tc.unstructuredOutput {
				t.Errorf("unstructured output = %v, want %d", found.unstructuredOutput, tc.unstructuredOutput)
			}
			if len(found.assembledMessage) != tc.assembledMessage {
				t.Errorf("assembled messages = %v, want %d", found.assembledMessage, tc.assembledMessage)
			}
			if found.logCalls != tc.logCalls {
				t.Errorf("recognized %d emitting log call(s), want %d", found.logCalls, tc.logCalls)
			}
		})
	}
}
