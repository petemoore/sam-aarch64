; bdos_picker.asm — i119d (B4): record-selection UX for the Trinity SD card.
;
; Provides bdos_pick_record, the user-facing record picker that implements the
; three selection modes from docs/specs/trinity-record-detection-design.md §6:
;
;   0 = auto-pick-free      find the first unnamed record (B3's find_free)
;   1 = manual-by-number    user types a record number + CR
;   2 = manual-by-name      user types a record name + CR; first match wins
;   3 = list-then-number    enumerate all records to display, then pick by number
;
; In every mode the SAFETY GATE holds (design §1, §6): the chosen record's name
; (or "free / unnamed") is shown, then the user must type 'y'/'Y' to confirm.
; BD_PICK_CONFIRMED is set to 1 ONLY after pick_show_name AND pick_read_yesno.
; There is NO path to CONFIRMED=1 that bypasses either step.
;
; HONESTY LINE (CLAUDE.md §5): all selection / show-name / confirm DECISION
; logic is host-verified — pure arithmetic + memory reads + keyboard sysvar
; polls, no RST 8 dispatch. picker_render (the raw display blit) carries no
; decision logic and is hardware-gated (no screen model in the harness, exactly
; like the SD SPI ladder); it is a stub ret so host tests assert PICK_DISPLAY
; content directly. See design §5/§6 and CLAUDE.md rule 7.
;
; Primitives reused from co-included files:
;   bdos_find_free_record  (bdos_seam.asm) — find first unnamed record
;   bdos_record_entry      (bdos_seam.asm) — fetch a 16-byte list entry
;   key_read_loop          (key_read_test.asm) — poll KB_FLAGS/LASTK until CR
;   key_poll_once          (key_read_test.asm) — single key poll
;   atoi_dec               (netboot_client.asm) — decimal string -> DE
; Data symbols reused:
;   BD_RECORDS, BD_FREE_RECORD, BD_ENTRY_REC, BD_ENTRY_BUF (bdos_seam.asm)
;   KR_BUF, KR_COUNT (key_read_test.asm)
;
; PROVENANCE: design §5/§6 (docs/specs/trinity-record-detection-design.md).

; --- picker modes -----------------------------------------------------------
BD_PICK_MODE_AUTO:    equ 0      ; auto-pick: first free record
BD_PICK_MODE_NUM:     equ 1      ; manual by number
BD_PICK_MODE_NAME:    equ 2      ; manual by name
BD_PICK_MODE_LIST:    equ 3      ; list all, then pick by number

; --- display buffer size ----------------------------------------------------
BD_PICK_DISPLAY_MAX:  equ 256    ; PICK_DISPLAY capacity (NUL-padded)

; ---------------------------------------------------------------------------
; bdos_pick_record — select a Trinity SD record with mandatory show-name +
; confirm gate (design §6).
;
; In:  BD_PICK_MODE   1 byte   0/1/2/3 (see above)
;      BD_RECORDS     2 bytes  total record count (reused from bdos_seam.asm)
; Out: BD_PICK_RECORD   2 bytes  chosen record n (1-based), or 0 = none
;      BD_PICK_CONFIRMED 1 byte  1 = user confirmed; 0 = declined/cancelled
;      PICK_DISPLAY   256 bytes  text shown (NUL-padded); host tests assert this
;
; Safety invariant: BD_PICK_CONFIRMED = 1 ONLY via pick_show_name then
; pick_read_yesno. No path to CONFIRMED=1 skips either step.
;
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
bdos_pick_record:
                ; init outputs
                xor     a
                ld      (BD_PICK_RECORD), a
                ld      (BD_PICK_RECORD + 1), a
                ld      (BD_PICK_CONFIRMED), a

                ; zero PICK_DISPLAY (NUL-pad the whole buffer)
                ld      hl, PICK_DISPLAY
                ld      de, PICK_DISPLAY + 1
                ld      (hl), 0
                ld      bc, BD_PICK_DISPLAY_MAX - 1
                ldir

                ; dispatch on BD_PICK_MODE
                ld      a, (BD_PICK_MODE)
                cp      BD_PICK_MODE_NUM
                jr      z, bpr_by_number
                cp      BD_PICK_MODE_NAME
                jr      z, bpr_by_name
                cp      BD_PICK_MODE_LIST
                jr      z, bpr_list
                ; mode 0 (auto) and any other value
                call    pick_auto_free
                jr      bpr_gate
bpr_by_number:
                call    pick_by_number
                jr      bpr_gate
bpr_by_name:
                call    pick_by_name
                jr      bpr_gate
bpr_list:
                call    pick_list

bpr_gate:
                ; if BD_PICK_RECORD == 0: cancelled (CONFIRMED stays 0), render and return
                ld      hl, (BD_PICK_RECORD)
                ld      a, h
                or      l
                jr      z, bpr_cancelled

                ; SAFETY GATE (design §1, §6):
                ;   1. pick_show_name writes the chosen record's name into PICK_DISPLAY
                ;   2. pick_read_yesno requires 'y'/'Y' from the user
                ; BD_PICK_CONFIRMED = 1 is ONLY set after BOTH calls below complete.
                call    pick_show_name
                call    picker_render
                call    pick_append_confirm    ; append "\nOverwrite? y/n" to PICK_DISPLAY
                call    picker_render
                call    pick_read_yesno        ; Z = 'y'/'Y'; NZ = declined
                jr      nz, bpr_declined

                ld      a, 1
                ld      (BD_PICK_CONFIRMED), a ; only reachable after show_name + read_yesno
                ret

bpr_declined:
                ; user did not confirm — CONFIRMED stays 0
                ret

bpr_cancelled:
                ; no record chosen — render "cancelled"
                ld      hl, PICK_DISPLAY
                call    pick_emit_lit
                defm    "cancelled"
                defb    0
                call    picker_render
                ret

; ---------------------------------------------------------------------------
; pick_auto_free — mode 0: call B3's bdos_find_free_record and copy its result
; (the first free record number, or 0) to BD_PICK_RECORD.
; ---------------------------------------------------------------------------
pick_auto_free:
                call    bdos_find_free_record
                ld      hl, (BD_FREE_RECORD)
                ld      (BD_PICK_RECORD), hl
                ret

; ---------------------------------------------------------------------------
; pick_by_number — mode 1: read a decimal number + CR via key_read_loop,
; parse with atoi_dec, range-check 1 <= n <= BD_RECORDS. Stores in
; BD_PICK_RECORD; sets 0 on empty input or out-of-range.
; ---------------------------------------------------------------------------
pick_by_number:
                call    key_read_loop

                ; empty input? (KR_COUNT == 0)
                ld      hl, (KR_COUNT)
                ld      a, h
                or      l
                jr      z, pbn_cancel

                ; parse decimal from KR_BUF -> DE
                ld      hl, KR_BUF
                call    atoi_dec               ; DE = parsed value

                ; check DE >= 1
                ld      a, d
                or      e
                jr      z, pbn_cancel          ; DE == 0: out of range

                ; check DE <= BD_RECORDS (BD_RECORDS - DE; borrow => DE > BD_RECORDS)
                push    de
                ld      hl, (BD_RECORDS)
                or      a
                sbc     hl, de
                pop     de
                jr      c, pbn_cancel

                ld      (BD_PICK_RECORD), de
                ret

pbn_cancel:
                xor     a
                ld      (BD_PICK_RECORD), a
                ld      (BD_PICK_RECORD + 1), a
                ret

; ---------------------------------------------------------------------------
; pick_by_name — mode 2: read a name string + CR via key_read_loop, then scan
; records 1..BD_RECORDS. For each, fetch its 16-byte list entry and compare
; KR_COUNT bytes (entry bytes masked 0x7F, case-sensitive) against KR_BUF.
; First full match -> BD_PICK_RECORD = n. Empty input or no match -> 0.
; ---------------------------------------------------------------------------
pick_by_name:
                call    key_read_loop

                ; empty input -> cancel
                ld      hl, (KR_COUNT)
                ld      a, h
                or      l
                jr      z, pbn2_cancel

                ; scan n = 1 .. BD_RECORDS (n held in DE)
                ld      de, 1
pbn2_loop:
                ld      hl, (BD_RECORDS)
                or      a
                sbc     hl, de                 ; BD_RECORDS - n; borrow => n > BD_RECORDS
                jr      c, pbn2_notfound

                ld      (BD_ENTRY_REC), de
                push    de
                call    bdos_record_entry
                pop     de

                ; compare KR_COUNT bytes: KR_BUF vs BD_ENTRY_BUF (masked)
                push    de
                ld      hl, KR_BUF
                ld      de, BD_ENTRY_BUF
                ld      bc, (KR_COUNT)
pbn2_cmp:
                ld      a, (de)
                and     &7F                    ; mask write-protect bit (design §4.3)
                cp      (hl)
                jr      nz, pbn2_mismatch
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, pbn2_cmp
                ; all bytes matched
                pop     de                     ; restore n
                ld      (BD_PICK_RECORD), de
                ret

pbn2_mismatch:
                pop     de                     ; restore n
                inc     de
                jr      pbn2_loop

pbn2_notfound:
pbn2_cancel:
                xor     a
                ld      (BD_PICK_RECORD), a
                ld      (BD_PICK_RECORD + 1), a
                ret

; ---------------------------------------------------------------------------
; pick_list — mode 3: build a listing of all records into PICK_DISPLAY
; ("N: <name>\n" per record), render, then call pick_by_number.
; ---------------------------------------------------------------------------
pick_list:
                ; init write cursor to PICK_DISPLAY
                ld      hl, PICK_DISPLAY
                ld      (PDC), hl

                ld      de, 1                  ; DE = current record n
pl_loop:
                ld      hl, (BD_RECORDS)
                or      a
                sbc     hl, de                 ; borrow => n > BD_RECORDS
                jr      c, pl_done

                ; emit decimal n at cursor (pnum_dest <- PDC, then call pick_emit_num16)
                push    de
                ld      hl, (PDC)
                ld      (pnum_dest), hl
                pop     de
                push    de
                call    pick_emit_num16        ; DE as decimal; pnum_dest advanced
                ld      hl, (pnum_dest)        ; reload cursor

                ; emit ": "
                ld      (hl), &3A              ; ':'
                inc     hl
                ld      (hl), &20              ; ' '
                inc     hl

                ; fetch list entry for record n
                pop     de
                ld      (BD_ENTRY_REC), de
                push    de
                push    hl
                call    bdos_record_entry
                pop     hl                     ; restore cursor
                pop     de                     ; restore n

                ; emit name (masked+trimmed) or "free" at HL; returns HL advanced
                call    pick_emit_entry_name

                ld      (hl), &0A              ; newline
                inc     hl
                ld      (PDC), hl

                inc     de
                jr      pl_loop

pl_done:
                ; NUL-terminate
                ld      hl, (PDC)
                ld      (hl), 0
                call    picker_render

                ; read a record number
                call    pick_by_number
                ret

; ---------------------------------------------------------------------------
; pick_show_name — load-bearing safety step (design §1, §6): format the chosen
; record's name into PICK_DISPLAY as "Record N: <name>" or
; "Record N: free / unnamed". Pre: BD_PICK_RECORD >= 1.
; ---------------------------------------------------------------------------
pick_show_name:
                ; zero PICK_DISPLAY
                ld      hl, PICK_DISPLAY
                ld      de, PICK_DISPLAY + 1
                ld      (hl), 0
                ld      bc, BD_PICK_DISPLAY_MAX - 1
                ldir

                ; fetch the list entry for the chosen record
                ld      hl, (BD_PICK_RECORD)
                ld      (BD_ENTRY_REC), hl
                call    bdos_record_entry

                ; emit "Record " into PICK_DISPLAY
                ld      hl, PICK_DISPLAY
                call    pick_emit_lit
                defm    "Record "
                defb    0
                ; HL now points past "Record " in PICK_DISPLAY

                ; emit the record number in decimal
                ld      de, (BD_PICK_RECORD)
                ld      (pnum_dest), hl
                call    pick_emit_num16        ; emits DE as decimal; pnum_dest advanced
                ld      hl, (pnum_dest)        ; HL = cursor after number

                ; emit ": "
                ld      (hl), &3A              ; ':'
                inc     hl
                ld      (hl), &20              ; ' '
                inc     hl

                ; is the entry free? (BD_ENTRY_BUF[0] AND 0x7F) == 0?
                ld      a, (BD_ENTRY_BUF)
                and     &7F
                jr      nz, psn_named

                ; free: emit "free / unnamed"
                call    pick_emit_lit
                defm    "free / unnamed"
                defb    0
                ret

psn_named:
                ; named: emit the masked+trimmed entry name at HL
                call    pick_emit_entry_name
                ret

; ---------------------------------------------------------------------------
; pick_append_confirm — find the NUL at end of PICK_DISPLAY and append
; "\nOverwrite? y/n" so the confirm prompt appears below the name.
; ---------------------------------------------------------------------------
pick_append_confirm:
                ld      hl, PICK_DISPLAY
pac_scan:
                ld      a, (hl)
                or      a
                jr      z, pac_found
                inc     hl
                jr      pac_scan
pac_found:
                ; pick_emit_lit emits the inline literal byte-by-byte until the NUL.
                ; A newline (0x0A) is the first byte; emit via pick_emit_lit one-at-a-time
                ; is not possible since it scans until NUL. Emit newline inline instead:
                ld      (hl), &0A              ; '\n'
                inc     hl
                call    pick_emit_lit
                defm    "Overwrite? y/n"
                defb    0
                ret

; ---------------------------------------------------------------------------
; pick_read_yesno — spin calling key_poll_once until a key is consumed (Z),
; then return Z if 'y'/'Y', NZ otherwise. (design §6: "requires user to confirm")
; Clobbers: A, HL.
; ---------------------------------------------------------------------------
pick_read_yesno:
pry_spin:
                call    key_poll_once
                jr      nz, pry_spin           ; NZ = no key yet
                ld      a, (KR_BUF)            ; key_poll_once stored the key here
                cp      "y"
                ret     z
                cp      "Y"
                ret     z
                or      1                      ; NZ = not 'y'/'Y'
                ret

; ---------------------------------------------------------------------------
; pick_emit_num16 — emit the 16-bit value in DE as decimal ASCII digits, writing
; to the address in pnum_dest, advancing pnum_dest past each digit written.
;
; Convention: caller stores HL (write pointer) into pnum_dest before calling;
; reloads HL from pnum_dest afterward. This routine uses HL/DE freely.
;
; Algorithm: walk pnum_pow10 table (10000..1). For each power, subtract it
; repeatedly from pnum_val while val >= power (counting the digit). Suppress
; leading zeros; always emit at least one digit so value==0 yields "0".
;
; pnum_val  — running value (modified as we subtract powers)
; pnum_pow  — current power-of-10
; pnum_dest — output write pointer
; Clobbers: A, BC, HL; DE consumed into pnum_val (= 0 at end).
; ---------------------------------------------------------------------------
pick_emit_num16:
                ld      (pnum_val), de         ; save running value in scratch cell
                ld      hl, pnum_pow10
                ld      b, 5                   ; 5 powers
                ld      c, 0                   ; C = 0: suppress leading zeros; 1: emitting
pn16_pow:
                ; load the current power into pnum_pow (HL -> table; read 2 bytes)
                ld      a, (hl)
                ld      (pnum_pow), a
                inc     hl
                ld      a, (hl)
                ld      (pnum_pow + 1), a
                inc     hl
                push    hl                     ; save updated table pointer
                push    bc                     ; save B and C

                ; count subtractions of pnum_pow from pnum_val
                ld      a, "0"                 ; digit starts at '0'
pn16_sub:
                ld      hl, (pnum_val)         ; HL = running value
                ld      de, (pnum_pow)         ; DE = current power
                or      a
                sbc     hl, de                 ; HL = val - power; C set => val < power
                jr      c, pn16_nosub          ; borrow: val < power, stop
                ; no borrow: val >= power — do the subtraction
                ld      (pnum_val), hl         ; pnum_val = val - power
                inc     a                      ; digit++
                jr      pn16_sub

pn16_nosub:
                ; A = digit character; check for leading-zero suppression
                pop     bc                     ; restore B (remaining powers) and C (suppress)
                pop     hl                     ; restore table pointer

                ; if digit != '0': always emit (not a leading zero)
                cp      "0"
                jr      nz, pn16_emit

                ; digit == '0'
                ; if C != 0 (already emitting): emit '0'
                ld      d, a                   ; D = '0' (save digit while we check C)
                ld      a, c
                or      a
                ld      a, d                   ; restore digit
                jr      nz, pn16_emit          ; C != 0: emit this '0'

                ; C == 0 (still suppressing): is this the last power? (B == 1)
                ld      d, a                   ; D = '0'
                ld      a, b
                cp      1
                ld      a, d                   ; restore digit
                jr      nz, pn16_skip          ; B != 1: more powers to go, skip

                ; B == 1 (last power, the "1" slot) and digit == '0' and C == 0:
                ; value was 0, emit "0" as the one digit
                ; fall through to pn16_emit

pn16_emit:
                ld      c, 1                   ; mark we're now emitting
                push    hl                     ; save table pointer
                ld      hl, (pnum_dest)
                ld      (hl), a
                inc     hl
                ld      (pnum_dest), hl
                pop     hl                     ; restore table pointer
pn16_skip:
                djnz    pn16_pow
                ret

; ---------------------------------------------------------------------------
; pick_emit_entry_name — emit the record name from BD_ENTRY_BUF into memory at
; HL (current write pointer). Masks 0x7F per byte; trims trailing spaces/NUL.
; All 16 bytes null/space after masking -> emits "free" instead.
; Returns HL advanced past the last byte written.
; Clobbers: A, BC, DE.
; ---------------------------------------------------------------------------
pick_emit_entry_name:
                ; find the last non-space, non-zero byte index (after 0x7F masking)
                ld      de, BD_ENTRY_BUF
                ld      b, 16
                ld      a, &FF                 ; A = last-useful index; &FF = none found
                ld      (peen_last), a
                xor     a                      ; A = current byte index
peen_scan:
                push    af                     ; save index
                push    de                     ; save scan pointer
                ld      a, (de)
                and     &7F
                jr      z, peen_blank
                cp      &20                    ; ' '
                jr      z, peen_blank
                pop     de
                pop     af
                ld      (peen_last), a         ; record last useful index
                inc     a
                inc     de
                djnz    peen_scan
                jr      peen_done
peen_blank:
                pop     de
                pop     af
                inc     a
                inc     de
                djnz    peen_scan
peen_done:
                ld      a, (peen_last)
                cp      &FF
                jr      z, peen_show_free      ; no useful bytes: emit "free"

                ; emit bytes 0..(peen_last) from BD_ENTRY_BUF, masked
                ld      de, BD_ENTRY_BUF
                ld      b, a
                inc     b                      ; emit peen_last+1 bytes
peen_emit:
                ld      a, (de)
                and     &7F
                ld      (hl), a
                inc     hl
                inc     de
                djnz    peen_emit
                ret

peen_show_free:
                call    pick_emit_lit
                defm    "free"
                defb    0
                ret

; ---------------------------------------------------------------------------
; pick_emit_lit — emit an inline NUL-terminated literal string into memory at
; HL, advancing HL past the last byte written (NUL not written). The literal
; follows the CALL as inline defm/defb 0; return address jumps past the NUL.
;
; Uses the self-threading trick (EX (SP),HL): on entry SP -> return address =
; first literal byte. We swap so HL becomes the literal pointer and the dest
; pointer lives on the stack. Clobbers: A; HL = dest after last byte.
; ---------------------------------------------------------------------------
pick_emit_lit:
                ex      (sp), hl               ; HL = literal ptr (ret addr); (SP) = dest ptr
pel_loop:
                ld      a, (hl)
                inc     hl
                or      a
                jr      z, pel_done
                ex      (sp), hl               ; HL = dest ptr; (SP) = literal ptr
                ld      (hl), a
                inc     hl
                ex      (sp), hl               ; HL = literal ptr; (SP) = dest ptr
                jr      pel_loop
pel_done:
                ; HL = address past the NUL = return address; (SP) = final dest ptr
                ex      (sp), hl               ; HL = final dest ptr; (SP) = return address
                ret

; ---------------------------------------------------------------------------
; picker_render — blit PICK_DISPLAY to the SAM screen.
;
; The raw MODE-2 display blit carries no decision logic — it is hardware-gated
; (no screen model in the harness, analogous to the SD SPI ladder — design §8 /
; CLAUDE.md rule 7). Registered as a follow-up item (see the i119 registry).
; In the host build and in this PR it is a stub ret; host tests assert
; PICK_DISPLAY content directly.
; ---------------------------------------------------------------------------
picker_render:
                if defined(NETBOOT_HOSTTEST)==0
                ; Real-hardware MODE-2 blit of PICK_DISPLAY to SAM screen.
                ; Registered as a follow-up item (see the i119 registry).
                endif
                ret

; ---------------------------------------------------------------------------
; Data region
; ---------------------------------------------------------------------------
BD_PICK_MODE:         defs 1           ; in: 0=auto-pick-free 1=by-number 2=by-name 3=list
BD_PICK_RECORD:       defs 2           ; out: chosen record n (1-based), or 0
BD_PICK_CONFIRMED:    defs 1           ; out: 1=confirmed; 0=declined/cancelled
PICK_DISPLAY:         defs BD_PICK_DISPLAY_MAX  ; text shown; host tests assert this
PICK_NAMEBUF:         defs 17          ; scratch: typed name for manual-by-name

PDC:                  defs 2           ; pick_list write cursor (internal)
pnum_dest:            defs 2           ; write-dest pointer for pick_emit_num16
pnum_val:             defs 2           ; running value scratch for pick_emit_num16
pnum_pow:             defs 2           ; current power scratch for pick_emit_num16
peen_last:            defs 1           ; last useful byte index for pick_emit_entry_name

pnum_pow10:                            ; 5 × 16-bit power-of-10 table (LE)
                      defw 10000
                      defw 1000
                      defw 100
                      defw 10
                      defw 1
