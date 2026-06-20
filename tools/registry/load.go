package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadItems parses a registry/items.yaml file and returns the records in file
// order (sort validation is left to the validator, not the loader).
func loadItems(path string) ([]Item, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	var items []Item
	if err := yaml.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("load items %s: %w", path, err)
	}
	return items, nil
}

// loadQuestions parses a registry/questions.yaml file.
func loadQuestions(path string) ([]Question, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load questions: %w", err)
	}
	var questions []Question
	if err := yaml.Unmarshal(data, &questions); err != nil {
		return nil, fmt.Errorf("load questions %s: %w", path, err)
	}
	return questions, nil
}

// loadPriority parses a priority.yaml file — an ordered YAML list of item ids,
// highest-priority first. Returns an empty slice when the file is absent so a
// missing file is treated as "no priority order yet" rather than an error.
func loadPriority(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load priority: %w", err)
	}
	var ids []string
	if err := yaml.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("load priority %s: %w", path, err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// writePriority serializes the ordered id list to path as a YAML list.
func writePriority(path string, ids []string) error {
	var sb strings.Builder
	sb.WriteString("# Priority queue — ordered list of open pullable item ids, highest-priority first.\n")
	sb.WriteString("# Spec: docs/specs/registry-source-of-truth-design.md §\"Priority queue + ready\"\n")
	sb.WriteString("# Re-rank with: registry prioritize --id iN --to-top\n")
	sb.WriteString("#               registry move --id iN --before iM\n")
	sb.WriteString("#               registry move --id iN --after iM\n")
	for _, id := range ids {
		sb.WriteString("- ")
		sb.WriteString(id)
		sb.WriteString("\n")
	}
	return atomicWriteFile(path, []byte(sb.String()), 0o644)
}
