package main

import (
	"strings"
	"testing"
)

// validReg returns a small valid registry for use as the green baseline.
func validReg() *Registry {
	return &Registry{
		Items: []Item{
			{
				ID:     "i1",
				Title:  "Base item (umbrella)",
				Status: StatusOpen,
				Kind:   "umbrella",
				Owner:  "agent",
			},
			{
				ID:     "i1a",
				Title:  "Sub-item leaf",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
				Parent: "i1",
				Refs:   []string{"i1"},
			},
			{
				ID:     "i2",
				Title:  "Done leaf",
				Status: StatusDone,
				Kind:   "leaf",
				Owner:  "agent",
				PRs:    []PRRef{{Num: 10, Role: RoleCompleting}},
			},
			{
				ID:      "i3",
				Title:   "Blocked item",
				Status:  StatusBlocked,
				Blocker: "waiting on i2",
				Kind:    "leaf",
				Owner:   "agent",
				Refs:    []string{"i2"},
			},
			{
				ID:     "i10",
				Title:  "Item after i3 — numeric sort",
				Status: StatusOpen,
				Kind:   "leaf",
				Owner:  "agent",
			},
		},
		Questions: []Question{
			{
				ID:     "q1",
				Title:  "A valid question",
				Status: StatusOpen,
				Owner:  "pete",
			},
		},
	}
}

// assertNoErrors asserts the valid registry produces no validation errors.
func TestValidate_GreenCase(t *testing.T) {
	reg := validReg()
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("expected no errors on valid registry; got:\n%s", strings.Join(ve.msgs, "\n"))
	}
}

// Invariant 1: duplicate id.
func TestValidate_Inv1_DuplicateID(t *testing.T) {
	reg := validReg()
	reg.Items = append(reg.Items, Item{
		ID:     "i2", // duplicate
		Title:  "Duplicate",
		Status: StatusOpen,
		Kind:   "leaf",
		Owner:  "agent",
	})
	assertError(t, reg, "i2", "duplicate id")
}

// Invariant 2: malformed item id.
func TestValidate_Inv2_MalformedItemID(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "x99", Title: "bad id", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "x99", "malformed item id")
}

// Invariant 2: malformed question id.
func TestValidate_Inv2_MalformedQuestionID(t *testing.T) {
	reg := &Registry{
		Questions: []Question{
			{ID: "i5", Title: "wrong prefix", Status: StatusOpen, Owner: "agent"},
		},
	}
	assertError(t, reg, "i5", "malformed question id")
}

// Invariant 3: out of sort order (i10 before i2 violates numeric sort).
func TestValidate_Inv3_OutOfOrder(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i10", Title: "should come after i2", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
			{ID: "i2", Title: "comes numerically before i10", Status: StatusDone, Kind: "leaf", Owner: "agent",
				PRs: []PRRef{{Num: 1, Role: RoleCompleting}}},
		},
	}
	assertError(t, reg, "i2", "out of canonical sort order")
}

// Invariant 4: unknown status.
func TestValidate_Inv4_UnknownStatus(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "bad status", Status: "PENDING", Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "unknown status")
}

// Invariant 5: pr with missing role.
func TestValidate_Inv5_PRMissingRole(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "pr no role", Status: StatusDone, Kind: "leaf", Owner: "agent",
				PRs: []PRRef{{Num: 5, Role: ""}}},
		},
	}
	assertError(t, reg, "i1", "missing role")
}

// Invariant 5: pr with zero num.
func TestValidate_Inv5_PRZeroNum(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "pr zero num", Status: StatusDone, Kind: "leaf", Owner: "agent",
				PRs: []PRRef{{Num: 0, Role: RoleCompleting}}},
		},
	}
	assertError(t, reg, "i1", "num must be a positive integer")
}

// Invariant 6: umbrella carrying a PR.
func TestValidate_Inv6_UmbrellaWithPR(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "umbrella", Status: StatusOpen, Kind: "umbrella", Owner: "agent",
				PRs: []PRRef{{Num: 3, Role: RoleCompleting}}},
		},
	}
	assertError(t, reg, "i1", "umbrella must carry no prs")
}

// Invariant 6: DONE leaf without a completing PR.
func TestValidate_Inv6_DoneLeafNoCompletingPR(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "done no pr", Status: StatusDone, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "DONE leaf must have exactly 1 completing PR (found 0)")
}

// Invariant 6: umbrella DONE with non-DONE child.
func TestValidate_Inv6_UmbrellaDoneWithOpenChild(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "umbrella", Status: StatusDone, Kind: "umbrella", Owner: "agent"},
			{ID: "i1a", Title: "open child", Status: StatusOpen, Kind: "leaf", Owner: "agent", Parent: "i1"},
		},
	}
	assertError(t, reg, "i1", "umbrella marked DONE but child i1a is OPEN")
}

// Invariant 7: non-umbrella with >1 completing PR.
func TestValidate_Inv7_MultipleCompletingPRs(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "two completing PRs", Status: StatusDone, Kind: "leaf", Owner: "agent",
				PRs: []PRRef{
					{Num: 1, Role: RoleCompleting},
					{Num: 2, Role: RoleCompleting},
				}},
		},
	}
	assertError(t, reg, "i1", "split into sub-items")
}

// Invariant 8: title too long.
func TestValidate_Inv8_TitleTooLong(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: strings.Repeat("x", 121), Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "title exceeds 120 chars")
}

// Invariant 8: description too long.
func TestValidate_Inv8_DescTooLong(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "ok", Description: strings.Repeat("x", 601), Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "description exceeds 600 chars")
}

// Invariant 8: description too many lines.
func TestValidate_Inv8_DescTooManyLines(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{
				ID:          "i1",
				Title:       "ok",
				Description: "line1\nline2\nline3\nline4\nline5\nline6\nline7",
				Status:      StatusOpen,
				Kind:        "leaf",
				Owner:       "agent",
			},
		},
	}
	assertError(t, reg, "i1", "description exceeds 6 lines")
}

// Invariant 9: BLOCKED without blocker.
func TestValidate_Inv9_BlockedMissingBlocker(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "blocked no blocker", Status: StatusBlocked, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "BLOCKED status requires a non-empty blocker field")
}

// Invariant 9: OPEN with non-empty blocker.
func TestValidate_Inv9_OpenWithBlocker(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "open with blocker", Status: StatusOpen, Blocker: "spurious", Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i1", "OPEN status must have empty blocker")
}

// Invariant 10: id-shaped ref that doesn't exist.
func TestValidate_Inv10_RefNotFound(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "bad ref", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				Refs: []string{"i999"}},
		},
	}
	assertError(t, reg, "i1", `ref "i999" is id-shaped but not found in the registry`)
}

// Invariant 10: non-id-shaped refs pass through without error.
func TestValidate_Inv10_NonIDRefPassThrough(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i1", Title: "non-id refs", Status: StatusOpen, Kind: "leaf", Owner: "agent",
				Refs: []string{"docs/specs/foo.md §3", "https://example.com"}},
		},
	}
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("expected no errors for non-id refs; got:\n%s", strings.Join(ve.msgs, "\n"))
	}
}

// assertError checks that validation produces at least one error message for
// the given id containing the given substring.
func assertError(t *testing.T, reg *Registry, id, substr string) {
	t.Helper()
	ve := validate(reg)
	if !ve.hasErrors() {
		t.Fatalf("expected a validation error for %s %q; got none", id, substr)
	}
	prefix := "registry: ERROR " + id + ":"
	for _, msg := range ve.msgs {
		if strings.HasPrefix(msg, prefix) && strings.Contains(msg, substr) {
			return
		}
	}
	t.Fatalf("expected error for %s containing %q; got:\n%s", id, substr, strings.Join(ve.msgs, "\n"))
}
