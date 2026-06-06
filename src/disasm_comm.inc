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
; Total: 38 bytes, ending at &7EBC — 20 bytes before PAGED_CALL_HMPR_SAVE.
DISASM_COMM_MNEM:        equ     &7E99
DISASM_COMM_OPS:         equ     &7EA3

; Entry point for the disasm self-test stub that lives at the tail of
; disasm.bin (page 15).  Placed at &8100 (256 bytes into the page) to
; give the main disasm_entry / helpers room below; enforced by an
; `org DISASM_SELF_TEST_ENTRY` in disasm.asm.
DISASM_SELF_TEST_ENTRY:  equ     &8100
