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

// validate runs all invariants 1–10 on the registry.  It returns the
// ValidationError (non-nil always; call hasErrors() to test).
func validate(reg *Registry) *ValidationError {
	ve := &ValidationError{}

	// Build id sets for cross-reference checks (invariant 10).
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

		// Invariant 4: status in the closed enum.
		switch it.Status {
		case StatusOpen, StatusInProgress, StatusBlocked, StatusDone, StatusWontfix:
			// valid
		case "":
			ve.add(id, "missing status field")
		default:
			ve.add(id, fmt.Sprintf("unknown status %q (must be OPEN|IN_PROGRESS|BLOCKED|DONE|WONTFIX)", it.Status))
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
		if len(it.Blocker) > 200 {
			ve.add(id, fmt.Sprintf("blocker exceeds 200 chars (%d)", len(it.Blocker)))
		}

		// Invariant 9: required fields per status.
		switch it.Status {
		case StatusBlocked:
			if strings.TrimSpace(it.Blocker) == "" {
				ve.add(id, "BLOCKED status requires a non-empty blocker field")
			}
		case StatusOpen, StatusInProgress:
			if strings.TrimSpace(it.Blocker) != "" {
				ve.add(id, fmt.Sprintf("%s status must have empty blocker (found %q)", it.Status, it.Blocker))
			}
		}

		// Invariant 10: id-shaped refs exist in the union.
		for _, ref := range it.Refs {
			if idRefRe.MatchString(ref) && !allIDs[ref] {
				ve.add(id, fmt.Sprintf("ref %q is id-shaped but not found in the registry", ref))
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
	itemStatus := map[string]Status{}
	for _, it := range reg.Items {
		itemStatus[it.ID] = it.Status
	}
	itemKind := map[string]string{}
	for _, it := range reg.Items {
		itemKind[it.ID] = it.Kind
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
			n, _ := strconv.Atoi(id[1:func() int {
				for k, c := range id[1:] {
					if c < '0' || c > '9' {
						return k + 1
					}
				}
				return len(id)
			}()])
			letter := ""
			for k, c := range id[1:] {
				if c < '0' || c > '9' {
					letter = id[1+k:]
					break
				}
			}
			key := sortKey{n: n, letter: letter, brickN: -1}
			if !firstQ && !prevQKey.less(key) && prevQKey != key {
				ve.add(id, "out of canonical sort order (run `registry gen` to re-sort)")
			}
			prevQKey = key
			firstQ = false
		}

		// Invariant 4: status enum.
		switch q.Status {
		case StatusOpen, StatusInProgress, StatusBlocked, StatusDone, StatusWontfix:
		case "":
			ve.add(id, "missing status field")
		default:
			ve.add(id, fmt.Sprintf("unknown status %q", q.Status))
		}

		// Invariant 5: prs entries.
		for j, pr := range q.PRs {
			if pr.Num <= 0 {
				ve.add(id, fmt.Sprintf("prs[%d]: num must be a positive integer", j))
			}
			switch pr.Role {
			case RoleCompleting, RoleFollowup:
			case "":
				ve.add(id, fmt.Sprintf("prs[%d]: missing role", j))
			default:
				ve.add(id, fmt.Sprintf("prs[%d]: unknown role %q", j, pr.Role))
			}
		}

		// Invariant 8: bounded fields.
		if len(q.Title) > 120 {
			ve.add(id, fmt.Sprintf("title exceeds 120 chars (%d)", len(q.Title)))
		}
		if strings.ContainsRune(q.Title, '\n') {
			ve.add(id, "title must be single-line")
		}
		if len(q.Description) > 600 {
			ve.add(id, fmt.Sprintf("description exceeds 600 chars (%d)", len(q.Description)))
		}
		if strings.Count(q.Description, "\n") >= 6 {
			ve.add(id, fmt.Sprintf("description exceeds 6 lines (%d newlines)", strings.Count(q.Description, "\n")))
		}
		if len(q.Blocker) > 200 {
			ve.add(id, fmt.Sprintf("blocker exceeds 200 chars (%d)", len(q.Blocker)))
		}

		// Invariant 9: per-status required fields.
		switch q.Status {
		case StatusBlocked:
			if strings.TrimSpace(q.Blocker) == "" {
				ve.add(id, "BLOCKED status requires a non-empty blocker field")
			}
		case StatusOpen, StatusInProgress:
			if strings.TrimSpace(q.Blocker) != "" {
				ve.add(id, fmt.Sprintf("%s status must have empty blocker (found %q)", q.Status, q.Blocker))
			}
		}

		// Invariant 10: id-shaped refs must exist.
		for _, ref := range q.Refs {
			if idRefRe.MatchString(ref) && !allIDs[ref] {
				ve.add(id, fmt.Sprintf("ref %q is id-shaped but not found in the registry", ref))
			}
		}
	}

	return ve
}
