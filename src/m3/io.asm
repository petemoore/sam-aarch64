; SAMDOS hook wrappers — shared with M0's src/stub.asm.
; Provides: fill_uifa, open_input, read_byte, close_input.
; Hook codes: HGFLE (158), LBYT (159), HSAVE (132).
;
; Per docs/notes/sam-stub-audit.md and src/sam_io.inc.
; Include this from the top-level assembler.asm, not directly.

        include "../sam_io.inc"
