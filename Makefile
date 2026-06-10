# sam-aarch64 build — the SAM-side Z80 aarch64 assembler + its Go-side
# host toolchain (sam-aarch64 / enctab-gen) and round-trip gates.
# (The original M0 nop-to-disk round-trip oracle was retired once the
# core–paged fixture corpora + the release-gate 3-way gate fully subsumed it.)

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BUILD := build
TESTS := tests

.PHONY: all clean

# Default build: the two shipping assembler variants (the recipe for each
# also runs tools/check-code-budget.sh inline).
all: assembler assembler-prod

clean:
	rm -rf $(BUILD)

.PHONY: sam-aarch64 test-format ci-format

# sam-aarch64 — the integrated host assembler: source -> {binary, compact .tbn},
# .tbn -> binary, .tbn -> text. Replaces the former text2bin/refenc/bin2text
# trio; the "symbolic" record stream is an in-memory IR, never serialized to
# disk (i48 decision A).
sam-aarch64:
	cd tools/sam-aarch64 && go build -o $(CURDIR)/$(BUILD)/sam-aarch64 .

# aarch64dec — Go-side aarch64 disassembler (strand B); inverse of aarch64enc.
.PHONY: aarch64dec test-disasm ci-disasm ci-disasm-roundtrip

aarch64dec:
	cd tools/aarch64dec/cmd/aarch64dec && go build -o $(CURDIR)/$(BUILD)/aarch64dec .

# Unit tests for the disassembler package.
test-disasm:
	cd tools/aarch64dec && go test ./...

# Oracle gate: aarch64dec vs binutils objdump on the vendored release.img.
# RED until the decoder is complete (TDD); the diff is the worklist.  See
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-28-go-aarch64-disassembler.md.  Needs an aarch64
# objdump (binutils-aarch64-linux-gnu) + Go; no SimCoupé/container.
ci-disasm: test-disasm
	./tests/disasm/run-oracle-comparison.sh

# Round-trip gate: encode→decode→encode must produce identical bytes for
# all core–paged fixture sources.  Pure Go pipeline, no binutils or container.
ci-disasm-roundtrip: test-disasm
	./tools/run-disasm-roundtrip.sh

test-format: sam-aarch64
	cd tools/sam-aarch64-format && go test ./...
	cd tools/sam-aarch64 && go test ./...
	./tests/format/run-gnu-as-check.sh

ci-format: test-format

# sysreg-sync-check — Go↔Z80 sysreg/pstate/dc/tlbi table sync guard
# (repo-audit 2026-05-29 §5 / §6 item #9).  Asserts every entry in the
# hand-maintained Z80 table src/sysreg_data.asm matches the Go authority
# tools/sam-aarch64-format/sysregs.go byte-for-byte, so the two can't
# silently drift.  Cheap (pure Go, no container) — also runs implicitly
# inside test-format / test-encoder's `go test ./...`, but is exposed here
# as a standalone target so it can be a named CI check / branch-protection
# gate.
.PHONY: sysreg-sync-check
sysreg-sync-check:
	cd tools/sam-aarch64-format && go test -run TestSysregZ80Sync -v ./...

# staticcheck — dead-code gate (the `unused`/U1000 check, i.e. the same
# analysis golangci-lint's `unused` linter wraps) across the core host
# toolchain modules.  Pure Go, no container.  Pinned + forced to build with
# go1.26.1 via GOTOOLCHAIN: a released staticcheck builds with go1.25 and
# can't parse the go1.26 modules, so GOTOOLCHAIN=go1.26.1 makes `go run`
# build the checker with the matching toolchain (downloaded if absent).
# Scoped to U1000 so it is green on a never-linted tree and precisely
# catches dead code; broaden the -checks set later if desired.  Add new
# modules to STATICCHECK_MODULES as they appear.
.PHONY: staticcheck
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.7.0
STATICCHECK_MODULES := sam-aarch64-format sam-aarch64 aarch64enc aarch64dec enctab-gen z80-test-harness-go
staticcheck:
	for m in $(STATICCHECK_MODULES); do \
	    echo "=== staticcheck (U1000) $$m ==="; \
	    ( cd tools/$$m && GOTOOLCHAIN=go1.26.1 go run $(STATICCHECK) -checks U1000 ./... ); \
	done

# check-doc-links — assert every relative markdown link in the entry docs
# (README.md, CLAUDE.md, src/README.md) and under docs/, tools/, tests/
# resolves to an existing path.  Pure shell, no toolchain; runs as an
# extra step of the staticcheck CI job.
.PHONY: check-doc-links
check-doc-links:
	bash tools/check-doc-links.sh

.PHONY: enctab-gen enctab test-encoder ci-encoder

enctab-gen:
	cd tools/enctab-gen && go build -o $(CURDIR)/$(BUILD)/enctab-gen .

# Build the binary enctab.enc artefact from the vendored MRA snapshot.
# Includes both MRA-derived (data.go) and hand-curated (manual_forms.go)
# forms; the binary mirrors the Go-side runtime form table.  Does NOT
# touch any source files.
enctab: enctab-gen
	$(BUILD)/enctab-gen \
	    -mra reference/arm-mra \
	    -out $(BUILD)/enctab.enc

# Regenerate tools/aarch64enc/data.go from the vendored MRA snapshot.
# Safe to run at any time: data.go is purely the MRA projection; all
# hand-curated forms live in tools/aarch64enc/manual_forms.go which
# this target never touches.  See https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m2-status.md.
.PHONY: enctab-regen-source
enctab-regen-source: enctab-gen
	$(BUILD)/enctab-gen \
	    -mra reference/arm-mra \
	    -gopkg tools/aarch64enc/data.go \
	    -out $(BUILD)/enctab.enc

test-encoder: sam-aarch64
	cd tools/sam-aarch64-format && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/enctab-gen && go test ./...
	cd tools/sam-aarch64 && go test ./...
	./tests/format/run-refenc-roundtrip.sh
	./tests/spectrum4/run-roundtrip.sh

ci-encoder: test-encoder

.PHONY: assembler assembler-prod build-disk disk test-mem-offaxis cluster-offaxis paged-call-payload sysreg-data disasm-payload disasm-test-payload test-core ci-core check-budget

# check-budget — fail if either assembler variant has grown into the
# &C000 stack page (the silent boot-hang cliff; see
# tools/check-code-budget.sh + memory/feedback_test_variant_fragility.md).
# The same assertion also runs inline at the tail of each assembler build
# recipe, so any `make assembler` / `make assembler-prod` enforces it too;
# this target is the explicit both-variants entry point used by CI.
check-budget: assembler assembler-prod
	./tools/check-code-budget.sh

# Two build variants of the SAM-side assembler:
#
#   assembler       (test variant, default for dev / ci-core / ci-symbols)
#                   Includes all boot-time self-tests (slots / symbols /
#                   local labels / expr_eval / PC-rel).  Larger binary
#                   but catches per-routine regressions before the
#                   fixture-corpus round-trip even runs.  This is what
#                   tests/core/run-roundtrip.sh expect.
#
#   assembler-prod  (production variant, for end-user shipping)
#                   Self-tests #ifdef'd out via `-D BUILD_TESTS=0` (i.e.
#                   BUILD_TESTS is undefined; `if defined(BUILD_TESTS)`
#                   blocks in src/assembler.asm are skipped).  Smaller
#                   binary — frees code budget.  Identical OUT bytes on
#                   every fixture (the self-tests don't affect the assemble
#                   path); the build-split-status target verifies this.
#
# Both variants byte-match GNU on all fixture corpora.

assembler: $(BUILD)/assembler.bin

assembler-prod: $(BUILD)/assembler-prod.bin

# Test-variant build also exports the symbol table for the off-axis
# test_mem.bin to import (plan-PR 3 — see
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md).
$(BUILD)/assembler.bin $(BUILD)/assembler.sym: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 \
	    --obj=$(BUILD)/assembler.bin \
	    --exportfile=$(BUILD)/assembler.sym \
	    src/assembler.asm
	@./tools/check-code-budget.sh $(BUILD)/assembler.bin test

$(BUILD)/assembler-prod.bin: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/assembler-prod.bin src/assembler.asm
	@./tools/check-code-budget.sh $(BUILD)/assembler-prod.bin prod

# Off-axis test_mem build (BUILD_TESTS only).
#
# test_mem_offaxis.asm is a thin wrapper that does `org &0000` then
# `include "test_mem.asm"`.  Imports section-C symbols (encode_mem_word,
# assert_eq32_de_hl_imm, OPVAL_ARRAY, ...) from the just-built
# assembler.sym so that production calls resolve to their real
# addresses in the main binary.  The resulting build/test_mem.bin is
# small (~780 B) and is HLOADed at boot into physical page 13 by
# src/loader.asm::load_test_mem_off_axis.  See plan-PR 3 brief.
$(BUILD)/test_mem.bin: src/test_mem_offaxis.asm src/test_mem.asm $(BUILD)/assembler.sym
	pyz80 --importfile=$(BUILD)/assembler.sym \
	    --obj=$(BUILD)/test_mem.bin \
	    src/test_mem_offaxis.asm

test-mem-offaxis: $(BUILD)/test_mem.bin

# Off-axis "operands + misc encoder" cluster build (BUILD_TESTS only).
#
# test_offaxis_cluster.asm is a thin wrapper that does `org &0000` then
# includes the pc_rel / directives / ror_imm / shifted_reg /
# extended_reg / litpool self-test suites behind a small dispatcher.
# Imports section-C/D production symbols (encode_*, litpool_*, symbol_*,
# compute_directive_size, assert_eq32_de_hl_imm, fail, ...) from the
# just-built assembler.sym.  The resulting build/test_cluster.bin
# (~1225 B) is HLOADed at boot into physical page 12 by
# src/loader.asm::load_offaxis_cluster and invoked via one LMPR swap.
# See src/test_offaxis_cluster.asm.
$(BUILD)/test_cluster.bin: src/test_offaxis_cluster.asm \
		src/test_slots.asm src/test_pc_rel.asm \
		src/test_directives.asm src/test_ror_imm.asm \
		src/test_shifted_reg.asm src/test_extended_reg.asm \
		src/test_litpool.asm \
		$(BUILD)/assembler.sym
	pyz80 --importfile=$(BUILD)/assembler.sym \
	    --obj=$(BUILD)/test_cluster.bin \
	    src/test_offaxis_cluster.asm

cluster-offaxis: $(BUILD)/test_cluster.bin

# paged_call self-test payload (BUILD_TESTS only).
#
# A 3-byte standalone binary (`ld a, &42; ret`) HLOAD'd at boot into
# physical page 14 by src/loader.asm::load_page14_payload.
# Exercised by src/test_paged_call.asm.  Per plan-PR 1 of
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md.
$(BUILD)/paged_call_test_payload.bin: src/paged_call_test_payload.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/paged_call_test_payload.bin src/paged_call_test_payload.asm

paged-call-payload: $(BUILD)/paged_call_test_payload.bin

# Page-13 sysreg lookup data (PRODUCTION feature — both variants).
#
# A small standalone binary (~480 B) holding the four sysname lookup
# tables (sysreg / pstate / dc / tlbi) and a self-contained matcher,
# org &8000.  HLOAD'd at boot into physical page 13 by
# src/loader.asm::load_page13_payload and read at runtime by the
# sysname_lookup_* routines via paged_call.  Per PR-2 of
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-29-m6-closure-release-bytematch.md (split-design
# correction documented in src/sysreg_data.asm).  Needed by EVERY
# build, not just BUILD_TESTS — sysreg/dc/tlbi/pstate operands appear
# in shipping sources.
$(BUILD)/sysreg_data.bin: src/sysreg_data.asm src/sysreg_tables.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/sysreg_data.bin src/sysreg_data.asm

sysreg-data: $(BUILD)/sysreg_data.bin

# Disassembler binary — two variants, mirroring the assembler split.
#
# Standalone disassembler stub (org &8000) HLOAD'd at boot into physical
# page 15 by src/loader.asm::load_page15_payload.  The decoder entry at
# DISASM_ENTRY (&8000) is production; the &8003 self-test slot
# (DISASM_SELF_TEST_ENTRY) and the whole run_disasm_self_test body are
# wrapped in `if defined(BUILD_TESTS)` in src/disasm.asm.  Not an
# importfile user — assembles standalone.
#
#   disasm.bin       (PROD)  no flag — decoder only, self-test stripped.
#                    Ships on every production disk (symbols-prod,
#                    operands-prod, paged-prod, release-gate) where no
#                    boot self-test runs.
#
#   disasm-test.bin  (TEST)  -D BUILD_TESTS=1 — includes the &8003
#                    self-test entry + fixtures.  Ships only on the test
#                    disk (the BUILD_TESTS assembler boot calls &8003 via
#                    paged_call to verify the decoder).
$(BUILD)/disasm.bin: src/disasm.asm src/sysreg_names.inc src/sysreg_tables.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/disasm.bin src/disasm.asm

$(BUILD)/disasm-test.bin: src/disasm.asm src/sysreg_names.inc src/sysreg_tables.inc
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 --obj=$(BUILD)/disasm-test.bin src/disasm.asm

disasm-payload: $(BUILD)/disasm.bin

disasm-test-payload: $(BUILD)/disasm-test.bin

$(BUILD)/build-disk: tools/build-disk/main.go tools/build-disk/go.mod
	@mkdir -p $(BUILD)
	cd tools/build-disk && go build -o ../../$(BUILD)/build-disk .

build-disk: $(BUILD)/build-disk

# disk uses the TEST assembler (assembler.bin, BUILD_TESTS=1), whose
# boot sequence calls the disasm &8003 self-test via paged_call — so it
# must ship the TEST disasm binary (disasm-test.bin).
disk: assembler test-mem-offaxis cluster-offaxis paged-call-payload sysreg-data disasm-test-payload enctab $(BUILD)/build-disk
	$(BUILD)/build-disk \
	    -test-mem $(BUILD)/test_mem.bin \
	    -cluster $(BUILD)/test_cluster.bin \
	    -paged-call $(BUILD)/paged_call_test_payload.bin \
	    -sysreg-data $(BUILD)/sysreg_data.bin \
	    -disasm $(BUILD)/disasm-test.bin \
	    $(BUILD)/assembler.bin $(BUILD)/enctab.enc $(BUILD)/test.mgt

# test-core — sweep every fixture under tests/core/sources/ end-to-end:
# sam-aarch64 → build-disk → SimCoupé → samfile extract OUT →
# byte-compare against aarch64-{none-elf,linux-gnu}-as + objcopy -O binary.
test-core: assembler test-mem-offaxis paged-call-payload enctab $(BUILD)/build-disk sam-aarch64
	./tests/core/run-roundtrip.sh

ci-core: test-core

.PHONY: test-symbols ci-symbols

# test-symbols — sweep every fixture under tests/symbols/sources/.  Reuses
# the assembler binary and build-disk, but feeds it symbols-fixture .tbn
# inputs and uses an oracle that includes `ld -Ttext=0` so :lo12: /
# branch-to-label relocations resolve.  See
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md §3.
test-symbols: assembler test-mem-offaxis paged-call-payload enctab $(BUILD)/build-disk sam-aarch64
	./tests/symbols/run-roundtrip.sh

ci-symbols: test-symbols

.PHONY: test-core-prod test-symbols-prod ci-core-prod ci-symbols-prod

# Production-variant sweeps — same fixture corpora, same oracle, but
# with the smaller assembler binary that omits the boot-time self-tests.
# Useful as a correctness check that the BUILD_TESTS=1 / undefined
# fork in src/assembler.asm doesn't accidentally change emit behaviour.
# ci-core/ci-symbols cover the test variant; these cover prod.
test-core-prod: assembler-prod enctab $(BUILD)/build-disk sam-aarch64
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/core/run-roundtrip.sh

test-symbols-prod: assembler-prod enctab $(BUILD)/build-disk sam-aarch64
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/symbols/run-roundtrip.sh

ci-core-prod: test-core-prod

ci-symbols-prod: test-symbols-prod

.PHONY: test-operands ci-operands test-operands-prod ci-operands-prod

# test-operands — sweep every fixture under tests/operands/sources/.  Same
# pipeline as test-symbols (sam-aarch64 → build-disk → SimCoupé → samfile
# extract OUT → byte-compare against aarch64-*-as + ld -Ttext=0 +
# objcopy -O binary).  Per
# https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-27-m5-compound-operands-directives-design.md §3.
test-operands: assembler test-mem-offaxis paged-call-payload enctab $(BUILD)/build-disk sam-aarch64
	./tests/operands/run-roundtrip.sh

test-operands-prod: assembler-prod enctab $(BUILD)/build-disk sam-aarch64
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/operands/run-roundtrip.sh

ci-operands: test-operands

ci-operands-prod: test-operands-prod

.PHONY: test-paged ci-paged test-paged-prod ci-paged-prod

# test-paged — sweep every fixture under tests/paged/sources/.  Same
# pipeline as test-operands (sam-aarch64 → build-disk → SimCoupé → samfile
# extract OUT → byte-compare against aarch64-*-as + ld -Ttext=0 +
# objcopy -O binary).  Per docs/specs/paged-out-design.md.  The paged
# fixtures exercise the paged-OUT machinery (sections-B emit + HSAVE
# auto-paging across &C000) by emitting > 16 KB of output to cross the
# OUT_ZONE low → high boundary.
test-paged: assembler test-mem-offaxis paged-call-payload enctab $(BUILD)/build-disk sam-aarch64
	./tests/paged/run-roundtrip.sh

test-paged-prod: assembler-prod enctab $(BUILD)/build-disk sam-aarch64
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/paged/run-roundtrip.sh

ci-paged: test-paged

ci-paged-prod: test-paged-prod

.PHONY: release-stripped-tbn

# Build the comment-stripped, flattened spectrum4 release .tbn that fits
# the SAM assembler's 96 KB IN-buffer ceiling.  Used for the
# release-bytematch milestone iteration (FAIL40+ coverage-gap closure).
# Without -strip-comments the flattened release source is ~408 KB and the
# assembler trips FAIL03 (in_file_pages > 6) immediately at load.
#
# Emits the COMPACT overlay .tbn (the form the SAM reader consumes since
# the v2 instruction-overlay flip); the discardable -o binary is the Go-side
# assembly of the same source.
#
# SPECTRUM4_SRC defaults to ~/git/spectrum4/src/spectrum4; override on
# the command line if your checkout lives elsewhere.
SPECTRUM4_SRC ?= $(HOME)/git/spectrum4/src/spectrum4

release-stripped-tbn: sam-aarch64
	$(BUILD)/sam-aarch64 -flatten -strip-comments \
	    -I $(SPECTRUM4_SRC) \
	    -I $(SPECTRUM4_SRC)/kernel \
	    -I $(SPECTRUM4_SRC)/roms \
	    -I $(SPECTRUM4_SRC)/tests \
	    -I $(SPECTRUM4_SRC)/demo \
	    -I $(SPECTRUM4_SRC)/libextra \
	    -origin 0xfffffff000000000 \
	    -o $(BUILD)/release-stripped.img \
	    --emit-tbn $(BUILD)/release-stripped.tbn \
	    $(SPECTRUM4_SRC)/targets/release.target
	@echo "release-stripped.tbn: $$(stat -f%z $(BUILD)/release-stripped.tbn 2>/dev/null || stat -c%s $(BUILD)/release-stripped.tbn) bytes"
