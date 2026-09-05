package postgres_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The name of the one statement permitted to project the secret column.
// Renaming the constant without updating this is caught immediately: the
// exemption stops matching and the statement is reported.
const findDeliverableConstName = "findDeliverablePreferencesQuery"

// These assertions are source-level rather than behavioural, and they have to
// be. A runtime test can only observe statements that ran; a second query
// selecting the secret would be invisible to it until something called the
// method holding it, which is exactly the state this rule exists to prevent
// reaching production.

// secretProjection matches the presence projection the read paths use. The
// column may be *named* outside FindDeliverable — the upsert inserts it and
// the conflict clause assigns it — so the rule is about what a projection
// yields, not about the identifier appearing anywhere in a statement.
var secretProjection = regexp.MustCompile(`(?i)\bsecret\b\s*<>\s*''`)

var bareSecret = regexp.MustCompile(`(?i)\bsecret\b`)

// projectionClauses regexes: everything a statement hands back to Go. A
// SELECT's target list and a RETURNING clause are the only two ways a value
// leaves PostgreSQL through this adapter.
var (
	selectTargets   = regexp.MustCompile(`(?is)\bSELECT\b(.*?)\bFROM\b`)
	returningTarget = regexp.MustCompile(`(?is)\bRETURNING\b(.*)$`)
)

// projectsTheSecret reports whether the statement hands the secret's value
// back, as opposed to merely naming the column in a write or projecting
// whether one is set.
func projectsTheSecret(statement string) bool {
	clauses := make([]string, 0, 2)
	for _, match := range selectTargets.FindAllStringSubmatch(statement, -1) {
		clauses = append(clauses, match[1])
	}
	if match := returningTarget.FindStringSubmatch(statement); match != nil {
		clauses = append(clauses, match[1])
	}

	for _, clause := range clauses {
		// Strip the presence projections first, then look for what is left:
		// "secret <> ''" contains the identifier but yields a boolean, and
		// "has_secret" is not the column at all.
		if bareSecret.MatchString(secretProjection.ReplaceAllString(clause, "")) {
			return true
		}
	}
	return false
}

// sqlLiteral is one string constant or inline literal that looks like a
// statement, paired with the name it is bound to for the failure message.
type sqlLiteral struct {
	name      string
	statement string
}

func packageSQLLiterals(t *testing.T) []sqlLiteral {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("unexpected error reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	literals := make([]sqlLiteral, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("unexpected error parsing %s: %v", name, err)
		}

		// Which literal belongs to which constant, so a violation can be
		// reported by the name a reader would go looking for.
		names := make(map[token.Pos]string)
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.CONST {
				continue
			}
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, value := range valueSpec.Values {
					if i < len(valueSpec.Names) {
						names[value.Pos()] = valueSpec.Names[i].Name
					}
				}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			statement, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			upper := strings.ToUpper(statement)
			if !strings.Contains(upper, "SELECT") && !strings.Contains(upper, "RETURNING") {
				return true
			}
			bound, found := names[lit.Pos()]
			if !found {
				bound = name + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
			}
			literals = append(literals, sqlLiteral{name: bound, statement: statement})
			return true
		})
	}

	if len(literals) == 0 {
		t.Fatal("found no statements to check; the walk is not reaching this package's sources")
	}
	return literals
}

// TestNoQueryOutsideFindDeliverableSelectsTheSecret is the assertion the
// domain port names: the stored secret is loadable through exactly one read
// path, which is what makes domain.PreferenceView's missing secret field a
// guarantee rather than a convention.
func TestNoQueryOutsideFindDeliverableSelectsTheSecret(t *testing.T) {
	literals := packageSQLLiterals(t)

	// The detector has to be shown working on the one statement that does
	// project the secret, or a broken matcher would report the whole package
	// clean and this test would pass by seeing nothing.
	var exemptFound bool
	for _, lit := range literals {
		if lit.name != findDeliverableConstName {
			continue
		}
		exemptFound = true
		if !projectsTheSecret(lit.statement) {
			t.Fatalf("%s does not project the secret; the detector below proves nothing", lit.name)
		}
	}
	if !exemptFound {
		t.Fatalf("no statement named %s was found; the exemption below matches nothing", findDeliverableConstName)
	}

	for _, lit := range literals {
		if lit.name == findDeliverableConstName {
			continue
		}
		if projectsTheSecret(lit.statement) {
			t.Errorf("%s projects the secret column; only %s may load it", lit.name, findDeliverableConstName)
		}
	}
}

// TestProjectsTheSecretDistinguishesReadsFromWrites covers the detector
// itself over the forms this package actually contains, so a future statement
// is judged by a matcher whose behaviour is stated rather than assumed.
func TestProjectsTheSecretDistinguishesReadsFromWrites(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		want      bool
	}{
		{"presence projection", "SELECT enabled, secret <> '' AS has_secret FROM notification_preferences", false},
		{"presence projection in RETURNING", "UPDATE notification_preferences SET enabled = $1 RETURNING secret <> '' AS has_secret", false},
		{"insert naming the column", "INSERT INTO notification_preferences (user_id, secret) VALUES ($1, $2) RETURNING created_at", false},
		{"conflict clause assigning the column", "INSERT INTO t (secret) VALUES ($1) ON CONFLICT (user_id) DO UPDATE SET secret = EXCLUDED.secret RETURNING created_at", false},
		{"select naming the column", "SELECT event_type, secret, created_at FROM notification_preferences WHERE user_id = $1", true},
		{"returning the column", "UPDATE notification_preferences SET enabled = $1 RETURNING secret", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectsTheSecret(tt.statement); got != tt.want {
				t.Fatalf("projectsTheSecret() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDeliveryTableHoldsNoSecret reads the embedded schema rather than the
// database, so it holds whether or not a server is configured — and it is
// the delivery record's own requirement: the record says whether a
// notification was delivered, and everything needed to send one is read from
// notification_preferences at delivery time.
func TestDeliveryTableHoldsNoSecret(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("unexpected error reading schema: %v", err)
	}

	deliveries := createTableBlock(t, string(schema), "notification_deliveries")
	if bareSecret.MatchString(deliveries) {
		t.Error("notification_deliveries names a secret column; the delivery record must hold none")
	}
	for _, forbidden := range []string{"destination", "payload", "body"} {
		if strings.Contains(strings.ToLower(deliveries), forbidden) {
			t.Errorf("notification_deliveries names %q; the record carries no delivered content", forbidden)
		}
	}

	// The extractor is shown working on the table that legitimately holds
	// one, so a block that failed to match cannot report the delivery table
	// clean by returning nothing.
	preferences := createTableBlock(t, string(schema), "notification_preferences")
	if !bareSecret.MatchString(preferences) {
		t.Fatal("notification_preferences appears to hold no secret column; the block extractor is not working")
	}
}

// createTableBlock returns the body of one CREATE TABLE statement. The schema
// is one file of statements terminated by ");" at the start of a line, which
// is what this relies on.
func createTableBlock(t *testing.T, schema, table string) string {
	t.Helper()

	start := strings.Index(schema, "CREATE TABLE IF NOT EXISTS "+table)
	if start < 0 {
		t.Fatalf("schema.sql declares no table named %s", table)
	}
	rest := schema[start:]
	end := strings.Index(rest, "\n);")
	if end < 0 {
		t.Fatalf("the %s declaration is not terminated as expected", table)
	}
	return stripSQLComments(rest[:end])
}

// stripSQLComments drops the explanatory prose from a block so the assertions
// above read column definitions rather than the comments describing them —
// this schema explains at length why the delivery record holds no secret,
// destination or request body, and a naive substring search finds those words
// in the explanation.
func stripSQLComments(block string) string {
	lines := strings.Split(block, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if comment := strings.Index(line, "--"); comment >= 0 {
			line = line[:comment]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
