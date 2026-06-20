package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// mutatingCommands are the subcommands that rewrite registry SOURCE files and so
// must run under the registry lock. Read-only queries (validate/gen/ready/view/
// dependents/dag/next-id) are absent. `gen` writes only the derived docs/notes
// views (regenerable, drift caught by registry-sync-check), not source, so it is
// deliberately not locked.
var mutatingCommands = map[string]bool{
	"add":        true,
	"split":      true,
	"set-status": true,
	"set-pr":     true,
	"dep":        true,
	"answer":     true,
	"prioritize": true,
	"move":       true,
}

// lockTimeout bounds how long a mutating command waits for the registry lock
// before giving up. A variable (not a const) so tests can shorten it.
var lockTimeout = 10 * time.Second

// registryLockFile holds the locked .lock file for the process lifetime. We keep
// a package-level reference so the *os.File is never garbage-collected (its
// finalizer would close the fd and release the flock early). The OS releases the
// advisory lock automatically when the process exits — normally or via os.Exit —
// so no explicit unlock is needed, and a crashed holder never deadlocks others.
var registryLockFile *os.File

// lockRegistry takes an exclusive advisory lock on <registryDir>/.lock so that
// concurrent mutating invocations serialize: their load → mutate → write
// sequences cannot interleave and clobber each other (last-writer-wins data
// loss). Combined with atomicWriteFile, a concurrent reader always sees a
// complete file. It blocks (polling) up to lockTimeout, then returns a clear
// error rather than hanging forever.
func lockRegistry(registryDir string) error {
	path := filepath.Join(registryDir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open registry lock %s: %w", path, err)
	}
	if err := acquireFlock(f, lockTimeout); err != nil {
		f.Close()
		return err
	}
	registryLockFile = f
	return nil
}

// acquireFlock takes an exclusive non-blocking flock on f, retrying until it
// succeeds or timeout elapses. Non-blocking + poll (rather than a blocking
// LOCK_EX) lets us bound the wait and emit a helpful message on contention.
func acquireFlock(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK {
			return fmt.Errorf("lock registry %s: %w", f.Name(), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("registry is locked by another process (waited %s on %s); retry, or remove the lock file if it is stale", timeout, f.Name())
		}
		time.Sleep(25 * time.Millisecond)
	}
}
