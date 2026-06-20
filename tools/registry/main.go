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
// defaultMutatorPaths resolves where the registry CLI reads and writes when run
// without explicit REGISTRY_* env vars. Resolution order, per path:
//
//  1. an explicit REGISTRY_* env var (always wins);
//  2. the LIVE repo registry/, discovered by walking up from the current working
//     directory for a registry/items.yaml — so a bare `build/registry …` run from
//     the repo root operates on the real registry;
//  3. the bundled testdata fixtures, but only with a LOUD stderr warning.
//
// Step 2 is the fix for the footgun where a bare invocation silently read (and
// `add`/`set-status` would have written) the test fixtures instead of the real
// registry — the very thing the docs tell agents to run. When the live registry
// is found, the generated docs/notes views are also regenerated in place (unless
// REGISTRY_OUTDIR overrides) so a mutation never leaves the .md views stale.
func defaultMutatorPaths() mutatorPaths {
	td := toolDir()

	// Discover the live repo registry/ by walking up from cwd.
	repoReg := ""
	if cwd, err := os.Getwd(); err == nil {
		repoReg = findRepoRegistryDir(cwd)
	}

	// Base dir that unset item/question/priority paths derive from, plus the
	// matching templates dir.
	var baseDir, tmplDir string
	if repoReg != "" {
		repoRoot := filepath.Dir(repoReg)
		baseDir = repoReg
		tmplDir = filepath.Join(repoRoot, "tools", "registry", "templates")
	} else {
		baseDir = filepath.Join(td, "testdata")
		tmplDir = filepath.Join(td, "templates")
	}

	itemsYAML := os.Getenv("REGISTRY_ITEMS")
	if itemsYAML == "" {
		if repoReg == "" {
			fmt.Fprintln(os.Stderr, "registry: WARNING — no live registry/items.yaml found by walking up from the current directory; falling back to the bundled testdata fixtures. Run from the repo root, or set REGISTRY_ITEMS, to operate on the real registry.")
		}
		itemsYAML = filepath.Join(baseDir, "items.yaml")
	}

	questionsYAML := os.Getenv("REGISTRY_QUESTIONS")
	if questionsYAML == "" {
		questionsYAML = filepath.Join(baseDir, "questions.yaml")
	}

	// priorityYAML is a sibling of items.yaml — honours an explicit
	// REGISTRY_ITEMS override and works for both the live and testdata bases.
	priorityYAML := os.Getenv("REGISTRY_PRIORITY")
	if priorityYAML == "" {
		priorityYAML = filepath.Join(filepath.Dir(itemsYAML), "priority.yaml")
	}

	// .id-ledger.txt lives alongside the YAML sources.
	registryDir := os.Getenv("REGISTRY_DIR")
	if registryDir == "" {
		if repoReg != "" {
			registryDir = repoReg
		} else {
			registryDir = td
		}
	}

	templatesDir := os.Getenv("REGISTRY_TEMPLATES")
	if templatesDir == "" {
		templatesDir = tmplDir
	}

	// outDir empty = stdout mode. Mutators print the regenerated views to stdout;
	// `make registry` (REGISTRY_OUTDIR=docs/notes) writes the .md views in place,
	// and the registry-sync-check CI gate catches any forgotten regen. We do NOT
	// auto-write docs/notes here: `gen`/`validate` take positional file args, so a
	// discovered outDir would let `gen testdata/items.yaml` clobber the real views.
	outDir := os.Getenv("REGISTRY_OUTDIR")

	return mutatorPaths{
		itemsYAML:     itemsYAML,
		questionsYAML: questionsYAML,
		priorityYAML:  priorityYAML,
		registryDir:   registryDir,
		templatesDir:  templatesDir,
		outDir:        outDir,
	}
}

// findRepoRegistryDir walks up from startDir looking for a directory that
// contains registry/items.yaml, and returns the path to that registry/ dir
// (or "" if none is found before the filesystem root). This is how a bare CLI
// invocation locates the live repo registry regardless of the subdirectory it
// is run from.
func findRepoRegistryDir(startDir string) string {
	dir := startDir
	for {
		if fileExists(filepath.Join(dir, "registry", "items.yaml")) {
			return filepath.Join(dir, "registry")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the filesystem root
		}
		dir = parent
	}
}

// fileExists reports whether path names an existing (non-directory) file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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

	// Load priority as the sibling of the positional items file (tolerate absent
	// file) so `validate path/items.yaml` checks path/priority.yaml — keeping the
	// command self-consistent regardless of which registry the cwd-walk found.
	priorityPath := priorityForPositional(args[0])
	if priorityPath != "" {
		reg.Priority, err = loadPriority(priorityPath)
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

// priorityForPositional resolves the priority.yaml path for a validate/gen
// invocation given its positional items arg. An explicit REGISTRY_PRIORITY env
// var wins; otherwise priority is the sibling of the positional items file, so
// the command validates a self-consistent (items, questions, priority) triple
// from the same directory rather than mixing in whatever registry the cwd-walk
// discovered.
func priorityForPositional(itemsArg string) string {
	if p := os.Getenv("REGISTRY_PRIORITY"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(itemsArg), "priority.yaml")
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
	// Priority is the sibling of the positional items file (REGISTRY_PRIORITY env
	// overrides) — self-consistent with the items/questions just loaded.
	priorityPath := priorityForPositional(args[0])
	if priorityPath != "" {
		reg.Priority, err = loadPriority(priorityPath)
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
