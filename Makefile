# sam-aarch64 build — the SAM-side Z80 aarch64 assembler + its Go-side
# host toolchain (sam-aarch64 / tables-gen) and round-trip gates.
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

# netboot-oracle — Phase-3 host harness: the DHCP/TFTP packet builders +
# parsers (the Z80 i82/i83/i86 authority) replayed against masked golden
# vectors extracted from a real Pi 400 netboot capture.  Pure Go, no
# container, no off-repo captures (the committed golden vectors are the
# fixtures).  Validates the protocol logic in isolation — NOT the Z80
# execution or the ENC28J60 hardware (those are gated on i80/real-Trinity).
.PHONY: ci-netboot-oracle
ci-netboot-oracle:
	cd tools/netboot-oracle && go test ./...

# netboot Z80 routines — the SAM-side port (src/netboot/*.asm) of the netboot
# protocol logic, assembled with pyz80 to a standalone &8000 binary + symbol
# map.  Host-verifiable: ci-netboot-z80 runs each routine under the flat-memory
# koron-go/z80 harness (tools/netboot-oracle/z80) and byte-compares its emitted
# packet against the same golden vectors the Go authority is checked against.
# Needs pyz80 (the dev container), unlike the pure-Go ci-netboot-oracle.
.PHONY: netboot-build-udp-frame netboot-dhcp-reply netboot-tftp-build netboot-tftp-parse netboot-tftp-client netboot-build-arp-request netboot-encdrv netboot-dhcp-loop netboot-tftp-server-loop netboot-tftp-client-loop netboot-tftp-client-front netboot-bdos-seam netboot-z80-routines ci-netboot-z80
$(BUILD)/netboot_build_udp_frame.bin $(BUILD)/netboot_build_udp_frame.map: src/netboot/build_udp_frame.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_build_udp_frame.bin \
	    --mapfile=$(BUILD)/netboot_build_udp_frame.map \
	    src/netboot/build_udp_frame.asm

netboot-build-udp-frame: $(BUILD)/netboot_build_udp_frame.bin $(BUILD)/netboot_build_udp_frame.map

$(BUILD)/netboot_dhcp_reply.bin $(BUILD)/netboot_dhcp_reply.map: src/netboot/dhcp_reply.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_dhcp_reply.bin \
	    --mapfile=$(BUILD)/netboot_dhcp_reply.map \
	    src/netboot/dhcp_reply.asm

netboot-dhcp-reply: $(BUILD)/netboot_dhcp_reply.bin $(BUILD)/netboot_dhcp_reply.map

$(BUILD)/netboot_tftp_build.bin $(BUILD)/netboot_tftp_build.map: src/netboot/tftp_build.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_tftp_build.bin \
	    --mapfile=$(BUILD)/netboot_tftp_build.map \
	    src/netboot/tftp_build.asm

netboot-tftp-build: $(BUILD)/netboot_tftp_build.bin $(BUILD)/netboot_tftp_build.map

$(BUILD)/netboot_tftp_parse.bin $(BUILD)/netboot_tftp_parse.map: src/netboot/tftp_parse.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_tftp_parse.bin \
	    --mapfile=$(BUILD)/netboot_tftp_parse.map \
	    src/netboot/tftp_parse.asm

netboot-tftp-parse: $(BUILD)/netboot_tftp_parse.bin $(BUILD)/netboot_tftp_parse.map

$(BUILD)/netboot_tftp_client.bin $(BUILD)/netboot_tftp_client.map: src/netboot/tftp_client.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_tftp_client.bin \
	    --mapfile=$(BUILD)/netboot_tftp_client.map \
	    src/netboot/tftp_client.asm

netboot-tftp-client: $(BUILD)/netboot_tftp_client.bin $(BUILD)/netboot_tftp_client.map

$(BUILD)/netboot_build_arp_request.bin $(BUILD)/netboot_build_arp_request.map: src/netboot/build_arp_request.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_build_arp_request.bin \
	    --mapfile=$(BUILD)/netboot_build_arp_request.map \
	    src/netboot/build_arp_request.asm

netboot-build-arp-request: $(BUILD)/netboot_build_arp_request.bin $(BUILD)/netboot_build_arp_request.map

# encdrv — the vendored Trinity ENC28J60 driver (simonowen/trinload, verbatim),
# orged at &8000 by encdrv_harness.asm.  The i80 emulation test (enc28j60_test)
# runs drv_init/drv_write/drv_read from this binary against the emulated Trinity
# (tools/netboot-oracle/z80/enc28j60.go), the host-verifiable ENC28J60 wire path.
$(BUILD)/netboot_encdrv.bin $(BUILD)/netboot_encdrv.map: src/netboot/encdrv_harness.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_encdrv.bin \
	    --mapfile=$(BUILD)/netboot_encdrv.map \
	    src/netboot/encdrv_harness.asm

netboot-encdrv: $(BUILD)/netboot_encdrv.bin $(BUILD)/netboot_encdrv.map

# dhcp-loop — the i86 DHCP responder loop (state machine): drv_read a DISCOVER/
# REQUEST, dispatch + build the OFFER/ACK, drv_write it.  Composes the
# host-verified primitives (build_udp_frame + dhcp_reply) and the real driver
# (encdrv.asm) into one binary; the i80 emulation test (dhcp_loop_test) runs it
# against the emulated Trinity and asserts the wire frame matches the Go
# Responder authority byte-for-byte.
$(BUILD)/netboot_dhcp_loop.bin $(BUILD)/netboot_dhcp_loop.map: src/netboot/dhcp_loop.asm src/netboot/build_udp_frame.asm src/netboot/dhcp_reply.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_dhcp_loop.bin \
	    --mapfile=$(BUILD)/netboot_dhcp_loop.map \
	    src/netboot/dhcp_loop.asm

netboot-dhcp-loop: $(BUILD)/netboot_dhcp_loop.bin $(BUILD)/netboot_dhcp_loop.map

# tftp-server-loop — the i83 TFTP server transfer loop (state machine):
# drv_read an RRQ, parse + resolve, reply with an OACK (hit) or ERROR(1) (miss),
# then the DATA/ACK send loop.  Composes the host-verified primitives
# (build_udp_frame + tftp_build + tftp_parse) and the real driver (encdrv.asm)
# into one binary; the i80 emulation test (tftp_server_loop_test) runs it
# against the emulated Trinity and asserts each wire frame matches the Go
# ServerLoop authority byte-for-byte.
$(BUILD)/netboot_tftp_server_loop.bin $(BUILD)/netboot_tftp_server_loop.map: src/netboot/tftp_server_loop.asm src/netboot/build_udp_frame.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_tftp_server_loop.bin \
	    --mapfile=$(BUILD)/netboot_tftp_server_loop.map \
	    src/netboot/tftp_server_loop.asm

netboot-tftp-server-loop: $(BUILD)/netboot_tftp_server_loop.bin $(BUILD)/netboot_tftp_server_loop.map

# tftp-client-loop — the i82 TFTP client transfer loop (the receive side):
# drv_read a DATA, validate the server TID, accumulate the payload, drv_write an
# ACK; the SAS timeout retransmits the last ACK only.  Composes the host-verified
# primitives (build_udp_frame + tftp_client) and the real driver (encdrv.asm);
# the i80 emulation test (tftp_client_loop_test) asserts each wire frame matches
# the Go ClientLoop authority byte-for-byte.
$(BUILD)/netboot_tftp_client_loop.bin $(BUILD)/netboot_tftp_client_loop.map: src/netboot/tftp_client_loop.asm src/netboot/build_udp_frame.asm src/netboot/tftp_client.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_tftp_client_loop.bin \
	    --mapfile=$(BUILD)/netboot_tftp_client_loop.map \
	    src/netboot/tftp_client_loop.asm

netboot-tftp-client-loop: $(BUILD)/netboot_tftp_client_loop.bin $(BUILD)/netboot_tftp_client_loop.map

# tftp-client-front — the i82 TFTP client's request-origination front (the step
# before the receive loop): broadcast an ARP request, learn the server MAC from
# the reply, then send the RRQ.  Composes the host-verified primitives
# (build_arp_request + build_rrq + build_udp_frame) and the real driver
# (encdrv.asm); the i80 emulation test (tftp_client_front_test) asserts the ARP
# request + RRQ wire frames match the Go ClientFront authority byte-for-byte.
$(BUILD)/netboot_tftp_client_front.bin $(BUILD)/netboot_tftp_client_front.map: src/netboot/tftp_client_front.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_request.asm src/netboot/tftp_client.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_tftp_client_front.bin \
	    --mapfile=$(BUILD)/netboot_tftp_client_front.map \
	    src/netboot/tftp_client_front.asm

netboot-tftp-client-front: $(BUILD)/netboot_tftp_client_front.bin $(BUILD)/netboot_tftp_client_front.map

# bdos-seam — the netboot storage seam: the UIFA/DIFA field arithmetic gluing the
# i83 server (serve by name) + i82 client (write by name) to the B-DOS hooks.
# Built with NETBOOT_HOSTTEST so the RST 8 hook dispatch (HGTHD/HSAVE/HRECORD,
# NOT host-verifiable — no ROM/SAMDOS in the harness) is excluded; the host test
# (bdos_seam_test) byte-compares the built UIFA + decoded size vs the Go authority
# (tools/netboot-oracle/bdos).  The hook path stays unverified until real Trinity.
$(BUILD)/netboot_bdos_seam.bin $(BUILD)/netboot_bdos_seam.map: src/netboot/bdos_seam.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/netboot_bdos_seam.bin \
	    --mapfile=$(BUILD)/netboot_bdos_seam.map \
	    src/netboot/bdos_seam.asm

netboot-bdos-seam: $(BUILD)/netboot_bdos_seam.bin $(BUILD)/netboot_bdos_seam.map

# Every netboot routine binary the harness tests load.
netboot-z80-routines: netboot-build-udp-frame netboot-dhcp-reply netboot-tftp-build netboot-tftp-parse netboot-tftp-client netboot-build-arp-request netboot-encdrv netboot-dhcp-loop netboot-tftp-server-loop netboot-tftp-client-loop netboot-tftp-client-front netboot-bdos-seam

ci-netboot-z80: netboot-z80-routines
	cd tools/netboot-oracle/z80 && go test ./...

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
STATICCHECK_MODULES := comment-bench sam-aarch64-format sam-aarch64 aarch64enc aarch64dec tables-gen z80-test-harness-go zx0-greedy editor-prototype netboot-oracle netboot-oracle/z80
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

.PHONY: tables-gen enctab test-encoder ci-encoder

# tables-gen — the Z80-table generator (renamed from enctab-gen as it grew
# the sysreg/pstate/dc/tlbi emitter; the function is now "generate every Z80
# data table whose authority is Go source", i7).  Imports both authority
# packages (aarch64enc, sam-aarch64-format).
tables-gen:
	cd tools/tables-gen && go build -o $(CURDIR)/$(BUILD)/tables-gen .

# Build the binary enctab.enc artefact from the vendored MRA snapshot.
# Includes both MRA-derived (data.go) and hand-curated (manual_forms.go)
# forms; the binary mirrors the Go-side runtime form table.  Does NOT
# touch any source files.
enctab: tables-gen
	$(BUILD)/tables-gen \
	    -mra reference/arm-mra \
	    -out $(BUILD)/enctab.enc

# Regenerate tools/aarch64enc/data.go from the vendored MRA snapshot.
# Safe to run at any time: data.go is purely the MRA projection; all
# hand-curated forms live in tools/aarch64enc/manual_forms.go which
# this target never touches.  See https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/m2-status.md.
.PHONY: enctab-regen-source
enctab-regen-source: tables-gen
	$(BUILD)/tables-gen \
	    -mra reference/arm-mra \
	    -gopkg tools/aarch64enc/data.go \
	    -out $(BUILD)/enctab.enc

# tables — regenerate every committed generated Z80 table in place.  Today
# that is the sysreg/pstate/dc/tlbi tables (src/sysreg_tables.inc), projected
# from tools/sam-aarch64-format/sysregs.go.  (enctab.enc is generated by the
# separate `make enctab` target — it is a binary payload, not a committed
# source table — so it is deliberately not part of `make tables`.)
.PHONY: tables
tables: tables-gen
	$(BUILD)/tables-gen -sysreg-inc src/sysreg_tables.inc

# tables-sync-check — freshness guard: regenerate the committed tables into
# build/gen/ and diff against the in-tree copies; fail on any drift (a Go-side
# edit that forgot `make tables`, or a hand edit to a generated file).  Runs
# as a step of the `sysreg-sync` CI job.  Closes the hand-sync drift class for
# the sysreg tables (i7); the structural twin of `make enctab` mirroring the
# Go runtime form table.
.PHONY: tables-sync-check
tables-sync-check: tables-gen
	@mkdir -p $(BUILD)/gen
	$(BUILD)/tables-gen -sysreg-inc $(BUILD)/gen/sysreg_tables.inc
	@if ! diff -u src/sysreg_tables.inc $(BUILD)/gen/sysreg_tables.inc; then \
	    echo ""; \
	    echo "ERROR: src/sysreg_tables.inc is stale — it differs from the"; \
	    echo "tools/tables-gen output.  Run 'make tables' and commit the result"; \
	    echo "(or, if you edited sysregs.go, this is the expected regeneration)."; \
	    exit 1; \
	fi
	@echo "tables-sync-check: src/sysreg_tables.inc is up to date with tools/tables-gen."

test-encoder: sam-aarch64 tables-gen
	cd tools/sam-aarch64-format && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/tables-gen && go test ./...
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
$(BUILD)/assembler.bin $(BUILD)/assembler.sym: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) $(wildcard src/*.inc)
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 \
	    --obj=$(BUILD)/assembler.bin \
	    --exportfile=$(BUILD)/assembler.sym \
	    src/assembler.asm
	@./tools/check-code-budget.sh $(BUILD)/assembler.bin test

$(BUILD)/assembler-prod.bin: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) $(wildcard src/*.inc)
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

# zx0_compress.bin — standalone ZX0 greedy compressor (org &8400, the
# page-13 product address).  Byte-identical to the compressor head of the
# combined zx0 payload below; consumed by the harness battery
# (tools/z80-test-harness-go zx0_*_test.go).
$(BUILD)/zx0_compress.bin: src/zx0_compress.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/zx0_compress.bin --mapfile=$(BUILD)/zx0_compress.map src/zx0_compress.asm

.PHONY: zx0-compress-payload
zx0-compress-payload: $(BUILD)/zx0_compress.bin

# Page-13 zx0 payload (PRODUCTION feature — both variants): greedy
# compressor (&8400) + turbo decoder (&8B00), per
# docs/specs/comment-storage-design.md §5/§6.  HLOAD'd at boot into
# physical page 13 at &8400 (alongside sysreg_data at &8000) by
# src/loader.asm::load_zx0_payload.  Two variants, mirroring the disasm
# split:
#
#   zx0.bin       (PROD)  compressor + decoder only (~2 KB).
#   zx0-test.bin  (TEST)  -D BUILD_TESTS=1 — adds the boot self-test
#                 driver + the baked Go-authority fixture at &AFA0
#                 (generated below); pads across the &8B80 workspace
#                 region, so ~11 KB.  Ships only on the test disk.
$(BUILD)/zx0.bin: src/zx0_payload.asm src/zx0_compress.asm src/dzx0_turbo.asm src/zx0_comm.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/zx0.bin src/zx0_payload.asm

$(BUILD)/zx0-test.bin: src/zx0_payload.asm src/zx0_compress.asm src/dzx0_turbo.asm src/zx0_comm.inc $(BUILD)/zx0_selftest_fixture.inc
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 --obj=$(BUILD)/zx0-test.bin src/zx0_payload.asm

# Baked self-test fixture: a fixed 1 KB block of tests/release/release.s
# comment text + its greedy-compressed bytes (H=512 D=16), emitted by the
# Go authority so the boot self-tests are exact byte-compares
# (comment-storage-design §7.1).
$(BUILD)/zx0_selftest_fixture.inc: tests/release/release.s tools/zx0-greedy/compress.go tools/zx0-greedy/cmd/zx0fixture/main.go
	@mkdir -p $(BUILD)
	cd tools/zx0-greedy && go run ./cmd/zx0fixture \
	    -src $(CURDIR)/tests/release/release.s \
	    -out $(CURDIR)/$(BUILD)/zx0_selftest_fixture.inc

.PHONY: zx0-payload zx0-test-payload
zx0-payload: $(BUILD)/zx0.bin

zx0-test-payload: $(BUILD)/zx0-test.bin

$(BUILD)/build-disk: tools/build-disk/main.go tools/build-disk/go.mod
	@mkdir -p $(BUILD)
	cd tools/build-disk && go build -o ../../$(BUILD)/build-disk .

build-disk: $(BUILD)/build-disk

# disk uses the TEST assembler (assembler.bin, BUILD_TESTS=1), whose
# boot sequence calls the disasm &8003 and zx0 &AFA0 self-tests via
# paged_call — so it must ship the TEST disasm + zx0 binaries
# (disasm-test.bin, zx0-test.bin).
disk: assembler test-mem-offaxis cluster-offaxis paged-call-payload sysreg-data disasm-test-payload zx0-test-payload enctab $(BUILD)/build-disk
	$(BUILD)/build-disk \
	    -test-mem $(BUILD)/test_mem.bin \
	    -cluster $(BUILD)/test_cluster.bin \
	    -paged-call $(BUILD)/paged_call_test_payload.bin \
	    -sysreg-data $(BUILD)/sysreg_data.bin \
	    -disasm $(BUILD)/disasm-test.bin \
	    -zx0 $(BUILD)/zx0-test.bin \
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

.PHONY: release-stripped-tbn release-unstripped-tbn comment-bench

# Build the comment-stripped, flattened spectrum4 release .tbn.  Used for the
# release-bytematch milestone iteration (FAIL40+ coverage-gap closure).  The
# two-phase prefix-only load (i40) means the full-comment .tbn is now also
# loadable on the SAM; this stripped target is retained for benchmark use.
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

# Build the full (comment-retaining) flattened release .tbn, used as input
# to comment-bench.  Comments are NOT stripped so the editor-region comment
# sidecar carries the full corpus (i57).  Reads the VENDORED release source
# (tests/release/release.s — the whole release pre-flattened into one
# self-contained file, the same input the release-gate uses), so no
# spectrum4 checkout is needed (i68 §7.5).
release-unstripped-tbn: sam-aarch64
	$(BUILD)/sam-aarch64 -flatten \
	    -origin 0xfffffff000000000 \
	    -o $(BUILD)/release-unstripped.img \
	    --emit-tbn $(BUILD)/release-unstripped.tbn \
	    tests/release/release.s
	@echo "release-unstripped.tbn: $$(stat -f%z $(BUILD)/release-unstripped.tbn 2>/dev/null || stat -c%s $(BUILD)/release-unstripped.tbn) bytes"

# Run the comment-compression benchmark against the unstripped release .tbn.
# Builds sam-aarch64 + the unstripped .tbn if needed, then runs comment-bench.
comment-bench: release-unstripped-tbn
	cd tools/comment-bench && go build -o $(CURDIR)/$(BUILD)/comment-bench .
	$(BUILD)/comment-bench $(BUILD)/release-unstripped.tbn

# Generate the editor-prototype mockup sheets (i76 P1): a PNG + SAM SCREEN$
# `.mgt` per screen geometry, rendering the real release `.tbn` through the
# samscreen abstraction. 8×8 geometries render real glyphs; 6-px geometries
# render a placeholder note until the font-proof leg vendors their font.
# Output: build/mockups/ (gitignored). Run: make editor-mockups
.PHONY: editor-mockups
editor-mockups: release-unstripped-tbn
	mkdir -p $(BUILD)/mockups
	cd tools/editor-prototype && go run . -mockup \
	    -tbn $(CURDIR)/$(BUILD)/release-unstripped.tbn \
	    -o $(CURDIR)/$(BUILD)/mockups/
	@echo "editor-mockups: $$(ls $(BUILD)/mockups/*.png | wc -l) PNG sheets in $(BUILD)/mockups/"

# Generate ZX0 test blocks for harness T-state measurement (i60a).
# Requires: the real ZX0 compressor (zx0 binary on PATH or at /tmp/zx0).
# Produces: build/zx0-blocks/block_NNkb_NNNN.{raw,zx0}
# Run: make zx0-blocks
.PHONY: zx0-blocks
ZX0_BINARY ?= $(shell which zx0 2>/dev/null || echo /tmp/zx0)
zx0-blocks: release-unstripped-tbn
	cd tools/comment-bench && go build -o $(CURDIR)/$(BUILD)/comment-bench .
	mkdir -p $(BUILD)/zx0-blocks
	$(BUILD)/comment-bench --dump-blocks=$(BUILD)/zx0-blocks $(BUILD)/release-unstripped.tbn > /dev/null
	@for f in $(BUILD)/zx0-blocks/*.raw; do \
	    $(ZX0_BINARY) "$$f" "$${f%.raw}.zx0" 2>/dev/null || true; \
	done
	@echo "zx0-blocks: $$(ls $(BUILD)/zx0-blocks/*.zx0 | wc -l) compressed blocks written to $(BUILD)/zx0-blocks/"

# Dump the full flat comment corpus for whole-corpus T-state totals (i67).
# Produces: build/zx0-corpus.raw (consumed by TestZX0CorpusTotals).
# Run: make zx0-corpus
.PHONY: zx0-corpus
zx0-corpus: release-unstripped-tbn
	cd tools/comment-bench && go build -o $(CURDIR)/$(BUILD)/comment-bench .
	$(BUILD)/comment-bench --dump-corpus=$(BUILD)/zx0-corpus.raw $(BUILD)/release-unstripped.tbn > /dev/null

# i76 P1b font-proof (editor-tui-prototype-design.md §5): bootable disks
# that render a release.s window on a real SAM MODE 3 screen — 85x32 with
# the vendored 6x6 font, and 64x24 with the ROM 8x8 charset as reference.
# The start line picks a representative stretch of handle_irq_bcm283x /
# handle_irq_bcm2711: banner comments, prose, labels, and code lines with
# long trailing comments. Capture: tools/font-proof/run-capture.sh.
# GOWORK=off: the throwaway probe tool stays out of the workspace.
.PHONY: font-proof
font-proof:
	mkdir -p $(BUILD)
	cd tools/font-proof && GOWORK=off go build -o $(CURDIR)/$(BUILD)/fontproof-tool .
	$(BUILD)/fontproof-tool font -header tools/editor-prototype/fonts/five_pixel_font.h -o $(BUILD)/font6.bin
	$(BUILD)/fontproof-tool text -src tests/release/release.s -start-line 3837 -rows 32 -cols 85 -o $(BUILD)/text6.bin
	$(BUILD)/fontproof-tool text -src tests/release/release.s -start-line 3837 -rows 24 -cols 64 -o $(BUILD)/text8.bin
	pyz80 --obj=$(BUILD)/fontproof.bin tools/font-proof/fontproof.asm
	$(BUILD)/fontproof-tool disk -dos reference/samdos/samdos2.bin -bin $(BUILD)/fontproof.bin -call 32768 -o $(BUILD)/font-proof.mgt
	$(BUILD)/fontproof-tool disk -dos reference/samdos/samdos2.bin -bin $(BUILD)/fontproof.bin -call 32771 -o $(BUILD)/font-proof-8x8.mgt
