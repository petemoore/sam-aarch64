package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// itemIDRe matches the item id grammar:
//
//	i<N>                       base item            e.g. i115
//	i<N><letter>               sub-item (level 1)   e.g. i48c
//	i<N><letter>-b<M>          brick (level 2)      e.g. i48c-b5
//	i<N><letter>-b<M><letter>  brick part           e.g. i48c-b5a
var itemIDRe = regexp.MustCompile(`^i[0-9]+[a-z]?(-b[0-9]+[a-z]?)?$`)

// topLevelItemIDRe matches a bare top-level item id (i<N>, no sub-suffix). An id
// matching itemIDRe but NOT this is "sub-shaped" — it must declare a parent.
var topLevelItemIDRe = regexp.MustCompile(`^i[0-9]+$`)

// questionIDRe matches the question id grammar: q<N> or q<N><letter>.
var questionIDRe = regexp.MustCompile(`^q[0-9]+[a-z]?$`)

// idRefRe matches id-shaped refs (items or questions) to check existence.
var idRefRe = regexp.MustCompile(`^[iq][0-9]`)

// sortKey is the typed sort key for an item id, enabling true-numeric ordering.
type sortKey struct {
	n           int    // base number
	letter      string // sub-item letter (empty = sorts first)
	brickN      int    // brick number (-1 = no brick)
	brickLetter string // brick-part letter (empty = sorts first)
}

// parseItemSortKey parses an item id into its typed sort key.
// It assumes the id is already validated by itemIDRe.
func parseItemSortKey(id string) sortKey {
	// Strip leading 'i'.
	rest := id[1:]
	var sk sortKey
	sk.brickN = -1

	// Split on '-b' if present.
	parts := strings.SplitN(rest, "-b", 2)
	base := parts[0]
	// Base is <N>[letter].
	i := strings.IndexFunc(base, func(r rune) bool { return r < '0' || r > '9' })
	if i == -1 {
		sk.n, _ = strconv.Atoi(base)
	} else {
		sk.n, _ = strconv.Atoi(base[:i])
		sk.letter = base[i:]
	}

	if len(parts) == 2 {
		brick := parts[1]
		j := strings.IndexFunc(brick, func(r rune) bool { return r < '0' || r > '9' })
		if j == -1 {
			sk.brickN, _ = strconv.Atoi(brick)
		} else {
			sk.brickN, _ = strconv.Atoi(brick[:j])
			sk.brickLetter = brick[j:]
		}
	}
	return sk
}

// less reports whether sk sorts before other.
func (sk sortKey) less(other sortKey) bool {
	if sk.n != other.n {
		return sk.n < other.n
	}
	// Empty letter sorts before any letter.
	if sk.letter != other.letter {
		if sk.letter == "" {
			return true
		}
		if other.letter == "" {
			return false
		}
		return sk.letter < other.letter
	}
	// No-brick (-1) sorts before any brick.
	if sk.brickN != other.brickN {
		if sk.brickN == -1 {
			return true
		}
		if other.brickN == -1 {
			return false
		}
		return sk.brickN < other.brickN
	}
	if sk.brickLetter != other.brickLetter {
		if sk.brickLetter == "" {
			return true
		}
		if other.brickLetter == "" {
			return false
		}
		return sk.brickLetter < other.brickLetter
	}
	return false
}

// ValidationError collects errors from validate; each has a greppable prefix.
type ValidationError struct {
	msgs []string
}

func (ve *ValidationError) add(id, msg string) {
	ve.msgs = append(ve.msgs, fmt.Sprintf("registry: ERROR %s: %s", id, msg))
}

func (ve *ValidationError) hasErrors() bool {
	return len(ve.msgs) > 0
}

// validateOpts controls optional validation behaviour.
type validateOpts struct {
	// migrating defers invariant 10 (id-shaped ref existence) to allow refs
	// to point at ids that have not yet been migrated into the YAML source.
	// Invariants 11/12/13 (depends_on DAG, WONTFIX target, delete-gate) remain
	// strict regardless of this flag.
	migrating bool
}

// validate runs all invariants 1-13 on the registry using default (strict)
// options. It returns the ValidationError (non-nil always; call hasErrors() to
// test).
func validate(reg *Registry) *ValidationError {
	return validateWith(reg, validateOpts{})
}

// validateWith runs all invariants 1-13 on the registry with the given options.
//
// Invariant summary (spec §"Invariants (the validator)"):
//  1. Ids globally unique.
//  2. Well-formed ids.
//  3. Canonical typed sort.
//  4. Status in the enum with required payload per status.
//  5. PR entries have num > 0; role is optional — empty is fine, but if present
//     must be "completing" or "followup".
//  6. Umbrella carries no prs. DONE-umbrella coherence: all children DONE/WONTFIX.
//     (PR-less DONE leaf is valid; any number of PRs on a DONE leaf is valid.)
//  8. Bounded description (title <= 200 chars/1 line; description <= 4096 chars/30 lines).
//  9. Required-fields-per-status (WONTFIX => reason in description).
//  10. Id-shaped refs exist in the union. (Deferred when opts.migrating is true.)
//  11. Dependencies form a DAG: every depends_on target exists; no cycles.
//  12. No non-WONTFIX item depends on a WONTFIX node.
//  13. Question delete-gate: falls out of inv 11's existence check (tested explicitly).
func validateWith(reg *Registry, opts validateOpts) *ValidationError {
	ve := &ValidationError{}

	// Build id sets for cross-reference checks (invariants 10, 11, 13).
	allIDs := map[string]bool{}
	for _, it := range reg.Items {
		if it.ID != "" {
			allIDs[it.ID] = true
		}
	}
	for _, q := range reg.Questions {
		if q.ID != "" {
			allIDs[q.ID] = true
		}
	}

	// Build status map for invariant 12.
	itemStatus := map[string]Status{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
	}

	// --- Items ---

	// Invariant 1 (items): ids globally unique.
	seenItemIDs := map[string]bool{}

	// Invariant 3 (items): canonical sort order.
	var prevItemKey sortKey
	firstItem := true

	for i, it := range reg.Items {
		id := it.ID
		if id == "" {
			ve.add(fmt.Sprintf("items[%d]", i), "missing id field")
			continue
		}

		// Invariant 1: unique.
		if seenItemIDs[id] {
			ve.add(id, "duplicate id")
		}
		seenItemIDs[id] = true

		// Invariant 2: well-formed item id.
		if !itemIDRe.MatchString(id) {
			ve.add(id, fmt.Sprintf("malformed item id (must match ^i[0-9]+[a-z]?(-b[0-9]+[a-z]?)?$): %q", id))
		}

		// Invariant 3: canonical typed sort order.
		if itemIDRe.MatchString(id) {
			key := parseItemSortKey(id)
			if !firstItem && !prevItemKey.less(key) && prevItemKey != key {
				ve.add(id, "out of canonical sort order (run `registry gen` to re-sort)")
			}
			prevItemKey = key
			firstItem = false
		}

		// Invariant 4: status in the enum (no BLOCKED — spec §"Status enum").
		switch it.Status {
		case StatusOpen, StatusInProgress, StatusDone, StatusWontfix:
			// valid
		case "":
			ve.add(id, "missing status field")
		default:
			ve.add(id, fmt.Sprintf("unknown status %q (must be OPEN|IN_PROGRESS|DONE|WONTFIX)", it.Status))
		}

		// Invariant 5: prs entries have num > 0; role is optional but, if present,
		// must be a known value.
		for j, pr := range it.PRs {
			if pr.Num <= 0 {
				ve.add(id, fmt.Sprintf("prs[%d]: num must be a positive integer", j))
			}
			switch pr.Role {
			case RoleCompleting, RoleFollowup, "":
				// valid — empty role is allowed
			default:
				ve.add(id, fmt.Sprintf("prs[%d]: unknown role %q (must be completing|followup or empty)", j, pr.Role))
			}
		}

		// Invariant 6: umbrella carries no prs (PR budget lives on leaves).
		if it.isUmbrella() {
			if len(it.PRs) > 0 {
				ve.add(id, "umbrella must carry no prs (completing PRs live on its leaf children)")
			}
		}

		// Invariant 8: bounded fields. Lengths are counted in runes (the spec's
		// "chars"), so multibyte content (em dashes, accents) is not penalised.
		// The trailing newline a YAML block scalar appends on round-trip is
		// trimmed first, so the bound measures content, not the serialization
		// artifact (an in-memory 4096-char desc stays valid after reload).
		if n := utf8.RuneCountInString(it.Title); n > 200 {
			ve.add(id, fmt.Sprintf("title exceeds 200 chars (%d)", n))
		}
		if strings.ContainsRune(it.Title, '\n') {
			ve.add(id, "title must be single-line")
		}
		desc := strings.TrimRight(it.Description, "\n")
		if n := utf8.RuneCountInString(desc); n > 4096 {
			ve.add(id, fmt.Sprintf("description exceeds 4096 chars (%d)", n))
		}
		if n := strings.Count(desc, "\n"); n >= 30 {
			ve.add(id, fmt.Sprintf("description exceeds 30 lines (%d newlines)", n))
		}

		// Invariant 9: required fields per status.
		// WONTFIX => reason in description (spec §"Status enum").
		if it.Status == StatusWontfix && strings.TrimSpace(it.Description) == "" {
			ve.add(id, "WONTFIX item must have a non-empty description explaining the reason")
		}

		// Invariant 10: id-shaped refs exist in the union.
		// Deferred under --migrating to allow refs to point at ids not yet in YAML.
		if !opts.migrating {
			for _, ref := range it.Refs {
				if idRefRe.MatchString(ref) && !allIDs[ref] {
					ve.add(id, fmt.Sprintf("ref %q is id-shaped but not found in the registry", ref))
				}
			}
		}

		// Invariant 11 (existence part): every depends_on target exists.
		// Invariant 12: no non-WONTFIX item depends on a WONTFIX node.
		// Invariant 13: a deleted question leaves a dangling edge => caught here.
		for _, dep := range it.DependsOn {
			if !allIDs[dep] {
				ve.add(id, fmt.Sprintf("depends_on %q: target does not exist in the registry (dangling edge — possible deleted question)", dep))
			} else if it.Status != StatusWontfix {
				// Invariant 12: check for WONTFIX target among items only
				// (questions that exist are open by definition).
				if targetStatus, ok := itemStatus[dep]; ok && targetStatus == StatusWontfix {
					ve.add(id, fmt.Sprintf("depends_on %q: non-WONTFIX item may not depend on a WONTFIX node (stale gate — drop or re-point the edge)", dep))
				}
			}
		}
	}

	// Invariant 6 (umbrella coherence): an umbrella marked DONE must have all
	// children DONE or WONTFIX.
	parentChildren := map[string][]string{}
	for _, it := range reg.Items {
		if it.Parent != "" {
			parentChildren[it.Parent] = append(parentChildren[it.Parent], it.ID)
		}
	}
	for _, it := range reg.Items {
		if !it.isUmbrella() || it.Status != StatusDone {
			continue
		}
		for _, childID := range parentChildren[it.ID] {
			childStatus := itemStatus[childID]
			if childStatus != StatusDone && childStatus != StatusWontfix {
				ve.add(it.ID, fmt.Sprintf("umbrella marked DONE but child %s is %s", childID, childStatus))
			}
		}
	}

	// Invariant 14 (parent integrity): a declared parent must exist and be an
	// umbrella; a sub-shaped id (a letter and/or -bN suffix) must declare a
	// parent; the parent chain must be acyclic. The CLI's split path always
	// produces conforming structure; this catches HAND-EDITS that bypass it —
	// a floating sub-id (e.g. i44a with no parent), a parent: pointing at a leaf
	// or a missing id, or a parent loop.
	kindByID := map[string]string{}
	for _, it := range reg.Items {
		kindByID[it.ID] = it.Kind
	}
	for _, it := range reg.Items {
		// 14a: parent (if set) exists and is an umbrella.
		if it.Parent != "" {
			if _, ok := itemStatus[it.Parent]; !ok {
				ve.add(it.ID, fmt.Sprintf("parent %q does not exist in the registry", it.Parent))
			} else if kindByID[it.Parent] != "umbrella" {
				ve.add(it.ID, fmt.Sprintf("parent %q is not an umbrella (an item with children must be kind:umbrella)", it.Parent))
			}
		}
		// 14b: a sub-shaped id must declare a parent (catches floating sub-ids).
		if itemIDRe.MatchString(it.ID) && !topLevelItemIDRe.MatchString(it.ID) && it.Parent == "" {
			ve.add(it.ID, "sub-shaped id (letter/-bN suffix) must declare a parent: field (every non-top-level id is a child of an umbrella)")
		}
	}
	// 14c: parent chain acyclic.
	parentOf := map[string]string{}
	for _, it := range reg.Items {
		if it.Parent != "" {
			parentOf[it.ID] = it.Parent
		}
	}
	for _, it := range reg.Items {
		seen := map[string]bool{it.ID: true}
		for cur := it.ID; ; {
			p, ok := parentOf[cur]
			if !ok {
				break
			}
			if seen[p] {
				ve.add(it.ID, fmt.Sprintf("parent cycle detected via %q", p))
				break
			}
			seen[p] = true
			cur = p
		}
	}

	// Invariant 11 (acyclicity): dependency graph must be a DAG.
	// Build adjacency list (items only; questions have no outgoing edges).
	adj := map[string][]string{}
	for _, it := range reg.Items {
		if len(it.DependsOn) > 0 {
			adj[it.ID] = append(adj[it.ID], it.DependsOn...)
		}
	}
	if cycle := findCycle(adj); len(cycle) > 0 {
		ve.add(cycle[0], fmt.Sprintf("depends_on cycle detected: %s", strings.Join(cycle, " -> ")))
	}

	// --- Questions ---

	seenQIDs := map[string]bool{}
	var prevQKey sortKey
	firstQ := true

	for i, q := range reg.Questions {
		id := q.ID
		if id == "" {
			ve.add(fmt.Sprintf("questions[%d]", i), "missing id field")
			continue
		}

		// Invariant 1: unique across all ids.
		if seenQIDs[id] || seenItemIDs[id] {
			ve.add(id, "duplicate id")
		}
		seenQIDs[id] = true

		// Invariant 2: well-formed question id.
		if !questionIDRe.MatchString(id) {
			ve.add(id, fmt.Sprintf("malformed question id (must match ^q[0-9]+[a-z]?$): %q", id))
		}

		// Invariant 3: canonical sort (question-space is q<N>[letter]).
		if questionIDRe.MatchString(id) {
			key := parseQSortKey(id)
			if !firstQ && !prevQKey.less(key) && prevQKey != key {
				ve.add(id, "out of canonical sort order (run `registry gen` to re-sort)")
			}
			prevQKey = key
			firstQ = false
		}

		// Invariant 8: question body bounded (trailing newline trimmed — see items).
		body := strings.TrimRight(q.Body, "\n")
		if n := utf8.RuneCountInString(body); n > 4096 {
			ve.add(id, fmt.Sprintf("body exceeds 4096 chars (%d)", n))
		}
		if n := strings.Count(body, "\n"); n >= 30 {
			ve.add(id, fmt.Sprintf("body exceeds 30 lines (%d newlines)", n))
		}
	}

	return ve
}

// findCycle detects a cycle in the directed graph given by adj (id -> []id).
// Returns the cycle path (with the triggering node appended) if found, or nil.
// Uses recursive DFS with color marking (white/gray/black).
func findCycle(adj map[string][]string) []string {
	// Collect nodes in a stable order for deterministic output.
	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	// Sort for determinism.
	sortStrings(nodes)

	const (
		white = 0 // unvisited
		gray  = 1 // in current DFS path
		black = 2 // fully processed
	)
	color := map[string]int{}
	parent := map[string]string{}

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		neighbors := make([]string, len(adj[node]))
		copy(neighbors, adj[node])
		sortStrings(neighbors)
		for _, neighbor := range neighbors {
			if color[neighbor] == gray {
				// Cycle found: reconstruct the path from neighbor to node via the
				// parent chain, then close it back to neighbor.
				// path = [neighbor, ..., node, neighbor]
				path := []string{node}
				cur := node
				for cur != neighbor {
					p, ok := parent[cur]
					if !ok {
						break
					}
					cur = p
					path = append([]string{cur}, path...)
				}
				path = append(path, neighbor) // close the cycle
				return path
			}
			if color[neighbor] == white {
				parent[neighbor] = node
				if cyc := dfs(neighbor); len(cyc) > 0 {
					return cyc
				}
			}
		}
		color[node] = black
		return nil
	}

	for _, n := range nodes {
		if color[n] == white {
			if cyc := dfs(n); len(cyc) > 0 {
				return cyc
			}
		}
	}
	return nil
}

// sortStrings sorts a string slice in place (insertion sort for small slices).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		key := ss[i]
		j := i - 1
		for j >= 0 && ss[j] > key {
			ss[j+1] = ss[j]
			j--
		}
		ss[j+1] = key
	}
}

// pullableItems returns the set of item ids that are "pullable" — OPEN or
// IN_PROGRESS with kind != umbrella. These are the ids that must appear in
// priority.yaml exactly once.
func pullableItems(items []Item) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		if (it.Status == StatusOpen || it.Status == StatusInProgress) && !it.isUmbrella() {
			out[it.ID] = true
		}
	}
	return out
}

// childrenByParent maps each parent id to the ids of its direct children.
func childrenByParent(items []Item) map[string][]string {
	out := map[string][]string{}
	for _, it := range items {
		if it.Parent != "" {
			out[it.Parent] = append(out[it.Parent], it.ID)
		}
	}
	return out
}

// expandDepTarget resolves a depends_on target to the set of pullable ids that
// must precede a dependent for ordering purposes:
//   - a pullable target resolves to itself;
//   - an umbrella target resolves to the union of its descendants' pullable
//     leaves (recursively) — an umbrella is "done" only once all its children
//     are, so depending on it means depending on every open pullable leaf
//     beneath it;
//   - a closed leaf / question / unknown id resolves to nothing (no ordering
//     constraint).
//
// Without this, an edge to an umbrella (never itself pullable, so never a queue
// entry) imposed no constraint, and a dependent could be ranked ahead of its
// real prerequisites while validation stayed green. The `seen` set guards
// against a malformed parent cycle.
func expandDepTarget(itemKind map[string]string, children map[string][]string, pullable map[string]bool, dep string, seen map[string]bool) []string {
	if pullable[dep] {
		return []string{dep}
	}
	if itemKind[dep] != "umbrella" {
		return nil
	}
	if seen[dep] {
		return nil
	}
	seen[dep] = true
	var out []string
	for _, child := range children[dep] {
		out = append(out, expandDepTarget(itemKind, children, pullable, child, seen)...)
	}
	return out
}

// validatePriority checks the priority list against the registry. When the
// priority list is empty it is treated as absent and no errors are reported.
// The invariants enforced:
//
//  1. No duplicate ids in the list.
//  2. Every id in the list is a pullable item (not unknown, not closed, not umbrella).
//  3. Every pullable item appears exactly once (no missing ids).
//  4. The list is a topological extension of the dependency DAG: for every
//     pullable item X with a depends_on edge to pullable item Y, X appears after Y.
func validatePriority(reg *Registry, priority []string) *ValidationError {
	ve := &ValidationError{}
	if len(priority) == 0 {
		return ve
	}

	pullable := pullableItems(reg.Items)

	// Build status and kind maps for meaningful error messages.
	itemStatus := map[string]Status{}
	itemKind := map[string]string{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
		itemKind[it.ID] = it.Kind
	}

	// Check 1 + 2: no duplicates; every listed id is a pullable item.
	seen := map[string]int{} // id -> first position (0-based)
	for pos, id := range priority {
		if prev, dup := seen[id]; dup {
			ve.add("priority", fmt.Sprintf("duplicate id %q (first at rank %d, again at rank %d)",
				id, prev+1, pos+1))
			continue
		}
		seen[id] = pos

		if !pullable[id] {
			// Distinguish the error: unknown id vs closed status vs umbrella.
			if st, exists := itemStatus[id]; exists {
				if !isOpen(st) {
					ve.add("priority", fmt.Sprintf("id %q ranked but status is %s (only OPEN/IN_PROGRESS non-umbrella items belong in the queue)",
						id, st))
				} else if itemKind[id] == "umbrella" {
					ve.add("priority", fmt.Sprintf("id %q ranked but is an umbrella (umbrellas are not queue entries — only their leaf children are)",
						id))
				} else {
					ve.add("priority", fmt.Sprintf("id %q ranked but is not a pullable item", id))
				}
			} else {
				ve.add("priority", fmt.Sprintf("id %q ranked but not found in the registry (unknown id)", id))
			}
		}
	}

	// Check 3: every pullable item appears exactly once (no missing ids).
	for id := range pullable {
		if _, inList := seen[id]; !inList {
			ve.add("priority", fmt.Sprintf("pullable item %q is missing from the priority queue", id))
		}
	}

	// Check 4: topological extension — X must appear after all its pullable deps.
	// A dependency on an umbrella expands to the umbrella's pullable leaf
	// descendants (an umbrella is never itself a queue entry), so an edge to an
	// umbrella still constrains ordering against its real prerequisite leaves.
	pos := map[string]int{}
	for i, id := range priority {
		pos[id] = i
	}
	children := childrenByParent(reg.Items)
	for _, it := range reg.Items {
		if !pullable[it.ID] {
			continue
		}
		xPos, xInList := pos[it.ID]
		if !xInList {
			continue // already reported as missing above
		}
		for _, dep := range it.DependsOn {
			for _, target := range expandDepTarget(itemKind, children, pullable, dep, map[string]bool{}) {
				yPos, yInList := pos[target]
				if !yInList {
					continue // already reported as missing
				}
				if xPos < yPos {
					if target == dep {
						ve.add("priority", fmt.Sprintf("%q ranked before its dependency %q (rank %d < %d)",
							it.ID, target, xPos+1, yPos+1))
					} else {
						ve.add("priority", fmt.Sprintf("%q ranked before %q (rank %d < %d), a pullable child of its umbrella dependency %q",
							it.ID, target, xPos+1, yPos+1, dep))
					}
				}
			}
		}
	}

	return ve
}
