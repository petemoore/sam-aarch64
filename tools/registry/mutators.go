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
	priorityYAML  string // registry/priority.yaml (absent = no priority order)
	registryDir   string // directory containing .id-ledger.txt
	templatesDir  string // directory containing *.head.md header templates
	outDir        string // write generated .md files here; empty → stdout mode
	migrating     bool   // defer invariant 10 (id-shaped ref existence)
}

// reconcilePriority rebuilds reg.Priority to equal exactly the pullable set
// while preserving Pete's curated ordering of items that are still pullable.
// The algorithm:
//  1. Keep every id in the current priority that is still pullable, in order.
//  2. Append every pullable id NOT already listed, in canonical id sort order
//     (deterministic: new items land at the end where Pete can re-rank them).
//
// The result satisfies validatePriority's strict-permutation invariant:
// exactly the pullable set, each id once, no closed/umbrella ids.
//
// reconcilePriority is a no-op when paths.priorityYAML is "" (dormant mode)
// — callers must guard on that condition.
func reconcilePriority(reg *Registry) {
	pullable := pullableItems(reg.Items)

	// Collect existing ranked ids that are still pullable (preserve order).
	kept := make([]string, 0, len(reg.Priority))
	inKept := map[string]bool{}
	for _, id := range reg.Priority {
		if pullable[id] {
			kept = append(kept, id)
			inKept[id] = true
		}
	}

	// Append any pullable id not already in the kept list, in canonical order.
	for _, it := range sortedItems(reg.Items) {
		if pullable[it.ID] && !inKept[it.ID] {
			kept = append(kept, it.ID)
		}
	}

	// Repair ordering so the queue is a valid topological extension of the
	// dependency DAG. Membership reconciliation above preserves Pete's ranking
	// but can leave a dependent ahead of a newly-added dependency (e.g. after
	// `dep add`); this slides each dependency just ahead of its dependents with
	// minimal perturbation, so validatePriority passes instead of the mutation
	// being rejected. (i146 ordering-repair — "the queue always stays correct".)
	reg.Priority = topoRepairPriority(reg.Items, kept)
}

// topoRepairPriority returns a permutation of `order` that is a valid topological
// extension of the pullable dependency DAG: every id appears after all of its
// depends_on targets that are also in `order`. It is a STABLE repair — when
// `order` is already valid it is returned unchanged, and when it is not the
// result preserves Pete's curated ranking as closely as possible.
//
// Algorithm: a depth-first topological sort that visits roots in the ORIGINAL
// priority order and emits each item only after its (in-queue) dependencies. The
// effect is that every item keeps its rank except that a dependency is pulled
// FORWARD to just before its earliest dependent — the curation-preserving choice
// (a high-priority item blocked by a low-ranked gate pulls the gate up, rather
// than the item being demoted below it). On an already-valid input it reproduces
// the input exactly. This is what lets `dep add` (and prioritize/move) keep the
// queue topologically valid automatically instead of failing validation.
//
// An edge to an umbrella expands to the umbrella's in-queue pullable leaves
// (matching validatePriority's check 4), so a dependency on an umbrella still
// pulls its real prerequisite leaves forward. Edges to other out-of-queue ids
// (closed deps, questions) impose no ordering constraint and are ignored.
//
// A cycle (which validate rejects separately) cannot livelock this: the on-stack
// guard breaks the recursion and the node is still emitted as the stack unwinds,
// so no id is ever dropped.
func topoRepairPriority(items []Item, order []string) []string {
	inQueue := make(map[string]bool, len(order))
	for _, id := range order {
		inQueue[id] = true
	}
	itemKind := make(map[string]string, len(items))
	for _, it := range items {
		itemKind[it.ID] = it.Kind
	}
	children := childrenByParent(items)
	// deps[id] = id's depends_on targets that are in the queue (in declared
	// order), with an umbrella target expanded to its in-queue pullable leaves.
	// Edges out of the queue carry no ordering constraint here.
	deps := make(map[string][]string, len(order))
	for _, it := range items {
		if !inQueue[it.ID] {
			continue
		}
		var ds []string
		for _, dep := range it.DependsOn {
			for _, target := range expandDepTarget(itemKind, children, inQueue, dep, map[string]bool{}) {
				if inQueue[target] {
					ds = append(ds, target)
				}
			}
		}
		deps[it.ID] = ds
	}

	const (
		unseen  = 0
		onStack = 1
		emitted = 2
	)
	state := make(map[string]int, len(order))
	out := make([]string, 0, len(order))

	var visit func(id string)
	visit = func(id string) {
		switch state[id] {
		case emitted:
			return
		case onStack:
			return // cycle — stop recursing; emitted as this frame unwinds
		}
		state[id] = onStack
		for _, dep := range deps[id] {
			visit(dep)
		}
		state[id] = emitted
		out = append(out, id)
	}

	// Visiting roots in the original order is what preserves the curated ranking:
	// items keep their rank except where a dependency is pulled forward.
	for _, id := range order {
		visit(id)
	}
	return out
}

// equalStringSlice reports whether two string slices are element-wise equal.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyAndCommit is the canonical end-of-mutator pipeline:
//
//  1. sort the in-memory registry into canonical order (so validate's sort
//     check passes even when a mutator appended a record at the end);
//  2. when a priority file is active, reconcile the priority queue so that
//     it equals exactly the pullable set (drop closed/umbrella ids, append
//     new pullable ids) — this keeps the queue consistent without requiring
//     hand-edits after add/set-status/split;
//  3. validate the registry — if it fails print errors and exit 1, leaving
//     the source files unchanged;
//  4. write canonical YAML (items, questions, and when active: priority)
//     to the source files;
//  5. re-load from disk and run gen (to stdout or to paths.outDir),
//     so the caller can confirm the regenerated output is consistent.
//
// Because mutators modify reg in memory first and only write here (after
// validate passes), the "leaving source unchanged on error" invariant holds.
func applyAndCommit(reg *Registry, paths mutatorPaths) {
	// Sort in-memory before validate so that a record appended at the tail
	// does not trip the canonical-order invariant.
	reg.Items = sortedItems(reg.Items)
	reg.Questions = sortedQuestions(reg.Questions)

	// Auto-maintain the priority queue when a priority file is active.
	// This eliminates the hand-edit burden after add/set-status/split.
	if paths.priorityYAML != "" {
		reconcilePriority(reg)
	}

	ve := validateWith(reg, validateOpts{migrating: paths.migrating})
	// Include priority validation when a priority list is loaded.
	if len(reg.Priority) > 0 {
		pve := validatePriority(reg, reg.Priority)
		ve.msgs = append(ve.msgs, pve.msgs...)
	}
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
	// Persist the reconciled priority queue alongside items and questions.
	// Only written when a priority file is active; guards against clobbering
	// a non-existent file in dormant/stdout modes.
	if paths.priorityYAML != "" {
		if err := writePriority(paths.priorityYAML, reg.Priority); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// Re-load from disk and regenerate the three views.
	// Re-loading ensures the canonical serializer's output round-trips cleanly.
	reg2, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	genToOutDirOrStdout(reg2, paths)
}

// loadReg loads items, questions, and priority into a Registry.
func loadReg(paths mutatorPaths) (*Registry, error) {
	// No data source resolved — refuse rather than read an empty/guessed path.
	// The CLI never falls back to the bundled test fixtures (an accidental
	// testdata read/write could be catastrophic).
	if paths.itemsYAML == "" {
		return nil, fmt.Errorf("registry: no data source — run from the repo root so registry/items.yaml is found by walking up, or set REGISTRY_ITEMS explicitly; the CLI never falls back to the bundled test fixtures")
	}
	items, err := loadItems(paths.itemsYAML)
	if err != nil {
		return nil, err
	}
	questions, err := loadQuestions(paths.questionsYAML)
	if err != nil {
		return nil, err
	}
	var priority []string
	if paths.priorityYAML != "" {
		priority, err = loadPriority(paths.priorityYAML)
		if err != nil {
			return nil, err
		}
	}
	return &Registry{Items: items, Questions: questions, Priority: priority}, nil
}

// genToOutDirOrStdout runs the gen pipeline. When paths.outDir is set, it
// writes the four .md files into that directory (in-place mode). When outDir
// is empty, it prints the four views to stdout (dormant/stdout mode).
// The four views are: item-open, item-closed, question-open, backlog.
// backlog.md is only written when reg.Priority is non-empty.
func genToOutDirOrStdout(reg *Registry, paths mutatorPaths) {
	var itemsOpen, itemsClosed, qOpen bytes.Buffer
	if err := genItemsOpenClosed(reg, &itemsOpen, &itemsClosed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := genQuestionsOpen(reg, &qOpen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var backlog bytes.Buffer
	hasBacklog := len(reg.Priority) > 0
	if hasBacklog {
		if err := genBacklog(reg, reg.Priority, &backlog); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if paths.outDir == "" {
		fmt.Print("=== item-registry-open ===\n")
		os.Stdout.Write(itemsOpen.Bytes())
		fmt.Print("\n=== item-registry-closed ===\n")
		os.Stdout.Write(itemsClosed.Bytes())
		fmt.Print("\n=== question-registry-open ===\n")
		os.Stdout.Write(qOpen.Bytes())
		if hasBacklog {
			fmt.Print("\n=== backlog ===\n")
			os.Stdout.Write(backlog.Bytes())
		}
		return
	}

	// In-place mode: write each view with its template header inserted between
	// the banner and the table (spec §"Generator" — in-place gen).
	writes := []struct {
		filename string
		tmpl     string
		content  []byte
	}{
		{"item-registry-open.md", "item-registry-open.head.md", itemsOpen.Bytes()},
		{"item-registry-closed.md", "item-registry-closed.head.md", itemsClosed.Bytes()},
		{"question-registry-open.md", "question-registry-open.head.md", qOpen.Bytes()},
	}
	for _, w := range writes {
		data, err := assembleGenFile(paths.templatesDir, w.tmpl, w.content)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		outPath := paths.outDir + "/" + w.filename
		if err := atomicWriteFile(outPath, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "registry gen: write %s: %v\n", outPath, err)
			os.Exit(1)
		}
	}

	// Backlog is only written when a priority list exists.
	if hasBacklog {
		data, err := assembleGenFile(paths.templatesDir, "backlog.head.md", backlog.Bytes())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		outPath := paths.outDir + "/backlog.md"
		if err := atomicWriteFile(outPath, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "registry gen: write %s: %v\n", outPath, err)
			os.Exit(1)
		}
	}
}

// assembleGenFile builds the content for one generated registry .md file:
// (a) the generated banner (already at the start of genContent), (b) a blank
// line, (c) the template header prose read from templatesDir/tmplName, (d) the
// generated markdown table (everything after the banner in genContent).
//
// The genContent produced by genItemsOpenClosed / genQuestionsOpen is:
//   banner + "\n" + table
//
// We splice the template between the banner-trailing-newline and the table.
func assembleGenFile(templatesDir, tmplName string, genContent []byte) ([]byte, error) {
	tmplPath := templatesDir + "/" + tmplName
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return nil, fmt.Errorf("registry gen: read template %s: %w", tmplPath, err)
	}

	// genContent layout: "<banner>\n<table...>".
	// Find the end of the banner block (the first blank line, i.e. "\n\n").
	sep := []byte("\n\n")
	idx := indexBytes(genContent, sep)
	if idx < 0 {
		// Fallback: no blank line found; treat the whole content as table.
		return append(append(tmplData, '\n'), genContent...), nil
	}

	// banner is everything up to and including the first "\n" after the block.
	banner := genContent[:idx+1]  // up to (but not including) the second \n
	table := genContent[idx+1:]   // starts at the blank line then the table

	// Output: banner + "\n" + template + table
	// (The template files already end with a trailing newline, so no extra needed.)
	var out []byte
	out = append(out, banner...)
	out = append(out, '\n')
	out = append(out, tmplData...)
	out = append(out, table...)
	return out, nil
}

// indexBytes returns the index of the first occurrence of sep in s, or -1.
func indexBytes(s, sep []byte) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		match := true
		for j := 0; j < len(sep); j++ {
			if s[i+j] != sep[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// runAdd implements `add [--space items|questions] --owner … (items: --title --status [--desc] [--kind] [--pr N [--role …]] [--dep …]… [--ref …]…) (questions: --desc)`.
// The id is ALWAYS tool-determined (next free top-level iN / qN) — never supplied
// by the caller (spec: ids are tool-determined, never hand-picked, so there is no
// next-id→add race and no wrong/duplicate id). SUB-items (children of an umbrella)
// are created with `split`, which determines the child id from the parent. There
// is therefore no `--id` and no `--parent` on add.
func runAdd(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	space := fs.String("space", "items", "id-space: items | questions")
	title := fs.String("title", "", "title (≤120 chars, single line; items only)")
	desc := fs.String("desc", "", "description (items) or body (questions)")
	status := fs.String("status", "", "status: OPEN|IN_PROGRESS|DONE|WONTFIX (items only)")
	owner := fs.String("owner", "", "owner: agent|pete|name")
	kind := fs.String("kind", "leaf", "item kind: leaf|umbrella (items only)")
	prNum := fs.Int("pr", 0, "completing PR number to attach (items only; required for a DONE leaf)")
	prRole := fs.String("role", "completing", "role for --pr: completing|followup")
	var deps, refs multiFlag
	fs.Var(&deps, "dep", "depends_on edge (repeatable; items only)")
	fs.Var(&refs, "ref", "ref entry (repeatable; items only)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *space != "items" && *space != "questions" {
		fmt.Fprintf(os.Stderr, "registry add: unknown --space %q (must be items|questions)\n", *space)
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
	ledger, err := loadLedger(ledgerPath(paths.registryDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var newID string
	if *space == "questions" {
		// Question record — id auto-allocated (next free qN).
		if *desc == "" {
			fmt.Fprintln(os.Stderr, "registry add: --desc (question body) is required")
			os.Exit(2)
		}
		newID = nextQuestionID(reg, ledger)
		reg.Questions = append(reg.Questions, Question{
			ID:    newID,
			Body:  *desc,
			Owner: *owner,
		})
	} else {
		// Top-level item record — id auto-allocated (next free iN).
		if *title == "" {
			fmt.Fprintln(os.Stderr, "registry add: --title is required for item records")
			os.Exit(2)
		}
		if *status == "" {
			fmt.Fprintln(os.Stderr, "registry add: --status is required for item records")
			os.Exit(2)
		}
		newID = nextItemID(reg, ledger)
		it := Item{
			ID:          newID,
			Title:       *title,
			Description: *desc,
			Status:      Status(*status),
			DependsOn:   []string(deps),
			Kind:        *kind,
			Owner:       *owner,
			Refs:        []string(refs),
		}
		if *prNum > 0 {
			it.PRs = append(it.PRs, PRRef{Num: *prNum, Role: PRRole(*prRole)})
		}
		if it.DependsOn == nil {
			it.DependsOn = []string{}
		}
		if it.Refs == nil {
			it.Refs = []string{}
		}
		reg.Items = append(reg.Items, it)
	}

	// Record in the ledger before validate+gen so the id is persisted even if
	// we exit non-zero on validation (the id was minted; it must never be reused).
	if err := appendToLedger(ledgerPath(paths.registryDir), newID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	applyAndCommit(reg, paths)
	fmt.Printf("registry: added %s\n", newID)
}

// runSplit implements `split --parent iN --title … [--desc …] [--status …] [--owner …] [--kind …] [--pr N [--role …]] [--dep …]… [--ref …]…`.
// Promotes the parent to kind:umbrella, adds a leaf child whose id is
// TOOL-DETERMINED from the parent (nextSubID — the caller never supplies it),
// and rewrites dependents: any item with depends_on:[parentID] has parentID
// replaced by all children (conservative reading — spec §"Dependencies":
// "Split-rewrites-dependents"). This is the only way to create a sub-item; it
// subsumes "add another child to an existing umbrella" (call it again).
func runSplit(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	parentID := fs.String("parent", "", "existing item to promote to umbrella (its id determines the child id)")
	childTitle := fs.String("title", "", "title for the new child item")
	desc := fs.String("desc", "", "description for the new child item")
	status := fs.String("status", "OPEN", "child status: OPEN|IN_PROGRESS|DONE|WONTFIX")
	owner := fs.String("owner", "", "child owner (default: the parent's owner)")
	kind := fs.String("kind", "leaf", "child kind: leaf|umbrella")
	prNum := fs.Int("pr", 0, "completing PR number to attach to the child")
	prRole := fs.String("role", "completing", "role for --pr: completing|followup")
	var deps, refs multiFlag
	fs.Var(&deps, "dep", "depends_on edge for the child (repeatable)")
	fs.Var(&refs, "ref", "ref entry for the child (repeatable)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *parentID == "" || *childTitle == "" {
		fmt.Fprintln(os.Stderr, "registry split: --parent and --title are required")
		os.Exit(2)
	}

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

	// Determine the child id from the parent's id shape (never caller-supplied).
	childID, err := nextSubID(*parentID, mintedIDs(reg, ledger))
	if err != nil {
		fmt.Fprintf(os.Stderr, "registry split: %v\n", err)
		os.Exit(1)
	}

	// Collect existing children of this parent (already-existing leaves before this split).
	existingChildren := []string{}
	for _, it := range reg.Items {
		if it.Parent == *parentID {
			existingChildren = append(existingChildren, it.ID)
		}
	}

	childOwner := *owner
	if childOwner == "" {
		childOwner = reg.Items[parentIdx].Owner
	}

	// Add the new child.
	child := Item{
		ID:          childID,
		Title:       *childTitle,
		Description: *desc,
		Status:      Status(*status),
		DependsOn:   []string(deps),
		Kind:        *kind,
		Owner:       childOwner,
		Parent:      *parentID,
		Refs:        []string(refs),
	}
	if *prNum > 0 {
		child.PRs = append(child.PRs, PRRef{Num: *prNum, Role: PRRole(*prRole)})
	}
	if child.DependsOn == nil {
		child.DependsOn = []string{}
	}
	if child.Refs == nil {
		child.Refs = []string{}
	}
	reg.Items = append(reg.Items, child)

	// Record the new child id in the ledger.
	if err := appendToLedger(ledgerPath(paths.registryDir), childID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// All children of this parent after the split (existing + new).
	allChildren := append(existingChildren, childID)

	// Rewrite dependents: any item with depends_on containing parentID gets
	// parentID replaced by all allChildren (conservative reading).
	for i := range reg.Items {
		if reg.Items[i].ID == *parentID || reg.Items[i].ID == childID {
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
		*parentID, childID, allChildren)
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
