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

# Regenerate enctab.enc + aarch64enc/data.go from the vendored MRA snapshot.
# Note: the manually-added 0-operand ret form must be re-added after each
# regeneration. See docs/notes/m2-status.md.
enctab: enctab-gen
	$(BUILD)/enctab-gen \
	    -mra reference/arm-mra \
	    -gopkg tools/aarch64enc/data.go \
	    -out $(BUILD)/enctab.enc

test-m2: refenc
	cd tools/sam-aarch64-format && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/enctab-gen && go test ./...
	cd tools/refenc && go test ./...
	cd tools/text2bin && go test ./...

ci-m2: test-m2

.PHONY: m3-asm test-m3 ci-m3

m3-asm: $(BUILD)/assembler.bin

$(BUILD)/assembler.bin: src/m3/assembler.asm $(wildcard src/m3/*.asm) $(wildcard src/m3/**/*.asm) src/sam_io.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/assembler.bin src/m3/assembler.asm

test-m3: m3-asm
	@echo "test-m3: round-trip driver lands in Task 21; m3-asm built successfully"

ci-m3: test-m3
