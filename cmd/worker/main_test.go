package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateDirs_CreatesOnlyTemp pins the directory set this binary owns.
// temp/ is the only one: per-delivery scratch for the downloaded source, the
// extracted frames, and the zip built from them. Both source videos and
// results are objects in a bucket, so uploads/ and outputs/ must not
// reappear — and the API creates nothing at all now, which is why this test
// lives here rather than beside it.
func TestCreateDirs_CreatesOnlyTemp(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	if err := createDirs(); err != nil {
		t.Fatalf("createDirs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "temp")); err != nil {
		t.Fatalf("expected temp/ to exist: %v", err)
	}
	for _, name := range []string{"uploads", "outputs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected %s/ not to be created, stat err = %v", name, err)
		}
	}
}
