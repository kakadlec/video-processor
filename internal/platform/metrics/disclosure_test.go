package metrics_test

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
// internal/platform/metrics is three levels below the repository root.
var walkRoots = []string{
	filepath.Join("..", "..", "..", "cmd"),
	filepath.Join("..", "..", "..", "internal"),
}

const (
	repositoryPrefix = ".." + string(filepath.Separator) + ".." + string(filepath.Separator) + ".." + string(filepath.Separator)

	prometheusImportPath = "github.com/prometheus/client_golang/prometheus"
	promautoImportPath   = "github.com/prometheus/client_golang/prometheus/promauto"
	metricsImportPath    = "video-processor/internal/platform/metrics"
)

// The two total label constructors. Each returns a member of a bounded set
// for every input, including inputs it does not recognize, which is the
// property that makes them admissible where a rendered value is not. Named
// here rather than described, so the permission is a closed set the way
// logging's slog.String/Int/… allow-list is.
var permittedConstructor = map[string]bool{
	"Route":  true,
	"Method": true,
}

// The vector constructors, whose results are the receivers a With call has to
// be judged on, and whose second argument is the label-name list rule 2
// covers.
var vectorConstructor = map[string]bool{
	"NewCounterVec":   true,
	"NewGaugeVec":     true,
	"NewHistogramVec": true,
	"NewSummaryVec":   true,
}

// The declared vector types, for a receiver written as a field or a typed
// variable rather than assigned from a constructor.
var vectorType = map[string]bool{
	"CounterVec":   true,
	"GaugeVec":     true,
	"HistogramVec": true,
	"SummaryVec":   true,
}

// The methods a metric vector carries that provably accept no label value.
// Every other method on a settled vector receiver is judged, so the whole
// label-accepting surface — WithLabelValues, With, GetMetricWith,
// GetMetricWithLabelValues, CurryWith, MustCurryWith, Delete,
// DeleteLabelValues, DeletePartialMatch — is covered by construction rather
// than by an enumeration that a later release can outgrow.
var vectorMethodWithoutLabels = map[string]bool{
	"Describe": true,
	"Collect":  true,
	"Reset":    true,
}

// The const-metric constructors, mapped to the number of leading arguments
// before their trailing label values. A NewConst… name absent from this table
// fails outright rather than passing: the arity is what says where the labels
// start, and a constructor whose arity this walk does not know is one whose
// labels it cannot find.
var constMetricLabelOffset = map[string]int{
	"NewConstMetric":                            3,
	"MustNewConstMetric":                        3,
	"NewConstMetricWithCreatedTimestamp":        4,
	"MustNewConstMetricWithCreatedTimestamp":    4,
	"NewConstHistogram":                         4,
	"MustNewConstHistogram":                     4,
	"NewConstHistogramWithCreatedTimestamp":     5,
	"MustNewConstHistogramWithCreatedTimestamp": 5,
	"NewConstSummary":                           4,
	"MustNewConstSummary":                       4,
	"NewConstSummaryWithCreatedTimestamp":       5,
	"MustNewConstSummaryWithCreatedTimestamp":   5,
}

// The options structs whose Name and Help are a series' identity. Every
// scalar field of one is held to rule 2.
var optionsType = map[string]bool{
	"CounterOpts":   true,
	"GaugeOpts":     true,
	"HistogramOpts": true,
	"SummaryOpts":   true,
	"Opts":          true,
}

// The fields of those structs that name a series. ConstLabels is absent: it
// is a prometheus.Labels value, and the rule that judges those reaches it
// wherever it is written.
var nameField = map[string]bool{
	"Name":      true,
	"Help":      true,
	"Namespace": true,
	"Subsystem": true,
}

// The default-registry names, denied by selector on the client library's own
// package. Matching the bare name would fire on registry.MustRegister, which
// is the explicit registration this rule exists to require.
var defaultRegistryName = map[string]bool{
	"DefaultRegisterer": true,
	"DefaultGatherer":   true,
	"Register":          true,
	"MustRegister":      true,
}

// labelsTypeName is the one type whose every appearance is judged: as a
// composite literal (whose keys and values are then held to rule 1), and
// anywhere else at all — a conversion, a declared variable, a parameter, a
// result — which fails, because a Labels value this walk did not see built
// carries label names and values it cannot read.
const labelsTypeName = "Labels"

type findings struct {
	files      int
	labelSites int

	unboundedLabel  []string
	assembledName   []string
	defaultRegistry []string
}

var scanRepositoryOnce = sync.OnceValues(scanRepository)

func repositoryFindings(t *testing.T) findings {
	t.Helper()

	found, err := scanRepositoryOnce()
	if err != nil {
		t.Fatalf("%v", err)
	}
	// Vacuity guard, carried over from the logging walk rather than
	// rediscovered. The walk below skips an entry that vanished under it, so
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
				// The race #251 fixed for the existing source walkers:
				// internal/video/infrastructure/ffmpeg's tests create and
				// remove temp/<jobID> under their own package directory while
				// `go test ./...` runs this package in parallel. An entry that
				// is gone holds no metric call, and the count above still
				// fails a walk that skipped too much.
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
	// vectors holds the identifiers and field names this file settles as
	// metric vectors, so a With call on one is judged as a label site while
	// the logger's With, which shares the name and is everywhere, is not.
	vectors map[string]bool
	// labelsLiteral holds the positions at which prometheus.Labels is named
	// as a composite literal's type, so every other appearance of that type
	// can be failed without failing the one admissible form.
	labelsLiteral map[token.Pos]bool
}

func newFileScan(fset *token.FileSet, file *ast.File, found *findings) *fileScan {
	scan := &fileScan{
		fset:          fset,
		file:          file,
		found:         found,
		imports:       map[string]string{},
		vectors:       map[string]bool{},
		labelsLiteral: map[token.Pos]bool{},
	}
	scan.collectImports()
	scan.collectVectors()
	scan.collectLabelsLiterals()
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

// collectVectors settles which names in this file are metric vectors, by
// declared type and by what they were assigned. Provenance is needed as well
// as the type, because the ordinary declaration of a family carries no type
// at all: var x = prometheus.NewCounterVec(…).
//
// Collected in the constructor rather than during the walk, so a call site
// appearing before the declaration that settles its receiver is judged the
// same as one appearing after it.
func (s *fileScan) collectVectors() {
	assigned := map[string][]ast.Expr{}

	pair := func(targets, values []ast.Expr) {
		if len(targets) != len(values) {
			return
		}
		for i, target := range targets {
			if ident, ok := target.(*ast.Ident); ok && ident.Name != "_" {
				assigned[ident.Name] = append(assigned[ident.Name], values[i])
			}
		}
	}

	declare := func(names []*ast.Ident, typ ast.Expr) {
		name, ok := s.prometheusTypeName(typ)
		if !ok || !vectorType[name] {
			return
		}
		for _, ident := range names {
			s.vectors[ident.Name] = true
		}
	}

	ast.Inspect(s.file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.Field:
			if n.Type != nil {
				declare(n.Names, n.Type)
			}
		case *ast.ValueSpec:
			if n.Type != nil {
				declare(n.Names, n.Type)
			}
			if len(n.Values) > 0 {
				targets := make([]ast.Expr, 0, len(n.Names))
				for _, name := range n.Names {
					targets = append(targets, name)
				}
				pair(targets, n.Values)
			}
		case *ast.AssignStmt:
			pair(n.Lhs, n.Rhs)
		}
		return true
	})

	var resolves func(name string, seen map[string]bool) bool
	resolves = func(name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, value := range assigned[name] {
			switch expr := unparenthesize(value).(type) {
			case *ast.CallExpr:
				if s.isPrometheusCall(expr, vectorConstructor) {
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
			s.vectors[name] = true
		}
	}
}

func (s *fileScan) collectLabelsLiterals() {
	ast.Inspect(s.file, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok || lit.Type == nil {
			return true
		}
		if name, isPrometheus := s.prometheusTypeName(lit.Type); isPrometheus && name == labelsTypeName {
			s.labelsLiteral[lit.Type.Pos()] = true
		}
		return true
	})
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
		switch n := node.(type) {
		case *ast.CallExpr:
			s.checkCall(n)
		case *ast.CompositeLit:
			s.checkCompositeLit(n)
		case *ast.SelectorExpr:
			s.checkLabelsType(n)
			s.checkDefaultRegistry(n)
		}
		return true
	})
	s.checkImports()
}

// checkImports holds half of rule 3. An import ban is needed as well as the
// name ban below, because promauto registers into the default registry
// without naming it: promauto.NewCounterVec(…) is a complete bypass of every
// check written against prometheus.MustRegister.
func (s *fileScan) checkImports() {
	for _, imp := range s.file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != promautoImportPath {
			continue
		}
		s.found.defaultRegistry = append(s.found.defaultRegistry,
			fmt.Sprintf("%s: imports %q, which registers into the library's default registry", s.position(imp.Pos()), promautoImportPath))
	}
}

func (s *fileScan) checkCall(call *ast.CallExpr) {
	s.checkDeclaration(call)
	s.checkLabelSite(call)
}

// checkDefaultRegistry holds the other half of rule 3, qualified on the
// client library's own package: a bare name check would fire on
// registry.MustRegister, which is the explicit registration this rule exists
// to require rather than to forbid.
//
// It judges the name wherever it is written rather than only where it is
// called, because prometheus.DefaultRegisterer is a value: reaching the
// default registry through a method on it names no forbidden function at all.
func (s *fileScan) checkDefaultRegistry(sel *ast.SelectorExpr) {
	if !s.isPrometheusSelector(sel) || !defaultRegistryName[sel.Sel.Name] {
		return
	}
	s.found.defaultRegistry = append(s.found.defaultRegistry,
		fmt.Sprintf("%s: prometheus.%s reaches the library's default registry", s.position(sel.Pos()), sel.Sel.Name))
}

// checkDeclaration holds rule 2 at the call sites that declare a series: the
// vector constructors, whose second argument is the label-name list, and
// NewDesc, whose first three arguments are a name, a help string and a
// label-name list.
func (s *fileScan) checkDeclaration(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !s.isPrometheusSelector(sel) {
		return
	}

	switch {
	case vectorConstructor[sel.Sel.Name]:
		if len(call.Args) > 0 {
			s.checkOptions(call.Args[0])
		}
		if len(call.Args) > 1 {
			s.checkNameList(call.Args[1])
		}
	case sel.Sel.Name == "NewDesc":
		for _, arg := range call.Args[:min(2, len(call.Args))] {
			s.checkName(arg, "a metric name or help string")
		}
		if len(call.Args) > 2 {
			s.checkNameList(call.Args[2])
		}
	}
}

// checkOptions judges the options value a vector constructor is handed. An
// options struct assembled anywhere but here is one whose Name this walk
// never reads, which is the same defect as a label-name list that is not a
// literal — and it fails loudly for the same reason, rather than being
// quietly unchecked.
func (s *fileScan) checkOptions(arg ast.Expr) {
	lit, ok := unparenthesize(arg).(*ast.CompositeLit)
	if ok && lit.Type != nil {
		if name, isPrometheus := s.prometheusTypeName(lit.Type); isPrometheus && optionsType[name] {
			return
		}
	}
	s.found.assembledName = append(s.found.assembledName,
		fmt.Sprintf("%s: the options are %s, not a literal this walk can read the metric name out of", s.position(arg.Pos()), types.ExprString(arg)))
}

// checkNameList judges a label-name list. A list that is not a composite
// literal fails: its entries are then chosen somewhere this walk cannot read,
// which is the same defect as a computed key.
func (s *fileScan) checkNameList(arg ast.Expr) {
	expr := unparenthesize(arg)
	if ident, ok := expr.(*ast.Ident); ok && ident.Name == "nil" {
		return
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		s.found.assembledName = append(s.found.assembledName,
			fmt.Sprintf("%s: the label-name list is %s, not a literal list this walk can read", s.position(arg.Pos()), types.ExprString(arg)))
		return
	}
	for _, element := range lit.Elts {
		s.checkName(element, "a label name")
	}
}

func (s *fileScan) checkName(arg ast.Expr, what string) {
	if isStringLiteral(arg) {
		return
	}
	s.found.assembledName = append(s.found.assembledName,
		fmt.Sprintf("%s: %s is %s, not a string literal", s.position(arg.Pos()), what, types.ExprString(arg)))
}

// checkLabelSite holds rule 1 wherever a label value or a label set can
// reach the client library, and it reaches that surface by two devices rather
// than by listing the calls that exist today.
//
// The first is the receiver: **every** method on a value settled as a metric
// vector is judged, except the three that provably accept no label value. The
// vector's label-accepting surface is wider than the two calls this
// repository happens to make — WithLabelValues, With, GetMetricWith,
// GetMetricWithLabelValues, CurryWith, MustCurryWith, Delete,
// DeleteLabelValues, DeletePartialMatch — and an enumeration of the two would
// have left the other seven passing arbitrary values while all three rules
// stayed green. Judging by receiver covers the ones a later release adds too.
//
// The second is the constructor table: a prometheus-qualified NewConst… name
// is judged from the arity that says where its trailing labels begin, and a
// name absent from that table **fails outright**. A constructor whose arity
// this walk does not know is one whose labels it cannot find, which is the
// same condition as an argument form it cannot resolve.
//
// WithLabelValues and DeleteLabelValues are additionally judged wherever they
// appear, whatever the receiver: both names belong to the client library and
// to nothing else here, so they cover a vector whose provenance this file
// does not carry. What that leaves is a Labels-taking method on such a
// receiver, and the Labels rule closes it from the other side — the argument
// has to be a Labels value, and every appearance of that type other than a
// composite literal fails.
//
// With is deliberately not judged on an unsettled receiver: it is also the
// name every logger in this repository binds attributes with, and a rule that
// judged both would either fail hundreds of legitimate records or be weakened
// until it judged neither.
func (s *fileScan) checkLabelSite(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if s.isPrometheusSelector(sel) && strings.Contains(sel.Sel.Name, "NewConst") {
		s.checkConstMetric(call, sel.Sel.Name)
		return
	}

	onVector := s.isVectorReceiver(sel.X)
	if onVector && vectorMethodWithoutLabels[sel.Sel.Name] {
		return
	}
	byName := sel.Sel.Name == "WithLabelValues" || sel.Sel.Name == "DeleteLabelValues"
	if !onVector && !byName {
		return
	}

	s.found.labelSites++
	if call.Ellipsis.IsValid() {
		s.found.unboundedLabel = append(s.found.unboundedLabel,
			fmt.Sprintf("%s: label values are spread from a slice, so this walk cannot read them", s.position(call.Pos())))
		return
	}
	for _, arg := range call.Args {
		s.checkLabelArgument(arg)
	}
}

// checkLabelArgument judges one argument of a call on a metric vector. It is
// either a label value, or the whole label set as a Labels composite literal,
// which checkCompositeLit judges element by element.
func (s *fileScan) checkLabelArgument(arg ast.Expr) {
	if lit, isLiteral := unparenthesize(arg).(*ast.CompositeLit); isLiteral && lit.Type != nil {
		if name, isPrometheus := s.prometheusTypeName(lit.Type); isPrometheus && name == labelsTypeName {
			return
		}
	}
	s.checkLabelValue(arg)
}

func (s *fileScan) checkConstMetric(call *ast.CallExpr, name string) {
	offset, known := constMetricLabelOffset[name]
	if !known {
		s.found.unboundedLabel = append(s.found.unboundedLabel,
			fmt.Sprintf("%s: prometheus.%s is a constant-metric constructor this walk does not know the arity of, so it cannot find its label values", s.position(call.Pos()), name))
		return
	}

	s.found.labelSites++
	if call.Ellipsis.IsValid() {
		s.found.unboundedLabel = append(s.found.unboundedLabel,
			fmt.Sprintf("%s: label values are spread from a slice, so this walk cannot read them", s.position(call.Pos())))
		return
	}
	if len(call.Args) <= offset {
		return
	}
	for _, value := range call.Args[offset:] {
		s.checkLabelValue(value)
	}
}

// checkLabelValue is the failing default itself. It admits two forms and
// fails everything else, including a form it does not recognize at all: that
// is what separates a permission from an enumeration of the ways around one.
func (s *fileScan) checkLabelValue(arg ast.Expr) {
	if isStringLiteral(arg) || s.isPermittedConstructor(arg) {
		return
	}
	s.found.unboundedLabel = append(s.found.unboundedLabel,
		fmt.Sprintf("%s: %s is neither a string literal nor a call to metrics.Route/Method", s.position(arg.Pos()), types.ExprString(arg)))
}

func (s *fileScan) isPermittedConstructor(arg ast.Expr) bool {
	call, ok := unparenthesize(arg).(*ast.CallExpr)
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
	return s.imports[ident.Name] == metricsImportPath && permittedConstructor[sel.Sel.Name]
}

func (s *fileScan) checkCompositeLit(lit *ast.CompositeLit) {
	if lit.Type == nil {
		return
	}
	name, ok := s.prometheusTypeName(lit.Type)
	if !ok {
		return
	}

	switch {
	case name == labelsTypeName:
		s.found.labelSites++
		for _, element := range lit.Elts {
			kv, ok := element.(*ast.KeyValueExpr)
			if !ok {
				s.checkLabelValue(element)
				continue
			}
			// The key is a label name chosen at the call site. Rule 2 covers
			// only the names written at declaration, so a computed key here
			// is a name this walk never reads.
			s.checkName(kv.Key, "a label name written at a call site")
			s.checkLabelValue(kv.Value)
		}
	case optionsType[name]:
		for _, element := range lit.Elts {
			kv, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || !nameField[key.Name] {
				continue
			}
			s.checkName(kv.Value, "a metric name or help string")
		}
	}
}

// checkLabelsType fails every appearance of prometheus.Labels that is not a
// composite literal's type: a conversion, a declared variable, a parameter, a
// result. Each of those is a label set built somewhere this walk does not
// read, and each is a one-line bypass of a rule that judged only the argument
// positions it knows about.
func (s *fileScan) checkLabelsType(sel *ast.SelectorExpr) {
	if !s.isPrometheusSelector(sel) || sel.Sel.Name != "Labels" {
		return
	}
	if s.labelsLiteral[sel.Pos()] {
		return
	}
	s.found.unboundedLabel = append(s.found.unboundedLabel,
		fmt.Sprintf("%s: prometheus.Labels is named outside a composite literal, so its names and values are chosen where this walk cannot read them", s.position(sel.Pos())))
}

func (s *fileScan) isVectorReceiver(expr ast.Expr) bool {
	switch receiver := unparenthesize(expr).(type) {
	case *ast.Ident:
		return s.vectors[receiver.Name]
	case *ast.SelectorExpr:
		return s.vectors[receiver.Sel.Name]
	}
	return false
}

func (s *fileScan) isPrometheusCall(call *ast.CallExpr, names map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return s.isPrometheusSelector(sel) && names[sel.Sel.Name]
}

// prometheusTypeName resolves a type expression to the bare name it has in
// the client library, or reports that it is not one of that package's types.
//
// Resolution goes through this file's import table rather than through the
// rendered spelling, because an aliased import renders as prom.CounterOpts
// and would slip past every rule keyed on the canonical spelling — which is
// a one-line bypass of the metric-name rule and of the Labels rule the
// failing default leans on.
func (s *fileScan) prometheusTypeName(expr ast.Expr) (string, bool) {
	unwrapped := unparenthesize(expr)
	if star, ok := unwrapped.(*ast.StarExpr); ok {
		unwrapped = unparenthesize(star.X)
	}
	sel, ok := unwrapped.(*ast.SelectorExpr)
	if !ok || !s.isPrometheusSelector(sel) {
		return "", false
	}
	return sel.Sel.Name, true
}

func (s *fileScan) isPrometheusSelector(sel *ast.SelectorExpr) bool {
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return s.imports[ident.Name] == prometheusImportPath
}

func isStringLiteral(expr ast.Expr) bool {
	lit, ok := unparenthesize(expr).(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func (s *fileScan) position(pos token.Pos) string {
	position := s.fset.Position(pos)
	return fmt.Sprintf("%s:%d:%d", filepath.ToSlash(strings.TrimPrefix(position.Filename, repositoryPrefix)), position.Line, position.Column)
}

// TestNoLabelValueIsUnbounded enforces rule 1, the rule the whole capability
// rests on: a label value creates a time series that persists for the
// retention period and is never reclaimed while it is still being written, so
// one identifier in a label is one series per identifier, forever.
//
// It is written as a permission with a failing default rather than as a list
// of prohibited forms, because an enumerating walk is bypassed in one line:
// WithLabelValues(values...) spreads a slice and With(prometheus.Labels(m))
// converts a map, and neither call site contains a literal or a permitted
// constructor anywhere for such a walk to reject.
func TestNoLabelValueIsUnbounded(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.unboundedLabel {
		t.Errorf("%s", finding)
	}
}

// TestEveryMetricNameIsAStringLiteral enforces rule 2. A formatted metric
// name satisfies every rule about label values while putting the identifier
// straight back into the series — the same defect logging's string-literal
// message rule exists to prevent, one level worse, because a name is a series
// too.
func TestEveryMetricNameIsAStringLiteral(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.assembledName {
		t.Errorf("%s", finding)
	}
}

// TestNoDefaultRegistryIsUsed enforces rule 3. The client library's default
// registry is process-global mutable state any transitively imported package
// can write a family into, with no call site in this repository at all — the
// arbitrary-value hole in registry form.
func TestNoDefaultRegistryIsUsed(t *testing.T) {
	found := repositoryFindings(t)
	for _, finding := range found.defaultRegistry {
		t.Errorf("%s", finding)
	}
}

// TestTheSourceWalkReadsTheRepository reports what the three rules above were
// applied to. The label-site count is the reconciliation: a call site this
// walk fails to recognize would leave all three rules quietly checking less
// than they claim.
func TestTheSourceWalkReadsTheRepository(t *testing.T) {
	found := repositoryFindings(t)
	t.Logf("parsed %d non-test files under cmd/ and internal/, recognizing %d label site(s)", found.files, found.labelSites)
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

// TestTheWalkJudgesEachFormItMustJudge covers the walker itself, on the forms
// this repository does not contain.
//
// The four cases that matter most are the ones carrying no literal and no
// permitted constructor anywhere — the spread, the conversion, the variable
// label set, and the computed key. Each is a single line, each would pass a
// walk that inspects only the argument positions it knows about, and each is
// what "structurally impossible" would otherwise be false about. The logger's
// own With is here for the opposite reason: judging it would fail hundreds of
// legitimate records, and that pressure is how this rule gets weakened.
func TestTheWalkJudgesEachFormItMustJudge(t *testing.T) {
	cases := []struct {
		name            string
		src             string
		unboundedLabel  int
		assembledName   int
		defaultRegistry int
		labelSites      int
	}{
		{
			name: "a literal and the two permitted constructors",
			src: `package p
import (
	"github.com/prometheus/client_golang/prometheus"
	"video-processor/internal/platform/metrics"
)
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fiapx_http_requests_total", Help: "requests"}, []string{"method", "route", "status_class"})
func f(method, route string) { c.WithLabelValues(metrics.Method(method), metrics.Route(route), "2xx") }`,
			labelSites: 1,
		},
		{
			name: "a label value rendered from an integer",
			src: `package p
import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"status"})
func f(status int) { c.WithLabelValues(strconv.Itoa(status)) }`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a label value read from a constant",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
const outcome = "reserved"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"outcome"})
func f() { c.WithLabelValues(outcome) }`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a label value read from a variable",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"user"})
func f(userID string) { c.WithLabelValues(userID) }`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a label value assembled by formatting",
			src: `package p
import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"job"})
func f(id string) { c.WithLabelValues(fmt.Sprintf("job-%s", id)) }`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a Labels literal with one literal and one variable value",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"route", "job"})
func f(id string) { c.With(prometheus.Labels{"route": "/upload", "job": id}) }`,
			unboundedLabel: 1,
			labelSites:     2,
		},
		{
			name: "a Labels literal with a computed key",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "n", Help: "h"}, []string{"state"})
func f(key string) { c.With(prometheus.Labels{key: "queued"}) }`,
			assembledName: 1,
			labelSites:    2,
		},
		{
			name: "label values spread from a slice",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"a", "b"})
func f(values []string) { c.WithLabelValues(values...) }`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a converted Labels map",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"a"})
func f(m map[string]string) { c.With(prometheus.Labels(m)) }`,
			unboundedLabel: 2,
			labelSites:     1,
		},
		{
			name: "a Labels value held in a variable",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"a"})
func f() {
	var labels prometheus.Labels
	c.With(labels)
}`,
			unboundedLabel: 2,
			labelSites:     1,
		},
		{
			name: "a Labels value returned by a helper",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func build(id string) prometheus.Labels { return prometheus.Labels{"job": id} }`,
			unboundedLabel: 2,
			labelSites:     1,
		},
		{
			name: "a constant metric with a variable label",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var desc = prometheus.NewDesc("fiapx_n", "h", []string{"state"}, nil)
func f(ch chan<- prometheus.Metric, state string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1, state)
}`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a metric name assembled by formatting",
			src: `package p
import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: fmt.Sprintf("fiapx_%s_total", "http"), Help: "h"}, []string{"a"})`,
			assembledName: 1,
		},
		{
			name: "a label-name list that is not a literal",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var names = []string{"a"}
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, names)`,
			assembledName: 1,
		},
		{
			name: "the automatic-registration helper, which names no registry",
			src: `package p
import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)
var c = promauto.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"a"})`,
			defaultRegistry: 1,
		},
		{
			name: "the package-level registration function, which names no helper",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func f(c prometheus.Collector) { prometheus.MustRegister(c) }`,
			defaultRegistry: 1,
		},
		{
			name: "the default registerer named as a value",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func f(c prometheus.Collector) { prometheus.DefaultRegisterer.Unregister(c) }`,
			defaultRegistry: 1,
		},
		{
			name: "registration into an explicit registry, which is the rule rather than its violation",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func f(registry *prometheus.Registry, c prometheus.Collector) { registry.MustRegister(c) }`,
		},
		{
			name: "the vector's other label-taking readers, which an enumeration of two would miss",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"user"})
func f(userID string) {
	c.GetMetricWithLabelValues(userID)
	c.DeleteLabelValues(userID)
}`,
			unboundedLabel: 2,
			labelSites:     2,
		},
		{
			name: "currying a vector, which returns a vector carrying the label",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"job", "route"})
func f(id string) { c.MustCurryWith(prometheus.Labels{"job": id}) }`,
			unboundedLabel: 1,
			labelSites:     2,
		},
		{
			name: "a vector's own collector methods, which take no label value",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var c = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "n", Help: "h"}, []string{"a"})
func f(descs chan<- *prometheus.Desc, metrics chan<- prometheus.Metric) {
	c.Describe(descs)
	c.Collect(metrics)
	c.Reset()
}`,
		},
		{
			name: "the error-returning constant-metric constructor, which the Must form's name does not cover",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var desc = prometheus.NewDesc("fiapx_n", "h", []string{"state"}, nil)
func f(state string) (prometheus.Metric, error) {
	return prometheus.NewConstMetric(desc, prometheus.GaugeValue, 1, state)
}`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "a constant histogram, whose labels begin one argument later",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
var desc = prometheus.NewDesc("fiapx_n", "h", []string{"state"}, nil)
func f(buckets map[float64]uint64, state string) prometheus.Metric {
	return prometheus.MustNewConstHistogram(desc, 1, 1.5, buckets, state)
}`,
			unboundedLabel: 1,
			labelSites:     1,
		},
		{
			name: "an aliased import, which the canonical spelling would not recognize",
			src: `package p
import prom "github.com/prometheus/client_golang/prometheus"
var name = "fiapx_" + "n"
var c = prom.NewCounterVec(prom.CounterOpts{Name: name, Help: "h"}, []string{"job"})
func f(id string) { c.With(prom.Labels{"job": id}) }`,
			unboundedLabel: 1,
			assembledName:  1,
			labelSites:     2,
		},
		{
			name: "options assembled elsewhere, whose metric name this walk never reads",
			src: `package p
import "github.com/prometheus/client_golang/prometheus"
func build(suffix string) prometheus.CounterOpts {
	return prometheus.CounterOpts{Name: "fiapx_" + suffix, Help: "h"}
}
var c = prometheus.NewCounterVec(build("http"), []string{"a"})`,
			assembledName: 2,
		},
		{
			name: "the logger's With, which is not a label site",
			src: `package p
import "log/slog"
func f(id string) { slog.Default().With(slog.String("job_id", id)).Info("a job was accepted") }`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := scanSource(t, tc.src)
			if len(found.unboundedLabel) != tc.unboundedLabel {
				t.Errorf("unbounded labels = %v, want %d", found.unboundedLabel, tc.unboundedLabel)
			}
			if len(found.assembledName) != tc.assembledName {
				t.Errorf("assembled names = %v, want %d", found.assembledName, tc.assembledName)
			}
			if len(found.defaultRegistry) != tc.defaultRegistry {
				t.Errorf("default-registry uses = %v, want %d", found.defaultRegistry, tc.defaultRegistry)
			}
			if found.labelSites != tc.labelSites {
				t.Errorf("recognized %d label site(s), want %d", found.labelSites, tc.labelSites)
			}
		})
	}
}
