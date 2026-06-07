; zx0_payload.asm — the page-13 ZX0 payload (PRODUCTION feature, both
; variants): greedy compressor + turbo decoder, per the comment-storage
; design (docs/specs/comment-storage-design.md §5/§6) and its i68
; implementation plan (§7).
;
; Assembled STANDALONE in two variants, mirroring the disasm.bin split:
;
;   zx0.bin       (PROD)  no flag — compressor (&8400) + decoder (&8B00).
;                 Ships on every production disk; the editor's
;                 compress-at-save / decode-at-read paths call the
;                 ZX0_*_ENTRY addresses via paged_call.
;
;   zx0-test.bin  (TEST)  -D BUILD_TESTS=1 — adds the boot self-test
;                 driver + the baked fixture (raw block + its
;                 Go-authority compressed bytes) at ZX0_SELF_TEST_ENTRY
;                 (&AFA0, the §5 spare region).  Ships only on the test
;                 disk; the BUILD_TESTS assembler boot paged_calls it.
;                 The file pads across the &8B80-&AF9D workspace region
;                 (RAM scratch — the zero fill is simply never read), so
;                 the test payload is ~11 KB vs the prod payload's ~2 KB.
;
; HLOAD'd at boot into physical page 13 at ZX0_LOAD_ADDR (&8400) by
; src/loader.asm::load_zx0_payload — alongside sysreg_data.bin, which
; occupies the same page at &8000-&83B3.  Page-13 boot-time
; multiplexing (BUILD_TESTS): test_mem.bin is HLOAD'd at page offset 0
; first and fully consumed by run_mem_self_tests; sysreg_data + this
; payload load after it, before the suites that page_call into them.
; See assembler.asm's boot sequence + loader.asm ordering contracts.
;
; All addresses are pinned by src/zx0_comm.inc (shared with
; src/trampoline.asm) and asserted below — the two builds cannot drift.

                include "zx0_comm.inc"

; ── Compressor at &8400 (org directive lives in the included file) ────
                include "zx0_compress.asm"

                assert  zx0_compress == ZX0_COMPRESS_ENTRY
                assert  $ <= ZX0_DECODE_ENTRY
                defs    ZX0_DECODE_ENTRY - $

; ── Turbo decoder at &8B00 ────────────────────────────────────────────
                include "dzx0_turbo.asm"

                assert  dzx0_turbo == ZX0_DECODE_ENTRY
                assert  $ <= ZX0_WORKSPACE

if defined(BUILD_TESTS)

; ── Boot self-test driver + baked fixture (zx0-test.bin only) ─────────
;
; Pad across the workspace region (&8B80-&AF9D — RAM scratch the
; compressor owns at run time; the loaded fill bytes are never read)
; to the spare-region entry address.
                defs    ZX0_SELF_TEST_ENTRY - $

; -----------------------------------------------------------------------
; run_zx0_self_test — paged_call target (HMPR=13: section C = this page,
; section D = page 14 = staging).  Implements the design's §7.1 exact
; tests, both directions baked from the Go authority (tools/zx0-greedy
; cmd/zx0fixture emits build/zx0_selftest_fixture.inc at build time):
;
;   (a) stage the raw fixture block into ZX0_STAGING_SRC (page 14),
;       run the page-13 compressor over it into ZX0_STAGING_DST, and
;       byte-compare length + bytes against the baked compressed form.
;       Greedy is deterministic and the Z80 port is byte-identical to
;       Go, so any drift in either direction is a hard FAIL.
;   (b) run the turbo decoder over the BAKED compressed bytes (not the
;       bytes (a) just produced — (b) must not depend on (a)) into a
;       poisoned staging buffer and byte-compare against the raw block.
;
; Both staging buffers are poisoned first so a do-nothing routine cannot
; pass vacuously.
;
; In:  nothing (paged_call shape: call paged_call / defw
;      ZX0_SELF_TEST_ENTRY / defb ZX0_PAGE).  Interrupts must be
;      disabled (boot invariant; the compressor keeps its bit-writer
;      state in the shadow registers).
; Out: BC = 0 on success; else B=0, C = fail tag (&E1 compressed-length
;      mismatch, &E2 compressed-bytes mismatch, &E3 decoded-bytes
;      mismatch) — the disasm boot self-test convention.
; Clobbers: everything except SP (the compressor clobbers IX/IY and the
;      full shadow set).
;
; Stack: runs on the paged_call safe stack (SP descending from
; TRAMP_SAFE_SP = &7F00 in section B; entry SP &7EFE).  Deepest use is
; the compressor call: driver frame (2 B) + zx0_compress's own worst
; case — measured 6 B across all 24 corpus blocks by the harness's
; TestZX0CompressStackDepth, which asserts a 32 B ceiling against the
; 41 B hard budget above PAGED_CALL_SP_SAVE's end (&7ED2).
; -----------------------------------------------------------------------
run_zx0_self_test:
; -- (a) compressor vs baked compressed bytes ---------------------------
; Stage the raw block: page 13 (section C) → page 14 (section D).
                ld      hl, zx0t_raw
                ld      de, ZX0_STAGING_SRC
                ld      bc, ZX0T_RAW_LEN
                ldir
; Poison the compressed-output staging region (&AA ripple fill, compare
; length + slack) so stale RAM can't satisfy the compare.
                ld      hl, ZX0_STAGING_DST
                ld      (hl), &AA
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, ZX0T_COMP_LEN + 15
                ldir
; Compress the staged block (entry contract: src/zx0_compress.asm).
                ld      hl, ZX0_STAGING_SRC
                ld      de, ZX0T_RAW_LEN
                ld      bc, ZX0_STAGING_DST
                ld      ix, ZX0_WORKSPACE
                call    ZX0_COMPRESS_ENTRY
; HL = compressed length; exact-match the baked length first (cheap, and
; a length drift makes the byte compare meaningless).
                ld      de, ZX0T_COMP_LEN
                or      a
                sbc     hl, de
                ld      a, h
                or      l
                jp      nz, zx0t_fail_clen
; Byte-compare the produced stream against the Go authority's bytes.
                ld      hl, ZX0_STAGING_DST
                ld      de, zx0t_comp
                ld      bc, ZX0T_COMP_LEN
                call    zx0t_memcmp
                jp      nz, zx0t_fail_cbytes

; -- (b) decoder vs baked raw block -------------------------------------
; Poison the decode destination (&55 ripple fill over the raw length).
                ld      hl, ZX0_STAGING_SRC
                ld      (hl), &55
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, ZX0T_RAW_LEN - 1
                ldir
; Decode the BAKED compressed bytes straight off this page (HL = page-13
; source is fine: section C stays mapped throughout).
                ld      hl, zx0t_comp
                ld      de, ZX0_STAGING_SRC
                call    ZX0_DECODE_ENTRY
; Byte-compare the decoded output against the raw original.
                ld      hl, ZX0_STAGING_SRC
                ld      de, zx0t_raw
                ld      bc, ZX0T_RAW_LEN
                call    zx0t_memcmp
                jp      nz, zx0t_fail_dbytes
                ld      bc, 0
                ret

zx0t_fail_clen:
                ld      bc, &00E1
                ret
zx0t_fail_cbytes:
                ld      bc, &00E2
                ret
zx0t_fail_dbytes:
                ld      bc, &00E3
                ret

; -----------------------------------------------------------------------
; zx0t_memcmp — compare BC bytes at (HL) vs (DE).
; Out: Z set if all bytes equal; NZ on first mismatch.
; Clobbers: A, BC, DE, HL, F.
; -----------------------------------------------------------------------
zx0t_memcmp:
                ld      a, (de)
                cp      (hl)
                ret     nz
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, zx0t_memcmp
                ret

; Baked fixture — generated at build time by the Go authority
; (tools/zx0-greedy/cmd/zx0fixture; see Makefile zx0-test-payload).
; Defines ZX0T_RAW_LEN / ZX0T_COMP_LEN and the zx0t_raw / zx0t_comp
; byte blocks.
                include "../build/zx0_selftest_fixture.inc"

; The whole test payload must stay inside page 13's section-C window.
                assert  $ <= &C000

endif
