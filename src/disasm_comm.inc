; disasm_comm.inc — disassembler comm-buffer constants.
;
; Included by src/trampoline.asm AND src/disasm.asm (standalone).
; Single source of truth — eliminates the manual "keep in lock-step"
; synchronisation between the two files.
;
; TRAMPOLINE_DST = &7E00 (trampoline.asm).  All values are absolute
; so this file can be included in disasm.asm, which has no access to
; trampoline.asm's symbols.

; Comm buffer layout (LMPR-stable section B, directly after
; SYSREG_COMM_RESULT at &7E99):
;   &7E99..&7EA2  DISASM_COMM_MNEM  12 bytes — mnemonic, null-terminated
;   &7EA3..&7EBC  DISASM_COMM_OPS   26 bytes — operands, null-terminated
;   &7EBD..&7EC4  DISASM_COMM_PC     8 bytes — instruction PC, little-endian
; Total: 46 bytes, ending at &7EC4 — 11 bytes before PAGED_CALL_HMPR_SAVE.
DISASM_COMM_MNEM:        equ     &7E99
DISASM_COMM_OPS:         equ     &7EA3

; DISASM_COMM_PC — the program-counter (byte address) of the instruction
; being decoded, as an 8-byte little-endian value.  The caller writes it
; before paged_call; disasm_entry reads it for the PC-relative families
; (b/bl/b.cc/cbz/cbnz/tbz/tbnz/adr/adrp/ldr-literal), mirroring the Go
; oracle's DecodeAt(pc, word).  Mnemonics with no PC-relative target
; (everything else) ignore it.
;
; Placement: it occupies the 8-byte gap &7EBD..&7EC4, immediately after
; DISASM_COMM_OPS (&7EA3..&7EBC) and below PAGED_CALL_HMPR_SAVE (&7ED0).
; This is part of the verified-free region &7E99..&7ECF documented in
; src/trampoline.asm.  The paged_call stack (descending from
; TRAMP_SAFE_SP=&7F00; deepest write &7EFC) never reaches it, and it does
; not overlap PAGED_CALL_HMPR_SAVE/SP_SAVE (&7ED0..&7ED2).
DISASM_COMM_PC:          equ     &7EBD

; Entry point for the disasm boot self-test.  disasm.asm opens the page
; with a two-slot jump table so both the decoder entry (&8000) and the
; self-test entry have FIXED addresses that src/assembler.asm can name at
; its own (separate) build time, while the decoder body and the self-test
; code grow freely after the table with no padding:
;   &8000  jp disasm_dispatch        (DISASM_ENTRY, in src/trampoline.asm)
;   &8003  jp run_disasm_self_test   (DISASM_SELF_TEST_ENTRY, here)
DISASM_SELF_TEST_ENTRY:  equ     &8003
