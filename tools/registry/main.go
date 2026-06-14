// registry — validate and generate the iN/qN work-item registries from
// structured YAML source.
//
// Usage:
//
//	registry validate <items.yaml> [questions.yaml]
//	registry gen     <items.yaml> <questions.yaml>
//
// Exit codes: 0 = ok, 1 = validation or drift error, 2 = usage error.
package main

import (
	"bytes"
	"fmt"
	"os"
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
	fmt.Fprintln(os.Stderr, "  registry gen      <items.yaml> <questions.yaml>")
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
