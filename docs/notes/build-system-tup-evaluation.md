# Build system: `make` → `tup` evaluation (i123)

**Question.** Should sam-aarch64 migrate its build from the current 1700-line GNU
`Makefile` (167 targets) to [`tup`](http://gittup.org/tup/)? Motivated by the
recurring **stale-artifact bug class** — the off-axis test binaries baking
**stale symbol addresses** when only part of the build is rebuilt (the
`rebuild_offaxis_cluster_after_main_changes` memory entry; the class i116 patches
test-by-test). Pete uses tup in `~/git/spectrum4` ("amazing"). This note is the
evaluation; performing the migration is a separate, Pete-gated step (i123b).

## How tup differs from make (the two facts that decide it)

tup runs every build command inside a **FUSE filesystem** (or an `LD_PRELOAD`
shim where FUSE is unavailable) and records the files the command *actually*
opens — so dependencies are observed, not declared
([tup manual](http://gittup.org/tup/manual.html); [FUSE blog](https://gittup.org/blog/2013/04/3-linking-libxul-with-tup-and-fuse/)).
Two consequences are decisive here:

1. **Source includes are auto-tracked.** When `pyz80 foo.asm` internally opens
   `include "bar.asm"`, tup records `bar.asm` as a dependency with no Tupfile
   declaration — editing `bar.asm` re-triggers the assemble. The current
   Makefile's defensive `$(wildcard src/**/*.asm)` over-declaration
   (`Makefile:1313`) becomes unnecessary. spectrum4 proves this live: its `as`
   rule lists only the `.target` file, yet every `.include`d source (dozens, via
   `-I` dirs) re-triggers correctly (`src/spectrum4/targets/Tupfile`).

2. **Reading an *undeclared generated* file is a hard error — this is the cure.**
   A file that another rule produces, opened for reading but not declared as an
   input, makes tup **fail the build** rather than silently use a stale copy
   ([manual](http://gittup.org/tup/manual.html)). Our footgun is exactly this:
   `test_mem.bin`/`test_cluster.bin` import `assembler.sym` (`Makefile:1336`,
   `:1360`); make rebuilds them only on `assembler.sym`'s *timestamp*, so a
   source edit that leaves the symbol values' file mtime untouched ships stale
   addresses. Under tup, `assembler.sym` is a generated input that **must** be
   declared on the rule — and tup re-runs the rule whenever the file's contents
   change, not its mtime. The missing edge is converted from a silent stale
   binary into a build-time error. spectrum4 declares its analogue
   (`sysvars.gen-s`) exactly this way: `: foreach *.target | ../libextra/sysvars.gen-s |> as ... |>`.

A third, distinct win: tup has **no `clean` target** — when a rule is removed or
its output renamed, tup deletes the now-orphaned output, so dead `.bin`s can't
linger ([manual](http://gittup.org/tup/manual.html)).

## How each part of our build ports

| Build category (see the Makefile map) | tup shape | Notes |
|---|---|---|
| pyz80 `.asm` → `.bin`/`.sym` (~90 rules) | `: foreach *.asm \|> pyz80 ... \|> %B.bin` | source `include`s auto-tracked (fact 1); `%B`/`%o` flags |
| Symbol-import chain (enc_fix_payload→assembler→test_mem/test_cluster) | generated `.sym` declared as order-only input `\| ...sym` | **the footgun cure** (fact 2) |
| Go tool builds (`tools/*`) | `: \|> go build -buildvcs=false -o %o <explicit .go> \|> tool` | must list sources + `-buildvcs=false` or `go build`'s `git status` drags the whole repo in (spectrum4 `utils/*/Tupfile`) |
| Table codegen (tables-gen → `enctab.enc`, `*.inc`) | a rule whose outputs are declared; consumers declare them as inputs | replaces the `tables-sync-check` freshness guard with a structural edge |
| Disk-image (`build-disk` → `.mgt`) | one multi-input rule listing each `.bin` (or auto-detected if it reads a manifest) | mirrors spectrum4 `dist/Tupfile` staging |
| `^o^` byte-compare short-circuit | prefix rules with `^o^` | a byte-identical re-assemble skips downstream — fits our byte-match-gate workflow |
| SimCoupé / release-gate / harness | **stay in `ci.yml` / scripts** | tup covers the host build graph, not the Docker-gated emulation runs |

## Costs and risks (the honest other side)

- **CI needs privileged FUSE.** The monitor doesn't work in a default container;
  it needs `--cap-add SYS_ADMIN --device /dev/fuse --security-opt apparmor:unconfined`
  (or a tup built with the `ldpreload` server)
  ([tup #394](https://github.com/gittup/tup/issues/394)). spectrum4 already runs
  exactly this on GitHub Actions (`tup-under-docker.sh`), so it is proven viable
  in our Docker-based CI — but it is a real operational cost, not free.
- **No CI wall-time win.** tup keys on `ctime`, which a fresh checkout resets, so
  CI is always a full build (spectrum4's own CI comment). tup's incrementality
  speeds **local** iteration, not the CI matrix — the inner-loop benefit is real,
  the gate-time benefit is nil.
- **A real rewrite.** 167 make targets → per-directory Tupfiles + a `Tuprules.tup`;
  the netboot ecosystem alone is ~80 standalone binaries. Mechanical but not
  small; best done incrementally, validated against the existing make outputs.
- **Model constraints.** Outputs must be declared up front; exactly one rule may
  write a given file; generated files must be tracked or tup errors. These are
  the price of the guarantees, not incidental bugs.
- **Second build system to learn/maintain** until make is fully retired (a
  during-migration period of two systems).

## Recommendation

**Migrate — but as a deliberate, Pete-greenlit step, not unattended.** tup
*structurally eliminates* the stale-artifact class that has bitten this project
(the off-axis cluster footgun becomes a build error; orphaned `.bin`s self-delete),
which is worth more here than in most projects because so much of the build is
symbol-import-chained pyz80 output. The pattern is de-risked: Pete's spectrum4 is
a working, CI-gated tup build with a directly-reusable template, including the
hard part (FUSE-in-Docker on GitHub Actions). The costs (privileged CI, a real
rewrite, no CI-time win) are bounded and known.

Because a build-system swap affects every contributor and the CI gate, the
decision to *do* it is Pete's. The migration itself is tracked as **i123b**
(owner: **pete** — the go/no-go is his; once greenlit, the implementation is
agent work). A sensible migration order, when greenlit: (1) the symbol-import cluster
(enc_fix_payload → assembler → test_mem/test_cluster) first — highest-value, it is
the footgun; (2) the netboot standalone binaries (parallel, no inter-deps);
(3) the disk-pack + table-codegen edges; keeping SimCoupé/release-gate in `ci.yml`
throughout and byte-comparing tup outputs against make outputs until parity.

## Sources

- [tup manual](http://gittup.org/tup/manual.html), [tup homepage](http://gittup.org/tup/),
  [tup + FUSE blog](https://gittup.org/blog/2013/04/3-linking-libxul-with-tup-and-fuse/),
  [tup #394 (ldpreload / CI)](https://github.com/gittup/tup/issues/394).
- `~/git/spectrum4` Tupfiles: `Tuprules.tup`, `src/spectrum4/targets/Tupfile`,
  `src/spectrum4/libextra/Tupfile`, `src/spectrum4/dist/Tupfile`,
  `utils/*/Tupfile`, `tup-under-docker.sh`.
- This repo's `Makefile` (the build-graph map behind the porting table above) and
  the `rebuild_offaxis_cluster_after_main_changes` memory entry / i116 (the
  stale-artifact class).
