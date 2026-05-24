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
