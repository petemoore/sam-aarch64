; secd_probe.asm — section-D loadability probe (a SimCoupe regression).
;
; Settles, empirically and faithfully, the question that gates how big a bootable
; netboot program may be: when a SAM disk's AUTO BASIC does
;   CLEAR &7FFF : LOAD "probe" CODE 32768 : CALL 32768
; and the loaded file is LARGER than 16 KB (so its bytes span section C, &8000-&BFFF,
; into section D, &C000-&FFFF), are the bytes above &BFFF actually deposited into
; section-D RAM and visible to the running program?
;
; The answer is YES (proven by this probe): section D is RAM at boot — the boot LMPR
; is &1F (bit 6 / ROM1 clear), so section D = HMPR+1 RAM, and SAMDOS/B-DOS LOAD CODE
; writes the >&BFFF bytes straight into it. So a bootable netboot program may use the
; whole &8000-&FFFF (32768-byte) window as RAM with NO self-paging — the "section-D
; overlay" pattern (http_main, and the i145b serve/client SD CSD read). This
; corrects the earlier (untested) assumption that "section D is ROM1 at boot, so
; code/data above &BFFF is never loaded into RAM" (see docs/notes/sam-paging.md).
;
; HOW: distinct sentinel bytes are baked into the FILE at three section-D addresses
; (&C000 — the section boundary, &C1F0, &C500). At run time the code (in section C)
; reads them back; if all three match, the load reached section-D RAM and the run
; sees it as RAM. The result is reported "OK"/"FAIL" over printer channel 1, which
; run-simcoupe.sh captures (the same OK/FAIL banner the assembler self-tests use).
;
; Build + run: `make secd-loadability` (builds the >16 KB disk and runs it in
; SimCoupe, asserting "^OK$").

PRINT_DATA_PORT:  equ &E8
PRINT_STAT_PORT:  equ &E9

SENTINEL_C000:    equ &11
SENTINEL_C1F0:    equ &22
SENTINEL_C500:    equ &33

                  org &8000
                  jp entry

entry:
                  di
                  ld   hl, &C000          ; the section C/D boundary byte
                  ld   a, (hl)
                  cp   SENTINEL_C000
                  jr   nz, fail
                  ld   hl, &C1F0          ; just past the i145b overlay extent
                  ld   a, (hl)
                  cp   SENTINEL_C1F0
                  jr   nz, fail
                  ld   hl, &C500          ; well into section D
                  ld   a, (hl)
                  cp   SENTINEL_C500
                  jr   nz, fail
ok:
                  ld   hl, msg_ok
                  call print_status_string
                  jr   done
fail:
                  ld   hl, msg_fail
                  call print_status_string
done:
                  di
                  halt

; print_status_char / print_status_string — the OUT (&E8)+strobe printer-channel
; banner emit (mirrors src/print.asm, kept self-contained so this probe needs no
; includes).
print_status_char:
                  push af
                  out  (PRINT_DATA_PORT), a
                  ld   a, 1
                  out  (PRINT_STAT_PORT), a
                  xor  a
                  out  (PRINT_STAT_PORT), a
                  pop  af
                  ret
print_status_string:
                  ld   a, (hl)
                  or   a
                  ret  z
                  call print_status_char
                  inc  hl
                  jr   print_status_string

msg_ok:           defm "OK"
                  defb 10, 0
msg_fail:         defm "FAIL"
                  defb 10, 0

; Plant the sentinels at their section-D file offsets. defs zero-fills the gaps, so
; the assembled image runs &8000..&C500+ (> 16 KB), forcing the load to cross &C000.
                  defs &C000 - $
                  defb SENTINEL_C000
                  defs &C1F0 - $
                  defb SENTINEL_C1F0
                  defs &C500 - $
                  defb SENTINEL_C500
                  defs 16                 ; a small tail past the last sentinel
