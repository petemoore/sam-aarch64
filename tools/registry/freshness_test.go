package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBinaryStaleAgainst pins the i142 staleness guard: a binary older than its
// newest non-test source is stale; a binary at least as new is fresh; and a newer
// _test.go never counts (editing a test can't change the built binary).
func TestBinaryStaleAgainst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "registry")
	gofile := filepath.Join(src, "mutators.go")
	testfile := filepath.Join(src, "mutators_test.go")
	for _, f := range []string{exe, gofile, testfile} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().Truncate(time.Second)
	set := func(path string, t2 time.Time) {
		if err := os.Chtimes(path, t2, t2); err != nil {
			t.Fatal(err)
		}
	}

	// Binary newer than the source → fresh.
	set(gofile, base)
	set(testfile, base)
	set(exe, base.Add(time.Hour))
	if _, _, _, stale, err := binaryStaleAgainst(exe, src); err != nil || stale {
		t.Fatalf("binary newer than source: got stale=%v err=%v, want fresh", stale, err)
	}

	// Source newer than the binary → stale, and it names the offending source.
	set(gofile, base.Add(2*time.Hour))
	sp, _, _, stale, err := binaryStaleAgainst(exe, src)
	if err != nil || !stale {
		t.Fatalf("source newer than binary: got stale=%v err=%v, want stale", stale, err)
	}
	if filepath.Base(sp) != "mutators.go" {
		t.Errorf("stale source = %q, want mutators.go", sp)
	}

	// A newer _test.go must NOT mark the binary stale.
	set(gofile, base) // real source back to old
	set(testfile, base.Add(3*time.Hour))
	if _, _, _, stale, err := binaryStaleAgainst(exe, src); err != nil || stale {
		t.Errorf("a newer _test.go wrongly marked the binary stale (stale=%v err=%v)", stale, err)
	}
}

// TestNewestGoSourceEmptyDir confirms an empty source dir yields no source (so the
// guard treats it as indeterminate and never blocks).
func TestNewestGoSourceEmptyDir(t *testing.T) {
	dir := t.TempDir()
	path, _, err := newestGoSource(dir)
	if err != nil {
		t.Fatalf("newestGoSource(empty) error: %v", err)
	}
	if path != "" {
		t.Errorf("newestGoSource(empty) = %q, want empty", path)
	}
}
