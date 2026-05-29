; SAMDOS hook wrappers — originally shared with M0's src/stub.asm
; (the stub was retired in M7; see git history).
; Provides: fill_uifa, open_input, read_byte, close_input.
; Hook codes: HGFLE (158), LBYT (159), HSAVE (132).
;
; Per docs/notes/sam-stub-audit.md and src/sam_io.inc.
; Include this from the top-level assembler.asm, not directly.

        include "sam_io.inc"
