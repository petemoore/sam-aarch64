# sam-aarch64 build — the SAM-side Z80 aarch64 assembler + its Go-side
# host toolchain (text2bin / refenc / enctab-gen) and round-trip gates.
# (The original M0 nop-to-disk round-trip oracle was retired once the
# M3–M6 fixture corpora + the m6-release 3-way gate fully subsumed it.)

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BUILD := build
TESTS := tests

.PHONY: all clean

# Default build: the two shipping assembler variants (the recipe for each
# also runs scripts/check-code-budget.sh inline).
all: m3-asm m3-asm-prod

clean:
	rm -rf $(BUILD)

.PHONY: text2bin bin2text test-m1 ci-m1

text2bin:
	cd tools/text2bin && go build -o $(CURDIR)/$(BUILD)/text2bin .

bin2text:
	cd tools/bin2text && go build -o $(CURDIR)/$(BUILD)/bin2text .

# aarch64dec — Go-side aarch64 disassembler (strand B); inverse of aarch64enc.
.PHONY: aarch64dec test-disasm ci-disasm ci-disasm-roundtrip

aarch64dec:
	cd tools/aarch64dec/cmd/aarch64dec && go build -o $(CURDIR)/$(BUILD)/aarch64dec .

# Unit tests for the disassembler package.
test-disasm:
	cd tools/aarch64dec && go test ./...

# Oracle gate: aarch64dec vs binutils objdump on the vendored release.img.
# RED until the decoder is complete (TDD); the diff is the worklist.  See
# docs/plans/2026-05-28-go-aarch64-disassembler.md.  Needs an aarch64
# objdump (binutils-aarch64-linux-gnu) + Go; no SimCoupé/container.
ci-disasm: test-disasm
	./tests/disasm/run-oracle-comparison.sh

# Round-trip gate: encode→decode→encode must produce identical bytes for
# all M3-M6 fixture sources.  Pure Go pipeline, no binutils or container.
ci-disasm-roundtrip: test-disasm
	./tools/run-disasm-roundtrip.sh

test-m1: text2bin bin2text
	cd tools/sam-aarch64-format && go test ./...
	cd tools/text2bin && go test ./...
	cd tools/bin2text && go test ./...
	./tests/m1/run-gnu-as-check.sh

ci-m1: test-m1

# sysreg-sync-check — Go↔Z80 sysreg/pstate/dc/tlbi table sync guard
# (repo-audit 2026-05-29 §5 / §6 item #9).  Asserts every entry in the
# hand-maintained Z80 table src/sysreg_data.asm matches the Go authority
# tools/sam-aarch64-format/sysregs.go byte-for-byte, so the two can't
# silently drift.  Cheap (pure Go, no container) — also runs implicitly
# inside test-m1 / test-m2's `go test ./...`, but is exposed here as a
# standalone target so it can be a named CI check / branch-protection gate.
.PHONY: sysreg-sync-check
sysreg-sync-check:
	cd tools/sam-aarch64-format && go test -run TestSysregZ80Sync -v ./...

.PHONY: enctab-gen refenc enctab test-m2 ci-m2

enctab-gen:
	cd tools/enctab-gen && go build -o $(CURDIR)/$(BUILD)/enctab-gen .

refenc:
	cd tools/refenc && go build -o $(CURDIR)/$(BUILD)/refenc .

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
# this target never touches.  See docs/notes/m2-status.md.
.PHONY: enctab-regen-source
enctab-regen-source: enctab-gen
	$(BUILD)/enctab-gen \
	    -mra reference/arm-mra \
	    -gopkg tools/aarch64enc/data.go \
	    -out $(BUILD)/enctab.enc

test-m2: refenc text2bin
	cd tools/sam-aarch64-format && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/enctab-gen && go test ./...
	cd tools/refenc && go test ./...
	cd tools/text2bin && go test ./...
	./tests/m1/run-refenc-roundtrip.sh
	./tests/spectrum4/run-roundtrip.sh

ci-m2: test-m2

.PHONY: m3-asm m3-asm-prod build-m3-disk m3-disk test-mem-offaxis cluster-offaxis paged-call-payload sysreg-data disasm-payload test-m3 ci-m3 check-budget

# check-budget — fail if either assembler variant has grown into the
# &C000 stack page (the silent boot-hang cliff; see
# scripts/check-code-budget.sh + memory/feedback_test_variant_fragility.md).
# The same assertion also runs inline at the tail of each assembler build
# recipe, so any `make m3-asm` / `make m3-asm-prod` enforces it too; this
# target is the explicit both-variants entry point used by CI.
check-budget: m3-asm m3-asm-prod
	./scripts/check-code-budget.sh

# Two build variants of the SAM-side assembler:
#
#   m3-asm       (test variant, default for dev / ci-m3 / ci-m4)
#                Includes all boot-time self-tests (slots / symbols /
#                local labels / M4 expr_eval / PC-rel).  Larger binary
#                but catches per-routine regressions before the
#                fixture-corpus round-trip even runs.  This is what
#                tests/m{3,4}/run-roundtrip.sh expect.
#
#   m3-asm-prod  (production variant, for end-user shipping)
#                Self-tests #ifdef'd out via `-D BUILD_TESTS=0` (i.e.
#                BUILD_TESTS is undefined; `if defined(BUILD_TESTS)`
#                blocks in src/assembler.asm are skipped).  Smaller
#                binary — frees code budget for M5.  Identical OUT
#                bytes on every fixture (the self-tests don't affect
#                the assemble path); the build-split-status target
#                verifies this.
#
# Both variants byte-match GNU on the M3 + M4 fixture corpora.

m3-asm: $(BUILD)/assembler.bin

m3-asm-prod: $(BUILD)/assembler-prod.bin

# Test-variant build also exports the symbol table for the off-axis
# test_mem.bin to import (plan-PR 3 — see
# docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md).
$(BUILD)/assembler.bin $(BUILD)/assembler.sym: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 \
	    --obj=$(BUILD)/assembler.bin \
	    --exportfile=$(BUILD)/assembler.sym \
	    src/assembler.asm
	@./scripts/check-code-budget.sh $(BUILD)/assembler.bin test

$(BUILD)/assembler-prod.bin: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/assembler-prod.bin src/assembler.asm
	@./scripts/check-code-budget.sh $(BUILD)/assembler-prod.bin prod

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

# Off-axis "M5 + misc encoder" cluster build (BUILD_TESTS only).
#
# test_offaxis_cluster.asm is a thin wrapper that does `org &0000` then
# includes the pc_rel / directives_m5 / ror_imm / shifted_reg /
# extended_reg / litpool self-test suites behind a small dispatcher.
# Imports section-C/D production symbols (encode_*, litpool_*, symbol_*,
# compute_directive_size, assert_eq32_de_hl_imm, fail, ...) from the
# just-built assembler.sym.  The resulting build/test_cluster.bin
# (~1225 B) is HLOADed at boot into physical page 12 by
# src/loader.asm::load_offaxis_cluster and invoked via one LMPR swap.
# M6 budget-relief PR (2026-05-29); mirrors the test_mem off-axis
# pattern (PR #52).  See src/test_offaxis_cluster.asm.
$(BUILD)/test_cluster.bin: src/test_offaxis_cluster.asm \
		src/test_slots.asm src/test_pc_rel.asm \
		src/test_directives_m5.asm src/test_ror_imm.asm \
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
# docs/notes/2026-05-28-paged-call-architecture.md.
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
# docs/plans/2026-05-29-m6-closure-release-bytematch.md (split-design
# correction documented in src/sysreg_data.asm).  Needed by EVERY
# build, not just BUILD_TESTS — sysreg/dc/tlbi/pstate operands appear
# in shipping sources.
$(BUILD)/sysreg_data.bin: src/sysreg_data.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/sysreg_data.bin src/sysreg_data.asm

sysreg-data: $(BUILD)/sysreg_data.bin

# Page-15 disassembler stub (PRODUCTION feature — both variants).
#
# A standalone binary (org &8000) implementing the NOP-only disassembler
# stub.  HLOAD'd at boot into physical page 15 by
# src/loader.asm::load_page15_payload and called at runtime via paged_call
# (DISASM_ENTRY = &8000, DISASM_PAGE = 15).  Per strand-B PR-3 and
# docs/notes/2026-06-07-disassembler-page-placement.md.  Needed by EVERY
# build — the disassembler is a production feature.
$(BUILD)/disasm.bin: src/disasm.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/disasm.bin src/disasm.asm

disasm-payload: $(BUILD)/disasm.bin

$(BUILD)/build-m3-disk: tools/build-m3-disk/main.go tools/build-m3-disk/go.mod
	@mkdir -p $(BUILD)
	cd tools/build-m3-disk && go build -o ../../$(BUILD)/build-m3-disk .

build-m3-disk: $(BUILD)/build-m3-disk

m3-disk: m3-asm test-mem-offaxis cluster-offaxis paged-call-payload sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk
	$(BUILD)/build-m3-disk \
	    -test-mem $(BUILD)/test_mem.bin \
	    -cluster $(BUILD)/test_cluster.bin \
	    -paged-call $(BUILD)/paged_call_test_payload.bin \
	    -sysreg-data $(BUILD)/sysreg_data.bin \
	    -disasm $(BUILD)/disasm.bin \
	    $(BUILD)/assembler.bin $(BUILD)/enctab.enc $(BUILD)/m3-test.mgt

# test-m3 — sweep every fixture under tests/m3/sources/ end-to-end:
# text2bin → build-m3-disk → SimCoupé → samfile extract OUT →
# byte-compare against aarch64-{none-elf,linux-gnu}-as + objcopy -O binary.
test-m3: m3-asm test-mem-offaxis paged-call-payload sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	./tests/m3/run-roundtrip.sh

ci-m3: test-m3

.PHONY: test-m4 ci-m4

# test-m4 — sweep every fixture under tests/m4/sources/.  Reuses the M3
# assembler binary (which is M4-capable post-PR-#22) and build-m3-disk,
# but feeds it M4-fixture .tbn inputs and uses an oracle that includes
# `ld -Ttext=0` so :lo12: / branch-to-label relocations resolve.  See
# docs/specs/2026-05-24-m4-symbols-multipass-design.md §3.
test-m4: m3-asm test-mem-offaxis paged-call-payload sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	./tests/m4/run-roundtrip.sh

ci-m4: test-m4

.PHONY: test-m3-prod test-m4-prod ci-m3-prod ci-m4-prod

# Production-variant sweeps — same fixture corpora, same oracle, but
# with the smaller assembler binary that omits the boot-time self-tests.
# Useful as a correctness check that the BUILD_TESTS=1 / undefined
# fork in src/assembler.asm doesn't accidentally change emit
# behaviour.  ci-m{3,4} cover the test variant; these cover prod.
test-m3-prod: m3-asm-prod sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m3/run-roundtrip.sh

test-m4-prod: m3-asm-prod sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m4/run-roundtrip.sh

ci-m3-prod: test-m3-prod

ci-m4-prod: test-m4-prod

.PHONY: test-m5 ci-m5 test-m5-prod ci-m5-prod

# test-m5 — sweep every fixture under tests/m5/sources/.  Same pipeline
# as test-m4 (text2bin → build-m3-disk → SimCoupé → samfile extract OUT →
# byte-compare against aarch64-*-as + ld -Ttext=0 + objcopy -O binary).
# Per docs/specs/2026-05-27-m5-compound-operands-directives-design.md §3.
#
# The GitHub Actions `m5` job is added in M5 PR E (the final integration
# PR); for now ci-m5 / ci-m5-prod run locally + via the dev container.
test-m5: m3-asm test-mem-offaxis paged-call-payload sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	./tests/m5/run-roundtrip.sh

test-m5-prod: m3-asm-prod sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m5/run-roundtrip.sh

ci-m5: test-m5

ci-m5-prod: test-m5-prod

.PHONY: test-m6 ci-m6 test-m6-prod ci-m6-prod

# test-m6 — sweep every fixture under tests/m6/sources/.  Same pipeline
# as test-m5 (text2bin → build-m3-disk → SimCoupé → samfile extract OUT
# → byte-compare against aarch64-*-as + ld -Ttext=0 + objcopy -O binary).
# Per docs/specs/2026-05-27-m6-paged-out-design.md.  The M6 fixtures
# exercise the paged-OUT machinery (sections-B emit + HSAVE auto-paging
# across &C000) by emitting > 16 KB of output to cross the OUT_ZONE
# low → high boundary.
test-m6: m3-asm test-mem-offaxis paged-call-payload sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	./tests/m6/run-roundtrip.sh

test-m6-prod: m3-asm-prod sysreg-data disasm-payload enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m6/run-roundtrip.sh

ci-m6: test-m6

ci-m6-prod: test-m6-prod

.PHONY: release-stripped-tbn

# Build the comment-stripped, flattened spectrum4 release.tbn (~88 KB)
# that fits the SAM assembler's 96 KB IN-buffer ceiling.  Used for the
# release-bytematch milestone iteration (FAIL40+ coverage-gap closure).
# Without -strip-comments the flattened release.tbn is ~408 KB and the
# assembler trips FAIL03 (in_file_pages > 6) immediately at load.
#
# SPECTRUM4_SRC defaults to ~/git/spectrum4/src/spectrum4; override on
# the command line if your checkout lives elsewhere.
SPECTRUM4_SRC ?= $(HOME)/git/spectrum4/src/spectrum4

release-stripped-tbn: text2bin
	$(BUILD)/text2bin -flatten -strip-comments \
	    -I $(SPECTRUM4_SRC) \
	    -I $(SPECTRUM4_SRC)/kernel \
	    -I $(SPECTRUM4_SRC)/roms \
	    -I $(SPECTRUM4_SRC)/tests \
	    -I $(SPECTRUM4_SRC)/demo \
	    -I $(SPECTRUM4_SRC)/libextra \
	    -origin 0xfffffff000000000 \
	    -o $(BUILD)/release-stripped.tbn \
	    $(SPECTRUM4_SRC)/targets/release.target
	@echo "release-stripped.tbn: $$(stat -f%z $(BUILD)/release-stripped.tbn 2>/dev/null || stat -c%s $(BUILD)/release-stripped.tbn) bytes"
