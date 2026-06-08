; sysreg_data.asm — page-13 sysname lookup tables + self-contained matcher.
;
; Per docs/plans/2026-05-29-m6-closure-release-bytematch.md PR-2, AS
; CORRECTED by the PR-2 implementation spec (the plan's "leaf-only,
; lift verbatim" assumption was wrong — see the PR body / the sysname.asm
; header for the split-design rationale).
;
; This file is assembled STANDALONE (org &8000) into build/sysreg_data.bin
; and HLOAD'd into physical page 13 at boot by
; src/loader.asm::load_page13_payload.  It is a PRODUCTION feature
; (sysreg lookups are needed by every build, not just BUILD_TESTS), so
; the loader call and the disk deposit happen in both variants.
;
; ---------------------------------------------------------------------
; Why this lives off-axis on page 13
; ---------------------------------------------------------------------
; The four sysname tables (sysreg / pstate / dc / tlbi) plus the generic
; table-walker `sysname_match` used to live in section C/D inside
; sysname.asm.  Moving them to page 13 frees ~0.5 KB of the precious
; section-C code budget (docs/notes/sam-paging.md:61-91 — section C =
; HMPR low5, section D = HMPR+1, both invariant only under a fixed HMPR).
;
; The split is forced by the paging model.  A routine running under
; HMPR=13 (reached via `paged_call`, see src/paged_bodies.asm) sees:
;   - section A/B = LMPR / LMPR+1  (invariant across HMPR — VISIBLE)
;   - section C   = page 13        (THIS code + tables)
;   - section D   = page 14        (NOT the caller's section D)
; so it CANNOT read OPVAL_ARRAY (&C100, section D), the sysname scratch
; (section D), or IN_BUF (section C/D).  The matcher is therefore made
; fully self-contained: it reads the operand name from a comm buffer in
; LMPR-stable section B (SYSREG_COMM_NAME / SYSREG_COMM_LEN, addresses
; defined in trampoline.asm), walks the selected table (a &800x address
; on THIS page), and writes the matched field bytes back to
; SYSREG_COMM_RESULT (also section B).  It NEVER touches section C/D
; data and NEVER `jp fail`s — on a miss it returns A=0 and lets the
; main-side caller (sysname.asm) run the generic-form fallback / fail.
;
; ---------------------------------------------------------------------
; Comm-buffer ABI (the contract with sysname.asm + trampoline.asm)
; ---------------------------------------------------------------------
; Input (set by the main side before paged_call):
;   (SYSREG_COMM_NAME)  — up to 16 name bytes, raw (NOT lowercased; the
;                         matcher lowercases each byte on compare).
;   (SYSREG_COMM_LEN)   — name length, u8.
;   (fields-per-entry is NOT passed by the caller — each jump-table entry
;    point sets it internally in A, see "Each entry sets ..." below.  It
;    corresponds to sysname_match's old `sysname_field_count`.)
; Output:
;   A = 1 and SYSREG_COMM_RESULT[0..fields-1] = matched field bytes, on hit.
;   A = 0 on miss (no fields written; main side handles the miss).
;
; The table base is selected by the paged_call entry point (one of four
; jump-table slots at the head of the page); each entry loads HL with
; its table base and falls into the common matcher.
;
; Field counts per table (must match sysname.asm's per-lookup setup):
;   sysreg_table  — 5 bytes: op0, op1, CRn, CRm, op2
;   pstate_table  — 2 bytes: op1, op2
;   dc_table      — 4 bytes: op1, CRn, CRm, op2
;   tlbi_table    — 5 bytes: op1, CRn, CRm, op2, NeedsXt
; ---------------------------------------------------------------------

; ---------------------------------------------------------------------
; Comm-buffer addresses — MUST match the equ definitions in
; src/trampoline.asm exactly (this file is assembled standalone, so
; it carries its own copies; trampoline.asm is the canonical source and
; the two are kept in lock-step by the shared-ABI comment there).
; Section B (LMPR-stable), in the verified-free region &7E72..&7ECF
; between the paged_call body end (&7E71) and PAGED_CALL_HMPR_SAVE
; (&7ED0).  See trampoline.asm for the safety derivation.
; ---------------------------------------------------------------------
SYSREG_COMM_NAME:       equ     &7E80   ; 16 bytes (longest name 13)
SYSREG_COMM_LEN:        equ     &7E90   ; 1 byte
SYSREG_COMM_RESULT:     equ     &7E91   ; up to 8 field bytes (ends &7E98)

                org     &8000

; -----------------------------------------------------------------------
; Jump table — paged_call targets, one per sysname table.  Fixed offsets
; so trampoline.asm can name them as SYSREG_*_ENTRY equ &8000/3/6/9.
; Each entry sets HL = its table base, A = fields-per-entry, then jumps
; to the common matcher.
; -----------------------------------------------------------------------
; Each slot is 8 bytes (ld a,n / ld hl,nn / jp), so they land at
; &8000 / &8008 / &8010 / &8018 — matching SYSREG_*_ENTRY in trampoline.asm.
sysreg_entry:                                   ; &8000
                ld      a, 5
                ld      hl, sysreg_table
                jp      do_match
sysreg_pstate_entry:                            ; &8008
                ld      a, 2
                ld      hl, pstate_table
                jp      do_match
sysreg_dc_entry:                                ; &8010
                ld      a, 4
                ld      hl, dc_table
                jp      do_match
sysreg_tlbi_entry:                              ; &8018
                ld      a, 5
                ld      hl, tlbi_table
                jp      do_match


; -----------------------------------------------------------------------
; do_match — self-contained case-insensitive table walker.
;
; Port of sysname.asm's sysname_match, rewritten to read the source name
; from the section-B comm buffer (SYSREG_COMM_NAME / SYSREG_COMM_LEN) and
; write matched fields to SYSREG_COMM_RESULT, so it runs correctly under
; HMPR=13 where section C/D are paged away.
;
; fields-per-entry is parked in the page-local scratch byte match_nfields
; (this page is section C under HMPR=13 — writable) so it survives the
; register churn of the name-compare / skip arithmetic without
; stack-juggling.  HL walks the table; BC is used freely per entry.
;
; Input:  A = fields-per-entry; HL = table base.
; Output: A = 1 + SYSREG_COMM_RESULT filled on hit; A = 0 on miss.
; Clobbers: A, BC, DE, HL (all caller-clobbered by paged_call anyway).
;
; Stack depth is shallow (only the leaf sysreg_tolower's one return
; slot) — bounded well within the TRAMP_SAFE_SP headroom (see
; trampoline.asm comm-buffer note).
; -----------------------------------------------------------------------
do_match:
                ld      (match_nfields), a  ; park fields-per-entry
sysreg_match_entry:
                ld      a, (hl)             ; entry name_len
                or      a
                jp      z, sysreg_match_miss   ; terminator → miss
                ld      c, a                ; C = entry name_len
                ld      b, 0                ; BC = entry name_len (for 16-bit add)
                inc     hl                  ; HL → entry name bytes
; Compare lengths first.
                ld      a, (SYSREG_COMM_LEN)
                cp      c
                jp      nz, sysreg_skip_entry
; Lengths match.  Compare bytes case-insensitively against comm name.
                ld      de, SYSREG_COMM_NAME
                ld      b, c                ; B = bytes to compare (djnz counter)
sysreg_match_loop:
                ld      a, (de)
                call    sysreg_tolower
                ld      c, a
                ld      a, (hl)             ; entry byte (already lower)
                cp      c
                jp      nz, sysreg_skip_entry_inloop
                inc     hl
                inc     de
                djnz    sysreg_match_loop
; Match!  HL → first field byte.  Copy match_nfields bytes to result.
                ld      a, (match_nfields)
                ld      b, a                ; B = field count (djnz)
                ld      de, SYSREG_COMM_RESULT
sysreg_copy_fields:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    sysreg_copy_fields
                ld      a, 1                ; A = 1 (hit)
                ret

sysreg_skip_entry_inloop:
; HL points partway through the entry name; B = name bytes remaining.
; Advance HL past those remaining name bytes, then fall into the
; field-skip.
                ld      c, b                ; BC = bytes remaining
                ld      b, 0
                add     hl, bc              ; HL → first field
                jp      sysreg_skip_fields

sysreg_skip_entry:
; HL → first byte of entry name; BC = entry name_len (B=0 already).
                add     hl, bc              ; HL → first field
sysreg_skip_fields:
; HL → first field byte.  Skip match_nfields bytes to reach next entry.
                ld      a, (match_nfields)
                ld      c, a
                ld      b, 0
                add     hl, bc              ; HL → next entry
                jp      sysreg_match_entry

sysreg_match_miss:
                xor     a                   ; A = 0 (miss)
                ret

; Page-local scratch (section C under HMPR=13 — writable).  Holds the
; fields-per-entry count for the duration of one do_match call.  Not
; re-entrant, which is fine: paged_call itself is not re-entrant.
match_nfields:  defb    0


; -----------------------------------------------------------------------
; sysreg_tolower — A: ASCII upper→lower in place.  'A'..'Z' → +0x20.
; Private copy of sysname.asm:to_lower_ascii (that routine lives in
; section C and is invisible here).
; -----------------------------------------------------------------------
sysreg_tolower:
                cp      &41                 ; 'A'
                ret     c
                cp      &5b                 ; 'Z' + 1
                ret     nc
                add     a, 32
                ret


; =======================================================================
; Data tables — the four System-group name↔encoding tables (sysreg /
; pstate / dc / tlbi) now live in the shared src/sysreg_tables.inc, which
; this page (name→encoding) and the page-15 disassembler (encoding→name,
; via src/sysreg_names.inc) both `include` so the bytes cannot drift.  See
; that file's header for the layout, field order, and the Go-authority
; sync guard.  The matcher above (do_match) walks these tables; the
; jump-table entries select a base (sysreg_table / pstate_table /
; dc_table / tlbi_table) defined by the include.
; =======================================================================

                include "sysreg_tables.inc"
