package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
)

// mutatorPaths holds the canonical paths for the items and questions YAML sources.
// Mutators receive these at startup from main and pass them through to the
// write-validate-gen pipeline.
type mutatorPaths struct {
	itemsYAML     string
	questionsYAML string
	registryDir   string // directory containing .id-ledger.txt
}

// applyAndCommit is the canonical end-of-mutator pipeline:
//
//  1. sort the in-memory registry into canonical order (so validate's sort
//     check passes even when a mutator appended a record at the end);
//  2. validate the registry — if it fails print errors and exit 1, leaving
//     the source files unchanged;
//  3. write canonical YAML to the source files;
//  4. re-load from disk and run gen (printing the three views to stdout),
//     so the caller can confirm the regenerated output is consistent.
//
// Because mutators modify reg in memory first and only write here (after
// validate passes), the "leaving source unchanged on error" invariant holds.
func applyAndCommit(reg *Registry, paths mutatorPaths) {
	// Sort in-memory before validate so that a record appended at the tail
	// does not trip the canonical-order invariant.
	reg.Items = sortedItems(reg.Items)
	reg.Questions = sortedQuestions(reg.Questions)

	ve := validate(reg)
	if ve.hasErrors() {
		for _, msg := range ve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}
	if err := writeItems(paths.itemsYAML, reg.Items); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeQuestions(paths.questionsYAML, reg.Questions); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Re-load from disk and regenerate the three views (printed to stdout).
	// Re-loading ensures the canonical serializer's output round-trips cleanly.
	reg2, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	genToStdout(reg2)
}

// loadReg loads both YAML files into a Registry.
func loadReg(paths mutatorPaths) (*Registry, error) {
	items, err := loadItems(paths.itemsYAML)
	if err != nil {
		return nil, err
	}
	questions, err := loadQuestions(paths.questionsYAML)
	if err != nil {
		return nil, err
	}
	return &Registry{Items: items, Questions: questions}, nil
}

// genToStdout runs the gen pipeline and prints the three generated views to
// stdout (dormant mode: does not touch docs/notes/*.md).
func genToStdout(reg *Registry) {
	var itemsOpen, itemsClosed, qOpen bytes.Buffer
	if err := genItemsOpenClosed(reg, &itemsOpen, &itemsClosed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := genQuestionsOpen(reg, &qOpen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print("=== item-registry-open ===\n")
	os.Stdout.Write(itemsOpen.Bytes())
	fmt.Print("\n=== item-registry-closed ===\n")
	os.Stdout.Write(itemsClosed.Bytes())
	fmt.Print("\n=== question-registry-open ===\n")
	os.Stdout.Write(qOpen.Bytes())
}

// runNextID implements `next-id [--space items|questions]`.
// Prints the next free id, consulting both the live source ids and the
// append-only .id-ledger.txt so that a previously-deleted id is never re-minted
// (spec invariant 1).
func runNextID(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("next-id", flag.ExitOnError)
	space := fs.String("space", "items", "id-space: items | questions")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch *space {
	case "items":
		fmt.Println(nextItemID(reg, ledger))
	case "questions":
		fmt.Println(nextQuestionID(reg, ledger))
	default:
		fmt.Fprintf(os.Stderr, "registry next-id: unknown space %q (must be items|questions)\n", *space)
		os.Exit(2)
	}
}

// runAdd implements `add --id … --title … --desc … --status … --owner … [--parent …] [--dep …]… [--ref …]…`.
// Appends a canonical record (item or question by id shape), re-canonicalizes,
// validates, and runs gen.
func runAdd(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	id := fs.String("id", "", "id of the new record (item: iN[…]; question: qN[letter])")
	title := fs.String("title", "", "title (≤120 chars, single line; items only)")
	desc := fs.String("desc", "", "description (items) or body (questions)")
	status := fs.String("status", "", "status: OPEN|IN_PROGRESS|DONE|WONTFIX (items only)")
	owner := fs.String("owner", "", "owner: agent|pete|name")
	parent := fs.String("parent", "", "umbrella parent id (items only, optional)")
	var deps, refs multiFlag
	fs.Var(&deps, "dep", "depends_on edge (repeatable; items only)")
	fs.Var(&refs, "ref", "ref entry (repeatable; items only)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry add: --id is required")
		os.Exit(2)
	}
	if *owner == "" {
		fmt.Fprintln(os.Stderr, "registry add: --owner is required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if questionIDRe.MatchString(*id) {
		// Question record.
		if *desc == "" {
			fmt.Fprintln(os.Stderr, "registry add: --desc (question body) is required")
			os.Exit(2)
		}
		q := Question{
			ID:    *id,
			Body:  *desc,
			Owner: *owner,
		}
		reg.Questions = append(reg.Questions, q)
	} else if itemIDRe.MatchString(*id) {
		// Item record.
		if *title == "" {
			fmt.Fprintln(os.Stderr, "registry add: --title is required for item records")
			os.Exit(2)
		}
		if *status == "" {
			fmt.Fprintln(os.Stderr, "registry add: --status is required for item records")
			os.Exit(2)
		}
		it := Item{
			ID:          *id,
			Title:       *title,
			Description: *desc,
			Status:      Status(*status),
			DependsOn:   []string(deps),
			Kind:        "leaf",
			Owner:       *owner,
			Parent:      *parent,
			Refs:        []string(refs),
		}
		if it.DependsOn == nil {
			it.DependsOn = []string{}
		}
		if it.Refs == nil {
			it.Refs = []string{}
		}
		reg.Items = append(reg.Items, it)
	} else {
		fmt.Fprintf(os.Stderr, "registry add: --id %q does not match item (i…) or question (q…) grammar\n", *id)
		os.Exit(2)
	}

	// Record in the ledger before validate+gen so the id is persisted even if
	// we exit non-zero on validation (the id was minted; it must never be reused).
	if err := appendToLedger(ledgerPath(paths.registryDir), *id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	applyAndCommit(reg, paths)
	fmt.Printf("registry: added %s\n", *id)
}

// runSplit implements `split --parent iN --child-id iN-bM --title …`.
// Sets the parent kind:umbrella, adds a leaf child, and rewrites dependents:
// any item with depends_on:[parentID] has parentID replaced by all children
// (conservative reading — spec §"Dependencies": "Split-rewrites-dependents").
func runSplit(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	parentID := fs.String("parent", "", "existing item to promote to umbrella")
	childID := fs.String("child-id", "", "new child item id")
	childTitle := fs.String("title", "", "title for the new child item")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *parentID == "" || *childID == "" || *childTitle == "" {
		fmt.Fprintln(os.Stderr, "registry split: --parent, --child-id, and --title are all required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Find and promote the parent to umbrella.
	parentIdx := -1
	for i := range reg.Items {
		if reg.Items[i].ID == *parentID {
			parentIdx = i
			break
		}
	}
	if parentIdx == -1 {
		fmt.Fprintf(os.Stderr, "registry split: --parent %q not found\n", *parentID)
		os.Exit(1)
	}
	reg.Items[parentIdx].Kind = "umbrella"
	// Umbrellas carry no completing PRs (spec §"One-PR / umbrella semantics").
	reg.Items[parentIdx].PRs = nil

	// Collect existing children of this parent (already-existing leaves before this split).
	existingChildren := []string{}
	for _, it := range reg.Items {
		if it.Parent == *parentID {
			existingChildren = append(existingChildren, it.ID)
		}
	}

	// Add the new child leaf.
	child := Item{
		ID:     *childID,
		Title:  *childTitle,
		Status: StatusOpen,
		Kind:   "leaf",
		Owner:  reg.Items[parentIdx].Owner,
		Parent: *parentID,
	}
	reg.Items = append(reg.Items, child)

	// Record the new child id in the ledger.
	if err := appendToLedger(ledgerPath(paths.registryDir), *childID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// All children of this parent after the split (existing + new).
	allChildren := append(existingChildren, *childID)

	// Rewrite dependents: any item with depends_on containing parentID gets
	// parentID replaced by all allChildren (conservative reading).
	for i := range reg.Items {
		if reg.Items[i].ID == *parentID || reg.Items[i].ID == *childID {
			continue
		}
		newDeps := []string{}
		rewrote := false
		for _, dep := range reg.Items[i].DependsOn {
			if dep == *parentID {
				newDeps = append(newDeps, allChildren...)
				rewrote = true
			} else {
				newDeps = append(newDeps, dep)
			}
		}
		if rewrote {
			reg.Items[i].DependsOn = dedupStrings(newDeps)
		}
	}

	applyAndCommit(reg, paths)
	fmt.Printf("registry: split %s → umbrella; added child %s; rewrote dependents onto %v\n",
		*parentID, *childID, allChildren)
}

// runSetStatus implements `set-status --id iN --status … [--pr N]`.
// Changes the status of an item; open↔closed move is automatic on regen.
// --pr attaches a completing PR for DONE transitions.
func runSetStatus(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("set-status", flag.ExitOnError)
	id := fs.String("id", "", "item id to update")
	status := fs.String("status", "", "new status: OPEN|IN_PROGRESS|DONE|WONTFIX")
	prNum := fs.Int("pr", 0, "completing PR number to attach (for DONE)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" || *status == "" {
		fmt.Fprintln(os.Stderr, "registry set-status: --id and --status are required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	idx := -1
	for i := range reg.Items {
		if reg.Items[i].ID == *id {
			idx = i
			break
		}
	}
	if idx == -1 {
		fmt.Fprintf(os.Stderr, "registry set-status: id %q not found\n", *id)
		os.Exit(1)
	}

	reg.Items[idx].Status = Status(*status)
	if *prNum > 0 {
		reg.Items[idx].PRs = append(reg.Items[idx].PRs, PRRef{Num: *prNum, Role: RoleCompleting})
	}

	applyAndCommit(reg, paths)
	fmt.Printf("registry: %s status → %s\n", *id, *status)
}

// runSetPR implements `set-pr --id iN --pr N [--role completing|followup]`.
// Attaches a PR reference to an item.
func runSetPR(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("set-pr", flag.ExitOnError)
	id := fs.String("id", "", "item id")
	prNum := fs.Int("pr", 0, "PR number")
	role := fs.String("role", "completing", "PR role: completing|followup")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" || *prNum == 0 {
		fmt.Fprintln(os.Stderr, "registry set-pr: --id and --pr are required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	idx := -1
	for i := range reg.Items {
		if reg.Items[i].ID == *id {
			idx = i
			break
		}
	}
	if idx == -1 {
		fmt.Fprintf(os.Stderr, "registry set-pr: id %q not found\n", *id)
		os.Exit(1)
	}

	reg.Items[idx].PRs = append(reg.Items[idx].PRs, PRRef{Num: *prNum, Role: PRRole(*role)})
	applyAndCommit(reg, paths)
	fmt.Printf("registry: attached PR #%d (role:%s) to %s\n", *prNum, *role, *id)
}

// runDep implements `dep add|rm --id iN --on iM|qN`.
// Adds or removes a depends_on edge. Validate (which includes the cycle check
// and WONTFIX-target check) runs as part of applyAndCommit, so a cycle or
// WONTFIX-target is rejected with the invariant error before any file is written.
func runDep(args []string, paths mutatorPaths) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "registry dep: need add|rm subcommand")
		os.Exit(2)
	}
	action := args[0]
	if action != "add" && action != "rm" {
		fmt.Fprintf(os.Stderr, "registry dep: unknown action %q (must be add|rm)\n", action)
		os.Exit(2)
	}

	fs := flag.NewFlagSet("dep "+action, flag.ExitOnError)
	id := fs.String("id", "", "item id to modify")
	on := fs.String("on", "", "target id to add/remove from depends_on")
	fs.Parse(args[1:]) //nolint:errcheck // ExitOnError handles

	if *id == "" || *on == "" {
		fmt.Fprintf(os.Stderr, "registry dep %s: --id and --on are required\n", action)
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	idx := -1
	for i := range reg.Items {
		if reg.Items[i].ID == *id {
			idx = i
			break
		}
	}
	if idx == -1 {
		fmt.Fprintf(os.Stderr, "registry dep: id %q not found\n", *id)
		os.Exit(1)
	}

	switch action {
	case "add":
		// Check that target exists.
		allIDs := map[string]bool{}
		for _, it := range reg.Items {
			allIDs[it.ID] = true
		}
		for _, q := range reg.Questions {
			allIDs[q.ID] = true
		}
		if !allIDs[*on] {
			fmt.Fprintf(os.Stderr, "registry dep add: target %q does not exist in the registry\n", *on)
			os.Exit(1)
		}
		// Pre-flight WONTFIX check (invariant 12) with a clear message before
		// we hit the generic validator error.
		for _, it := range reg.Items {
			if it.ID == *on && it.Status == StatusWontfix && reg.Items[idx].Status != StatusWontfix {
				fmt.Fprintf(os.Stderr,
					"registry dep add: REJECTED — target %q is WONTFIX; "+
						"a non-WONTFIX item may not depend on a WONTFIX node (invariant 12)\n", *on)
				os.Exit(1)
			}
		}
		// Idempotency guard.
		for _, existing := range reg.Items[idx].DependsOn {
			if existing == *on {
				fmt.Fprintf(os.Stderr, "registry dep add: %s already depends on %s\n", *id, *on)
				os.Exit(1)
			}
		}
		reg.Items[idx].DependsOn = append(reg.Items[idx].DependsOn, *on)
		// applyAndCommit runs validate which includes the full cycle check (inv 11).

	case "rm":
		found := false
		newDeps := []string{}
		for _, dep := range reg.Items[idx].DependsOn {
			if dep == *on {
				found = true
			} else {
				newDeps = append(newDeps, dep)
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "registry dep rm: %s does not depend on %s\n", *id, *on)
			os.Exit(1)
		}
		reg.Items[idx].DependsOn = newDeps
	}

	applyAndCommit(reg, paths)
	fmt.Printf("registry: dep %s --id %s --on %s\n", action, *id, *on)
}

// runAnswer implements `answer --id qN`.
// Deletes the question but fails (exit 1, clear message) if any item still
// depends_on it — this is the structural delete-gate (invariant 13 / spec
// §"Questions — transient by design": "The delete-gate is the no-information-loss
// guarantee").
func runAnswer(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	id := fs.String("id", "", "question id to delete (qN)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry answer: --id is required")
		os.Exit(2)
	}
	if !questionIDRe.MatchString(*id) {
		fmt.Fprintf(os.Stderr, "registry answer: %q is not a valid question id (must match ^q[0-9]+[a-z]?$)\n", *id)
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Delete-gate (invariant 13): fail explicitly if any item still depends_on
	// this question. The error message directs the caller to curate dependents
	// first, then retry.
	var blockers []string
	for _, it := range reg.Items {
		for _, dep := range it.DependsOn {
			if dep == *id {
				blockers = append(blockers, it.ID)
			}
		}
	}
	if len(blockers) > 0 {
		fmt.Fprintf(os.Stderr,
			"registry answer: REJECTED — cannot delete %s while %d item(s) still depend on it: %s\n"+
				"  Curate dependents first (registry dep rm / set-status / add), then retry.\n",
			*id, len(blockers), strings.Join(blockers, ", "))
		os.Exit(1)
	}

	// Find and remove the question.
	found := false
	newQs := []Question{}
	for _, q := range reg.Questions {
		if q.ID == *id {
			found = true
		} else {
			newQs = append(newQs, q)
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "registry answer: question %q not found\n", *id)
		os.Exit(1)
	}
	reg.Questions = newQs

	applyAndCommit(reg, paths)
	fmt.Printf("registry: deleted question %s (all dependents curated)\n", *id)
}

// multiFlag is a flag.Value that accumulates repeated --flag values into a slice.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// dedupStrings removes duplicate strings, preserving first-occurrence order.
func dedupStrings(ss []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
