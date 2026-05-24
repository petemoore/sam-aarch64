# M0 toolchain bootstrap — see docs/plans/2026-05-09-m0-toolchain-bootstrap.md

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

BUILD := build
TESTS := tests

.PHONY: all check stub disk run extract diff test ci clean

all: stub

check:
	./tools/check-toolchain.sh

stub: $(BUILD)/stub.bin

$(BUILD)/stub.bin: src/stub.asm
	@mkdir -p $(BUILD)
	./tools/build-stub.sh

disk: $(BUILD)/test.mgt

$(BUILD)/test.mgt: $(BUILD)/stub.bin $(TESTS)/fixtures/nop.s
	./tools/build-disk.sh $(TESTS)/fixtures/nop.s $@

run: disk
	./tools/run-simcoupe.sh $(BUILD)/test.mgt

extract: run
	./tools/extract-output.sh $(BUILD)/test.mgt $(BUILD)/out.bin

diff: extract
	./tools/diff-vs-gnu.sh $(TESTS)/fixtures/nop.s $(BUILD)/out.bin

test: check
	./tools/run-roundtrip.sh $(TESTS)/fixtures/nop.s

ci: check test

clean:
	rm -rf $(BUILD)

.PHONY: text2bin bin2text test-m1 ci-m1

text2bin:
	cd tools/text2bin && go build -o $(CURDIR)/$(BUILD)/text2bin .

bin2text:
	cd tools/bin2text && go build -o $(CURDIR)/$(BUILD)/bin2text .

test-m1: text2bin bin2text
	cd tools/sam-aarch64-format && go test ./...
	cd tools/text2bin && go test ./...
	cd tools/bin2text && go test ./...
	./tests/m1/run-gnu-as-check.sh

ci-m1: test-m1

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

.PHONY: m3-asm m3-asm-prod build-m3-disk m3-disk test-m3 ci-m3

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
#                blocks in src/m3/assembler.asm are skipped).  Smaller
#                binary — frees code budget for M5.  Identical OUT
#                bytes on every fixture (the self-tests don't affect
#                the assemble path); the build-split-status target
#                verifies this.
#
# Both variants byte-match GNU on the M3 + M4 fixture corpora.

m3-asm: $(BUILD)/assembler.bin

m3-asm-prod: $(BUILD)/assembler-prod.bin

$(BUILD)/assembler.bin: src/m3/assembler.asm $(wildcard src/m3/*.asm) $(wildcard src/m3/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 --obj=$(BUILD)/assembler.bin src/m3/assembler.asm

$(BUILD)/assembler-prod.bin: src/m3/assembler.asm $(wildcard src/m3/*.asm) $(wildcard src/m3/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/assembler-prod.bin src/m3/assembler.asm

$(BUILD)/build-m3-disk: tools/build-m3-disk/main.go tools/build-m3-disk/go.mod
	@mkdir -p $(BUILD)
	cd tools/build-m3-disk && go build -o ../../$(BUILD)/build-m3-disk .

build-m3-disk: $(BUILD)/build-m3-disk

m3-disk: m3-asm enctab $(BUILD)/build-m3-disk
	$(BUILD)/build-m3-disk $(BUILD)/assembler.bin $(BUILD)/enctab.enc $(BUILD)/m3-test.mgt

# test-m3 — sweep every fixture under tests/m3/sources/ end-to-end:
# text2bin → build-m3-disk → SimCoupé → samfile extract OUT →
# byte-compare against aarch64-{none-elf,linux-gnu}-as + objcopy -O binary.
test-m3: m3-asm enctab $(BUILD)/build-m3-disk text2bin
	./tests/m3/run-roundtrip.sh

ci-m3: test-m3

.PHONY: test-m4 ci-m4

# test-m4 — sweep every fixture under tests/m4/sources/.  Reuses the M3
# assembler binary (which is M4-capable post-PR-#22) and build-m3-disk,
# but feeds it M4-fixture .tbn inputs and uses an oracle that includes
# `ld -Ttext=0` so :lo12: / branch-to-label relocations resolve.  See
# docs/specs/2026-05-24-m4-symbols-multipass-design.md §3.
test-m4: m3-asm enctab $(BUILD)/build-m3-disk text2bin
	./tests/m4/run-roundtrip.sh

ci-m4: test-m4

.PHONY: test-m3-prod test-m4-prod ci-m3-prod ci-m4-prod

# Production-variant sweeps — same fixture corpora, same oracle, but
# with the smaller assembler binary that omits the boot-time self-tests.
# Useful as a correctness check that the BUILD_TESTS=1 / undefined
# fork in src/m3/assembler.asm doesn't accidentally change emit
# behaviour.  ci-m{3,4} cover the test variant; these cover prod.
test-m3-prod: m3-asm-prod enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m3/run-roundtrip.sh

test-m4-prod: m3-asm-prod enctab $(BUILD)/build-m3-disk text2bin
	ASSEMBLER_BIN=$(CURDIR)/$(BUILD)/assembler-prod.bin ./tests/m4/run-roundtrip.sh

ci-m3-prod: test-m3-prod

ci-m4-prod: test-m4-prod
