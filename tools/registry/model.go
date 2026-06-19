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
type PRRef struct {
	Num  int    `yaml:"num"`
	Role PRRole `yaml:"role"`
}

// Item is one row from registry/items.yaml.
// Spec §"Schema (per item record)".
type Item struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Status      Status   `yaml:"status"`
	DependsOn   []string `yaml:"depends_on"` // ids of items or questions this item is gated on
	Kind        string   `yaml:"kind"`       // "leaf" (default) | "umbrella"
	Owner       string   `yaml:"owner"`
	PRs         []PRRef  `yaml:"prs"`
	Parent      string   `yaml:"parent"`
	Refs        []string `yaml:"refs"`
}

// Question is one row from registry/questions.yaml.
// Questions are transient (spec §"Questions — transient by design"):
// a question exists only while open; it has no answer field and no status enum.
type Question struct {
	ID    string `yaml:"id"`
	Body  string `yaml:"body"`  // markdown question body (possibly multi-part)
	Owner string `yaml:"owner"` // usually "pete"
}

// Registry holds the parsed contents of both YAML source files.
type Registry struct {
	Items     []Item
	Questions []Question
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
