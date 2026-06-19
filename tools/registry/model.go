package main

// Status is the controlled-vocabulary item/question status.
type Status string

const (
	StatusOpen       Status = "OPEN"
	StatusInProgress Status = "IN_PROGRESS"
	StatusBlocked    Status = "BLOCKED"
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
type Item struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Status      Status   `yaml:"status"`
	Blocker     string   `yaml:"blocker"`
	Kind        string   `yaml:"kind"` // "leaf" (default) | "umbrella"
	Owner       string   `yaml:"owner"`
	PRs         []PRRef  `yaml:"prs"`
	Parent      string   `yaml:"parent"`
	Refs        []string `yaml:"refs"`
}

// Question is one row from registry/questions.yaml.
type Question struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Status      Status   `yaml:"status"`
	Blocker     string   `yaml:"blocker"`
	Owner       string   `yaml:"owner"`
	PRs         []PRRef  `yaml:"prs"`
	Refs        []string `yaml:"refs"`
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
func isOpen(s Status) bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked:
		return true
	}
	return false
}
