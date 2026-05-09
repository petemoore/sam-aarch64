; M0 stub assembler — halt-only version.
; Loads at the standard SAM external code address and halts.
; Task 7 will expand this to read input and write output.
;
; Note: pyz80 does not support the END directive (no op_END function).
; Assembly ends at end-of-file; the entry point is implicitly the first byte.

                org     &8000          ; SAM external memory page boundary

start:          di                     ; disable interrupts
                halt                   ; CPU halts; SimCoupé exits via batch flag
