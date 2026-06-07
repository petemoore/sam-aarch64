; i62test.asm — B-DOS storage-backend portability probe (registry item i62).
;
; A single standalone Z80 binary that exercises the project's SAMDOS
; file-io hook idioms (docs/specs/samdos-file-io.md) against whichever
; DOS booted the machine:
;
;   1. Detect B-DOS via the documented DVAR-7 idiom (B-DOS 1.7n manual,
;      "DVAR ACCESS" + "Some Tips"): the DOS page's first two bytes hold
;      the address of DVAR 0 (a section-C pointer), and PEEK DVAR 7 < 20
;      means B-DOS is booted (DVAR 7 = version*10-10, e.g. 5 for 1.5x;
;      SAMDOS has no such pointer at page offset 0).
;   2. If B-DOS: select RECORD 1 via hook HRECORD (156, A=0 = select by
;      number in HL — B-DOS 1.5a source `HRECORD:`/`exp.rcd`, which also
;      validates the record's BDOS ID and errors with "Invalid record"
;      (rep81) if the stamp is missing).  If SAMDOS: skip — the floppy
;      is the only device.  This is the ONE backend-conditional step.
;   3. HSAVE (132) a 1553-byte deterministic pattern from &9000 as CODE
;      file "I62DATA" — UIFA fill + RST 8 exactly as src/main_loop.asm's
;      save_out_file does.
;   4. HGTHD (129) the file back — name in UIFA at &4B00, header
;      deposited at &4B50 — and validate the recorded length.
;   5. HLOAD (130) into &A000 using the DIFA-supplied pages/length, the
;      same register contract as src/loader.asm's load_payload_generic
;      (no HMPR trampoline needed here: the destination page is the one
;      already mapped at section C).
;   6. Byte-compare &9000 vs &A000; report OK / FAILnn over the printer
;      status channel (ports &E8/&E9, same idiom as src/print.asm), then
;      DI; HALT so SimCoupé's -exitonhalt terminates the run.
;
; The SAME binary booted under plain SAMDOS+floppy and under B-DOS AL
; 1.5a + emulated Atom Lite is the portability proof: everything after
; the record-selection branch is common code.
;
; Status-channel transcript (one line per phase):
;   I62                      banner
;   DOS:BDOS V=nn R=nnnn     B-DOS detected (DVAR7 hex, record count hex)
;   DOS:SAMDOS               no B-DOS — floppy path
;   P1                       HRECORD 1 selected (B-DOS path only)
;   P2                       HSAVE completed
;   P3                       HGTHD completed, length verified
;   P4                       HLOAD completed
;   OK / FAILnn              verdict
;
; DOS errors (file not found, invalid record, write protect...) longjmp
; into BASIC's error path instead of returning, so a hook-level failure
; shows up as a phase-marker transcript that stops early plus a SimCoupé
; timeout (exit 124) — distinguishable from both OK and FAILnn.

PRINT_DATA_PORT: equ &E8        ; PRINTL1 data (see src/print.asm)
PRINT_STAT_PORT: equ &E9        ; PRINTL1 strobe

UIFA:           equ &4B00       ; 48-byte user file header (BASIC sys page)
DIFA:           equ &4B50       ; HGTHD deposits the found header here

HOOK_HGTHD:     equ 129
HOOK_HLOAD:     equ 130
HOOK_HSAVE:     equ 132
HOOK_HRECORD:   equ 156         ; B-DOS only: select record (A=0: number in HL)

DOSFLG:         equ &5BC2       ; sysvar: 0 = no DOS, else DOS page number

; The B-DOS detector must execute from LMPR-stable memory (section B)
; because it pages the DOS page into section C — where this program
; lives — via HMPR.  &7E00 is the same section-B home the production
; assembler uses for its HLOAD trampoline (src/trampoline.asm
; TRAMPOLINE_DST), proven free after BASIC's CLEAR &7FFF.
DETECT_DST:     equ &7E00

SRC_BUF:        equ &9000       ; pattern source (same page as this code)
DST_BUF:        equ &A000       ; HLOAD-back destination
PAT_LEN:        equ 1553        ; 3 full sectors + 17 bytes — crosses
                                ; sector boundaries, odd tail

                org &8000

; --------------------------------------------------------------------
; Entry: BASIC has done CLEAR 32767: LOAD "i62test" CODE 32768: CALL 32768.
; --------------------------------------------------------------------
start:
                ld      hl, msg_banner
                call    puts

; Copy the detector into section B and run it.  Returns A=1 with
; D=DVAR7 and HL=record count if B-DOS is present, else A=0.
                ld      hl, detect_body
                ld      de, DETECT_DST
                ld      bc, detect_len
                ldir
                di                       ; no interrupts while the DOS
                                         ; page transits section C
                call    DETECT_DST
                or      a
                jr      z, samdos_path

; ---- B-DOS path: report version + records, select record 1 ----------
bdos_path:
                push    hl               ; record count
                push    de               ; D = DVAR7
                ld      hl, msg_bdos
                call    puts
                pop     de
                ld      a, d
                call    puthex8          ; V=nn
                ld      hl, msg_recs
                call    puts
                pop     hl
                call    puthex16         ; R=nnnn
                ld      a, 10
                call    putc

; HRECORD: A=0 selects the record whose number is in HL.  Longjmps with
; "Invalid record" (rep81) if record 1 lacks the BDOS ID stamp.
                xor     a
                ld      hl, 1
                rst     8
                defb    HOOK_HRECORD
                ld      hl, msg_p1
                call    puts
                jr      common_path

; ---- SAMDOS path: no record selection — floppy is the device --------
samdos_path:
                ld      hl, msg_samdos
                call    puts

; ---- Common code from here on: the portability claim under test -----
common_path:

; Fill SRC_BUF with a deterministic pattern: f(addr) = 2*low ^ high.
                ld      hl, SRC_BUF
                ld      bc, PAT_LEN
fill_loop:      ld      a, l
                add     a, a
                xor     h
                ld      (hl), a
                cpi
                jp      pe, fill_loop

; HSAVE: UIFA <- type/name/ext, source page = current section-C page,
; source offset &9000, length 1553 (pages=0).  No trampoline: HSAVE
; manages HMPR itself (docs/specs/samdos-file-io.md "WRITE — HSAVE
; needs no trampoline").
                call    fill_uifa
                in      a, (251)
                and     31
                ld      (UIFA + 31), a   ; start page = current HMPR low 5
                ld      hl, SRC_BUF
                ld      (UIFA + 32), hl  ; start offset (section-C form)
                xor     a
                ld      (UIFA + 34), a   ; pages = 0 (PAT_LEN < 16K)
                ld      hl, PAT_LEN
                ld      (UIFA + 35), hl  ; length mod 16K
                rst     8
                defb    HOOK_HSAVE
                ld      hl, msg_p2
                call    puts

; Wipe DST_BUF so a vacuous compare cannot pass.
                ld      hl, DST_BUF
                ld      bc, PAT_LEN
                ld      a, &AA
wipe_loop:      ld      (hl), a
                cpi
                jp      pe, wipe_loop

; HGTHD: find "I62DATA" on the current device, deposit header at DIFA.
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD

; Validate the recorded length (DIFA+35/36, with HGTHD's `set 7,d`
; marker cleared) and page count (DIFA+34) — same reads as
; src/loader.asm load_payload_generic.
                ld      hl, (DIFA + 35)
                ld      a, h
                and     &7F
                ld      h, a
                ld      de, PAT_LEN
                or      a
                sbc     hl, de
                ld      a, h
                or      l
                jp      nz, fail03       ; wrong length came back
                ld      a, (DIFA + 34)
                or      a
                jp      nz, fail04       ; wrong page count came back
                ld      hl, msg_p3
                call    puts

; HLOAD into DST_BUF.  HL = destination (&8000-&BFFF window), C = pages
; from DIFA+34, DE = length-mod-16K from DIFA+35/36.  The destination
; page is the page already mapped at section C, so no HMPR trampoline
; is required (the trampoline exists for *cross-page* loads only).
                ld      hl, (DIFA + 35)
                ld      a, h
                and     &7F
                ld      d, a
                ld      e, l             ; DE = length mod 16K
                ld      a, (DIFA + 34)
                ld      c, a             ; C = pages count (0)
                ld      hl, DST_BUF
                rst     8
                defb    HOOK_HLOAD
                ld      hl, msg_p4
                call    puts

; Compare the round-tripped bytes.
                ld      hl, SRC_BUF
                ld      de, DST_BUF
                ld      bc, PAT_LEN
cmp_loop:       ld      a, (de)
                cpi                      ; Z: A == (HL); P/V: BC != 0
                jp      nz, fail05
                inc     de
                jp      pe, cmp_loop

                ld      hl, msg_ok
                call    puts
exit:           di                       ; load-bearing: PTDOS did EI
                halt                     ; SimCoupé -exitonhalt fires here

fail03:         ld      a, "3"
                jr      fail_common
fail04:         ld      a, "4"
                jr      fail_common
fail05:         ld      a, "5"
fail_common:    push    af
                ld      hl, msg_fail
                call    puts
                pop     af
                call    putc
                ld      a, 10
                call    putc
                jr      exit


; --------------------------------------------------------------------
; fill_uifa — UIFA <- 15-byte name block, &FF padding, IX = UIFA.
; Same shape as src/sam_io.inc fill_uifa.
; --------------------------------------------------------------------
fill_uifa:      ld      hl, name_block
                ld      de, UIFA
                ld      bc, 15
                ldir
                ld      a, &FF
                ld      b, 48 - 15
fu1:            ld      (de), a
                inc     de
                djnz    fu1
                ld      ix, UIFA
                ret

name_block:     defb    19               ; CODE
                defm    "I62DATA   "     ; 10-char name
                defm    "    "           ; 4-char ext


; --------------------------------------------------------------------
; B-DOS detector — copied to DETECT_DST (section B) and executed there.
; Position-independent (relative jumps only, no local statics).
;
; Output: A=1, D=DVAR7 value, HL=record count (DVARs 23/24)  if B-DOS;
;         A=0 otherwise.  HMPR restored either way.  Uses A/BC/DE/HL.
;
; Mechanism (B-DOS 1.7n manual "DVAR ACCESS"):
;   LD A,(&5BC2) / OUT (&FB),A   — page the DOS page at section C
;   LD HL,(32768)                — first 2 bytes = address of DVAR 0
; Under B-DOS that pointer lands in &8000-&BFFF (location C); under
; SAMDOS page offset 0 holds DOS code/header bytes instead, so the
; range check plus the version-byte (<20) and record-count (!=0) checks
; reject it.  Triple-checking makes a SAMDOS false-positive — which
; would send hook 156 to a DOS that doesn't implement it — implausible.
; --------------------------------------------------------------------
detect_body:
                in      a, (251)
                ld      e, a             ; E = saved HMPR
                ld      a, (DOSFLG)
                or      a
                jr      z, det_no        ; no DOS booted at all
                out     (251), a         ; DOS page -> section C+D
                ld      hl, (&8000)      ; candidate DVAR-0 pointer
                ld      a, h
                cp      &80
                jr      c, det_no        ; below section C — not B-DOS
                cp      &C0
                jr      nc, det_no       ; above section C — not B-DOS
                push    hl
                ld      bc, 7
                add     hl, bc
                ld      d, (hl)          ; D = DVAR 7 (version)
                pop     hl
                ld      a, d
                cp      20
                jr      nc, det_no       ; >=20 — not B-DOS (manual idiom)
                ld      bc, 23
                add     hl, bc
                ld      a, (hl)
                inc     hl
                ld      h, (hl)
                ld      l, a             ; HL = DVARs 23/24 (records)
                or      h
                jr      z, det_no        ; no mass-storage records
                ld      a, e
                out     (251), a         ; restore HMPR
                ld      a, 1
                ret
det_no:         ld      a, e
                out     (251), a         ; restore HMPR (no-op if unchanged)
                xor     a
                ret
detect_end:
detect_len:     equ     detect_end - detect_body


; --------------------------------------------------------------------
; Status-channel output (same hardware idiom as src/print.asm).
; --------------------------------------------------------------------
putc:           push    af
                out     (PRINT_DATA_PORT), a
                ld      a, 1
                out     (PRINT_STAT_PORT), a     ; strobe rising edge
                xor     a
                out     (PRINT_STAT_PORT), a
                pop     af
                ret

puts:           ld      a, (hl)
                or      a
                ret     z
                call    putc
                inc     hl
                jr      puts

puthex8:        push    af               ; A -> two hex chars
                rrca
                rrca
                rrca
                rrca
                call    puthex_nibble
                pop     af
puthex_nibble:  and     &0F
                add     a, "0"
                cp      "9" + 1
                jr      c, phn1
                add     a, "A" - "0" - 10
phn1:           jp      putc

puthex16:       ld      a, h             ; HL -> four hex chars
                call    puthex8
                ld      a, l
                jr      puthex8

msg_banner:     defm    "I62"
                defb    10, 0
msg_bdos:       defm    "DOS:BDOS V="
                defb    0
msg_recs:       defm    " R="
                defb    0
msg_samdos:     defm    "DOS:SAMDOS"
                defb    10, 0
msg_p1:         defm    "P1"
                defb    10, 0
msg_p2:         defm    "P2"
                defb    10, 0
msg_p3:         defm    "P3"
                defb    10, 0
msg_p4:         defm    "P4"
                defb    10, 0
msg_ok:         defm    "OK"
                defb    10, 0
msg_fail:       defm    "FAIL0"
                defb    0
