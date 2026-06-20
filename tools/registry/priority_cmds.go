package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
)

// runReady implements `ready`: prints pullable items whose all depends_on targets
// are satisfied (DONE, or non-pullable/umbrella, or answered/absent question),
// in priority order. "What can be worked next."
//
// A dep is satisfied when:
//   - it is a question — questions are open while they exist, and an unanswered
//     question means the dep is UNsatisfied; when the question is deleted (answered)
//     the edge is removed, so a non-existent question id is satisfied.
//   - it is a DONE item.
//   - it is a WONTFIX item — would be caught by inv 12, but treat as "no longer
//     blocking" for `ready` since the edge should be cleaned up.
//   - it is an umbrella item — umbrellas are not themselves satisfiable units, so
//     their `depends_on` edges are already edges to their children; an umbrella dep
//     is satisfied when the umbrella is DONE.
func runReady(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("ready", flag.ExitOnError)
	petePresent := fs.Bool("pete-present", false, "include owner:pete (needs-Pete-present) items and emit them first")
	peteAway := fs.Bool("pete-away", false, "exclude owner:pete items (the default)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	// Default is pete-away (exclude owner:pete) so the autonomous agent's tip is
	// always agent-actionable; --pete-present includes + prioritizes them. An
	// explicit --pete-away wins if both are passed (the safe choice).
	includePete := *petePresent && !*peteAway
	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Build a map of all item statuses for fast lookup.
	itemStatus := map[string]Status{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
	}
	// Build the question-id set (open questions block their dependents).
	questionIDs := map[string]bool{}
	for _, q := range reg.Questions {
		questionIDs[q.ID] = true
	}

	pullable := pullableItems(reg.Items)

	// depSatisfied reports whether a single depends_on target id is satisfied.
	depSatisfied := func(depID string) bool {
		if questionIDs[depID] {
			// Open question — not yet answered, blocks the item.
			return false
		}
		st, exists := itemStatus[depID]
		if !exists {
			// Target not in registry at all (deleted question or deleted item).
			// Treat as satisfied — the edge is stale but not our problem here.
			return true
		}
		// Satisfied for ready-purposes: DONE or WONTFIX (stale but unblocking).
		return st == StatusDone || st == StatusWontfix
	}

	// allDepsSatisfied reports whether every depends_on edge of item it is satisfied.
	allDepsSatisfied := func(it Item) bool {
		for _, dep := range it.DependsOn {
			if !depSatisfied(dep) {
				return false
			}
		}
		return true
	}

	// Iterate in priority order when a priority list exists, else canonical sort.
	var order []string
	if len(reg.Priority) > 0 {
		order = reg.Priority
	} else {
		for _, it := range sortedItems(reg.Items) {
			order = append(order, it.ID)
		}
	}
	byID := map[string]Item{}
	for _, it := range reg.Items {
		byID[it.ID] = it
	}

	// A ready item is pullable, not IN_PROGRESS (already being worked — see
	// `in-progress`), and has every depends_on edge satisfied.
	isReady := func(it Item) bool {
		return pullable[it.ID] && it.Status != StatusInProgress && allDepsSatisfied(it)
	}
	for _, id := range readyList(order, byID, isReady, includePete) {
		fmt.Println(id)
	}
}

// readyList computes the ready output for runReady: the ids in `order` that pass
// `isReady`, partitioned by owner. owner:pete (needs-Pete-present) items are
// excluded unless includePete, in which case they are emitted FIRST (don't waste
// Pete's presence), then the agent-actionable items.
func readyList(order []string, byID map[string]Item, isReady func(Item) bool, includePete bool) []string {
	var peteReady, agentReady []string
	for _, id := range order {
		it, ok := byID[id]
		if !ok || !isReady(it) {
			continue
		}
		if it.Owner == "pete" {
			peteReady = append(peteReady, id)
		} else {
			agentReady = append(agentReady, id)
		}
	}
	var out []string
	if includePete {
		out = append(out, peteReady...)
	}
	return append(out, agentReady...)
}

// runInProgress implements `in-progress`: prints the ids of all items currently
// marked IN_PROGRESS, in priority order (canonical sort order when no priority
// list exists). This is the session completeness ledger — at wind-down the agent
// lists it and ensures every entry ends DONE or is split into done/not-done
// parts, so no signed-off ask is lost in the chat scroll (i154).
func runInProgress(args []string, paths mutatorPaths) {
	_ = args // in-progress takes no flags
	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if len(reg.Priority) > 0 {
		byID := map[string]Item{}
		for _, it := range reg.Items {
			byID[it.ID] = it
		}
		// Priority covers pullable items; an IN_PROGRESS umbrella can't exist
		// (umbrellas carry derived status), so priority order suffices.
		for _, id := range reg.Priority {
			if it, ok := byID[id]; ok && it.Status == StatusInProgress {
				fmt.Println(id)
			}
		}
		return
	}

	for _, it := range sortedItems(reg.Items) {
		if it.Status == StatusInProgress {
			fmt.Println(it.ID)
		}
	}
}

// runDependents implements `dependents --id iN`: prints the ids of all items
// that have iN in their depends_on list, in canonical sort order.
func runDependents(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("dependents", flag.ExitOnError)
	id := fs.String("id", "", "item id to query dependents of")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry dependents: --id is required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	reverseEdges := buildReverseEdges(reg.Items)
	deps := reverseEdges[*id]
	for _, d := range deps {
		fmt.Println(d)
	}
}

// runDAG implements `dag`: prints every dependency edge in the registry as
// "X -> Y" lines (X depends on Y), sorted deterministically.
func runDAG(args []string, paths mutatorPaths) {
	_ = args // dag takes no flags

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	type edge struct{ from, to string }
	var edges []edge
	for _, it := range reg.Items {
		for _, dep := range it.DependsOn {
			edges = append(edges, edge{it.ID, dep})
		}
	}

	// Sort for deterministic output: first by from, then by to.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	for _, e := range edges {
		fmt.Printf("%s -> %s\n", e.from, e.to)
	}
}

// runPrioritize implements `prioritize --id iN --to-top`: moves iN to the
// front of the priority queue (rank 1), preserving the topo constraint.
// After rewriting priority.yaml, validate and gen run via applyPriorityChange.
func runPrioritize(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("prioritize", flag.ExitOnError)
	id := fs.String("id", "", "item id to re-rank")
	toTop := fs.Bool("to-top", false, "move item to rank 1 (top of queue)")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry prioritize: --id is required")
		os.Exit(2)
	}
	if !*toTop {
		fmt.Fprintln(os.Stderr, "registry prioritize: --to-top is required (the only supported action)")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Remove id from its current position and prepend it.
	newOrder := make([]string, 0, len(reg.Priority))
	found := false
	for _, existing := range reg.Priority {
		if existing == *id {
			found = true
			continue
		}
		newOrder = append(newOrder, existing)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "registry prioritize: id %q not found in priority queue\n", *id)
		os.Exit(1)
	}
	newOrder = append([]string{*id}, newOrder...)

	applyPriorityChange(reg, newOrder, paths)
	fmt.Printf("registry: moved %s to rank 1\n", *id)
}

// runMove implements `move --id iN --before iM` and `move --id iN --after iM`:
// re-ranks iN to immediately before/after iM in the priority queue.
func runMove(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("move", flag.ExitOnError)
	id := fs.String("id", "", "item id to move")
	before := fs.String("before", "", "place immediately before this id")
	after := fs.String("after", "", "place immediately after this id")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry move: --id is required")
		os.Exit(2)
	}
	if (*before == "") == (*after == "") {
		fmt.Fprintln(os.Stderr, "registry move: exactly one of --before or --after is required")
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Remove *id from its current position.
	withoutID := make([]string, 0, len(reg.Priority))
	found := false
	for _, existing := range reg.Priority {
		if existing == *id {
			found = true
			continue
		}
		withoutID = append(withoutID, existing)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "registry move: id %q not found in priority queue\n", *id)
		os.Exit(1)
	}

	// Find the anchor position and insert.
	anchor := *before
	if *after != "" {
		anchor = *after
	}
	anchorIdx := -1
	for i, existing := range withoutID {
		if existing == anchor {
			anchorIdx = i
			break
		}
	}
	if anchorIdx == -1 {
		fmt.Fprintf(os.Stderr, "registry move: anchor id %q not found in priority queue\n", anchor)
		os.Exit(1)
	}

	// Insert at anchorIdx (before) or anchorIdx+1 (after).
	insertAt := anchorIdx
	if *after != "" {
		insertAt = anchorIdx + 1
	}
	newOrder := make([]string, 0, len(reg.Priority))
	newOrder = append(newOrder, withoutID[:insertAt]...)
	newOrder = append(newOrder, *id)
	newOrder = append(newOrder, withoutID[insertAt:]...)

	applyPriorityChange(reg, newOrder, paths)
	if *before != "" {
		fmt.Printf("registry: moved %s before %s\n", *id, *before)
	} else {
		fmt.Printf("registry: moved %s after %s\n", *id, *after)
	}
}

// applyPriorityChange validates the new priority order, writes priority.yaml,
// validates the full registry (items + priority), and runs gen.
func applyPriorityChange(reg *Registry, newOrder []string, paths mutatorPaths) {
	// Repair ordering to a valid topological extension before validating, so an
	// explicit prioritize/move that would place an item ahead of its dependency
	// is auto-corrected to the nearest valid order rather than rejected. If the
	// repair had to move anything, say so — the user's literal request could not
	// be honoured because a dependency must precede its dependents.
	repaired := topoRepairPriority(reg.Items, newOrder)
	if !equalStringSlice(repaired, newOrder) {
		fmt.Fprintln(os.Stderr, "registry: note — adjusted the requested order to satisfy dependency constraints (a dependency must precede its dependents).")
		newOrder = repaired
	}

	// Validate the (repaired) order against the registry before writing.
	pve := validatePriority(reg, newOrder)
	if pve.hasErrors() {
		for _, msg := range pve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}

	if err := writePriority(paths.priorityYAML, newOrder); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Re-load with the new priority and regenerate all views.
	reg.Priority = newOrder
	genToOutDirOrStdout(reg, paths)
}
