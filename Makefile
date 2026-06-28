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
# SKIP_PRIVATE_TESTS gates the one test that needs the multi-MB Pi firmware
# blobs (http/manifest_test.go's local-file hash cross-check) — those live in
# Pete's spectrum4 checkout, are NOT committed, and are absent in CI. The
# always-on byte-for-byte gate is z80/fw_source_test.go; every other fixture
# here is committed and its test fails hard if missing (i253, no-silent-skips).
ci-netboot-oracle:
	cd tools/netboot-oracle && SKIP_PRIVATE_TESTS=true go test ./...

# ci-registry — run the registry CLI's Go unit tests (id-allocation / nextSubID,
# parent invariants, gate column, in-progress, and the live-registry conformance
# test). The registry-sync job validates the LIVE data during gen; this gates the
# tool's LOGIC so a regression can't merge green.
.PHONY: ci-registry
ci-registry: registry-gen
	cd tools/registry && go test ./...

# netboot Z80 routines — the SAM-side port (src/netboot/*.asm) of the netboot
# protocol logic, assembled with pyz80 to a standalone &8000 binary + symbol
# map.  Host-verifiable: ci-netboot-z80 runs each routine under the flat-memory
# koron-go/z80 harness (tools/netboot-oracle/z80) and byte-compares its emitted
# packet against the same golden vectors the Go authority is checked against.
# Needs pyz80 (the dev container), unlike the pure-Go ci-netboot-oracle.
.PHONY: netboot-build-udp-frame netboot-dhcp-reply netboot-tftp-build netboot-tftp-parse netboot-tftp-client netboot-build-arp-request netboot-build-arp-reply netboot-build-tcp-segment netboot-sha256 netboot-hmac-sha256 netboot-hkdf netboot-hkdf-expand-label netboot-chacha20 netboot-poly1305 netboot-x25519-field netboot-aead netboot-tls-keyschedule netboot-tls-record netboot-tls-transcript netboot-tls-client-hello netboot-tls-server-flight netboot-tls-client netboot-encdrv netboot-dhcp-loop netboot-tcp-conn netboot-tcp-conn-stream netboot-http-get netboot-fw-source netboot-body-sink netboot-tls-reasm netboot-fw-span netboot-http netboot-http-boot netboot-http-disk netboot-tftp-server-loop netboot-tftp-client-loop netboot-tftp-client-front netboot-bdos-seam netboot-smoke netboot-smoke-disk netboot-server netboot-server-disk netboot-serve-boot netboot-serve-boot-debug netboot-serve-trinload netboot-trinpush-test netboot-dumper netboot-csd-probe netboot-sd-push netboot-boot-record netboot-samboot-config netboot-trinity-identity netboot-trinload netboot-sd-csd netboot-sd-listread netboot-z80-routines asmlex-z80 asmparse-z80 pass1-ir-z80 compact-ir-z80 editmodel-z80 pagepool-z80 spill-z80 viewport-z80 ci-netboot-z80
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

$(BUILD)/netboot_build_arp_reply.bin $(BUILD)/netboot_build_arp_reply.map: src/netboot/build_arp_reply.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_build_arp_reply.bin \
	    --mapfile=$(BUILD)/netboot_build_arp_reply.map \
	    src/netboot/build_arp_reply.asm

netboot-build-arp-reply: $(BUILD)/netboot_build_arp_reply.bin $(BUILD)/netboot_build_arp_reply.map

# build-tcp-segment — the fresh TCP/IPv4/Ethernet segment builder (i70), the TCP
# analogue of build_udp_frame.  TCP is the transport the firmware-self-provisioning
# HTTP client rides on; the UDP-only trinload stack does not provide it.  Host-
# verified: tcp_segment_test.go byte-compares the emitted segment (incl. the
# mandatory pseudo-header checksum) against the Go authority tcp.BuildSegment.
$(BUILD)/netboot_build_tcp_segment.bin $(BUILD)/netboot_build_tcp_segment.map: src/netboot/build_tcp_segment.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_build_tcp_segment.bin \
	    --mapfile=$(BUILD)/netboot_build_tcp_segment.map \
	    src/netboot/build_tcp_segment.asm

netboot-build-tcp-segment: $(BUILD)/netboot_build_tcp_segment.bin $(BUILD)/netboot_build_tcp_segment.map

# sha256 — SHA-256 (FIPS 180-4), the streaming init/update/final verify primitive
# for the i70/i100 firmware-download path (the SAM fetches firmware over plain HTTP
# from cdn.githubraw.com and checks each file against a pinned hash); also an i88
# building block.  Host-verified: sha256_test.go byte-compares the Z80 digest
# against Go's crypto/sha256 over the FIPS/NIST vectors, fed both whole and in
# awkward chunks to exercise the streaming partial-block carry.
$(BUILD)/netboot_sha256.bin $(BUILD)/netboot_sha256.map: src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_sha256.bin \
	    --mapfile=$(BUILD)/netboot_sha256.map \
	    src/netboot/sha256.asm

netboot-sha256: $(BUILD)/netboot_sha256.bin $(BUILD)/netboot_sha256.map

# netboot-hmac-sha256 (i88) — HMAC-SHA256 (RFC 2104), the first i88 TLS building
# block above the SHA-256 primitive (TLS 1.3's HKDF key schedule is HMAC-SHA256).
# A thin orchestration over sha256.asm.  Standalone leaf, host-verified by
# hmac_sha256_test.go vs Go crypto/hmac over the RFC 4231 vectors.
$(BUILD)/netboot_hmac_sha256.bin $(BUILD)/netboot_hmac_sha256.map: src/netboot/hmac_sha256.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_hmac_sha256.bin \
	    --mapfile=$(BUILD)/netboot_hmac_sha256.map \
	    src/netboot/hmac_sha256.asm

netboot-hmac-sha256: $(BUILD)/netboot_hmac_sha256.bin $(BUILD)/netboot_hmac_sha256.map

# netboot-hkdf (i88) — HKDF (RFC 5869) over HMAC-SHA256, the TLS 1.3 key schedule.
# hkdf_extract (PRK = HMAC(salt, IKM)) + hkdf_expand (the T(i) chain to L bytes);
# orchestration over hmac_sha256.asm, no new arithmetic.  Standalone leaf,
# host-verified by hkdf_test.go vs Go crypto/hkdf over the RFC 5869 vectors.
$(BUILD)/netboot_hkdf.bin $(BUILD)/netboot_hkdf.map: src/netboot/hkdf.asm src/netboot/hmac_sha256.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_hkdf.bin \
	    --mapfile=$(BUILD)/netboot_hkdf.map \
	    src/netboot/hkdf.asm

netboot-hkdf: $(BUILD)/netboot_hkdf.bin $(BUILD)/netboot_hkdf.map

# netboot-hkdf-expand-label (i88) — TLS 1.3 HKDF-Expand-Label (RFC 8446 §7.1) over
# hkdf.asm: assemble the HkdfLabel (length + "tls13 "+label + context) then
# hkdf_expand.  The function the whole TLS key schedule is built from.  Standalone
# leaf, host-verified by hkdf_expand_label_test.go vs a Go RFC 8446 §7.1 reference
# (anchored to the RFC 8448 known-answer).
$(BUILD)/netboot_hkdf_expand_label.bin $(BUILD)/netboot_hkdf_expand_label.map: src/netboot/hkdf_expand_label.asm src/netboot/hkdf.asm src/netboot/hmac_sha256.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_hkdf_expand_label.bin \
	    --mapfile=$(BUILD)/netboot_hkdf_expand_label.map \
	    src/netboot/hkdf_expand_label.asm

netboot-hkdf-expand-label: $(BUILD)/netboot_hkdf_expand_label.bin $(BUILD)/netboot_hkdf_expand_label.map

# netboot-chacha20 (i88) — the ChaCha20 block function (RFC 8439 §2.3), the first
# i88 cipher (from-scratch ARX: the quarter-round + 20 rounds).  Standalone leaf,
# host-verified by chacha20_test.go vs the RFC 8439 known-answer vectors.
$(BUILD)/netboot_chacha20.bin $(BUILD)/netboot_chacha20.map: src/netboot/chacha20.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_chacha20.bin \
	    --mapfile=$(BUILD)/netboot_chacha20.map \
	    src/netboot/chacha20.asm

netboot-chacha20: $(BUILD)/netboot_chacha20.bin $(BUILD)/netboot_chacha20.map

# netboot-poly1305 (i88) — the Poly1305 one-time authenticator (RFC 8439 §2.5),
# the ChaCha20-Poly1305 MAC: byte-radix multi-precision (8x8 mul8, a 17x16
# schoolbook product, reduction mod 2^130-5).  Standalone leaf, host-verified by
# poly1305_test.go vs the RFC 8439 §2.5.2 KAT + a math/big reference.
$(BUILD)/netboot_poly1305.bin $(BUILD)/netboot_poly1305.map: src/netboot/poly1305.asm src/netboot/qsq.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_poly1305.bin \
	    --mapfile=$(BUILD)/netboot_poly1305.map \
	    src/netboot/poly1305.asm

netboot-poly1305: $(BUILD)/netboot_poly1305.bin $(BUILD)/netboot_poly1305.map

# netboot-x25519-field (i88) — Curve25519 field arithmetic over GF(2^255-19), the
# foundation for the X25519 key exchange: byte-radix multi-precision (8x8 mul8, a
# 32x32 schoolbook product, reduction via 2^256≡38 + 2^255≡19).  Standalone leaf,
# host-verified by x25519_field_test.go vs a math/big reference (the ladder builds
# on this in a follow-up).
$(BUILD)/netboot_x25519.bin $(BUILD)/netboot_x25519.map: src/netboot/x25519.asm src/netboot/qsq.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 --obj=$(BUILD)/netboot_x25519.bin \
	    --mapfile=$(BUILD)/netboot_x25519.map \
	    src/netboot/x25519.asm

netboot-x25519-field: $(BUILD)/netboot_x25519.bin $(BUILD)/netboot_x25519.map

# netboot-aead (i88) — AEAD_CHACHA20_POLY1305 (RFC 8439 §2.8), TLS 1.3 record
# protection.  Composes chacha20.asm + poly1305.asm (include'd; built with
# -D NETBOOT_AEAD so their NETBOOT_STANDALONE org guard stays inert and this file
# sets the org once).  Standalone leaf, host-verified by aead_test.go vs the RFC
# 8439 §2.8.2 (encrypt) + §2.6.2 (key-gen) KATs + a decrypt round-trip + tamper.
$(BUILD)/netboot_aead.bin $(BUILD)/netboot_aead.map: src/netboot/aead.asm src/netboot/chacha20.asm src/netboot/poly1305.asm src/netboot/qsq.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_AEAD=1 --obj=$(BUILD)/netboot_aead.bin \
	    --mapfile=$(BUILD)/netboot_aead.map \
	    src/netboot/aead.asm

netboot-aead: $(BUILD)/netboot_aead.bin $(BUILD)/netboot_aead.map

# netboot-tls-keyschedule (i88) — the TLS 1.3 key schedule (RFC 8446 §7.1), brick 1
# of the handshake (docs/specs/tls13-handshake.md).  Pure orchestration over the
# built hkdf_extract + expand_label (include'd; built with -D NETBOOT_TLS_KS so
# their NETBOOT_STANDALONE org guard stays inert and this file sets the org once).
# Standalone leaf, host-verified by tls_keyschedule_test.go vs a Go RFC 8446 §7.1
# reference anchored to the RFC 8448 known-answer.
$(BUILD)/netboot_tls_keyschedule.bin $(BUILD)/netboot_tls_keyschedule.map: src/netboot/tls_keyschedule.asm src/netboot/hkdf_expand_label.asm src/netboot/hkdf.asm src/netboot/hmac_sha256.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_KS=1 --obj=$(BUILD)/netboot_tls_keyschedule.bin \
	    --mapfile=$(BUILD)/netboot_tls_keyschedule.map \
	    src/netboot/tls_keyschedule.asm

netboot-tls-keyschedule: $(BUILD)/netboot_tls_keyschedule.bin $(BUILD)/netboot_tls_keyschedule.map

# netboot-tls-record (i88) — TLS 1.3 record protection (RFC 8446 §5.2-5.3), brick 2
# of the handshake (docs/specs/tls13-handshake.md).  The framing layer over the
# verified AEAD (include'd; built with -D NETBOOT_TLS_RECORD so the composed
# modules' org guards stay inert and this file sets the org once).  Standalone leaf,
# host-verified by tls_record_test.go (seal->open round-trip + a Go framing
# cross-check via the in-binary aead_encrypt + tamper/seq rejection).
$(BUILD)/netboot_tls_record.bin $(BUILD)/netboot_tls_record.map: src/netboot/tls_record.asm src/netboot/aead.asm src/netboot/chacha20.asm src/netboot/poly1305.asm src/netboot/qsq.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_RECORD=1 --obj=$(BUILD)/netboot_tls_record.bin \
	    --mapfile=$(BUILD)/netboot_tls_record.map \
	    src/netboot/tls_record.asm

netboot-tls-record: $(BUILD)/netboot_tls_record.bin $(BUILD)/netboot_tls_record.map

# netboot-tls-transcript (i88) — the TLS 1.3 handshake transcript hash (RFC 8446
# §4.4.1), brick 5 of the handshake (docs/specs/tls13-handshake.md).  A thin
# wrapper over sha256.asm streaming (include'd; built with -D NETBOOT_TLS_TRANSCRIPT
# so its org guard stays inert), adding a snapshot (save state -> final -> restore)
# so Derive-Secret can hash the transcript so far without ending the stream.
# Standalone leaf, host-verified by tls_transcript_test.go vs Go crypto/sha256.
$(BUILD)/netboot_tls_transcript.bin $(BUILD)/netboot_tls_transcript.map: src/netboot/tls_transcript.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_TRANSCRIPT=1 --obj=$(BUILD)/netboot_tls_transcript.bin \
	    --mapfile=$(BUILD)/netboot_tls_transcript.map \
	    src/netboot/tls_transcript.asm

netboot-tls-transcript: $(BUILD)/netboot_tls_transcript.bin $(BUILD)/netboot_tls_transcript.map

# netboot-tls-client-hello (i88) — the TLS 1.3 ClientHello builder (RFC 8446
# §4.1.2), brick 3 of the handshake (docs/specs/tls13-handshake.md).  Pure byte
# assembly (no crypto include'd; built with -D NETBOOT_TLS_CH so it sets the org
# once).  Standalone leaf, host-verified by tls_client_hello_test.go (byte-exact
# vs an independent Go reconstruction + a crypto/tls ClientHelloInfo parse).
$(BUILD)/netboot_tls_client_hello.bin $(BUILD)/netboot_tls_client_hello.map: src/netboot/tls_client_hello.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_CH=1 --obj=$(BUILD)/netboot_tls_client_hello.bin \
	    --mapfile=$(BUILD)/netboot_tls_client_hello.map \
	    src/netboot/tls_client_hello.asm

netboot-tls-client-hello: $(BUILD)/netboot_tls_client_hello.bin $(BUILD)/netboot_tls_client_hello.map

# netboot-tls-server-flight (i88) — the TLS 1.3 server-flight parser (RFC 8446
# §4.1.3/§4.3), brick 4 of the handshake (docs/specs/tls13-handshake.md).
# tls_parse_server_hello extracts the X25519 key_share + validates the negotiated
# suite/version; tls_walk_server_flight folds the decrypted EE/Certificate/
# CertificateVerify/Finished into the transcript, snapshots before the Finished,
# and captures its verify_data.  Built with -D NETBOOT_TLS_SF (the composition
# idiom; tls_transcript.asm + sha256.asm include'd, org guards inert).  Standalone
# leaf, host-verified by tls_server_flight_test.go.
$(BUILD)/netboot_tls_server_flight.bin $(BUILD)/netboot_tls_server_flight.map: src/netboot/tls_server_flight.asm src/netboot/tls_transcript.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_SF=1 --obj=$(BUILD)/netboot_tls_server_flight.bin \
	    --mapfile=$(BUILD)/netboot_tls_server_flight.map \
	    src/netboot/tls_server_flight.asm

netboot-tls-server-flight: $(BUILD)/netboot_tls_server_flight.bin $(BUILD)/netboot_tls_server_flight.map

# netboot-tls-client (i88) — the TLS 1.3 client, brick 6a: the record-level
# handshake state machine (docs/specs/tls13-handshake.md + the port plan).  It
# COMPOSES the five handshake bricks + x25519 into one binary and adds the client
# driver (tls_client_init + tls_client_first now; tls_client_on_record next).
# Built with -D NETBOOT_TLS_CLIENT=1: this file sets the org once, and the flag
# dedups the two cross-brick collisions (sha256 + qsq, port plan Part 0).  Host-
# verified by tls_client_test.go (capture-then-replay vs the Go authority).
$(BUILD)/netboot_tls_client.bin $(BUILD)/netboot_tls_client.map: \
	    src/netboot/tls_client.asm \
	    src/netboot/tls_keyschedule.asm src/netboot/hkdf_expand_label.asm \
	    src/netboot/hkdf.asm src/netboot/hmac_sha256.asm src/netboot/sha256.asm \
	    src/netboot/tls_record.asm src/netboot/aead.asm src/netboot/chacha20.asm \
	    src/netboot/poly1305.asm src/netboot/qsq.asm src/netboot/tls_client_hello.asm \
	    src/netboot/tls_server_flight.asm src/netboot/tls_transcript.asm src/netboot/x25519.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_CLIENT=1 --obj=$(BUILD)/netboot_tls_client.bin \
	    --mapfile=$(BUILD)/netboot_tls_client.map \
	    src/netboot/tls_client.asm

netboot-tls-client: $(BUILD)/netboot_tls_client.bin $(BUILD)/netboot_tls_client.map

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

# tcp-conn — the i70 TCP connection state machine (client active open):
# drv_read a segment, dispatch on the connection state (SYN-SENT/ESTABLISHED/
# FIN-WAIT), and emit the right control segment (ACK / FIN-ACK).  Composes the
# host-verified build_tcp_segment primitive and the real driver (encdrv.asm).
# Built with NETBOOT_HOSTTEST so the standalone test binary carries the i99
# streaming sink (CONN_SINK_* + the flush code) AND the i100 streamed-body
# SHA-256 verify (sha256.asm + conn_verify_init/final); both are excluded from
# the bootable images (which don't pass the flag) to keep them under &10000. The
# existing oracle tests are unaffected — CONN_SINK_ENABLED defaults to 0.
$(BUILD)/netboot_tcp_conn.bin $(BUILD)/netboot_tcp_conn.map: src/netboot/tcp_conn.asm src/netboot/build_tcp_segment.asm src/netboot/encdrv.asm src/netboot/sha256.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 --obj=$(BUILD)/netboot_tcp_conn.bin \
	    --mapfile=$(BUILD)/netboot_tcp_conn.map \
	    src/netboot/tcp_conn.asm

netboot-tcp-conn: $(BUILD)/netboot_tcp_conn.bin $(BUILD)/netboot_tcp_conn.map

# netboot-tcp-conn-stream — the i99 host-verification of tcp_conn.asm's opt-in
# streaming sink: drive a handshake + multi-segment body + FIN through the same
# netboot_tcp_conn binary with CONN_SINK_ENABLED=1 + a small CONN_FLUSH_WINDOW,
# and assert the recording test-double sink (CONN_SINK_OUT / CONN_SINK_CHUNKS)
# captured the body byte-for-byte across bounded flushes (the streaming analogue
# of the accumulated-CONN_DATA assert).  Mirrors the Go-authority streaming tests
# (PR #263).  Also covered by ci-netboot-z80 (which runs the whole package).
netboot-tcp-conn-stream: $(BUILD)/netboot_tcp_conn.bin $(BUILD)/netboot_tcp_conn.map
	cd tools/netboot-oracle/z80 && go test -run TestTCPConnStream ./...

# http-get — the i70 HTTP/1.0 GET client (firmware self-provisioning): build the
# request, send it over the established TCP connection (tcp_conn.asm), and parse
# the response status line + body offset.  Composes the connection state machine
# (which pulls in build_tcp_segment + encdrv) with the new http_build_request /
# http_parse_response; the i80 emulation test (http_get_test) drives a handshake,
# asserts the GET segment on the virtual wire matches the Go http.Client.Start
# authority byte-for-byte, streams a response, and checks the parse vs Go
# ParseResponse.
$(BUILD)/netboot_http_get.bin $(BUILD)/netboot_http_get.map: src/netboot/http_get.asm src/netboot/tcp_conn.asm src/netboot/build_tcp_segment.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_http_get.bin \
	    --mapfile=$(BUILD)/netboot_http_get.map \
	    src/netboot/http_get.asm

netboot-http-get: $(BUILD)/netboot_http_get.bin $(BUILD)/netboot_http_get.map

# fw-source (i100) — the cdn.githubraw.com commit-pinned firmware path builder.
# Standalone leaf (no TCP stack): fw_build_path concatenates
# /<owner>/<repo>/<sha>/<path> into FW_PATH; the host test (fw_source_test)
# byte-compares it vs the Go authority http.GithubRawPath, building both from the
# FW_* config strings read back out of the binary (one source of truth).
$(BUILD)/netboot_fw_source.bin $(BUILD)/netboot_fw_source.map: src/netboot/fw_source.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 \
	    --obj=$(BUILD)/netboot_fw_source.bin \
	    --mapfile=$(BUILD)/netboot_fw_source.map \
	    src/netboot/fw_source.asm

netboot-fw-source: $(BUILD)/netboot_fw_source.bin $(BUILD)/netboot_fw_source.map

# netboot-body-sink (i100) — the HTTP-header-skip sink adapter (the bodySink Z80
# port): the filter that drops the HTTP/1.0 response header so the streamed body
# reaching storage + the SHA-256 verify begins at the first body byte.  Standalone
# leaf (NETBOOT_STANDALONE, org &8000), host-verified by body_sink_test.go vs the
# Go authority http.NewBodySink — NOT wired into the bootable image yet (which
# stays byte-identical).
$(BUILD)/netboot_body_sink.bin $(BUILD)/netboot_body_sink.map: src/netboot/body_sink.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 \
	    --obj=$(BUILD)/netboot_body_sink.bin \
	    --mapfile=$(BUILD)/netboot_body_sink.map \
	    src/netboot/body_sink.asm

netboot-body-sink: $(BUILD)/netboot_body_sink.bin $(BUILD)/netboot_body_sink.map

# netboot-tls-reasm (i88 6b) — the TLS-record reassembler: frames complete TLS
# records out of an arbitrary byte stream (TCP segments do not align to record
# boundaries).  Standalone leaf, host-verified by tls_reasm_test.go vs the Go
# authority tls/reassembler.go::RecordReassembler over mis-aligned chunk sequences.
$(BUILD)/netboot_tls_reasm.bin $(BUILD)/netboot_tls_reasm.map: src/netboot/tls_reasm.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 \
	    --obj=$(BUILD)/netboot_tls_reasm.bin \
	    --mapfile=$(BUILD)/netboot_tls_reasm.map \
	    src/netboot/tls_reasm.asm

netboot-tls-reasm: $(BUILD)/netboot_tls_reasm.bin $(BUILD)/netboot_tls_reasm.map

# netboot-fw-span (i99/q16) — the firmware-spanning primitives (the Z80 port of
# the Go authority bdos.SpanPlan): fw_span_chunk_len (32-bit min(cap, remaining))
# + fw_span_record_name (<prefix><NNN>).  Standalone leaf (NETBOOT_STANDALONE,
# org &8000), host-verified by fw_span_test.go vs bdos.SpanPlan/SpanRecordName —
# NOT wired into the bootable image (which stays byte-identical).
$(BUILD)/netboot_fw_span.bin $(BUILD)/netboot_fw_span.map: src/netboot/fw_span.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STANDALONE=1 \
	    --obj=$(BUILD)/netboot_fw_span.bin \
	    --mapfile=$(BUILD)/netboot_fw_span.map \
	    src/netboot/fw_span.asm

netboot-fw-span: $(BUILD)/netboot_fw_span.bin $(BUILD)/netboot_fw_span.map

# netboot-http (i70 capstone) — the integrated HTTP fetch phase machine + the
# bootable HTTP-fetch disk.  http_fetch_first broadcasts the ARP, http_fetch_onframe
# drives ARP -> TCP handshake -> GET -> response/ACK -> FIN.  Composes http_get.asm
# (which pulls in tcp_conn + build_tcp_segment + encdrv) with build_arp_request +
# bdos_seam (the storage seam).  Two builds from one source:
#   * the host-test binary (NETBOOT_HOSTTEST) excludes http_main + eeprom.asm + the
#     B-DOS hook dispatch so the harness drives http_fetch_first/http_fetch_onframe
#     directly; netboot_http_test.go asserts the ARP/SYN/GET/ACK/FIN-ACK wire frames
#     + the accumulated body match the Go http.Fetcher authority byte-for-byte (the
#     B-DOS HSAVE write-out is real-hardware-only, not exercised).
#   * the bootable binary (no flag) includes http_main + eeprom.asm + the B-DOS HSAVE
#     so it reads the SAM's real MAC/IP, fetches the firmware blob, and writes it to
#     Trinity storage (the disk built by netboot-http-disk).
$(BUILD)/netboot_http.bin $(BUILD)/netboot_http.map: src/netboot/netboot_http.asm src/netboot/http_get.asm src/netboot/tcp_conn.asm src/netboot/build_tcp_segment.asm src/netboot/build_arp_request.asm src/netboot/bdos_seam.asm src/netboot/encdrv.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/netboot_http.bin \
	    --mapfile=$(BUILD)/netboot_http.map \
	    src/netboot/netboot_http.asm

netboot-http: $(BUILD)/netboot_http.bin $(BUILD)/netboot_http.map

# The bootable HTTP-fetch binary (http_main.asm): the full program for real Trinity
# and the Z80 port of the Go http.Provisioner — the EEPROM config read + the multi-
# file provisioning loop that streams each firmware file through the SHA-256 verify
# into bounded HSAVE records. Composes the single-file fetch + the pinned manifest +
# body_sink.asm. Built with -D NETBOOT_STREAM=1: the streaming sink + verify + sha256
# build in, and http_main.asm owns the &8000 org so `jp http_main` is the boot
# entry. The prov_* driver is host-verified against the Go authority (see the
# TestProvision*Z80 oracle tests). http is a section-D overlay program (its code runs
# above &C000, in section-D RAM), so its boot budget is the full 32768-byte window to
# &10000 (netboot-boot-fit-check.sh), not the 16384-byte section-C limit small images
# use.
$(BUILD)/netboot_http_boot.bin $(BUILD)/netboot_http_boot.map: src/netboot/http_main.asm src/netboot/netboot_http.asm src/netboot/http_get.asm src/netboot/tcp_conn.asm src/netboot/build_tcp_segment.asm src/netboot/build_arp_request.asm src/netboot/bdos_seam.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/eeprom.asm src/netboot/sha256.asm src/netboot/fw_source.asm src/netboot/body_sink.asm src/netboot/fw_span.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_STREAM=1 --obj=$(BUILD)/netboot_http_boot.bin \
	    --mapfile=$(BUILD)/netboot_http_boot.map \
	    src/netboot/http_main.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_http_boot.bin 32768 netboot_http_boot.bin

netboot-http-boot: $(BUILD)/netboot_http_boot.bin

# A bootable SAM disk image that auto-runs the HTTP fetch on power-on: it fetches
# the configured firmware blob from the configured HTTP server and writes it to
# Trinity storage (see docs/notes/netboot-trinity-testing.md "HTTP fetch").
netboot-http-disk: $(BUILD)/netboot_http_boot.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_http_boot.bin -netboot-name httpfetch \
	    $(BUILD)/netboot_http.mgt

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

# sd-csd (i145b) — the CSD-read -> BD_RECORDS decode (sd_csd.asm) as a standalone
# host-test fixture: encdrv (wait_ready) + bdos_seam (BD_RECORDS) + sd_csd, so
# csd_to_bd_records_test.go can Load it, attach the i145c SD model, and assert
# BD_RECORDS is COMPUTED from the modelled card's CSD (not injected).
$(BUILD)/netboot_sd_csd.bin $(BUILD)/netboot_sd_csd.map: src/netboot/sd_csd_standalone.asm src/netboot/sd_csd.asm src/netboot/encdrv.asm src/netboot/bdos_seam.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/netboot_sd_csd.bin \
	    --mapfile=$(BUILD)/netboot_sd_csd.map \
	    src/netboot/sd_csd_standalone.asm

netboot-sd-csd: $(BUILD)/netboot_sd_csd.bin $(BUILD)/netboot_sd_csd.map

# sd-listread (i141) — the same standalone fixture built with NETBOOT_REAL_LISTREAD,
# so bdos_read_list_sector routes to the REAL CMD17 single-block read (bd_list_read_hw,
# sd_csd.asm) instead of the BD_HOOK_LISTREAD harness hook. sd_listread_test.go Loads
# it, attaches the i145c/i145h SD model, SeedSectors the record-list sectors, and
# asserts the detection routines read them back through the raw CMD17 SPI path.
$(BUILD)/netboot_sd_listread.bin $(BUILD)/netboot_sd_listread.map: src/netboot/sd_listread_standalone.asm src/netboot/sd_csd.asm src/netboot/encdrv.asm src/netboot/bdos_seam.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 \
	    --obj=$(BUILD)/netboot_sd_listread.bin \
	    --mapfile=$(BUILD)/netboot_sd_listread.map \
	    src/netboot/sd_listread_standalone.asm

netboot-sd-listread: $(BUILD)/netboot_sd_listread.bin $(BUILD)/netboot_sd_listread.map

# eeprom-roundtrip — the non-destructive Trinity EEPROM write round-trip test
# (i225). Exercises the real eeprom.asm write path on a free scratch chunk and
# reports the result over the network (test_report.asm: a "SATR" UDP packet) +
# the border colour, so the SAME binary runs identically in the ENC28J60
# emulator and on real hardware. Driven by eeprom_roundtrip_test.go; gated by
# ci-netboot-z80.
$(BUILD)/eeprom_roundtrip.bin $(BUILD)/eeprom_roundtrip.map: src/netboot/eeprom_roundtrip_standalone.asm src/netboot/build_udp_frame.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/eeprom.asm src/netboot/test_report.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/eeprom_roundtrip.bin \
	    --mapfile=$(BUILD)/eeprom_roundtrip.map \
	    src/netboot/eeprom_roundtrip_standalone.asm
	@# org &8000, must fit section C so trinload can push it (push with
	@# tools/trinload-push/trinload-push.py <sam-ip> build/eeprom_roundtrip.bin 1 0x8000).
	@tools/netboot-boot-fit-check.sh $(BUILD)/eeprom_roundtrip.bin 16384 eeprom_roundtrip.bin

netboot-eeprom-roundtrip: $(BUILD)/eeprom_roundtrip.bin $(BUILD)/eeprom_roundtrip.map

# gen-bootloader-data — regenerate the embedded bootloader DEFB data from the
# sibling trinity-autoboot repo's build/bootloader.bin. The output
# (src/netboot/bootloader_chunk1_data.asm) is GITIGNORED: it holds the private
# bootloader (Colin's boot block + our patches), kept out of the repo and CI
# (q55). LOCAL-ONLY — needs ~/git/trinity-autoboot built (`cd ~/git/trinity-autoboot && make`).
TRINITY_AUTOBOOT ?= $(HOME)/git/trinity-autoboot
.PHONY: gen-bootloader-data
gen-bootloader-data:
	tools/gen-bootloader-data.py $(TRINITY_AUTOBOOT)/build/bootloader.bin src/netboot/bootloader_chunk1_data.asm

# eeprom-flash-chunk1 — flash the trinity-autoboot bootloader into the Trinity
# EEPROM bootblock (chunk 1) and verify the write (i135c — the first destructive
# EEPROM write). Reuses the i225/i226 hardware-proven write_chunk path + the
# test_report SATR primitive. The bootloader bytes are embedded verbatim from the
# gitignored bootloader_chunk1_data.asm (run `make gen-bootloader-data` first).
# LOCAL-ONLY: the embedded bootloader is private (q55), so this target and its
# test (eeprom_flash_chunk1_test.go, SKIP_PRIVATE_TESTS-gated) are NOT in CI.
$(BUILD)/eeprom_flash_chunk1.bin $(BUILD)/eeprom_flash_chunk1.map: src/netboot/eeprom_flash_chunk1.asm src/netboot/bootloader_chunk1_data.asm src/netboot/build_udp_frame.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/eeprom.asm src/netboot/test_report.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/eeprom_flash_chunk1.bin \
	    --mapfile=$(BUILD)/eeprom_flash_chunk1.map \
	    src/netboot/eeprom_flash_chunk1.asm
	@# org &8000, must fit section C so trinload can push it (push with
	@# tools/trinload-push/trinload-push.py <sam-ip> build/eeprom_flash_chunk1.bin 1 0x8000).
	@tools/netboot-boot-fit-check.sh $(BUILD)/eeprom_flash_chunk1.bin 16384 eeprom_flash_chunk1.bin

netboot-eeprom-flash-chunk1: $(BUILD)/eeprom_flash_chunk1.bin $(BUILD)/eeprom_flash_chunk1.map

# port-probe (i228 step A) — a hardware port-characterization probe: INs candidate
# unmapped ports and reports each value over the network (test_report SATR), to
# pick a port for runtime emulation-vs-hardware detection. org &8000, trinload-
# pushable. Gated by ci-netboot-z80.
$(BUILD)/port_probe.bin $(BUILD)/port_probe.map: src/netboot/port_probe_standalone.asm src/netboot/build_udp_frame.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/test_report.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/port_probe.bin \
	    --mapfile=$(BUILD)/port_probe.map \
	    src/netboot/port_probe_standalone.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/port_probe.bin 16384 port_probe.bin

netboot-port-probe: $(BUILD)/port_probe.bin $(BUILD)/port_probe.map

# mgt-screen-demo — trinload-pushable RAM test that redraws the MGT opening
# screen (rainbow stripes, ported verbatim from the stock ROM &ED1B; banner next).
# Emulation-tested (mgt_screen_demo_test.go), org &8000, RETs to trinload. i229.
$(BUILD)/mgt_screen_demo.bin $(BUILD)/mgt_screen_demo.map: src/netboot/mgt_screen_demo_standalone.asm src/netboot/build_udp_frame.asm src/netboot/encdrv.asm src/netboot/test_report.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/mgt_screen_demo.bin \
	    --mapfile=$(BUILD)/mgt_screen_demo.map \
	    src/netboot/mgt_screen_demo_standalone.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/mgt_screen_demo.bin 16384 mgt_screen_demo.bin

netboot-mgt-screen-demo: $(BUILD)/mgt_screen_demo.bin $(BUILD)/mgt_screen_demo.map

# smoke-test (i94) — the Trinity bring-up smoke test: drv_read a frame, answer an
# ARP request for the SAM's IP with build_arp_reply, drv_write the reply.  ONE
# binary, used by every test (no carve-out flat-beside-full split, i231b-b4):
# smoke_test_test.go drives smoke_serve_once directly and asserts the ARP reply
# on the virtual wire matches the Go smoke.Responder authority byte-for-byte;
# netboot_boot_test.go drives the bootable smoke_main end-to-end against the
# modelled EEPROM. The same binary boots real Trinity (the disk built by
# netboot-smoke-disk).
$(BUILD)/netboot_smoke.bin $(BUILD)/netboot_smoke.map: src/netboot/smoke_test.asm src/netboot/build_arp_reply.asm src/netboot/encdrv.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_smoke.bin \
	    --mapfile=$(BUILD)/netboot_smoke.map \
	    src/netboot/smoke_test.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_smoke.bin 16384 netboot_smoke.bin

netboot-smoke: $(BUILD)/netboot_smoke.bin $(BUILD)/netboot_smoke.map

# netboot-trinload (i132) — vendored simonowen/trinload, built to a raw &6000
# binary + symbol map so the koron-z80 harness can load and run it (trinload_test.go).
# Validates the push->run->return cycle (?/@/X protocol) in emulation before any
# hardware push. trinload includes the already-vendored encdrv.asm + eeprom.asm.
$(BUILD)/trinload.bin $(BUILD)/trinload.map: src/netboot/trinload.asm src/netboot/encdrv.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/trinload.bin \
	    --mapfile=$(BUILD)/trinload.map \
	    src/netboot/trinload.asm

netboot-trinload: $(BUILD)/trinload.bin $(BUILD)/trinload.map

# A bootable SAM disk image that auto-runs the smoke test on power-on.  Boot it
# on a SAM + Trinity, then from another machine on the same LAN `ping <sam-ip>`
# or `arping <sam-ip>` and watch the SAM's MAC come back (see
# docs/notes/netboot-trinity-testing.md).
netboot-smoke-disk: $(BUILD)/netboot_smoke.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_smoke.bin -netboot-name smoke \
	    $(BUILD)/netboot_smoke.mgt

# secd-loadability — the section-D loadability probe (src/secd_probe.asm). Builds a
# >16 KB bootable disk that bakes sentinels into section D (&C000+) and, at run time,
# reads them back; it prints "OK" iff `LOAD CODE 32768` deposited the >&BFFF bytes
# into section-D RAM and the run sees them. This is the empirical proof that section
# D is RAM at boot (not ROM1), which the i145b SD CSD read relies on (section-D
# overlay). Self-contained SimCoupe run (isolated HOME so Pete's ~/.simcoupe config
# is untouched; offscreen video). Asserts "^OK$" or fails.
$(BUILD)/secd_probe.bin: src/secd_probe.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/secd_probe.bin src/secd_probe.asm

.PHONY: secd-loadability
secd-loadability: $(BUILD)/secd_probe.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/secd_probe.bin -netboot-name probe \
	    $(BUILD)/secd_probe.mgt
	@simhome=$$(mktemp -d) ; mkdir -p "$$simhome/.simcoupe" ; \
	    HOME="$$simhome" SDL_VIDEODRIVER=$${SDL_VIDEODRIVER:-offscreen} \
	    SDL_AUDIODRIVER=$${SDL_AUDIODRIVER:-dummy} \
	    tools/run-simcoupe.sh $(BUILD)/secd_probe.mgt $(BUILD)/secd_probe.status.log ; \
	    rm -rf "$$simhome" ; \
	    status=$$(tr -d '\r\n ' < $(BUILD)/secd_probe.status.log || true) ; \
	    if [ "$$status" != "OK" ]; then \
	        echo "FAIL: section-D loadability probe status '$$status' (expected OK)" >&2 ; \
	        sed 's/^/    /' $(BUILD)/secd_probe.status.log >&2 || true ; \
	        exit 1 ; \
	    fi ; \
	    echo "secd-loadability OK — section D is RAM at boot (>&BFFF bytes loaded + readable)"

# netboot-server (i95) — the integrated netboot server: one main-loop dispatcher
# (netboot_serve_once) that routes a received frame to ARP / DHCP / TFTP-RRQ /
# TFTP-ACK, composing the host-verified builders/parsers + the real driver.  ONE
# binary, used by every test (no carve-out flat-beside-full split, i231b-b4b):
# netboot_server_test.go drives netboot_serve_once directly (asserting a full
# DISCOVER->OFFER->REQUEST->ACK->ARP->RRQ->OACK->ACK->DATA session on the virtual
# wire matches the Go server.Server.OnFrame authority byte-for-byte);
# netboot_serve_boot_test.go drives the bootable netboot_main end-to-end against
# the modelled EEPROM. The same binary boots real Trinity (the disk built by
# netboot-server-disk).
$(BUILD)/netboot_server.bin $(BUILD)/netboot_server.map: src/netboot/netboot_server.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/dhcp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_server.bin \
	    --mapfile=$(BUILD)/netboot_server.map \
	    src/netboot/netboot_server.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_server.bin 16384 netboot_server.bin

netboot-server: $(BUILD)/netboot_server.bin $(BUILD)/netboot_server.map

# A bootable SAM disk image that auto-runs the integrated netboot server on
# power-on.  Boot it on a SAM + Trinity, then point a Pi at the SAM and watch it
# netboot (see docs/notes/netboot-trinity-testing.md "Increment 2").
netboot-server-disk: $(BUILD)/netboot_server.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_server.bin -netboot-name netboot \
	    $(BUILD)/netboot_server.mgt

# netboot-serve (i96) — the serve-files TFTP demo server: ARP + TFTP only (no DHCP,
# no Pi PXE blob), serving a few files baked into the binary to a plain TFTP/curl
# client.  Two builds from one source:
#   * the host-test binary (NETBOOT_HOSTTEST) excludes serve_main + eeprom.asm so
#     the harness drives serve_serve_once directly; netboot_serve_test.go asserts a
#     full ARP + bare-RRQ->DATA + optioned-RRQ->OACK + miss->ERROR(1) session on the
#     virtual wire matches the Go serve.Responder.OnFrame authority byte-for-byte.
#   * the bootable binary (no flag) includes serve_main + eeprom.asm so it reads the
#     SAM's real MAC/IP, provisions the baked-in demo files, and serves on real
#     Trinity (the disk built by netboot-serve-disk).
$(BUILD)/netboot_serve.bin $(BUILD)/netboot_serve.map: src/netboot/netboot_serve.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/enc_link.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/netboot_serve.bin \
	    --mapfile=$(BUILD)/netboot_serve.map \
	    src/netboot/netboot_serve.asm

netboot-serve: $(BUILD)/netboot_serve.bin $(BUILD)/netboot_serve.map

# The bootable serve-files binary: the full program including the EEPROM config
# read + provision_demo + the serve_main forever-loop, for real Trinity. It carries
# the i145b-b2 SD CSD read (sd_csd.asm), whose ~600 bytes push the image's tail past
# &C000 into section D — RAM at boot (the section-D loadability probe proves LOAD CODE
# deposits >&BFFF into RAM and ROM1 is off at run), so its boot budget is the full
# 32768-byte &8000-&FFFF window, not the 16384-byte section-C limit.
$(BUILD)/netboot_serve_boot.bin $(BUILD)/netboot_serve_boot.map: src/netboot/netboot_serve.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/eeprom.asm src/netboot/sd_csd.asm src/netboot/bdos_seam.asm src/netboot/raw_record_sink.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 \
	    --obj=$(BUILD)/netboot_serve_boot.bin \
	    --mapfile=$(BUILD)/netboot_serve_boot.map \
	    src/netboot/netboot_serve.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_serve_boot.bin 32768 netboot_serve_boot.bin

netboot-serve-boot: $(BUILD)/netboot_serve_boot.bin $(BUILD)/netboot_serve_boot.map

# netboot-serve-boot-debug (i271) — the serve boot binary with the network debug
# step-markers compiled in (-D NETBOOT_DEBUG). It broadcasts a "SDBG" UDP packet at
# each WRQ/SD-write step (src/netboot/dbg_marker.asm), so an agent reads how far the
# SAM got off the wire (tcpdump / a UDP listener on port 9001) and a hang localizes
# to the last marker seen — the i270 debug bottleneck removed. Drop-in replacement
# for netboot_serve_boot.bin: push it to the SAM the same way for a diagnostic run,
# then deploy the non-debug serve. The Go harness asserts the marker sequence
# (netboot_serve_dbg_test.go). Same boot budget (32768) as the non-debug image.
$(BUILD)/netboot_serve_boot_debug.bin $(BUILD)/netboot_serve_boot_debug.map: src/netboot/netboot_serve.asm src/netboot/dbg_marker.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/eeprom.asm src/netboot/sd_csd.asm src/netboot/bdos_seam.asm src/netboot/raw_record_sink.asm src/netboot/test_report.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 -D NETBOOT_DEBUG=1 \
	    --obj=$(BUILD)/netboot_serve_boot_debug.bin \
	    --mapfile=$(BUILD)/netboot_serve_boot_debug.map \
	    src/netboot/netboot_serve.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_serve_boot_debug.bin 32768 netboot_serve_boot_debug.bin

netboot-serve-boot-debug: $(BUILD)/netboot_serve_boot_debug.bin $(BUILD)/netboot_serve_boot_debug.map

# A bootable SAM disk image that auto-runs the combined RRQ+WRQ serve program on
# power-on (i121i — the .mgt packaging vessel, sibling of the i121d trinload code
# block). The AUTO BASIC LOADs the serve binary at &8000 then OVERLAYS a small
# SERVE_CONFIG CODE file ("cfg") at the SERVE_CONFIG address, so the disk carries
# its WRQ record-placement strategy explicitly — the runtime image matches the
# trinload vessel's host-patched block exactly. Default strategy highest-free;
# override at build time with NETBOOT_STRATEGY=lowest | explicit:N. Boot it on a
# SAM + Trinity, then from any LAN machine `tftp <sam-ip>` (get serves files; put
# writes a disk image to a free record per the strategy) — see
# docs/notes/netboot-trinity-testing.md.
NETBOOT_STRATEGY ?= highest
netboot-serve-disk: $(BUILD)/netboot_serve_boot.bin $(BUILD)/netboot_serve_boot.map $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_serve_boot.bin -netboot-name serve \
	    -netboot-config-map $(BUILD)/netboot_serve_boot.map -netboot-strategy $(NETBOOT_STRATEGY) \
	    $(BUILD)/netboot_serve.mgt

# netboot-serve-trinload (i121d) — the pushable serve block, ALSO the i194 "disk-record
# push" deployable. Unlike the dumper (which has no boot build), the serve program's
# BOOT binary is already org &8000 with entry &8000 (`jp serve_main`) and self-
# contained, so it IS the trinload-pushable block — no separate build. The host
# launcher tools/trinload-push/trinpush-serve.py sets the WRQ placement strategy in its
# SERVE_CONFIG block (--strategy) and pushes it via the ?/@/X protocol; a subsequent
# `tftp put <image.mgt> trinity-sam-disks/<image.mgt>` lands an 819,200-byte disk image
# in a FREE Trinity record (the "trinity-sam-disks/" prefix selects the validated
# disk-record class — i121c / design §6.5; a NON-prefixed `put X` stores a flat file)
# (raw_record_sink + the size==819200 / "BDOS"-stamp validation gate), then the serve
# loop RETs to trinload via sv_exit_to_trinload — which quiesces the shared &DC
# microcontroller (deselect + bounded BUSY-poll + settle) so trinload's fixed-delay
# chk_trinity resumes cleanly (i194 clean-exit; designed, hardware-unverified). No free
# record -> ERROR(3,"no free record"), a named record is never touched. The wire push
# is hardware-gated (a real SAM running trinload); the config-patch logic is host-
# tested by netboot-trinpush-test, and the WRQ push + quiesce path is emulation-tested
# in Go (netboot_serve_wrq_record_test.go).
netboot-serve-trinload: $(BUILD)/netboot_serve_boot.bin $(BUILD)/netboot_serve_boot.map
	@echo "pushable serve / disk-record-push block: $(BUILD)/netboot_serve_boot.bin (push with tools/trinload-push/trinpush-serve.py <sam-ip> --strategy …; then 'tftp put <image.mgt> trinity-sam-disks/<image.mgt>' for a bootable disk record — a non-prefixed put stores a flat file, i121c)"

# netboot-trinpush-test (i121d) — host-test the serve push launcher's config patcher
# (mapfile parse, offset math, magic check, patched bytes) against the REAL built
# serve binary. Pure host Python; no hardware. The strategy->placement EFFECT is
# emulation-tested in Go (netboot_serve_wrq_record_test.go).
netboot-trinpush-test: $(BUILD)/netboot_serve_boot.bin $(BUILD)/netboot_serve_boot.map
	cd tools/trinload-push && python3 -m unittest test_trinpush -v

# trinpush-help (i277) — print the canonical SAM-push invocations so the exact
# command never has to be looked up. Print-only (no deploy): the actual push still
# goes through the deploy-guard (DEPLOY_CHECKED=1 + the hardware-readiness checklist).
.PHONY: trinpush-help
trinpush-help:
	@echo 'Push to the SAM (it auto-boots trinload ~80s after power-on; the pusher scripts are executable):'
	@echo
	@echo '  Full automated shot — power-cycle, push, capture :9001 markers, power off:'
	@echo '    DEPLOY_CHECKED=1 tools/hardware-shot/run-shot.sh [BIN] [MAP] [PAYLOAD] [IP]'
	@echo
	@echo '  Push the serve program, then store a disk record from any LAN host:'
	@echo '    make netboot-serve-trinload'
	@echo '    DEPLOY_CHECKED=1 tools/trinload-push/trinpush-serve.py <sam-ip> --strategy highest'
	@echo '    curl -T image.mgt tftp://<sam-ip>/trinity-sam-disks/image.mgt'
	@echo
	@echo '  Push + run any netboot *_trinload.bin (org &8000):'
	@echo '    DEPLOY_CHECKED=1 tools/trinload-push/trinload-push.py <sam-ip> build/netboot_dumper.bin 1 0x8000'
	@echo
	@echo 'DEPLOY_CHECKED=1 is required — without it the deploy-guard hook shows the hardware-readiness checklist.'
	@echo 'Details: tools/trinload-push/README.md ; docs/notes/netboot-trinity-testing.md'

# netboot-dumper (i173) — the SAMBOOT one-shot ROM+EEPROM dumper: trinload-pushed
# (NOT booted), it reads the patched 32 KB ROM + 128 KB Trinity EEPROM and serves
# them as 16 KB-region TFTP files (rom0/rom1 + eep0..eep7) so the host captures
# them. It reuses serve_serve_once + every helper from netboot_serve.asm verbatim
# (DUMPER arms the rrq_hit refresh hook there) and the EEPROM reader from
# eeprom.asm.  ONE build, no NETBOOT_HOSTTEST carve-out (i231b-b4e): a raw
# section-C image (org &8000) including the ROM-paging read + dumper_main; trinload
# pushes it to page P offset &8000 and jumps to &8000. Every test drives this one
# binary — the EEPROM region round-trip (dumper_test.go, flat harness, serve_serve_
# once by symbol) and the ROM-paging reads (dumper_rompaging_test.go, paged harness,
# dumper_read_rom0/rom1 by symbol over the i181 SAM pager). No -disk target: it is
# trinload-pushed, not booted (so no build-disk wiring). The patched-ROM CONTENTS
# stay hardware-gated (i87a captures + i87b diffs); the paging mechanics are
# emulation-verified. boot-fit-check applies (must fit section C so STAGE at &C000
# is free RAM).
$(BUILD)/netboot_dumper.bin $(BUILD)/netboot_dumper.map: src/netboot/netboot_dumper.asm src/netboot/netboot_serve.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/netboot_dumper.bin \
	    --mapfile=$(BUILD)/netboot_dumper.map \
	    src/netboot/netboot_dumper.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_dumper.bin 16384 netboot_dumper.bin

netboot-dumper: $(BUILD)/netboot_dumper.bin $(BUILD)/netboot_dumper.map

# netboot-csd-probe (i145a) — the trinload-pushable SD CSD-read probe: it reads
# the inserted SD card's 16-byte CSD register via a bare CMD9 over the Trinity
# SD-SPI bus (a new SD driver ported faithfully from docs/notes/
# trinity-sd-z80-interface.md) and serves it as a single TFTP file "csd.bin" so
# the host decodes the card capacity. It is the dumper (netboot_dumper.asm)
# re-pointed at the SD card's CSD as the served region; it reuses serve_serve_once
# + every helper from netboot_serve.asm verbatim (DUMPER arms the rrq_hit refresh
# hook there) + eeprom.asm (for the "Trinity Network " config read). ONE binary,
# used by every test (no carve-out flat-beside-full split, i231b-b4b): it is a raw
# section-C image (org &8000) including probe_main; trinload pushes it to page P
# offset &8000 and jumps to &8000. No -disk target — it is trinload-pushed, not
# booted; the boot-fit-check still applies (it must fit section C so STAGE at
# &C000 is free RAM). csd_probe_test.go drives the SD read + a bare RRQ for csd.bin
# through serve_serve_once piecewise (v2/64GB + v1 CSD via AttachSD, asserting the
# streamed 16 bytes == the configured CSD byte-for-byte against the i145c SD-SPI
# model validated by Colin's REAL B-DOS init ladder, i145f); csd_probe_main_test.go
# drives the full probe_main end-to-end. The REAL-card run is i145g (CLAUDE.md §5 —
# emulation-verified is not hardware-verified).
$(BUILD)/csd_probe.bin $(BUILD)/csd_probe.map: src/netboot/csd_probe.asm src/netboot/netboot_serve.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_reply.asm src/netboot/tftp_build.asm src/netboot/tftp_parse.asm src/netboot/encdrv.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/csd_probe.bin \
	    --mapfile=$(BUILD)/csd_probe.map \
	    src/netboot/csd_probe.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/csd_probe.bin 16384 csd_probe.bin

netboot-csd-probe: $(BUILD)/csd_probe.bin $(BUILD)/csd_probe.map

# netboot-sd-push (i293) — the small trinload-pushable cj.mgt -> Trinity-SD-record
# pusher: receives a .mgt over UDP (port 0xEDB0, our own ?/@/F framing) and writes
# each 512-byte sector into the FIRST FREE record via the B-DOS HWSAD hook (A=2 =
# Trinity SD), composing encdrv/eeprom/bdos_seam/sd_csd. ONE build, flag-free (no
# NETBOOT_HOSTTEST carve-out, i231b); NETBOOT_REAL_LISTREAD selects the real CMD17
# free-record list read (sd_csd.asm bd_list_read_hw). Section-C only (16384 budget).
# sd_push_test.go drives the receive->free-pick->HWSAD logic under the flat harness;
# sd_push_faithful_test.go (SKIP_PRIVATE_TESTS) drives it against Colin's real ROM +
# B-DOS and asserts the pushed bytes land at the free record's LBA in the SD model.
$(BUILD)/sd_push.bin $(BUILD)/sd_push.map: src/netboot/sd_push.asm src/netboot/encdrv.asm src/netboot/eeprom.asm src/netboot/bdos_seam.asm src/netboot/sd_csd.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 -D NETBOOT_WANT_CLAIM=1 --obj=$(BUILD)/sd_push.bin \
	    --mapfile=$(BUILD)/sd_push.map \
	    src/netboot/sd_push.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/sd_push.bin 16384 sd_push.bin

netboot-sd-push: $(BUILD)/sd_push.bin $(BUILD)/sd_push.map

# netboot-boot-record (i316) — the small trinload-pushable "boot Trinity SD record N"
# program: a host names a record number (a host-patched byte in the BOOT_CONFIG block),
# and this program HRECORD-selects that record and fires ALHK to load+run its AUTO file
# — the non-interactive network-driven counterpart to the i264 hold-key picker. It is a
# thin wrapper over bdos_boot_record (the i122a primitive), composing only bdos_seam.asm
# and calling its unconditional HRECORD+ALHK path (no free-record scan, no SD writes), so
# it needs neither NETBOOT_REAL_LISTREAD nor NETBOOT_WANT_CLAIM. ONE build, flag-free (no
# NETBOOT_HOSTTEST carve-out, i231). Section-C only (16384 budget). boot_record_test.go
# drives it under the flat harness with the BDOSStore attached (which models the RST 8
# HRECORD + ALHK dispatch), patches BOOT_CFG_RECORD, and asserts the record is selected
# and exactly one ALHK boot fires against it. The on-hardware boot shot is a SEPARATE
# follow-up (CLAUDE.md §5 — emulation-verified is not hardware-verified).
$(BUILD)/boot_record.bin $(BUILD)/boot_record.map: src/netboot/boot_record.asm src/netboot/bdos_seam.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/boot_record.bin \
	    --mapfile=$(BUILD)/boot_record.map \
	    src/netboot/boot_record.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/boot_record.bin 16384 boot_record.bin

netboot-boot-record: $(BUILD)/boot_record.bin $(BUILD)/boot_record.map

# netboot-samboot-config (i176) — the SAMBOOT BIOS config reader: a leaf routine
# (samboot_read_config) that reads the editable default-boot-record config from a
# named Trinity EEPROM chunk ("SAMBOOT Config  ") and returns the auto-boot
# decision, reusing eeprom.asm find_index + read_chunk verbatim. One flag-free
# build (no NETBOOT_HOSTTEST carve-out, i231b-b4f): the harness calls
# samboot_read_config by symbol; samboot_config_test.go programs the encoded config
# into the emulated EEPROM under the chunk name and asserts the reader decodes it
# back (A/HL contract). The on-hardware EEPROM WRITE (flashing the chunk) is the
# i135c path, out of scope. The host editor that produces the chunk bytes is
# tools/netboot-oracle/cmd/samboot-config (covered by `go test ./...`). Charter:
# docs/specs/samboot.md §4.
$(BUILD)/samboot_config.bin $(BUILD)/samboot_config.map: src/netboot/samboot_config.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/samboot_config.bin \
	    --mapfile=$(BUILD)/samboot_config.map \
	    src/netboot/samboot_config.asm

netboot-samboot-config: $(BUILD)/samboot_config.bin $(BUILD)/samboot_config.map

# netboot-trinity-identity (i213) — the Trinity firmware IDENTITY STAMP reader: a
# leaf routine (trinity_read_stamp) that reads a magic+version marker from a named
# Trinity EEPROM chunk ("Trinity Firmware") and reports whether OUR patched
# firmware is the one running (vs a stock floppy-loaded B-DOS), reusing eeprom.asm
# find_index + read_chunk verbatim. Built with NETBOOT_HOSTTEST so the harness
# calls trinity_read_stamp directly; trinity_identity_stamp_test.go programs the
# encoded stamp into the emulated EEPROM under the chunk name and asserts the
# reader decodes it back (A/CY contract). The on-hardware EEPROM WRITE (flashing
# the stamp) rides the i135c bootblock flash (private fork), out of scope. Host
# format authority: tools/netboot-oracle/trinityfw. Charter: registry item i213.
$(BUILD)/trinity_identity_stamp.bin $(BUILD)/trinity_identity_stamp.map: src/netboot/trinity_identity_stamp.asm src/netboot/eeprom.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/trinity_identity_stamp.bin \
	    --mapfile=$(BUILD)/trinity_identity_stamp.map \
	    src/netboot/trinity_identity_stamp.asm

netboot-trinity-identity: $(BUILD)/trinity_identity_stamp.bin $(BUILD)/trinity_identity_stamp.map

# netboot-client (i82) — the TFTP client boot disk: fetch a file (a .mgt image)
# from a TFTP server and write it to Trinity storage via the B-DOS hooks.  Two
# builds from one source:
#   * the host-test binary (NETBOOT_HOSTTEST) excludes client_main + eeprom.asm +
#     the B-DOS hook dispatch so the harness drives client_first/client_run_once
#     directly; netboot_client_test.go asserts the ARP request + the RRQ + the ACK
#     cadence + the accumulated STAGING bytes match the Go client.Client authority
#     byte-for-byte (the B-DOS HSAVE write-out is real-hardware-only, not exercised).
#   * the bootable binary (no flag) includes client_main + eeprom.asm + the B-DOS
#     HSAVE so it reads the SAM's real MAC/IP, fetches, and writes to Trinity (the
#     disk built by netboot-client-disk).
$(BUILD)/netboot_client.bin $(BUILD)/netboot_client.map: src/netboot/netboot_client.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_request.asm src/netboot/tftp_client.asm src/netboot/bdos_seam.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/key_read_test.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_HOSTTEST=1 \
	    --obj=$(BUILD)/netboot_client.bin \
	    --mapfile=$(BUILD)/netboot_client.map \
	    src/netboot/netboot_client.asm

netboot-client: $(BUILD)/netboot_client.bin $(BUILD)/netboot_client.map

# The bootable client binary: the full program including the EEPROM config read +
# the client_main fetch-then-HSAVE flow, for real Trinity. Like the serve image it
# carries the i145b-b2 SD CSD read (sd_csd.asm) as a section-D overlay, so its boot
# budget is the full 32768-byte &8000-&FFFF window (see the serve rule above).
$(BUILD)/netboot_client_boot.bin $(BUILD)/netboot_client_boot.map: src/netboot/netboot_client.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_request.asm src/netboot/tftp_client.asm src/netboot/bdos_seam.asm src/netboot/bdos_picker.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/key_read_test.asm src/netboot/eeprom.asm src/netboot/raw_record_sink.asm src/netboot/sd_csd.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 \
	    --obj=$(BUILD)/netboot_client_boot.bin \
	    --mapfile=$(BUILD)/netboot_client_boot.map \
	    src/netboot/netboot_client.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_client_boot.bin 32768 netboot_client_boot.bin

netboot-client-boot: $(BUILD)/netboot_client_boot.bin $(BUILD)/netboot_client_boot.map

# A bootable SAM disk image that auto-runs the TFTP client on power-on: it fetches
# the configured file from the configured TFTP server and writes it to Trinity
# storage (see docs/notes/netboot-trinity-testing.md "Increment 3").
netboot-client-disk: $(BUILD)/netboot_client_boot.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_client_boot.bin -netboot-name client \
	    $(BUILD)/netboot_client.mgt

# The bootable fetch-and-boot binary (i182a): netboot_client.asm with NETBOOT_FETCH_BOOT
# so the &8000 boot entry runs client_fetch_boot (the i122c PXE-style fetch -> stream
# into a scratch Trinity record -> validate -> ALHK-boot) instead of client_main. Same
# source + includes as netboot_client_boot (incl. the i145b-b2 SD CSD overlay), so the
# boot budget is the full 32768-byte &8000-&FFFF window.
$(BUILD)/netboot_fetch_boot.bin $(BUILD)/netboot_fetch_boot.map: src/netboot/netboot_client.asm src/netboot/build_udp_frame.asm src/netboot/build_arp_request.asm src/netboot/tftp_client.asm src/netboot/bdos_seam.asm src/netboot/bdos_picker.asm src/netboot/encdrv.asm src/netboot/enc_link.asm src/netboot/key_read_test.asm src/netboot/eeprom.asm src/netboot/raw_record_sink.asm src/netboot/sd_csd.asm
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_REAL_LISTREAD=1 -D NETBOOT_FETCH_BOOT=1 \
	    --obj=$(BUILD)/netboot_fetch_boot.bin \
	    --mapfile=$(BUILD)/netboot_fetch_boot.map \
	    src/netboot/netboot_client.asm
	@tools/netboot-boot-fit-check.sh $(BUILD)/netboot_fetch_boot.bin 32768 netboot_fetch_boot.bin

netboot-fetch-boot-boot: $(BUILD)/netboot_fetch_boot.bin $(BUILD)/netboot_fetch_boot.map

# A bootable SAM disk image that auto-runs the PXE-style fetch-and-boot on power-on:
# it fetches the configured .mgt straight into a scratch Trinity record, validates it,
# and ALHK-boots it (the i182 hardware run; mirrors netboot-client-disk).
netboot-fetch-boot-disk: $(BUILD)/netboot_fetch_boot.bin $(BUILD)/build-disk
	$(BUILD)/build-disk -netboot $(BUILD)/netboot_fetch_boot.bin -netboot-name fetchboot \
	    $(BUILD)/netboot_fetch_boot.mgt

# editmodel-z80 — editor edit-model block-list, Brick 1 (flat-memory, no SAM
# paging). The koron-go/z80 harness under tools/netboot-oracle/z80/ is a
# general flat-memory Z80 test driver (not netboot-specific); the editmodel
# test reuses it directly. Gated by ci-netboot-z80 alongside the netboot tests.
$(BUILD)/editmodel.bin $(BUILD)/editmodel.map: src/editmodel.asm
	@mkdir -p $(BUILD)
	pyz80 -D EM_STANDALONE=1 --obj=$(BUILD)/editmodel.bin \
	    --mapfile=$(BUILD)/editmodel.map \
	    src/editmodel.asm

editmodel-z80: $(BUILD)/editmodel.bin $(BUILD)/editmodel.map

# editmodel-paged-z80 — the same edit-model assembled with its PAGED backend
# (Brick 2): blocks live in real i2 page-pool pages reached via OUT (251)/HMPR
# section-C paging, instead of a flat arena. It orgs in LMPR low memory and
# includes pagepool.asm resident. Driven by editmodel_paged_test.go under the
# one sampage harness (where OUT (251) pages section C for real); gated by
# ci-netboot-z80 alongside the flat editmodel build.
$(BUILD)/editmodel-paged.bin $(BUILD)/editmodel-paged.map: src/editmodel.asm src/pagepool.asm
	@mkdir -p $(BUILD)
	pyz80 -D EM_PAGED=1 --obj=$(BUILD)/editmodel-paged.bin \
	    --mapfile=$(BUILD)/editmodel-paged.map \
	    src/editmodel.asm

editmodel-paged-z80: $(BUILD)/editmodel-paged.bin $(BUILD)/editmodel-paged.map

# pagepool-z80 — the on-SAM IDE page allocator core, i2a (flat-memory; no SAM
# paging yet — the page_owner[] table is just a byte array here). Same standalone
# koron-go/z80 harness as editmodel; gated by ci-netboot-z80.
$(BUILD)/pagepool.bin $(BUILD)/pagepool.map: src/pagepool.asm
	@mkdir -p $(BUILD)
	pyz80 -D PP_STANDALONE=1 --obj=$(BUILD)/pagepool.bin \
	    --mapfile=$(BUILD)/pagepool.map \
	    src/pagepool.asm

pagepool-z80: $(BUILD)/pagepool.bin $(BUILD)/pagepool.map

# spill-z80 — the page-persistence (spill) manager, i215b: the lazy-spill policy
# layered over pagepool (i2a) and ported from the i215a Go authority. Same
# standalone koron-go/z80 harness; gated by ci-netboot-z80.
$(BUILD)/spill.bin $(BUILD)/spill.map: src/spill.asm src/pagepool.asm
	@mkdir -p $(BUILD)
	pyz80 -D SP_STANDALONE=1 --obj=$(BUILD)/spill.bin \
	    --mapfile=$(BUILD)/spill.map \
	    src/spill.asm

spill-z80: $(BUILD)/spill.bin $(BUILD)/spill.map

# viewport-z80 — the read-only viewer's scroll/cursor state machine, i4a
# (flat-memory; no screen rendering). Same standalone koron-go/z80 harness as
# editmodel; gated by ci-netboot-z80.
$(BUILD)/viewport.bin $(BUILD)/viewport.map: src/viewport.asm
	@mkdir -p $(BUILD)
	pyz80 -D VP_STANDALONE=1 --obj=$(BUILD)/viewport.bin \
	    --mapfile=$(BUILD)/viewport.map \
	    src/viewport.asm

viewport-z80: $(BUILD)/viewport.bin $(BUILD)/viewport.map

# asmlex-z80 — aarch64 assembler-source tokenizer, i48c (flat-memory).
# Same standalone flat-memory harness as editmodel; gated by ci-netboot-z80.
$(BUILD)/asmlex.bin $(BUILD)/asmlex.map: src/asmlex.asm
	@mkdir -p $(BUILD)
	pyz80 -D ASMLEX_STANDALONE=1 --obj=$(BUILD)/asmlex.bin \
	    --mapfile=$(BUILD)/asmlex.map \
	    src/asmlex.asm

asmlex-z80: $(BUILD)/asmlex.bin $(BUILD)/asmlex.map

# asmparse-z80 — aarch64 assembler-source parser, i48c (flat-memory).
# Same standalone flat-memory harness as asmlex; gated by ci-netboot-z80.
# Depends on the generated src/mnemonic_names.inc (committed; `make tables`).
$(BUILD)/asmparse.bin $(BUILD)/asmparse.map: src/asmparse.asm src/mnemonic_names.inc
	@mkdir -p $(BUILD)
	pyz80 -D ASMPARSE_STANDALONE=1 --obj=$(BUILD)/asmparse.bin \
	    --mapfile=$(BUILD)/asmparse.map \
	    src/asmparse.asm

asmparse-z80: $(BUILD)/asmparse.bin $(BUILD)/asmparse.map

# pass1-ir-z80 — Z80 Pass1-over-IR walk, i48c-b8a (flat-memory). A standalone
# flat harness containing ONLY the pass-1 machinery (the reused leaves
# expr_eval/symbols/local_labels/litpool/ml + the new IR-walk); the IR record
# buffer is produced host-side. Verified by tools/netboot-oracle/z80/pass1_ir_test.go
# against assemble.Pass1. Depends on the included leaf sources + tbn_constants.inc.
$(BUILD)/test_pass1_ir.bin $(BUILD)/test_pass1_ir.map: src/test_pass1_ir.asm \
	    src/expr_eval.asm src/symbols.asm src/local_labels.asm src/litpool.asm \
	    src/ml.asm src/tbn_constants.inc
	@mkdir -p $(BUILD)
	pyz80 -D PASS1_IR_STANDALONE=1 --obj=$(BUILD)/test_pass1_ir.bin \
	    --mapfile=$(BUILD)/test_pass1_ir.map \
	    src/test_pass1_ir.asm

pass1-ir-z80: $(BUILD)/test_pass1_ir.bin $(BUILD)/test_pass1_ir.map

# compact-ir-z80 — Z80 compact-core walk, i48c-b8b (flat-memory). The NON-ENCODER
# body of assemble.Compact: COMMENT/BLANK_RUN → sidecar rows, `.global` → name_id
# list, constant-data directives → KindLitData runs (1016-byte split), LABEL_DEF/
# LOCAL_DEF dropped, INST → skeleton KindInsnRun elements. Includes the b8a
# pass1-ir harness (reusing its pass1 walk + leaves + RecordPC capture). Verified
# by tools/netboot-oracle/z80/compact_ir_test.go against assemble.Compact.
$(BUILD)/test_compact_ir.bin $(BUILD)/test_compact_ir.map: src/test_compact_ir.asm \
	    src/test_pass1_ir.asm src/expr_eval.asm src/symbols.asm src/local_labels.asm \
	    src/litpool.asm src/ml.asm src/tbn_constants.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/test_compact_ir.bin \
	    --mapfile=$(BUILD)/test_compact_ir.map \
	    src/test_compact_ir.asm

compact-ir-z80: $(BUILD)/test_compact_ir.bin $(BUILD)/test_compact_ir.map

# Every netboot routine binary the harness tests load.
netboot-z80-routines: netboot-build-udp-frame netboot-dhcp-reply netboot-tftp-build netboot-tftp-parse netboot-tftp-client netboot-build-arp-request netboot-build-arp-reply netboot-build-tcp-segment netboot-sha256 netboot-hmac-sha256 netboot-hkdf netboot-hkdf-expand-label netboot-chacha20 netboot-poly1305 netboot-x25519-field netboot-aead netboot-tls-keyschedule netboot-tls-record netboot-tls-transcript netboot-tls-client-hello netboot-tls-server-flight netboot-tls-client netboot-encdrv netboot-dhcp-loop netboot-tcp-conn netboot-http-get netboot-fw-source netboot-body-sink netboot-tls-reasm netboot-fw-span netboot-http netboot-http-boot netboot-tftp-server-loop netboot-tftp-client-loop netboot-tftp-client-front netboot-bdos-seam netboot-smoke netboot-server netboot-serve netboot-client netboot-dumper netboot-csd-probe netboot-sd-push netboot-boot-record netboot-samboot-config netboot-trinity-identity netboot-serve-boot netboot-serve-boot-debug netboot-client-boot netboot-fetch-boot-boot netboot-trinload netboot-sd-csd netboot-sd-listread netboot-eeprom-roundtrip netboot-port-probe netboot-mgt-screen-demo

ci-netboot-z80: netboot-z80-routines editmodel-z80 editmodel-paged-z80 pagepool-z80 spill-z80 viewport-z80 asmlex-z80 asmparse-z80 pass1-ir-z80 compact-ir-z80
	cd tools/sampage && go test ./...
	cd tools/netboot-oracle/z80 && go test ./...
	# Guard: the 8x-unrolled SHA-256 round block inlined in sha256.asm still
	# matches its generator (tools/sha256-unroll-gen) byte-for-byte.
	cd tools/sha256-unroll-gen && go test ./...

test-format: sam-aarch64 release-unstripped-tbn
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
STATICCHECK_MODULES := comment-bench sam-aarch64-format sam-aarch64 aarch64enc aarch64dec tables-gen z80-test-harness-go zx0-greedy editor-prototype netboot-oracle netboot-oracle/z80 registry sampage
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

.PHONY: check-no-silent-skips
check-no-silent-skips:
	bash tools/check-no-silent-skips.sh

.PHONY: check-hosttest-carveouts
check-hosttest-carveouts:
	bash tools/check-hosttest-carveouts.sh

.PHONY: check-trinity-authority
check-trinity-authority:
	bash tools/check-trinity-authority.sh

.PHONY: registry registry-sync-check registry-gen tables-gen enctab test-encoder ci-encoder

# registry-gen — build the registry validate/gen CLI.  Operates on
# registry/*.yaml sources; generates the four docs/notes/*-registry-*.md views.
registry-gen:
	cd tools/registry && go build -o $(CURDIR)/$(BUILD)/registry .

# registry — regenerate the four docs/notes/*.md views in place from
# registry/items.yaml, registry/questions.yaml, and registry/priority.yaml.
# The four views are: item-registry-open, item-registry-closed,
# question-registry-open, and backlog (priority queue).
.PHONY: registry
registry: registry-gen
	REGISTRY_ITEMS=registry/items.yaml \
	REGISTRY_QUESTIONS=registry/questions.yaml \
	REGISTRY_PRIORITY=registry/priority.yaml \
	REGISTRY_DIR=registry \
	REGISTRY_TEMPLATES=tools/registry/templates \
	REGISTRY_OUTDIR=docs/notes \
	$(BUILD)/registry gen registry/items.yaml registry/questions.yaml

# registry-sync-check — the local pre-commit registry gate, mirroring CI's two
# registry-sync steps: (1) validate the source against every invariant, then
# (2) regenerate the four views into build/gen/registry/ and diff them against
# the committed docs/notes/ copies, failing on drift (a YAML edit that forgot
# `make registry`, or a hand edit to a generated file).  Validating here too
# closes the i320 gap: a source-invariant violation that leaves the views
# self-consistent (e.g. a DONE item still ranked in priority.yaml) passed the
# view-diff locally yet failed CI's separate validate step.  Same REGISTRY_* env
# as CI's "Validate the registry source" step.
registry-sync-check: registry-gen
	REGISTRY_ITEMS=registry/items.yaml \
	REGISTRY_QUESTIONS=registry/questions.yaml \
	REGISTRY_PRIORITY=registry/priority.yaml \
	REGISTRY_DIR=registry \
	$(BUILD)/registry validate registry/items.yaml registry/questions.yaml
	@mkdir -p $(BUILD)/gen/registry
	REGISTRY_ITEMS=registry/items.yaml \
	REGISTRY_QUESTIONS=registry/questions.yaml \
	REGISTRY_PRIORITY=registry/priority.yaml \
	REGISTRY_DIR=registry \
	REGISTRY_TEMPLATES=tools/registry/templates \
	REGISTRY_OUTDIR=$(BUILD)/gen/registry \
	$(BUILD)/registry gen registry/items.yaml registry/questions.yaml
	@fail=0; \
	for f in item-registry-open.md item-registry-closed.md question-registry-open.md backlog.md; do \
	    if ! diff -u docs/notes/$$f $(BUILD)/gen/registry/$$f; then \
	        echo ""; \
	        echo "ERROR: docs/notes/$$f is stale — it differs from the registry/"; \
	        echo "YAML output.  Run 'make registry' and commit the result."; \
	        fail=1; \
	    fi; \
	done; \
	if [ $$fail -ne 0 ]; then exit 1; fi
	@echo "registry-sync-check: generated registry views are up to date."

# tables-gen — generates every Z80 data table whose authority is Go source:
# the binary enctab.enc form table (make enctab) and the sysreg/pstate/dc/tlbi
# tables in src/sysreg_tables.inc (make tables).  Imports both authority
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

# tables — regenerate every committed generated Z80 table in place.  Today:
#   - the sysreg/pstate/dc/tlbi tables (src/sysreg_tables.inc), projected
#     from tools/sam-aarch64-format/sysregs.go.
#   - the OP_KIND_*/REC_KIND_*/DIR_* equates (src/tbn_constants.inc), projected
#     from tools/sam-aarch64-format/{operands,kinds,directives}.go.
#   - the MNEM_<NAME> mnemonic-ID equates (src/mnemonic_ids.inc), projected
#     from tools/sam-aarch64-format/mnemonics.go.
# (enctab.enc is generated by the separate `make enctab` target — it is a
# binary payload, not a committed source table — so it is deliberately not
# part of `make tables`.)
.PHONY: tables
tables: tables-gen
	$(BUILD)/tables-gen -sysreg-inc src/sysreg_tables.inc
	$(BUILD)/tables-gen -constants-inc src/tbn_constants.inc
	$(BUILD)/tables-gen -mnemonic-ids-inc src/mnemonic_ids.inc
	$(BUILD)/tables-gen -mnemonic-names-inc src/mnemonic_names.inc
	$(BUILD)/tables-gen -sysopts-inc src/disasm_sysopts.inc

# tables-sync-check — freshness guard: regenerate the committed tables into
# build/gen/ and diff against the in-tree copies; fail on any drift (a Go-side
# edit that forgot `make tables`, or a hand edit to a generated file).  Runs
# as a step of the `sysreg-sync` CI job.  Closes the hand-sync drift class for
# the generated tables (i7); the structural twin of `make enctab` mirroring the
# Go runtime form table.
.PHONY: tables-sync-check
tables-sync-check: tables-gen
	@mkdir -p $(BUILD)/gen
	$(BUILD)/tables-gen -sysreg-inc $(BUILD)/gen/sysreg_tables.inc
	$(BUILD)/tables-gen -constants-inc $(BUILD)/gen/tbn_constants.inc
	$(BUILD)/tables-gen -mnemonic-ids-inc $(BUILD)/gen/mnemonic_ids.inc
	$(BUILD)/tables-gen -mnemonic-names-inc $(BUILD)/gen/mnemonic_names.inc
	$(BUILD)/tables-gen -sysopts-inc $(BUILD)/gen/disasm_sysopts.inc
	@fail=0; \
	for f in sysreg_tables.inc tbn_constants.inc mnemonic_ids.inc mnemonic_names.inc disasm_sysopts.inc; do \
	    if ! diff -u src/$$f $(BUILD)/gen/$$f; then \
	        echo ""; \
	        echo "ERROR: src/$$f is stale — it differs from the tools/tables-gen"; \
	        echo "output.  Run 'make tables' and commit the result (or, if you"; \
	        echo "edited the Go authority, this is the expected regeneration)."; \
	        fail=1; \
	    fi; \
	done; \
	if [ $$fail -ne 0 ]; then exit 1; fi
	@echo "tables-sync-check: generated tables are up to date with tools/tables-gen."

test-encoder: sam-aarch64 tables-gen release-unstripped-tbn
	cd tools/sam-aarch64-format && go test ./...
	cd tools/aarch64enc && go test ./...
	cd tools/tables-gen && go test ./...
	cd tools/sam-aarch64 && go test ./...
	./tests/format/run-refenc-roundtrip.sh
	./tests/spectrum4/run-roundtrip.sh

ci-encoder: test-encoder

.PHONY: assembler assembler-prod build-disk disk test-mem-offaxis cluster-offaxis paged-call-payload enc-fix-payload sysreg-data disasm-payload disasm-test-payload test-core ci-core check-budget

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
# Also imports enc_fix_payload.sym to pick up ENC_FIX_PAYLOAD_LEN (i69):
# the payload is org'd at &E100, assembled standalone (no assembler.sym
# dependency — all values are literals), and exports the payload length
# via its sym file.  The payload must be built before the assembler so
# this --importfile is satisfied.
$(BUILD)/assembler.bin $(BUILD)/assembler.sym: src/assembler.asm $(wildcard src/*.asm) $(wildcard src/**/*.asm) $(wildcard src/*.inc) $(BUILD)/enc_fix_payload.sym
	@mkdir -p $(BUILD)
	pyz80 -D BUILD_TESTS=1 \
	    --obj=$(BUILD)/assembler.bin \
	    --exportfile=$(BUILD)/assembler.sym \
	    --importfile=$(BUILD)/enc_fix_payload.sym \
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

# encode_inst fixture data payload (BUILD_TESTS only — i69 lever 3).
#
# src/test_encode_inst_payload.asm is a pure-data file (org &E100)
# holding enc_fix_table rows + operand streams.  It assembles
# standalone (all values are literals; no importfile needed) into
# build/enc_fix_payload.bin.  The sym file exports ENC_FIX_PAYLOAD_LEN,
# which the assembler target imports via --importfile to size the LDIR
# in run_encode_inst_self_tests.
#
# The payload is HLOADed at boot into physical page 11 by
# src/loader.asm::load_enc_fix_payload, then bulk-copied via LDIR into
# section-D RAM at ENC_FIX_TABLE_RAM (&E100) before enctab_map_in.
# Because the binary is assembled with org &E100, every row's "fixture
# ptr" field already holds a section-D absolute address after the copy.
$(BUILD)/enc_fix_payload.bin $(BUILD)/enc_fix_payload.sym: src/test_encode_inst_payload.asm
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/enc_fix_payload.bin \
	    --exportfile=$(BUILD)/enc_fix_payload.sym \
	    src/test_encode_inst_payload.asm

enc-fix-payload: $(BUILD)/enc_fix_payload.bin

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
$(BUILD)/disasm.bin: src/disasm.asm src/sysreg_names.inc src/sysreg_tables.inc src/disasm_sysopts.inc
	@mkdir -p $(BUILD)
	pyz80 --obj=$(BUILD)/disasm.bin src/disasm.asm

$(BUILD)/disasm-test.bin: src/disasm.asm src/sysreg_names.inc src/sysreg_tables.inc src/disasm_sysopts.inc
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
disk: assembler test-mem-offaxis cluster-offaxis enc-fix-payload paged-call-payload sysreg-data disasm-test-payload zx0-test-payload enctab $(BUILD)/build-disk
	$(BUILD)/build-disk \
	    -variant test \
	    -test-mem $(BUILD)/test_mem.bin \
	    -cluster $(BUILD)/test_cluster.bin \
	    -enc-fix $(BUILD)/enc_fix_payload.bin \
	    -paged-call $(BUILD)/paged_call_test_payload.bin \
	    -sysreg-data $(BUILD)/sysreg_data.bin \
	    -disasm $(BUILD)/disasm-test.bin \
	    -zx0 $(BUILD)/zx0-test.bin \
	    $(BUILD)/assembler.bin $(BUILD)/enctab.enc $(BUILD)/test.mgt

# harness-sweep — one-shot "build every artefact the koron-go/z80 harness
# reads, then run its full Go test suite" (tools/z80-test-harness-go).
# The per-variant prerequisites in that dir's README cover the standalone
# binary; the full `go test ./...` suite (boot self-tests + the fold / align /
# org guards + the disasm oracle + compact-.tbn round-trip) needs the complete
# artefact set below.  The corpus-dependent zx0 profiling tests skip when their
# inputs are absent — expected, not a failure.  Dev convenience, NOT a CI gate
# (SimCoupé is the gate); see tools/z80-test-harness-go/USAGE.md.
.PHONY: harness-sweep
harness-sweep: assembler assembler-prod enctab cluster-offaxis test-mem-offaxis enc-fix-payload paged-call-payload sysreg-data disasm-payload disasm-test-payload zx0-payload zx0-test-payload zx0-compress-payload sam-aarch64
	cd tools/z80-test-harness-go && go test -count=1 ./...

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
