; zx0_comm.inc — page-13 ZX0 payload address constants.
;
; Included by src/trampoline.asm AND src/zx0_payload.asm (standalone).
; Single source of truth, mirroring src/disasm_comm.inc: the payload is
; assembled separately and cannot see the main binary's symbols, so both
; sides name the same addresses through this file.  The payload pins its
; layout to these values with `assert` directives.
;
; The addresses are the comment-storage design's §5 placement
; (docs/specs/comment-storage-design.md) — everything co-resident on
; page 13 alongside sysreg_data (&8000-&83B3), reached via paged_call
; (HMPR=13 maps page 13 at section C and page 14 at section D):
;
;   &8000-&83B3  sysreg_data.bin (existing occupant, 948 B)
;   &8400-&8AFF  zx0 compressor (1,792 B; 256 B-aligned — the payload
;                carries assemble-time page-aligned hash tables)
;   &8B00-&8B7D  zx0 turbo decoder (126 B)
;   &8B80-&AF9D  compressor scratch workspace (9,246 B at 4 KB blocks =
;                HASH_SIZE*2 + blkLen*2 + 30; RAM scratch, not loaded)
;   &AF9E-&BFFF  spare (sysreg growth etc.); the BUILD_TESTS self-test
;                driver + baked fixture live here from &AFA0 (test
;                payload only)
;   &C000-&E0FF  staging on page 14 (section D under HMPR=13):
;                src 4 KB + dst 4 KB + 256 B headroom

; Physical page holding the zx0 payload.  Same page as SYSREG_DATA_PAGE
; (trampoline.asm) — the two payloads co-reside at disjoint offsets.
ZX0_PAGE:               equ     13

; HLOAD destination for the zx0 payload (section-C window address; the
; payload file's first byte lands here).  Unlike the other payloads
; (which all load at &8000), the zx0 payload loads at &8400 so it sits
; beside sysreg_data rather than over it — load_zx0_payload passes this
; through load_payload_generic_at (loader.asm).  &8400 is 256 B-aligned,
; preserving the alignment the compressor's hash tables assemble with.
ZX0_LOAD_ADDR:          equ     &8400

; Entry points (section-C view under HMPR=13).  No jump table: the
; compressor's entry is its first byte and the decoder is pinned at
; &8B00, so the addresses themselves are the contract (asserted in
; src/zx0_payload.asm).
;
; zx0_compress (Go authority: tools/zx0-greedy Compress, H=512 D=16):
;   In:  HL=src, DE=src_len (<= 8192), BC=dst, IX=workspace
;   Out: HL=compressed length
;   Requires DI; self-modifying (RAM-resident); non-reentrant; clobbers
;   IX/IY and the full shadow set.  See src/zx0_compress.asm header.
ZX0_COMPRESS_ENTRY:     equ     &8400

; dzx0_turbo (upstream ZX0 turbo decoder):
;   In:  HL=compressed src, DE=dst.  Self-modifying; non-reentrant.
ZX0_DECODE_ENTRY:       equ     &8B00

; Compressor scratch workspace (page-13 RAM, never loaded from disk).
; Sized for the design operating point: 4 KB blocks at H=512 need
; 9,246 B (&8B80 + 9,246 = &AF9E, the start of the spare region).
ZX0_WORKSPACE:          equ     &8B80

; BUILD_TESTS boot self-test entry (zx0-test.bin only; the prod
; zx0.bin ends at the decoder).  Lives in the §5 spare region, above
; the worst-case 4 KB-block workspace ceiling (&AF9D).  paged_call
; target; returns BC=0 on success, else C = fail tag (the disasm
; self-test convention).
ZX0_SELF_TEST_ENTRY:    equ     &AFA0

; Staging area on page 14 — section D while HMPR=13, so the page-13
; code reaches it with plain absolute addressing, no extra paging.
; The caller copies a raw block in through ZX0_STAGING_SRC and the
; compressed result out of ZX0_STAGING_DST (design §5: this ½ page is
; the design's only new RAM cost).  Page 14's other boot-time use, the
; 3-byte paged_call self-test stub at offset 0 ("p14", BUILD_TESTS
; only), is fully consumed by run_paged_call_self_tests before the zx0
; self-test stages anything over it — same time-multiplex discipline
; as test_mem/sysreg_data on page 13.
ZX0_STAGING_SRC:        equ     &C000   ; 4 KB raw block in
ZX0_STAGING_DST:        equ     &D000   ; 4 KB + 256 B headroom out
