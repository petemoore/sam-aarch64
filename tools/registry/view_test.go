package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// viewReg is a small registry for view tests: i10 (with deps/refs/pr) depends on
// i20; i20 is an umbrella; q1 is an open question that i10 depends on.
func viewReg() *Registry {
	return &Registry{
		Items: []Item{
			{
				ID: "i10", Title: "Leaf with everything", Status: StatusOpen,
				Kind: "leaf", Owner: "agent", DependsOn: []string{"i20", "q1"},
				Refs: []string{"docs/x.md"}, PRs: []PRRef{{Num: 7, Role: RoleCompleting}},
				Description: "first line\nsecond line",
			},
			{ID: "i20", Title: "An umbrella", Status: StatusOpen, Kind: "umbrella", Owner: "pete"},
			{ID: "i30", Title: "Depends on i10", Status: StatusOpen, Kind: "leaf", Owner: "agent", DependsOn: []string{"i10"}},
		},
		Questions: []Question{{ID: "q1", Body: "should we?", Owner: "pete"}},
		Priority:  []string{"i10", "i30"},
	}
}

func TestView_ItemText(t *testing.T) {
	var buf bytes.Buffer
	if err := writeView(&buf, viewReg(), "i10", "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"i10  [OPEN]  owner:agent",
		"title:       Leaf with everything",
		"priority:    rank 1",
		"depends_on:  i20, q1",
		"dependents:  i30", // i30 depends on i10 (computed reverse edge)
		"prs:         #7 (completing)",
		"refs:        docs/x.md",
		"  first line",
		"  second line",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q.\nGot:\n%s", want, out)
		}
	}
}

func TestView_UmbrellaTagged(t *testing.T) {
	var buf bytes.Buffer
	if err := writeView(&buf, viewReg(), "i20", "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "umbrella") {
		t.Errorf("umbrella item should be tagged; got:\n%s", buf.String())
	}
}

func TestView_QuestionText(t *testing.T) {
	var buf bytes.Buffer
	if err := writeView(&buf, viewReg(), "q1", "text"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "q1  [question]  owner:pete") {
		t.Errorf("question header missing; got:\n%s", out)
	}
	// i10 depends on q1 → q1's dependents include i10.
	if !strings.Contains(out, "dependents:  i10") {
		t.Errorf("question dependents missing i10; got:\n%s", out)
	}
}

func TestView_UnknownIDErrors(t *testing.T) {
	var buf bytes.Buffer
	err := writeView(&buf, viewReg(), "i999", "text")
	if err == nil {
		t.Fatal("expected error for unknown id, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found; got: %v", err)
	}
}

func TestView_ItemJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeView(&buf, viewReg(), "i10", "json"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got["id"] != "i10" {
		t.Errorf("json id = %v, want i10", got["id"])
	}
	if got["priority_rank"].(float64) != 1 {
		t.Errorf("json priority_rank = %v, want 1", got["priority_rank"])
	}
	deps, ok := got["dependents"].([]any)
	if !ok || len(deps) != 1 || deps[0] != "i30" {
		t.Errorf("json dependents = %v, want [i30]", got["dependents"])
	}
}

// TestView_JSONEmptySlicesNotNull asserts that an item with no depends_on/prs/refs
// emits [] (jq-friendly), never null.
func TestView_JSONEmptySlicesNotNull(t *testing.T) {
	reg := &Registry{Items: []Item{{ID: "i40", Title: "bare", Status: StatusOpen, Kind: "leaf", Owner: "agent"}}}
	var buf bytes.Buffer
	if err := writeView(&buf, reg, "i40", "json"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, field := range []string{`"depends_on": []`, `"prs": []`, `"refs": []`, `"dependents": []`} {
		if !strings.Contains(out, field) {
			t.Errorf("expected %s in JSON (not null); got:\n%s", field, out)
		}
	}
}
