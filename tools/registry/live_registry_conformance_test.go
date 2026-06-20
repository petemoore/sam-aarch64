package main

import (
	"path/filepath"
	"testing"
)

// TestLiveRegistry_IDGrammarConformance is the guard Pete asked for against
// hand-edits to the LIVE registry: every item id must match itemIDRe, every
// question id must match questionIDRe, and every sub-shaped item id (one with a
// letter and/or -bN suffix) must declare a parent that exists and is an umbrella.
//
// It loads the real registry/items.yaml + registry/questions.yaml (discovered by
// walking up to the repo root for the Makefile), not the testdata fixtures, so a
// hand-edit that introduces a malformed or orphaned id fails this test directly.
func TestLiveRegistry_IDGrammarConformance(t *testing.T) {
	repoRoot := findRepoRootRegistry(t)
	itemsPath := filepath.Join(repoRoot, "registry", "items.yaml")
	questionsPath := filepath.Join(repoRoot, "registry", "questions.yaml")

	items, err := loadItems(itemsPath)
	if err != nil {
		t.Fatalf("load live items.yaml: %v", err)
	}
	questions, err := loadQuestions(questionsPath)
	if err != nil {
		t.Fatalf("load live questions.yaml: %v", err)
	}

	// Guard against a vacuous pass: the live registry has many items, so an empty
	// load means the loader or path resolution silently broke.
	if len(items) == 0 {
		t.Fatalf("loaded 0 items from %s — live registry not found or empty", itemsPath)
	}

	// Build the kind map for the parent-is-umbrella check.
	kindByID := map[string]string{}
	existing := map[string]bool{}
	for _, it := range items {
		kindByID[it.ID] = it.Kind
		existing[it.ID] = true
	}

	// Every item id matches the item grammar.
	for _, it := range items {
		if !itemIDRe.MatchString(it.ID) {
			t.Errorf("live item id %q does not match itemIDRe %s", it.ID, itemIDRe.String())
			continue
		}
		// Sub-shaped ids (letter and/or -bN suffix) must declare a valid parent.
		if !topLevelItemIDRe.MatchString(it.ID) {
			if it.Parent == "" {
				t.Errorf("live sub-shaped item id %q does not declare a parent", it.ID)
				continue
			}
			if !existing[it.Parent] {
				t.Errorf("live item %q declares parent %q which does not exist", it.ID, it.Parent)
				continue
			}
			if kindByID[it.Parent] != "umbrella" {
				t.Errorf("live item %q declares parent %q which is not an umbrella (kind=%q)",
					it.ID, it.Parent, kindByID[it.Parent])
			}
		}
	}

	// Every question id matches the question grammar.
	for _, q := range questions {
		if !questionIDRe.MatchString(q.ID) {
			t.Errorf("live question id %q does not match questionIDRe %s", q.ID, questionIDRe.String())
		}
	}
}
