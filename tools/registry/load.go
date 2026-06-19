package main

import (
	"fmt"
	"os"

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
