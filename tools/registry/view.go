package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// runView implements `view --id <id> [--format text|json]`: print one item or
// question record from the live registry, human-readably (default) or as JSON.
//
// It is the read counterpart to the YAML — it replaces ad-hoc grep / inline YAML
// parsing to inspect a single record, and it surfaces two things the raw YAML
// does not carry: the computed dependents (reverse depends_on edges) and the
// item's priority-queue rank.
func runView(args []string, paths mutatorPaths) {
	fs := flag.NewFlagSet("view", flag.ExitOnError)
	id := fs.String("id", "", "item (iN) or question (qN) id to view")
	format := fs.String("format", "text", "output format: text | json")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles

	if *id == "" {
		fmt.Fprintln(os.Stderr, "registry view: --id is required")
		os.Exit(2)
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(os.Stderr, "registry view: unknown --format %q (must be text|json)\n", *format)
		os.Exit(2)
	}

	reg, err := loadReg(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := writeView(os.Stdout, reg, *id, *format); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// writeView renders the record with the given id to w. It returns an error if the
// id is not found or JSON marshalling fails — runView turns that into exit 1.
// Factored out of runView (which os.Exits) so it is unit-testable with a buffer.
func writeView(w io.Writer, reg *Registry, id, format string) error {
	// Dependents (reverse edges) and priority rank are computed, not stored.
	dependents := buildReverseEdges(reg.Items)[id]
	rank := 0
	for i, pid := range reg.Priority {
		if pid == id {
			rank = i + 1
			break
		}
	}

	for _, it := range reg.Items {
		if it.ID == id {
			if format == "json" {
				return writeItemJSON(w, it, dependents, rank)
			}
			writeItemText(w, it, dependents, rank)
			return nil
		}
	}
	for _, q := range reg.Questions {
		if q.ID == id {
			if format == "json" {
				return writeQuestionJSON(w, q, dependents)
			}
			writeQuestionText(w, q, dependents)
			return nil
		}
	}
	return fmt.Errorf("registry view: id %q not found in the registry", id)
}

// writeItemText renders an item as a human-readable block.
func writeItemText(w io.Writer, it Item, dependents []string, rank int) {
	status := string(it.Status)
	if it.isUmbrella() {
		status += " · umbrella"
	}
	fmt.Fprintf(w, "%s  [%s]  owner:%s\n", it.ID, status, orNone(it.Owner))
	fmt.Fprintf(w, "title:       %s\n", it.Title)
	if rank > 0 {
		fmt.Fprintf(w, "priority:    rank %d\n", rank)
	}
	fmt.Fprintf(w, "depends_on:  %s\n", joinOrNone(it.DependsOn))
	fmt.Fprintf(w, "dependents:  %s\n", joinOrNone(dependents))
	if it.Parent != "" {
		fmt.Fprintf(w, "parent:      %s\n", it.Parent)
	}
	if len(it.PRs) > 0 {
		fmt.Fprintf(w, "prs:         %s\n", formatPRs(it.PRs))
	}
	if len(it.Refs) > 0 {
		fmt.Fprintf(w, "refs:        %s\n", strings.Join(it.Refs, ", "))
	}
	if d := strings.TrimRight(it.Description, "\n"); strings.TrimSpace(d) != "" {
		fmt.Fprintf(w, "description:\n%s\n", indentLines(d, "  "))
	}
}

// writeQuestionText renders a question as a human-readable block.
func writeQuestionText(w io.Writer, q Question, dependents []string) {
	fmt.Fprintf(w, "%s  [question]  owner:%s\n", q.ID, orNone(q.Owner))
	fmt.Fprintf(w, "dependents:  %s\n", joinOrNone(dependents))
	if b := strings.TrimRight(q.Body, "\n"); strings.TrimSpace(b) != "" {
		fmt.Fprintf(w, "body:\n%s\n", indentLines(b, "  "))
	}
}

// writeItemJSON emits the item plus its computed dependents and priority rank.
// Nil slices are normalised to [] so the JSON is jq-friendly (`.depends_on[]`
// works on an absent edge list rather than erroring on null).
func writeItemJSON(w io.Writer, it Item, dependents []string, rank int) error {
	if dependents == nil {
		dependents = []string{}
	}
	if it.DependsOn == nil {
		it.DependsOn = []string{}
	}
	if it.PRs == nil {
		it.PRs = []PRRef{}
	}
	if it.Refs == nil {
		it.Refs = []string{}
	}
	out := struct {
		Item
		Dependents   []string `json:"dependents"`
		PriorityRank int      `json:"priority_rank"`
	}{Item: it, Dependents: dependents, PriorityRank: rank}
	return emitJSON(w, out)
}

// writeQuestionJSON emits the question plus its computed dependents.
func writeQuestionJSON(w io.Writer, q Question, dependents []string) error {
	if dependents == nil {
		dependents = []string{}
	}
	out := struct {
		Question
		Dependents []string `json:"dependents"`
	}{Question: q, Dependents: dependents}
	return emitJSON(w, out)
}

func emitJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// orNone returns s, or "(none)" when empty.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// joinOrNone joins ids with ", ", or returns "(none)" for an empty list.
func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	return strings.Join(ss, ", ")
}

// formatPRs renders PR refs as "#N (role)" (role omitted when empty).
func formatPRs(prs []PRRef) string {
	parts := make([]string, 0, len(prs))
	for _, pr := range prs {
		if pr.Role != "" {
			parts = append(parts, fmt.Sprintf("#%d (%s)", pr.Num, pr.Role))
		} else {
			parts = append(parts, fmt.Sprintf("#%d", pr.Num))
		}
	}
	return strings.Join(parts, ", ")
}

// indentLines prefixes every line of s with prefix.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
