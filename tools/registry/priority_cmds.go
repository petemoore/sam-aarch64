package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// presenceMarkerExists reports whether the Pete-present marker file exists.
// The marker path is read from ALOOP_PRESENCE_FILE; if that env var is unset,
// it defaults to ~/.claude/autonomous-loop/pete-present. If os.UserHomeDir
// fails, the function returns false (no crash — treat as no marker).
//
// Precedence for includePete (highest to lowest):
//  1. --pete-away explicitly passed → exclude (force off; the safe override).
//  2. --pete-present explicitly passed → include (force on).
//  3. Marker file present → include; absent → exclude (ambient persistent default).
func presenceMarkerExists() bool {
	path := os.Getenv("ALOOP_PRESENCE_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = filepath.Join(home, ".claude", "autonomous-loop", "pete-present")
	}
	_, err := os.Stat(path)
	return err == nil
}

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
	// includePete precedence (highest to lowest):
	//   1. --pete-away explicitly passed → exclude (force off; the safe override).
	//   2. --pete-present explicitly passed → include (force on).
	//   3. Presence marker file present → include; absent → exclude (persistent
	//      ambient default across sessions). Marker path: ALOOP_PRESENCE_FILE env
	//      var, or ~/.claude/autonomous-loop/pete-present when unset.
	// When no marker and no flags: owner:pete is excluded — the current default,
	// unchanged (important for CI, which runs without the marker).
	includePete := !*peteAway && (*petePresent || presenceMarkerExists())
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
	// Re-load to see the post-topo-repair order, then report the resulting ready position.
	reg2, err := loadReg(paths)
	if err == nil {
		reportReadyPosition(reg2, *id)
	}
	printStageHint(paths)
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
	// Re-load to see the post-topo-repair order, then report the resulting ready position.
	reg2, err := loadReg(paths)
	if err == nil {
		reportReadyPosition(reg2, *id)
	}
	printStageHint(paths)
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

	// Re-load with the new priority and regenerate all views (silently in
	// stdout mode — mutations never dump views).
	reg.Priority = newOrder
	genToOutDirOrStdout(reg, paths, false)
}

// reportReadyPosition prints a self-explanatory position summary for id after
// an add or prioritize operation. It shows the item's ready-queue rank and
// explains why any items appear above it (owner:pete items, dep-blocked items).
// This prevents agents from wondering "why isn't my item at position 1?" and
// spelunking the source — the output tells them exactly where they are and why.
//
// Priority model (one paragraph for agents):
// The priority queue (registry/priority.yaml) is a total order over all pullable
// (OPEN/IN_PROGRESS non-umbrella) items. `ready` emits a FILTERED SUBSET of that
// queue: (1) items whose every depends_on target is DONE/WONTFIX or absent are
// "actionable"; (2) by default owner:pete items are excluded (needs Pete present
// to execute) — use --pete-present to include them; (3) IN_PROGRESS items are
// excluded (already being worked, see `in-progress`). The topological repair pass
// auto-adjusts rank so every item appears after its in-queue dependencies, so
// `prioritize --to-top` puts an item first among agent-actionable items ONLY when
// it has no in-queue prerequisites; otherwise it is pulled just after its last
// prerequisite. After any add/prioritize, this function reports the resulting
// ready-queue rank so you never have to guess.
func reportReadyPosition(reg *Registry, id string) {
	// Build data structures for the ready computation.
	itemStatus := map[string]Status{}
	itemKind := map[string]string{}
	itemOwner := map[string]string{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
		itemKind[it.ID] = it.Kind
		itemOwner[it.ID] = it.Owner
	}
	questionIDs := map[string]bool{}
	for _, q := range reg.Questions {
		questionIDs[q.ID] = true
	}
	pullable := pullableItems(reg.Items)

	depSatisfied := func(depID string) bool {
		if questionIDs[depID] {
			return false
		}
		st, exists := itemStatus[depID]
		if !exists {
			return true
		}
		return st == StatusDone || st == StatusWontfix
	}
	allDepsSatisfied := func(it Item) bool {
		for _, dep := range it.DependsOn {
			if !depSatisfied(dep) {
				return false
			}
		}
		return true
	}

	byID := map[string]Item{}
	for _, it := range reg.Items {
		byID[it.ID] = it
	}

	// Compute the order (priority list or canonical sort).
	var order []string
	if len(reg.Priority) > 0 {
		order = reg.Priority
	} else {
		for _, it := range sortedItems(reg.Items) {
			order = append(order, it.ID)
		}
	}

	// Walk the order, partitioning into the ready categories and counting.
	type reasonedItem struct {
		id     string
		reason string // why it's above the target
	}
	var abovePete, aboveAgent []reasonedItem
	targetReadyPos := -1
	totalReady := 0
	inQueuePos := -1
	for qPos, qid := range order {
		it, ok := byID[qid]
		if !ok {
			continue
		}
		if qid == id {
			inQueuePos = qPos
		}
		if !pullable[qid] || it.Status == StatusInProgress {
			continue
		}
		if !allDepsSatisfied(it) {
			continue
		}
		// This item is ready (agent or pete).
		if it.Owner == "pete" {
			if qid == id {
				targetReadyPos = totalReady + 1
			}
			totalReady++
			if targetReadyPos == -1 {
				abovePete = append(abovePete, reasonedItem{id: qid, reason: "owner:pete"})
			}
		} else {
			if qid == id {
				targetReadyPos = totalReady + 1
			}
			totalReady++
			if targetReadyPos == -1 {
				aboveAgent = append(aboveAgent, reasonedItem{id: qid, reason: "agent-actionable"})
			}
		}
	}

	// Count dep-blocked items above us in the queue.
	blockedAbove := 0
	if inQueuePos >= 0 {
		for _, qid := range order[:inQueuePos] {
			it, ok := byID[qid]
			if !ok || !pullable[qid] || it.Status == StatusInProgress {
				continue
			}
			if !allDepsSatisfied(it) {
				blockedAbove++
			}
		}
	}

	// Build the explanation.
	if targetReadyPos == -1 {
		// The item is not in the ready set (blocked or not pullable).
		it, ok := byID[id]
		if !ok {
			fmt.Printf("registry: %s: not found in registry\n", id)
			return
		}
		if !pullable[id] {
			fmt.Printf("registry: %s is not pullable (status:%s kind:%s) — it does not appear in `ready`\n",
				id, it.Status, it.Kind)
			return
		}
		if it.Status == StatusInProgress {
			fmt.Printf("registry: %s is IN_PROGRESS — it does not appear in `ready` (see `in-progress`)\n", id)
			return
		}
		// Find which deps are unsatisfied.
		var unsatisfied []string
		for _, dep := range it.DependsOn {
			if !depSatisfied(dep) {
				unsatisfied = append(unsatisfied, dep)
			}
		}
		fmt.Printf("registry: %s is dependency-blocked (unsatisfied: %v) — it does not appear in `ready`\n",
			id, unsatisfied)
		return
	}

	// Build the position line.
	var parts []string
	if len(abovePete) > 0 {
		ids := make([]string, len(abovePete))
		for i, r := range abovePete {
			ids[i] = r.id
		}
		parts = append(parts, fmt.Sprintf("owner:pete items (%s) shown first when pete-present", joinIDs(ids)))
	}
	if blockedAbove > 0 {
		parts = append(parts, fmt.Sprintf("%d dep-blocked item(s) hidden above it in the raw queue", blockedAbove))
	}

	agentPos := targetReadyPos - len(abovePete)
	if it, ok := byID[id]; ok && it.Owner != "pete" {
		// Report agent-queue position specifically.
		agentReady := totalReady - len(abovePete)
		if len(parts) == 0 {
			fmt.Printf("registry: %s is at ready-position %d of %d (top agent-actionable item)\n",
				id, agentPos, agentReady)
		} else {
			fmt.Printf("registry: %s is at ready-position %d of %d: %s\n",
				id, agentPos, agentReady, joinParts(parts))
		}
	} else {
		// Pete item.
		if len(parts) == 0 {
			fmt.Printf("registry: %s is at ready-position %d of %d (pete-present queue)\n",
				id, targetReadyPos, totalReady)
		} else {
			fmt.Printf("registry: %s is at ready-position %d of %d: %s\n",
				id, targetReadyPos, totalReady, joinParts(parts))
		}
	}
}

// joinIDs formats a slice of ids as "i1, i2, i3" (comma-separated).
func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	result := ids[0]
	for _, id := range ids[1:] {
		result += ", " + id
	}
	return result
}

// joinParts formats the explanation parts into a readable sentence.
func joinParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += "; " + p
	}
	return result
}
