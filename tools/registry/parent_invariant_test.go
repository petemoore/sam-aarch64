package main

import (
	"strings"
	"testing"
)

// These tests exercise validate invariant 14 (parent integrity) via the existing
// assertError helper (defined in validate_test.go), which checks validate(reg).

// TestValidate_Inv14a_ParentMissing: a parent: pointing at a non-existent id is an
// error (14a, existence half).
func TestValidate_Inv14a_ParentMissing(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			// i7a declares parent i7, but i7 does not exist.
			{ID: "i7a", Title: "orphan child", Status: StatusOpen, Kind: "leaf", Owner: "agent", Parent: "i7"},
		},
	}
	assertError(t, reg, "i7a", `parent "i7" does not exist`)
}

// TestValidate_Inv14a_ParentIsLeaf: a parent that exists but is a leaf (not an
// umbrella) is an error (14a, kind half).
func TestValidate_Inv14a_ParentIsLeaf(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			// i7 is a leaf (default kind), but i7a claims it as a parent.
			{ID: "i7", Title: "leaf parent", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
			{ID: "i7a", Title: "child of a leaf", Status: StatusOpen, Kind: "leaf", Owner: "agent", Parent: "i7"},
		},
	}
	assertError(t, reg, "i7a", "is not an umbrella")
}

// TestValidate_Inv14b_SubShapedIDWithoutParent: a sub-shaped id (letter and/or
// -bN suffix) with no parent: declared is a floating sub-id — an error.
func TestValidate_Inv14b_SubShapedIDWithoutParent(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			// i7a has a sub-shaped id but no parent field.
			{ID: "i7a", Title: "floating sub-id", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i7a", "must declare a parent")
}

// TestValidate_Inv14b_BrickShapedIDWithoutParent: a -bN brick id with no parent
// is also caught (the sub-shaped check covers the whole non-top-level grammar).
func TestValidate_Inv14b_BrickShapedIDWithoutParent(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i7-b1", Title: "floating brick", Status: StatusOpen, Kind: "leaf", Owner: "agent"},
		},
	}
	assertError(t, reg, "i7-b1", "must declare a parent")
}

// TestValidate_Inv14_ValidUmbrellaChild: a well-formed umbrella + sub-shaped child
// declaring it as parent produces no error.
func TestValidate_Inv14_ValidUmbrellaChild(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i7", Title: "umbrella parent", Status: StatusOpen, Kind: "umbrella", Owner: "agent"},
			{ID: "i7a", Title: "valid child", Status: StatusOpen, Kind: "leaf", Owner: "agent", Parent: "i7"},
		},
	}
	ve := validate(reg)
	if ve.hasErrors() {
		t.Fatalf("expected no errors for valid umbrella+child; got:\n%s", strings.Join(ve.msgs, "\n"))
	}
}

// TestValidate_Inv14c_ParentCycle: a parent chain that loops is an error (14c).
// i7 (umbrella) parents i8 (umbrella) which parents i7 — a 2-node parent cycle.
// Both ids are top-level so 14b does not fire; both parents exist and are
// umbrellas so 14a does not fire — only the cycle check trips.
func TestValidate_Inv14c_ParentCycle(t *testing.T) {
	reg := &Registry{
		Items: []Item{
			{ID: "i7", Title: "umbrella A", Status: StatusOpen, Kind: "umbrella", Owner: "agent", Parent: "i8"},
			{ID: "i8", Title: "umbrella B", Status: StatusOpen, Kind: "umbrella", Owner: "agent", Parent: "i7"},
		},
	}
	assertError(t, reg, "i7", "parent cycle detected")
}
