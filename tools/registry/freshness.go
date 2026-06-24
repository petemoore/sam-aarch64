package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The build/registry binary is a gitignored build artifact (Makefile: registry-gen).
// Agents invoke `build/registry <mutation>` directly, NOT via `make registry`, so a
// binary left stale after a tools/registry source change would mutate the live
// registry with old logic — silently. That happened once: a pre-#462 binary skipped
// the reconcilePriority step and stranded i138 in the priority queue (i142). This
// guard makes that state impossible to act on: before any mutating subcommand, if a
// non-test tools/registry source is newer than the running binary, the command
// refuses and tells the operator to rebuild. Read-only commands are unaffected (a
// stale view is harmless; only mutations corrupt state).

// assertBinaryFresh aborts a mutating command if the running binary is older than
// its own source. It is a no-op when freshness can't be determined (the binary was
// copied away from its checkout, os.Executable failed, etc.) — it only ever blocks
// on a positively-detected stale binary, never on uncertainty.
func assertBinaryFresh() {
	exe, err := os.Executable()
	if err != nil {
		return // can't locate the binary; don't block
	}
	// The binary is built to <repo>/build/registry; its source is <repo>/tools/registry.
	srcDir := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "tools", "registry"))
	if info, err := os.Stat(srcDir); err != nil || !info.IsDir() {
		return // source tree not alongside the binary (installed/copied); can't check
	}
	staleSrc, srcTime, binTime, stale, err := binaryStaleAgainst(exe, srcDir)
	if err != nil || !stale {
		return
	}
	fmt.Fprintf(os.Stderr,
		"registry: refusing to mutate — build/registry is STALE.\n"+
			"  tools/registry/%s was modified %s, newer than the running binary (built %s).\n"+
			"  A stale binary can silently skip new logic (it once stranded i138 in the priority queue — i142).\n"+
			"  Rebuild and retry:  make registry-gen\n",
		filepath.Base(staleSrc), srcTime.Format(time.RFC3339), binTime.Format(time.RFC3339))
	os.Exit(1)
}

// binaryStaleAgainst reports whether the binary at exePath is older than the newest
// non-test Go source under srcDir. It returns the offending source path and the two
// mtimes for the error message. stale is false (with no error) when the binary is
// at least as new as every source, or when srcDir holds no countable sources.
func binaryStaleAgainst(exePath, srcDir string) (staleSrc string, srcTime, binTime time.Time, stale bool, err error) {
	bi, err := os.Stat(exePath)
	if err != nil {
		return "", time.Time{}, time.Time{}, false, err
	}
	binTime = bi.ModTime()
	newestPath, newestTime, err := newestGoSource(srcDir)
	if err != nil {
		return "", time.Time{}, binTime, false, err
	}
	if newestPath != "" && newestTime.After(binTime) {
		return newestPath, newestTime, binTime, true, nil
	}
	return "", newestTime, binTime, false, nil
}

// newestGoSource returns the path and mtime of the most recently modified non-test
// .go file under srcDir (recursively). Test files (_test.go) are excluded: editing
// one cannot change the built binary's behaviour, so it must not force a rebuild
// before a mutation.
func newestGoSource(srcDir string) (path string, mtime time.Time, err error) {
	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return e
		}
		if fi.ModTime().After(mtime) {
			mtime = fi.ModTime()
			path = p
		}
		return nil
	})
	return path, mtime, err
}
