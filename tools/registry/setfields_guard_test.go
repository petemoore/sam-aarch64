package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestSetTitle_UpdatesItemTitle: set-title rewrites an item's title (no hand-edit
// of items.yaml needed) and the result validates.
func TestSetTitle_UpdatesItemTitle(t *testing.T) {
	paths := setupMutatorFixture(t)
	runSetTitle([]string{"--id", "i2", "--title", "Reworded title"}, paths)
	assertValidFromPaths(t, paths)
	for _, it := range loadRegFromPaths(t, paths).Items {
		if it.ID == "i2" {
			if it.Title != "Reworded title" {
				t.Errorf("i2.Title = %q, want %q", it.Title, "Reworded title")
			}
			return
		}
	}
	t.Fatal("i2 not found")
}

// TestSetDesc_UpdatesItemDescription: set-desc rewrites an item's description.
func TestSetDesc_UpdatesItemDescription(t *testing.T) {
	paths := setupMutatorFixture(t)
	runSetDesc([]string{"--id", "i2", "--desc", "Updated description text."}, paths)
	assertValidFromPaths(t, paths)
	for _, it := range loadRegFromPaths(t, paths).Items {
		if it.ID == "i2" {
			if strings.TrimSpace(it.Description) != "Updated description text." {
				t.Errorf("i2.Description = %q, want updated", it.Description)
			}
			return
		}
	}
	t.Fatal("i2 not found")
}

// TestSetDesc_UpdatesQuestionBody: set-desc on a qN rewrites the question body.
func TestSetDesc_UpdatesQuestionBody(t *testing.T) {
	paths := setupMutatorFixture(t)
	runSetDesc([]string{"--id", "q1", "--desc", "Reworded question?"}, paths)
	assertValidFromPaths(t, paths)
	for _, q := range loadRegFromPaths(t, paths).Questions {
		if q.ID == "q1" {
			// The YAML block-scalar round-trip appends a trailing newline (same as
			// `add`), so compare trimmed.
			if strings.TrimSpace(q.Body) != "Reworded question?" {
				t.Errorf("q1.Body = %q, want reworded", q.Body)
			}
			return
		}
	}
	t.Fatal("q1 not found")
}

// TestWarnDoneLeafDep covers the i87b guard: a dependency on a DONE leaf of an
// umbrella that still has OPEN siblings warns; a no-parent target or an
// all-siblings-closed umbrella does not.
func TestWarnDoneLeafDep(t *testing.T) {
	paths := setupMutatorFixture(t)
	reg := loadRegFromPaths(t, paths) // i1 umbrella -> i1a (DONE) + i1b (OPEN)

	var buf bytes.Buffer
	if !warnDoneLeafDep(&buf, reg, "i1a") {
		t.Fatal("expected a warning depending on i1a (DONE leaf of i1 with OPEN sibling i1b)")
	}
	if msg := buf.String(); !strings.Contains(msg, "i1a") || !strings.Contains(msg, "i1b") {
		t.Errorf("warning text should name the leaf and its open sibling; got %q", msg)
	}

	// A target with no parent (top-level item) never warns.
	buf.Reset()
	if warnDoneLeafDep(&buf, reg, "i2") {
		t.Errorf("did not expect a warning for top-level i2; got %q", buf.String())
	}

	// A DONE leaf whose siblings are all closed does not warn: close i1b in-memory.
	for i := range reg.Items {
		if reg.Items[i].ID == "i1b" {
			reg.Items[i].Status = StatusDone
		}
	}
	buf.Reset()
	if warnDoneLeafDep(&buf, reg, "i1a") {
		t.Errorf("did not expect a warning when all of i1's leaves are closed; got %q", buf.String())
	}
}

// NOTE: splitting a DONE item with an OPEN child is already HARD-BLOCKED by
// validation ("umbrella marked DONE but child OPEN") — applyAndCommit rejects it
// and leaves the source unchanged, which is stronger than the warning. The
// split-of-DONE warning printed by runSplit is a heads-up shown before that error
// (and for the rare DONE-parent + DONE-child case, which validation permits). It
// is not separately unit-tested here because the rejection path calls os.Exit.
