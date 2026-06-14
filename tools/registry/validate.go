package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// itemIDRe matches the item id grammar:
//
//	i<N>                       base item            e.g. i115
//	i<N><letter>               sub-item (level 1)   e.g. i48c
//	i<N><letter>-b<M>          brick (level 2)      e.g. i48c-b5
//	i<N><letter>-b<M><letter>  brick part           e.g. i48c-b5a
var itemIDRe = regexp.MustCompile(`^i[0-9]+[a-z]?(-b[0-9]+[a-z]?)?$`)

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

// validate runs all invariants 1-13 on the registry. It returns the
// ValidationError (non-nil always; call hasErrors() to test).
//
// Invariant summary (spec §"Invariants (the validator)"):
//  1. Ids globally unique.
//  2. Well-formed ids.
//  3. Canonical typed sort.
//  4. Status in the enum with required payload per status.
//  5. PR entries have num > 0 and a valid role.
//  6. One completing PR per DONE leaf; umbrellas carry none.
//  7. Atomic items: non-umbrella with >1 completing PR -> split.
//  8. Bounded description (title <= 120 chars/1 line; description <= 600 chars/6 lines).
//  9. Required-fields-per-status (DONE => closed + leaf has completing PR; etc.).
//  10. Id-shaped refs exist in the union.
//  11. Dependencies form a DAG: every depends_on target exists; no cycles.
//  12. No non-WONTFIX item depends on a WONTFIX node.
//  13. Question delete-gate: falls out of inv 11's existence check (tested explicitly).
func validate(reg *Registry) *ValidationError {
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

		// Invariant 5: prs entries have num and role.
		for j, pr := range it.PRs {
			if pr.Num <= 0 {
				ve.add(id, fmt.Sprintf("prs[%d]: num must be a positive integer", j))
			}
			switch pr.Role {
			case RoleCompleting, RoleFollowup:
				// valid
			case "":
				ve.add(id, fmt.Sprintf("prs[%d]: missing role (must be completing|followup)", j))
			default:
				ve.add(id, fmt.Sprintf("prs[%d]: unknown role %q (must be completing|followup)", j, pr.Role))
			}
		}

		// Invariants 6 & 7: umbrella/leaf PR semantics.
		if it.isUmbrella() {
			if len(it.PRs) > 0 {
				ve.add(id, "umbrella must carry no prs (completing PRs live on its leaf children)")
			}
		} else {
			// Leaf: count completing PRs.
			completing := 0
			for _, pr := range it.PRs {
				if pr.Role == RoleCompleting {
					completing++
				}
			}
			if it.Status == StatusDone && completing != 1 {
				ve.add(id, fmt.Sprintf("DONE leaf must have exactly 1 completing PR (found %d)", completing))
			}
			if completing > 1 {
				// Invariant 7: atomic items.
				ve.add(id, fmt.Sprintf("non-umbrella item has %d completing PRs — split into sub-items", completing))
			}
		}

		// Invariant 8: bounded fields.
		if len(it.Title) > 120 {
			ve.add(id, fmt.Sprintf("title exceeds 120 chars (%d)", len(it.Title)))
		}
		if strings.ContainsRune(it.Title, '\n') {
			ve.add(id, "title must be single-line")
		}
		if len(it.Description) > 600 {
			ve.add(id, fmt.Sprintf("description exceeds 600 chars (%d)", len(it.Description)))
		}
		if strings.Count(it.Description, "\n") >= 6 {
			ve.add(id, fmt.Sprintf("description exceeds 6 lines (%d newlines)", strings.Count(it.Description, "\n")))
		}

		// Invariant 9: required fields per status.
		// WONTFIX => reason in description (spec §"Status enum").
		if it.Status == StatusWontfix && strings.TrimSpace(it.Description) == "" {
			ve.add(id, "WONTFIX item must have a non-empty description explaining the reason")
		}

		// Invariant 10: id-shaped refs exist in the union.
		for _, ref := range it.Refs {
			if idRefRe.MatchString(ref) && !allIDs[ref] {
				ve.add(id, fmt.Sprintf("ref %q is id-shaped but not found in the registry", ref))
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

		// Invariant 8: question body bounded.
		if len(q.Body) > 600 {
			ve.add(id, fmt.Sprintf("body exceeds 600 chars (%d)", len(q.Body)))
		}
		if strings.Count(q.Body, "\n") >= 6 {
			ve.add(id, fmt.Sprintf("body exceeds 6 lines (%d newlines)", strings.Count(q.Body, "\n")))
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
