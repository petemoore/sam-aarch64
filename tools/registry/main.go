// registry — validate and generate the iN/qN work-item registries from
// structured YAML source.
//
// Usage:
//
//	registry [--migrating] validate <items.yaml> [questions.yaml]
//	registry [--migrating] gen     <items.yaml> <questions.yaml>
//
//	Read-only query subcommands:
//
//	registry ready                                    (unblocked pullable items, in priority order)
//	registry dependents --id iN                       (ids that depend_on iN)
//	registry dag                                      (all dependency edges, one per line)
//
//	Priority mutators (rewrite registry/priority.yaml; validate+gen after):
//
//	registry prioritize --id iN --to-top
//	registry move       --id iN --before iM
//	registry move       --id iN --after  iM
//
//	Mutating subcommands (operate on testdata/ and registry/.id-ledger.txt;
//	tool is dormant — does NOT touch docs/notes/*.md):
//
//	registry [--migrating] next-id [--space items|questions]
//	registry [--migrating] add     --id … --title … --desc … --status … --owner … [--parent …] [--dep …]… [--ref …]…
//	registry [--migrating] split   --parent iN --child-id iN-bM --title …
//	registry [--migrating] set-status --id iN --status … [--pr N]
//	registry [--migrating] set-pr  --id iN --pr N [--role completing|followup]
//	registry [--migrating] dep     add|rm --id iN --on iM|qN
//	registry [--migrating] answer  --id qN
//
// --migrating is a global flag: it may appear anywhere before the subcommand
// and defers only invariant 10 (id-shaped ref existence). Invariants 11/12/13
// (depends_on DAG, no-WONTFIX-target, delete-gate) remain strict.
//
// Environment variables (all optional; override the testdata defaults):
//
//	REGISTRY_ITEMS      path to items.yaml         (default: <toolDir>/testdata/items.yaml)
//	REGISTRY_QUESTIONS  path to questions.yaml      (default: <toolDir>/testdata/questions.yaml)
//	REGISTRY_PRIORITY   path to priority.yaml       (default: <registryDir>/priority.yaml or testdata/priority.yaml)
//	REGISTRY_DIR        directory for .id-ledger.txt (default: <toolDir>)
//	REGISTRY_TEMPLATES  directory of *.head.md templates (default: <toolDir>/templates)
//	REGISTRY_OUTDIR     directory to write generated .md files; empty → stdout mode
//
// Exit codes: 0 = ok, 1 = validation or operation error, 2 = usage error.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	args := os.Args[1:]

	// Strip the first --migrating occurrence from args (global flag).
	migrating := false
	filtered := args[:0:len(args)]
	for _, a := range args {
		if a == "--migrating" && !migrating {
			migrating = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	cmd := args[0]
	paths := defaultMutatorPaths()
	paths.migrating = migrating
	switch cmd {
	case "validate":
		runValidate(args[1:], migrating, paths)
	case "gen":
		runGen(args[1:], paths)
	case "ready":
		runReady(args[1:], paths)
	case "dependents":
		runDependents(args[1:], paths)
	case "dag":
		runDAG(args[1:], paths)
	case "prioritize":
		runPrioritize(args[1:], paths)
	case "move":
		runMove(args[1:], paths)
	case "next-id":
		runNextID(args[1:], paths)
	case "add":
		runAdd(args[1:], paths)
	case "split":
		runSplit(args[1:], paths)
	case "set-status":
		runSetStatus(args[1:], paths)
	case "set-pr":
		runSetPR(args[1:], paths)
	case "dep":
		runDep(args[1:], paths)
	case "answer":
		runAnswer(args[1:], paths)
	default:
		fmt.Fprintf(os.Stderr, "registry: unknown subcommand %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] validate <items.yaml> [questions.yaml]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] gen      <items.yaml> <questions.yaml>")
	fmt.Fprintln(os.Stderr, "  registry ready")
	fmt.Fprintln(os.Stderr, "  registry dependents --id iN")
	fmt.Fprintln(os.Stderr, "  registry dag")
	fmt.Fprintln(os.Stderr, "  registry prioritize --id iN --to-top")
	fmt.Fprintln(os.Stderr, "  registry move       --id iN --before iM")
	fmt.Fprintln(os.Stderr, "  registry move       --id iN --after  iM")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] next-id  [--space items|questions]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] add      --id … --title … --desc … --status … --owner … [--parent …] [--dep …]… [--ref …]…")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] split    --parent iN --child-id iN-bM --title …")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-status --id iN --status … [--pr N]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-pr   --id iN --pr N [--role completing|followup]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] dep      add|rm --id iN --on iM|qN")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] answer   --id qN")
}

// defaultMutatorPaths returns paths for all mutating subcommands, reading
// overrides from environment variables. When env vars are unset the tool
// operates on testdata/ (dormant mode — never touches docs/notes/*.md).
//
// Environment variables:
//
//	REGISTRY_ITEMS      override items.yaml path
//	REGISTRY_QUESTIONS  override questions.yaml path
//	REGISTRY_PRIORITY   override priority.yaml path
//	REGISTRY_DIR        override .id-ledger.txt directory
//	REGISTRY_TEMPLATES  override templates directory
//	REGISTRY_OUTDIR     set to enable in-place .md generation
func defaultMutatorPaths() mutatorPaths {
	dir := toolDir()

	itemsYAML := os.Getenv("REGISTRY_ITEMS")
	if itemsYAML == "" {
		itemsYAML = filepath.Join(dir, "testdata", "items.yaml")
	}

	questionsYAML := os.Getenv("REGISTRY_QUESTIONS")
	if questionsYAML == "" {
		questionsYAML = filepath.Join(dir, "testdata", "questions.yaml")
	}

	// priorityYAML lives in the registry/ dir (alongside items/questions), but in
	// dormant/testdata mode it is in testdata/ so the test fixtures are isolated.
	registryDir := os.Getenv("REGISTRY_DIR")
	if registryDir == "" {
		registryDir = dir // .id-ledger.txt lives alongside the tool sources
	}

	priorityYAML := os.Getenv("REGISTRY_PRIORITY")
	if priorityYAML == "" {
		// Infer from REGISTRY_ITEMS path: sibling to items.yaml.
		if os.Getenv("REGISTRY_ITEMS") != "" {
			priorityYAML = filepath.Join(filepath.Dir(os.Getenv("REGISTRY_ITEMS")), "priority.yaml")
		} else {
			priorityYAML = filepath.Join(dir, "testdata", "priority.yaml")
		}
	}

	templatesDir := os.Getenv("REGISTRY_TEMPLATES")
	if templatesDir == "" {
		templatesDir = filepath.Join(dir, "templates")
	}

	outDir := os.Getenv("REGISTRY_OUTDIR") // empty = stdout mode

	return mutatorPaths{
		itemsYAML:     itemsYAML,
		questionsYAML: questionsYAML,
		priorityYAML:  priorityYAML,
		registryDir:   registryDir,
		templatesDir:  templatesDir,
		outDir:        outDir,
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

func runValidate(args []string, migrating bool, paths mutatorPaths) {
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

	// Load priority from the configured path (tolerate absent file).
	if paths.priorityYAML != "" {
		reg.Priority, err = loadPriority(paths.priorityYAML)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ve := validateWith(reg, validateOpts{migrating: migrating})
	pve := validatePriority(reg, reg.Priority)
	ve.msgs = append(ve.msgs, pve.msgs...)

	if ve.hasErrors() {
		for _, msg := range ve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}
	fmt.Println("registry: validate OK")
}

func runGen(args []string, paths mutatorPaths) {
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
	if paths.priorityYAML != "" {
		reg.Priority, err = loadPriority(paths.priorityYAML)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ve := validateWith(reg, validateOpts{migrating: paths.migrating})
	pve := validatePriority(reg, reg.Priority)
	ve.msgs = append(ve.msgs, pve.msgs...)
	if ve.hasErrors() {
		for _, msg := range ve.msgs {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}

	// Four views: item-open, item-closed, question-open, backlog (when priority exists).
	// Spec §"Generator" and §"Questions — transient by design".
	// genToOutDirOrStdout handles both stdout and in-place file modes.
	genToOutDirOrStdout(reg, paths)
}
