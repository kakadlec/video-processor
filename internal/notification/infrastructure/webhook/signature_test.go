package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

func TestSign_IsTheHMACOverTheTimestampAndBody(t *testing.T) {
	const rawSecret = "s3cret-value-long-enough"
	body := []byte(`{"version":1}`)

	mac := hmac.New(sha256.New, []byte(rawSecret))
	mac.Write([]byte("1756732800.{\"version\":1}"))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if got := sign(secret(t, rawSecret), "1756732800", body); got != want {
		t.Errorf("sign() = %s, want %s", got, want)
	}
}

// Moving the timestamp changes the signature. This is the property that lets
// a receiver bound a captured request's age: if the body alone were signed,
// the timestamp would be a value an attacker could rewrite freely.
func TestSign_BindsTheTimestampIntoTheSignedValue(t *testing.T) {
	body := []byte(`{"version":1}`)
	first := sign(secret(t, "s3cret-value-long-enough"), "1756732800", body)
	second := sign(secret(t, "s3cret-value-long-enough"), "1756732801", body)
	if first == second {
		t.Error("the signature is unchanged by the timestamp: a captured request would be replayable forever")
	}
}

func TestSign_DiffersBySecret(t *testing.T) {
	body := []byte(`{"version":1}`)
	if sign(secret(t, "secret-number-one-here"), "1756732800", body) == sign(secret(t, "secret-number-two-here"), "1756732800", body) {
		t.Error("two secrets produced the same signature")
	}
}

// The separator is part of the signed value rather than a formatting detail:
// without it "1.23" and "12.3" would sign identically, and a receiver
// splitting on the dot would verify a value the sender never sent.
func TestSign_SeparatesTheTimestampFromTheBody(t *testing.T) {
	first := sign(secret(t, "separator-test-secret"), "1", []byte("23"))
	second := sign(secret(t, "separator-test-secret"), "12", []byte("3"))
	if first == second {
		t.Error("the timestamp and the body are concatenated without a separator")
	}
}

func TestRenderTimestamp_IsUnixSeconds(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 500_000_000, time.UTC)
	if got := renderTimestamp(at); got != "1788264000" {
		t.Errorf("renderTimestamp() = %s, want 1788264000", got)
	}
}

// revealCallSites are the only non-test files permitted to call Reveal.
//
// The claim being enforced is narrow and has to be: the write path in
// postgres reveals a secret its own caller handed it in the same call, in
// order to store it, and that path is required. What must stay singular is
// the reading of one back out of storage, which is what makes every other
// projection's "has_secret" a guarantee rather than a convention.
var revealCallSites = []string{
	filepath.Join("internal", "notification", "infrastructure", "postgres", "repository.go"),
	filepath.Join("internal", "notification", "infrastructure", "webhook", "signature.go"),
}

// TestOnlyTheSignerRevealsAStoredSecret reads source rather than running
// anything, for the same reason the postgres package's query scan does: a
// call that was never executed is invisible to a runtime test.
func TestOnlyTheSignerRevealsAStoredSecret(t *testing.T) {
	root := repoRoot(t)

	found := make([]string, 0, len(revealCallSites))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path) // #nosec G304
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(source), ".Reveal()") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		found = append(found, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk the repository: %v", err)
	}

	slices.Sort(found)
	want := slices.Clone(revealCallSites)
	slices.Sort(want)

	if !slices.Equal(found, want) {
		t.Errorf("files calling Reveal() = %v, want %v — a new caller reads a stored secret somewhere the design says nothing does", found, want)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("failed to resolve the repository root: %v", err)
	}
	// A guard against the walk silently covering the wrong tree, which would
	// leave this test passing while checking nothing.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root: %v", root, err)
	}
	return root
}

func secret(t *testing.T, raw string) domain.Secret {
	t.Helper()
	value, err := domain.NewSecret(raw)
	if err != nil {
		t.Fatalf("unexpected error building a secret: %v", err)
	}
	return value
}
