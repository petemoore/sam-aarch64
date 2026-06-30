package main

// Status is the controlled-vocabulary item status.
// There is no BLOCKED token — "blocked" is a derived property of the
// dependency graph (spec §"Status enum"), never a stored status.
type Status string

const (
	StatusOpen       Status = "OPEN"
	StatusInProgress Status = "IN_PROGRESS"
	StatusDone       Status = "DONE"
	StatusWontfix    Status = "WONTFIX"
)

// PRRole classifies a PR attached to an item.
type PRRole string

const (
	RoleCompleting PRRole = "completing"
	RoleFollowup   PRRole = "followup"
)

// PRRef is a structured PR attachment: number + role.
// json tags mirror the yaml tags so `registry view --format json` emits the same
// field names (the yaml serializer ignores json tags and vice versa).
type PRRef struct {
	Num  int    `yaml:"num" json:"num"`
	Role PRRole `yaml:"role" json:"role"`
}

// AddPR attaches a PR reference, upserting on (Num, Role): if an identical
// {num, role} pair is already present it is a no-op. This keeps the PR-attach
// paths idempotent (i282) — calling set-pr --pr N then set-status --pr N (or
// either twice) with the same PR must not accumulate a duplicate {num, role}
// entry in prs[].
func (it *Item) AddPR(num int, role PRRole) {
	for _, p := range it.PRs {
		if p.Num == num && p.Role == role {
			return
		}
	}
	it.PRs = append(it.PRs, PRRef{Num: num, Role: role})
}

// Item is one row from registry/items.yaml.
// Spec §"Schema (per item record)".
type Item struct {
	ID          string   `yaml:"id" json:"id"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Status      Status   `yaml:"status" json:"status"`
	DependsOn   []string `yaml:"depends_on" json:"depends_on"` // ids of items or questions this item is gated on
	Kind        string   `yaml:"kind" json:"kind"`             // "leaf" (default) | "umbrella"
	Owner       string   `yaml:"owner" json:"owner"`
	PRs         []PRRef  `yaml:"prs" json:"prs"`
	Parent      string   `yaml:"parent" json:"parent"`
	Refs        []string `yaml:"refs" json:"refs"`
}

// Question is one row from registry/questions.yaml.
// Questions are transient (spec §"Questions — transient by design"):
// a question exists only while open; it has no answer field and no status enum.
type Question struct {
	ID    string `yaml:"id" json:"id"`
	Body  string `yaml:"body" json:"body"`   // markdown question body (possibly multi-part)
	Owner string `yaml:"owner" json:"owner"` // usually "pete"
}

// Registry holds the parsed contents of the YAML source files plus the
// optional priority queue.
type Registry struct {
	Items     []Item
	Questions []Question
	Priority  []string // ordered list of pullable item ids; nil/empty when absent
}

// isUmbrella reports whether the item is an umbrella grouping.
func (it *Item) isUmbrella() bool {
	return it.Kind == "umbrella"
}

// isOpen reports whether a status maps to the open view.
// Spec §"Status enum": {OPEN, IN_PROGRESS} → open; {DONE, WONTFIX} → closed.
func isOpen(s Status) bool {
	switch s {
	case StatusOpen, StatusInProgress:
		return true
	}
	return false
}
