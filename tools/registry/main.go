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
//	registry ready                                    (unblocked, not-yet-started pullable items, in priority order)
//	registry in-progress                              (items currently IN_PROGRESS — the session completeness ledger)
//	registry view       --id iN|qN [--format text|json]  (one record + computed dependents/rank)
//	registry dependents --id iN                       (ids that depend_on iN)
//	registry dag                                      (all dependency edges, one per line)
//
//	Priority mutators (rewrite registry/priority.yaml; validate+gen after):
//
//	registry prioritize --id iN --to-top
//	registry move       --id iN --before iM
//	registry move       --id iN --after  iM
//
//	Mutating subcommands (rewrite the live registry/*.yaml + registry/.id-ledger.txt;
//	run `make registry`, or set REGISTRY_OUTDIR, to regenerate docs/notes/*.md).
//	Item/question ids are ALWAYS tool-determined — there is no next-id and no
//	caller-supplied --id:
//
//	registry [--migrating] add     [--space items|questions] --owner … (items: --title --status [--desc] [--kind] [--pr N [--role …]] [--dep …]… [--ref …]…) (questions: --desc)
//	registry [--migrating] split   --parent iN --title … [--desc …] [--status …] [--owner …] [--kind …] [--pr N [--role …]] [--dep …]… [--ref …]…   (child id determined from the parent)
//	registry [--migrating] set-status --id iN --status … [--pr N]
//	registry [--migrating] set-title  --id iN --title "…"        (items only)
//	registry [--migrating] set-owner  --id iN --owner agent|pete  (items only)
//	registry [--migrating] set-desc   --id iN|qN --desc "…"      (item description / question body)
//	registry [--migrating] set-pr  --id iN --pr N [--role completing|followup]
//	registry [--migrating] dep     add|rm --id iN --on iM|qN
//	registry [--migrating] answer  --id qN
//
// --migrating is a global flag: it may appear anywhere before the subcommand
// and defers only invariant 10 (id-shaped ref existence). Invariants 11/12/13
// (depends_on DAG, no-WONTFIX-target, delete-gate) remain strict.
//
// Environment variables (all optional). When unset, the data paths default to
// the live repo registry/ found by walking up from the current directory; the
// CLI never falls back to the bundled test fixtures (run from the repo root, or
// set REGISTRY_ITEMS, or it errors):
//
//	REGISTRY_ITEMS      path to items.yaml          (default: <live registry/>/items.yaml)
//	REGISTRY_QUESTIONS  path to questions.yaml       (default: sibling of items.yaml)
//	REGISTRY_PRIORITY   path to priority.yaml        (default: sibling of items.yaml)
//	REGISTRY_DIR        directory for .id-ledger.txt (default: the live registry/ dir)
//	REGISTRY_TEMPLATES  directory of *.head.md templates (default: tools/registry/templates)
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

	// A mutating command run with a stale binary would rewrite the live registry
	// with old logic, silently (the i138 incident — i142). Refuse before doing any
	// work (and before taking the lock) if the binary is older than its source.
	if mutatingCommands[cmd] {
		assertBinaryFresh()
	}

	// Serialize mutating commands behind an exclusive advisory lock so two
	// concurrent invocations (e.g. Pete adds an item while an agent runs `dep
	// add`) cannot interleave their load→mutate→write and clobber each other.
	// Read-only commands (validate/gen/ready/view/dependents/dag/next-id) do not
	// take the lock. Skipped when registryDir is unresolved — the command's
	// loadReg then fails with the "no data source" error instead.
	if mutatingCommands[cmd] && paths.registryDir != "" {
		if err := lockRegistry(paths.registryDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	switch cmd {
	case "validate":
		runValidate(args[1:], migrating, paths)
	case "gen":
		runGen(args[1:], paths)
	case "ready":
		runReady(args[1:], paths)
	case "in-progress":
		runInProgress(args[1:], paths)
	case "view":
		runView(args[1:], paths)
	case "dependents":
		runDependents(args[1:], paths)
	case "dag":
		runDAG(args[1:], paths)
	case "prioritize":
		runPrioritize(args[1:], paths)
	case "move":
		runMove(args[1:], paths)
	case "add":
		runAdd(args[1:], paths)
	case "split":
		runSplit(args[1:], paths)
	case "set-status":
		runSetStatus(args[1:], paths)
	case "set-title":
		runSetTitle(args[1:], paths)
	case "set-owner":
		runSetOwner(args[1:], paths)
	case "set-desc":
		runSetDesc(args[1:], paths)
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
	fmt.Fprintln(os.Stderr, "  registry ready [--pete-present] (unblocked pullable items in priority order; excludes owner:pete by default — --pete-present includes + prioritizes them)")
	fmt.Fprintln(os.Stderr, "  registry in-progress            (items currently IN_PROGRESS — the session completeness ledger)")
	fmt.Fprintln(os.Stderr, "  registry view       --id iN|qN [--format text|json]")
	fmt.Fprintln(os.Stderr, "  registry dependents --id iN")
	fmt.Fprintln(os.Stderr, "  registry dag")
	fmt.Fprintln(os.Stderr, "  registry prioritize --id iN --to-top")
	fmt.Fprintln(os.Stderr, "  registry move       --id iN --before iM")
	fmt.Fprintln(os.Stderr, "  registry move       --id iN --after  iM")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] add      [--space items|questions] --owner … (items: --title --status [--desc] [--kind] [--pr N] [--dep …]… [--ref …]…) (questions: --desc)   (id is auto-allocated)")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] split    --parent iN --title … [--desc …] [--status …] [--owner …] [--kind …] [--pr N] [--dep …]… [--ref …]…   (child id auto-determined)")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-status --id iN --status … [--pr N]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-title  --id iN --title \"…\"        (items only)")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-owner  --id iN --owner agent|pete  (items only)")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-desc   --id iN|qN --desc \"…\"      (item description / question body)")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] set-pr   --id iN --pr N [--role completing|followup]")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] dep      add|rm --id iN --on iM|qN")
	fmt.Fprintln(os.Stderr, "  registry [--migrating] answer   --id qN")
}

// defaultMutatorPaths resolves where the registry CLI reads and writes when run
// without explicit REGISTRY_* env vars. The data paths are resolved, in order:
// an explicit REGISTRY_* env var (always wins), else the LIVE repo registry/
// discovered by walking up from the current working directory for a
// registry/items.yaml. The data source is NEVER guessed to be the bundled test
// fixtures — an accidental testdata fallback could be catastrophic (stale reads,
// or writes to the fixtures). When no source is resolvable the item path is left
// empty and loadReg fails with a clear "no data source" message; the
// positional-arg commands (`validate`/`gen`) supply their own paths and are
// unaffected. (A fuller "always explicit" design — config file / required flag,
// dropping the cwd-walk — is tracked as i150.)
func defaultMutatorPaths() mutatorPaths {
	// Discover the live repo registry/ by walking up from cwd.
	repoReg := ""
	if cwd, err := os.Getwd(); err == nil {
		repoReg = findRepoRegistryDir(cwd)
	}

	// Resolve the items path and the base dir its siblings derive from: explicit
	// REGISTRY_ITEMS wins; else the discovered live registry; else leave empty
	// (loadReg surfaces the error) — never the test fixtures.
	itemsYAML := os.Getenv("REGISTRY_ITEMS")
	baseDir := ""
	switch {
	case itemsYAML != "":
		baseDir = filepath.Dir(itemsYAML)
	case repoReg != "":
		baseDir = repoReg
		itemsYAML = filepath.Join(repoReg, "items.yaml")
	}

	questionsYAML := os.Getenv("REGISTRY_QUESTIONS")
	if questionsYAML == "" && baseDir != "" {
		questionsYAML = filepath.Join(baseDir, "questions.yaml")
	}

	// priorityYAML is a sibling of items.yaml.
	priorityYAML := os.Getenv("REGISTRY_PRIORITY")
	if priorityYAML == "" && baseDir != "" {
		priorityYAML = filepath.Join(baseDir, "priority.yaml")
	}

	// .id-ledger.txt lives alongside the YAML sources.
	registryDir := os.Getenv("REGISTRY_DIR")
	if registryDir == "" {
		registryDir = baseDir
	}

	// Templates are read-only header prose (not data), so a default here is
	// harmless: prefer the repo's templates dir, else the tool's source dir.
	templatesDir := os.Getenv("REGISTRY_TEMPLATES")
	if templatesDir == "" {
		if repoReg != "" {
			templatesDir = filepath.Join(filepath.Dir(repoReg), "tools", "registry", "templates")
		} else {
			templatesDir = filepath.Join(toolDir(), "templates")
		}
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
