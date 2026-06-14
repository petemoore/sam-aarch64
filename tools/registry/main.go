// registry — validate and generate the iN/qN work-item registries from
// structured YAML source.
//
// Usage:
//
//	registry validate <items.yaml> [questions.yaml]
//	registry gen     <items.yaml> <questions.yaml>
//
//	Mutating subcommands (operate on testdata/ and registry/.id-ledger.txt;
//	tool is dormant — does NOT touch docs/notes/*.md):
//
//	registry next-id [--space items|questions]
//	registry add     --id … --title … --desc … --status … --owner … [--parent …] [--dep …]… [--ref …]…
//	registry split   --parent iN --child-id iN-bM --title …
//	registry set-status --id iN --status … [--pr N]
//	registry set-pr  --id iN --pr N [--role completing|followup]
//	registry dep     add|rm --id iN --on iM|qN
//	registry answer  --id qN
//
// Exit codes: 0 = ok, 1 = validation or operation error, 2 = usage error.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	case "next-id":
		runNextID(os.Args[2:], defaultMutatorPaths())
	case "add":
		runAdd(os.Args[2:], defaultMutatorPaths())
	case "split":
		runSplit(os.Args[2:], defaultMutatorPaths())
	case "set-status":
		runSetStatus(os.Args[2:], defaultMutatorPaths())
	case "set-pr":
		runSetPR(os.Args[2:], defaultMutatorPaths())
	case "dep":
		runDep(os.Args[2:], defaultMutatorPaths())
	case "answer":
		runAnswer(os.Args[2:], defaultMutatorPaths())
	default:
		fmt.Fprintf(os.Stderr, "registry: unknown subcommand %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  registry validate <items.yaml> [questions.yaml]")
	fmt.Fprintln(os.Stderr, "  registry gen      <items.yaml> <questions.yaml>")
	fmt.Fprintln(os.Stderr, "  registry next-id  [--space items|questions]")
	fmt.Fprintln(os.Stderr, "  registry add      --id … --title … --desc … --status … --owner … [--parent …] [--dep …]… [--ref …]…")
	fmt.Fprintln(os.Stderr, "  registry split    --parent iN --child-id iN-bM --title …")
	fmt.Fprintln(os.Stderr, "  registry set-status --id iN --status … [--pr N]")
	fmt.Fprintln(os.Stderr, "  registry set-pr   --id iN --pr N [--role completing|followup]")
	fmt.Fprintln(os.Stderr, "  registry dep      add|rm --id iN --on iM|qN")
	fmt.Fprintln(os.Stderr, "  registry answer   --id qN")
}

// defaultMutatorPaths returns the testdata paths used by all mutating
// subcommands. The tool is dormant (spec §"Rollout discipline"): it operates
// only on testdata/ and the .id-ledger.txt in its own directory, never on
// docs/notes/*.md.
func defaultMutatorPaths() mutatorPaths {
	dir := toolDir()
	return mutatorPaths{
		itemsYAML:     filepath.Join(dir, "testdata", "items.yaml"),
		questionsYAML: filepath.Join(dir, "testdata", "questions.yaml"),
		registryDir:   dir, // .id-ledger.txt lives here alongside the tool sources
	}
}

// toolDir returns the directory containing the registry tool source.
// Uses runtime.Caller so the path is correct regardless of the working directory
// from which the binary is invoked.
func toolDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		// Fallback: use the executable's directory.
		exe, _ := os.Executable()
		return filepath.Dir(exe)
	}
	return filepath.Dir(thisFile)
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
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "registry gen: need <items.yaml> <questions.yaml>")
		os.Exit(2)
	}

	reg := &Registry{}
	var err error
	reg.Items, err = loadItems(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	reg.Questions, err = loadQuestions(args[1])
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

	// Three views: item-open, item-closed, question-open (no closed question view).
	// Spec §"Generator" and §"Questions — transient by design".
	var itemsOpen, itemsClosed, qOpen bytes.Buffer
	if err := genItemsOpenClosed(reg, &itemsOpen, &itemsClosed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := genQuestionsOpen(reg, &qOpen); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Print("=== item-registry-open ===\n")
	os.Stdout.Write(itemsOpen.Bytes())
	fmt.Print("\n=== item-registry-closed ===\n")
	os.Stdout.Write(itemsClosed.Bytes())
	fmt.Print("\n=== question-registry-open ===\n")
	os.Stdout.Write(qOpen.Bytes())
}
