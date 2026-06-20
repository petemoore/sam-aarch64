package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")

	if err := atomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "hello" {
		t.Errorf("first write = %q, want hello", got)
	}

	// Overwrite atomically.
	if err := atomicWriteFile(path, []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "world" {
		t.Errorf("overwrite = %q, want world", got)
	}

	// Permission honoured.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %v, want 0644", info.Mode().Perm())
	}

	// No leftover temp files — only the target remains.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "x.yaml" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir should contain only x.yaml, got %v", names)
	}
}

// TestAcquireFlock_Contention: a second exclusive lock on the same file blocks
// until the timeout (the first holder still holds it), then succeeds once the
// first releases. flock is per open-file-description, so two os.OpenFile handles
// to the same path contend even within one process.
func TestAcquireFlock_Contention(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")

	f1, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f1.Close()
	if err := acquireFlock(f1, time.Second); err != nil {
		t.Fatalf("first lock: %v", err)
	}

	f2, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	// While f1 holds the lock, f2 cannot acquire it and times out.
	start := time.Now()
	if err := acquireFlock(f2, 120*time.Millisecond); err == nil {
		t.Fatal("expected contention timeout while f1 holds the lock, got success")
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Errorf("should have waited ~the timeout before failing, waited %v", waited)
	}

	// Release f1; f2 acquires promptly.
	if err := syscall.Flock(int(f1.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := acquireFlock(f2, time.Second); err != nil {
		t.Fatalf("after release, f2 should lock: %v", err)
	}
}

// TestLockRegistry_CreatesAndHolds: lockRegistry creates <dir>/.lock and a second
// independent acquire on the same file contends (proving the lock is real).
func TestLockRegistry_CreatesAndHolds(t *testing.T) {
	dir := t.TempDir()
	if err := lockRegistry(dir); err != nil {
		t.Fatalf("lockRegistry: %v", err)
	}
	defer func() {
		if registryLockFile != nil {
			registryLockFile.Close()
			registryLockFile = nil
		}
	}()
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatalf(".lock not created: %v", err)
	}
	// An independent handle cannot lock while lockRegistry holds it.
	other, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := acquireFlock(other, 80*time.Millisecond); err == nil {
		t.Error("expected the registry lock to be held, but a second acquire succeeded")
	}
}
