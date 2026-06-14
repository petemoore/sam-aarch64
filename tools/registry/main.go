// registry — validate and generate the iN/qN work-item registries from
// structured YAML source.
//
// Usage:
//
//	registry validate <items.yaml> [questions.yaml]
//	registry gen     <items.yaml> <questions.yaml> [--templates-dir <dir>]
//
// Exit codes: 0 = ok, 1 = validation or drift error, 2 = usage error.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "validate":
		runValidate(os.Args[2:])
	case "gen":
		runGen(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "registry: unknown subcommand %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  registry validate <items.yaml> [questions.yaml]")
	fmt.Fprintln(os.Stderr, "  registry gen      <items.yaml> <questions.yaml> [--templates-dir <dir>]")
}

func runValidate(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "registry validate: need at least <items.yaml>")
		os.Exit(2)
	}

	reg := &Registry{}
	var err error
	reg.Items, err = loadItems(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(args) >= 2 {
		reg.Questions, err = loadQuestions(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ve := validate(reg)
	if ve.hasErrors() {
		for _, msg := range ve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}
	fmt.Println("registry: validate OK")
}

func runGen(args []string) {
	// Parse positional args and optional --templates-dir flag.
	var positional []string
	templatesDir := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--templates-dir" && i+1 < len(args) {
			templatesDir = args[i+1]
			i++
		} else if strings.HasPrefix(args[i], "--templates-dir=") {
			templatesDir = strings.TrimPrefix(args[i], "--templates-dir=")
		} else {
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 2 {
		fmt.Fprintln(os.Stderr, "registry gen: need <items.yaml> <questions.yaml>")
		os.Exit(2)
	}

	// If no explicit templates dir, try to locate the repo root via the items
	// path so the binary can be run from any working directory.
	if templatesDir == "" {
		repoRoot := findRepoRootFromPath(positional[0])
		if repoRoot != "" {
			candidate := filepath.Join(repoRoot, defaultTemplatesDir)
			if _, err := os.Stat(candidate); err == nil {
				templatesDir = candidate
			}
		}
	}

	reg := &Registry{}
	var err error
	reg.Items, err = loadItems(positional[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	reg.Questions, err = loadQuestions(positional[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ve := validate(reg)
	if ve.hasErrors() {
		for _, msg := range ve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}

	var itemsOpen, itemsClosed, qOpen, qClosed bytes.Buffer
	if err := genItemsOpenClosedWithTemplates(reg, &itemsOpen, &itemsClosed, templatesDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := genQuestionsOpenClosedWithTemplates(reg, &qOpen, &qClosed, templatesDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Print("=== item-registry-open ===\n")
	os.Stdout.Write(itemsOpen.Bytes())
	fmt.Print("\n=== item-registry-closed ===\n")
	os.Stdout.Write(itemsClosed.Bytes())
	fmt.Print("\n=== question-registry-open ===\n")
	os.Stdout.Write(qOpen.Bytes())
	fmt.Print("\n=== question-registry-closed ===\n")
	os.Stdout.Write(qClosed.Bytes())
}

// findRepoRootFromPath walks up from the given file path looking for a
// Makefile, which marks the repo root.
func findRepoRootFromPath(path string) string {
	dir := filepath.Dir(filepath.Clean(path))
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
