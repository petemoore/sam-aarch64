; hook_roundtrip.asm — the small trinload-pushable B-DOS RST-8 hook round-trip
; probe (i93b): HRECORD select -> HSAVE -> HGTHD -> HLOAD -> byte-compare, all
; dispatched through the REAL RST 8 against whatever B-DOS is resident — the
; hardware proof that the whole whole-file hook family works from the pushed-
; program context (the i62 round-trip proved the same sequence on emulated Atom
; Lite B-DOS 1.5a; this tool carries it to real Trinity B-DOS 1.5t).
;
; THE WHOLE JOB: come up on the network (EEPROM config + ENC init, the sd_push
; come-up MINUS the SD/CSD/list-scan stages — B-DOS owns the card here, every
; card access below is an RST 8 hook), then run the probe against the host-
; patched target record:
;
;   '2' HRECORD (156): select HKRT_CFG_RECORD (validates its "BDOS" +232 body
;       stamp; a record pushed by sd_push carries it).
;   '3' HSAVE (132): save PAT_LEN deterministic pattern bytes from SRC_BUF as
;       CODE file "HKPROBE" (bdos_fill_save_uifa + bdos_save_hook). HSAVE
;       manages HMPR itself — no trampoline (docs/specs/samdos-file-io.md).
;   '4' HGTHD (129): find "HKPROBE" (bdos_name_to_uifa + bdos_lookup_hook),
;       decode the deposited DIFA (bdos_difa_to_size) and check the recorded
;       32-bit size == PAT_LEN (mismatch -> verdict F, detail &FFFF).
;   '5' HLOAD (130): load the file back into DST_BUF via bdos_load_hook. The
;       destination page is the page already mapped at section C, so no HMPR
;       trampoline (the i62test step-5 simplification).
;   '6' verdict: byte-compare SRC_BUF vs DST_BUF over PAT_LEN. 'P' pass, or
;       'F' + the first mismatch offset in hkrt_detail.
;
; then serve UDP port 0xEDB0 with a tiny framing (modelled on sd_push's):
;   '?'  discovery -> reply "!HR" — '!' + this tool's 2-byte tag (i329;
;        trinload alone answers a BARE '!').
;   'R'  report    -> reply [verdict][phase][detail LE16] (4 bytes): verdict
;        'P'/'F' ('-' = probe incomplete), phase = the last completed stage
;        digit, detail = &FFFF for the size-check failure else the first
;        mismatch offset (0 when passing).
;   'Q'  quit      -> reply "q", then EI + RET to trinload (clean, re-pushable
;        — never a bare di;halt, which strands the SAM; boot_record's exit
;        contract).
;   plus ARP-request and ICMP-echo replies (netglue.asm) so the host can reach us.
;
; RECORD-NUMBER INPUT — a host-patched config block (the boot_record idiom):
; HKRT_CONFIG is a small fixed region at the END of the binary that the host
; launcher (tools/trinload-push/hook-roundtrip.py) patches by file offset
; BEFORE pushing (magic &5A at +0, record LE16 at +1..2). An un-patched binary
; probes record 1.
;
; FAILURE MODES: DOS errors longjmp into BASIC's error path and never return
; (registry i25) — a hook-level failure leaves the tool GONE (the host sees '?'
; unanswered) with the on-screen stage digit localising WHICH hook died. The
; target record must be a fresh scratch record: HSAVE of an already-existing
; "HKPROBE" raises the DOS "file exists" error (= the longjmp above), so a
; re-run needs a fresh record (or a deleted file).
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7): hook_roundtrip_faithful_
; test.go boots Colin's real ROM + B-DOS 1.5t, seeds a scratch record on the SD
; model, loads this tool into the exact pushed context (page 1, DOSCNT=0), and
; asserts the probe reaches verdict 'P' with the pattern physically landed in
; the record's LBA band — plus the i327 come-up gate (entry -> serve loop ->
; "!HR"). Emulation-verified is not hardware-verified (CLAUDE.md §5): the
; real-Trinity run is the i93b gate itself.
;
; ONE BUILD, FLAG-FREE (no NETBOOT_HOSTTEST carve-out, i231): no
; NETBOOT_REAL_LISTREAD and no NETBOOT_WANT_CLAIM — this tool never scans or
; writes the record list and issues no raw SPI SD command; the card is reached
; ONLY via the RST 8 hooks, so sd_csd.asm is not even included.

                org     &8000

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`,
                ; landing here. The host harness runs hook_roundtrip_main by
                ; symbol; this jp is the hardware/trinload entry.
                jp      hook_roundtrip_main

; ===========================================================================
; Composed includes — the real wire driver, the EEPROM reader, and the B-DOS
; storage seam (the RST 8 hook bodies + UIFA/DIFA arithmetic). They come FIRST
; (right after the boot jp) so every symbol they define is resolved before the
; code below references it (pyz80 is single-symbol-table, no forward
; `equ`-of-a-later-label). No sd_csd.asm: every card access is an RST 8 hook.
; ===========================================================================
                include "encdrv.asm"          ; drv_init / drv_read / drv_write
                include "eeprom.asm"           ; find_index / read_chunk + value/chunk/name/part/total
                include "bdos_seam.asm"        ; bdos_fill_save_uifa / bdos_save_hook / bdos_name_to_uifa
                                               ;   / bdos_lookup_hook / bdos_difa_to_size / bdos_load_hook

; The SAM MAC/IP live in the EEPROM reader's `chunk` buffer (eeprom.asm): the
; "Trinity Network " chunk is read into `chunk`, MAC at +0, IP at +6.
sam_mac:        equ chunk+0                    ; SAM MAC, from the loaded flash chunk
sam_ip:         equ chunk+6                    ; SAM IP

; Network wire glue — the shared trinload-idiom reply plumbing (ack_len /
; return_* / checksums). Contract: packet / sam_mac / sam_ip / drv_write.
                include "netglue.asm"

PAT_LEN:        equ 1553                       ; 3 full sectors + 17 bytes — crosses
                                               ; sector boundaries, odd tail (i62test)

; ===========================================================================
; hook_roundtrip_main — the bootable / trinload entry.
; ===========================================================================
hook_roundtrip_main:
                di
                ; Clear the lower screen + home the print position (CLSLOWER,
                ; stock ROM &06B5) BEFORE any RST &10 print — a CR printed at
                ; the screen bottom sends the ROM into its scroll key-wait
                ; prompt, wedging an unattended tool (the i319a finding; see
                ; rom_print_scroll_test.go). CLS resets the scroll count first.
                call    &06b5
                ld      hl, hkrt_str_banner    ; CR + "HOOK-RT " — announce the
                call    hkrt_print_str         ; tool; stage digits follow

                ; --- locate + read the "Trinity Network " EEPROM chunk -> MAC/IP --
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, hkrt_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, hkrt_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, hkrt_fail_cfg

                ; both MAC and IP zero? -> missing settings.
                ld      a, (sam_mac+0)
                ld      b, a
                ld      a, (sam_ip+0)
                or      b
                jp      z, hkrt_fail_cfg

                ; --- init the ENC28J60 (i242: ENC before any heavy port work) ------
                ld      hl, sam_mac
                call    drv_init
                ld      a, b
                or      c
                jp      z, hkrt_fail_init
                ld      a, "1"                 ; stage 1: come-up complete (EEPROM + ENC)
                call    hkrt_stage

                ; --- '2' HRECORD: select the host-chosen target record ------------
                ; The 16-bit HL form of bdos_select_record's contract (HL = record
                ; number, A = 0 = select by number), taken directly so records > 255
                ; on a real 4809-record card stay addressable. HRECORD validates the
                ; record's "BDOS" +232 body stamp and longjmps ("Invalid record",
                ; rep81) if absent — a record pushed by sd_push carries it.
                ld      hl, (HKRT_CFG_RECORD)
                xor     a
                rst     8
                defb    BD_HOOK_HRECORD
                ld      a, "2"                 ; stage 2: record selected
                call    hkrt_stage

                ; --- '3' HSAVE: save the deterministic pattern as "HKPROBE" -------
                ; Fill SRC_BUF with i62test's generator: f(addr) = 2*low XOR high —
                ; address-derived, so the faithful test recomputes the same bytes
                ; from the SRC_BUF map symbol.
                ld      hl, SRC_BUF
                ld      bc, PAT_LEN
hkrt_fill:
                ld      a, l
                add     a, a
                xor     h
                ld      (hl), a
                cpi
                jp      pe, hkrt_fill

                ld      hl, hkrt_name
                ld      (BD_NAME_PTR), hl
                in      a, (251)
                and     &1F
                ld      (BD_SAVE_PAGE), a      ; source page = this tool's own page
                ld      hl, SRC_BUF
                ld      (BD_SAVE_ADDR), hl     ; section-C source offset
                ld      hl, PAT_LEN
                ld      (BD_SAVE_SIZE), hl
                call    bdos_fill_save_uifa
                call    bdos_save_hook         ; HSAVE manages HMPR itself
                ld      a, "3"                 ; stage 3: HSAVE returned
                call    hkrt_stage

                ; --- '4' HGTHD: find the file back + verify the recorded size -----
                ld      hl, hkrt_name
                ld      (BD_NAME_PTR), hl
                call    bdos_name_to_uifa
                call    bdos_lookup_hook       ; HGTHD; DIFA copied back to BD_DIFA
                call    bdos_difa_to_size      ; -> BD_SIZE (32-bit LE)
                ld      a, "4"                 ; stage 4: HGTHD returned
                call    hkrt_stage
                ; 32-bit compare BD_SIZE == PAT_LEN (high word must be 0).
                ld      hl, (BD_SIZE)
                ld      de, PAT_LEN
                or      a
                sbc     hl, de
                jp      nz, hkrt_fail_size
                ld      hl, (BD_SIZE + 2)
                ld      a, h
                or      l
                jp      nz, hkrt_fail_size

                ; --- '5' HLOAD: load the file back into DST_BUF -------------------
                ; Wipe DST_BUF first so a vacuous compare cannot pass (i62test).
                ld      hl, DST_BUF
                ld      bc, PAT_LEN
                ld      a, &AA
hkrt_wipe:
                ld      (hl), a
                cpi
                jp      pe, hkrt_wipe

                ; Register contract from the deposited DIFA (the load_payload_generic
                ; reads): C = pages (+34), DE = lengthMod16K (+35..36, bit-7 marker
                ; cleared). The destination page is the page already mapped at
                ; section C — no trampoline (bdos_load_hook's header).
                ld      hl, (BD_DIFA + BD_OFF_LENGTH)
                ld      a, h
                and     BD_DIFA_MARKER
                ld      d, a
                ld      e, l                   ; DE = lengthMod16K
                ld      a, (BD_DIFA + BD_OFF_PAGES)
                ld      c, a                   ; C = pages count
                ld      hl, DST_BUF
                call    bdos_load_hook
                ld      a, "5"                 ; stage 5: HLOAD returned
                call    hkrt_stage

                ; --- '6' verdict: byte-compare SRC_BUF vs DST_BUF ------------------
                ld      hl, SRC_BUF
                ld      de, DST_BUF
                ld      bc, PAT_LEN
hkrt_cmp:
                ld      a, (de)
                cpi                            ; Z: A == (HL); P/V: BC != 0
                jr      nz, hkrt_fail_cmp
                inc     de
                jp      pe, hkrt_cmp

                ; PASS: detail 0.
                ld      hl, 0
                ld      (hkrt_detail), hl
                ld      a, "P"
                jr      hkrt_verdict_set

hkrt_fail_size:
                ; The recorded size came back wrong — verdict F, detail &FFFF
                ; (distinguishes the size-check failure from a byte mismatch).
                ld      hl, &FFFF
                ld      (hkrt_detail), hl
                ld      a, "F"
                jr      hkrt_verdict_set

hkrt_fail_cmp:
                ; First mismatch at offset PAT_LEN - 1 - BC (cpi already decremented
                ; BC past the mismatching byte).
                ld      hl, PAT_LEN - 1
                or      a
                sbc     hl, bc
                ld      (hkrt_detail), hl
                ld      a, "F"

hkrt_verdict_set:
                ld      (hkrt_verdict), a
                ld      a, "6"                 ; stage 6: verdict computed
                call    hkrt_stage
                ld      a, (hkrt_verdict)
                call    dbg_char               ; close the screen story: P or F

; ===========================================================================
; hkrt_serve_loop — serve '?' / 'R' / 'Q' (+ ARP/ICMP) until quit or Esc.
; The sd_push serve-loop framing, verb set reduced to discovery + report.
; ===========================================================================
hkrt_serve_loop:
                ; Esc-to-exit (trinload idiom): poll the keyboard; on Esc, RET
                ; to trinload's start (it pushed start as our return address).
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc pressed?
                ret     z                      ; -> trinload's start

                ; read one frame; loop if nothing available.
                ld      hl, packet
                call    drv_read               ; BC = length (0 if nothing)
                ld      a, b
                or      c
                jr      z, hkrt_serve_loop

                ; --- frame type: IPv4 or ARP? -----------------------------------
                ld      a, (packet+12)         ; Ethernet frame type MSB
                cp      &08
                jr      nz, hkrt_serve_loop

                ld      a, (packet+13)
                cp      &06                    ; ARP?
                jr      nz, hkrt_try_ipv4

                ; ARP request for our IP? -> reply.
                ld      a, (packet+21)
                cp      &01                    ; request?
                jr      nz, hkrt_serve_loop
                inc     a                      ; -> reply (2)
                ld      (packet+21), a
                ld      hl, (packet+38)        ; requested IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jr      nz, hkrt_serve_loop    ; not for us
                ld      hl, (packet+40)        ; requested IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jr      nz, hkrt_serve_loop    ; not for us
                call    return_eth
                call    return_arp
                ld      hl, packet
                ld      bc, 42
                call    drv_write
                jr      hkrt_serve_loop

hkrt_try_ipv4:
                and     a                      ; IPv4? (type LSB == 0)
                jr      nz, hkrt_serve_loop
                ld      a, (packet+20)         ; IP flags
                and     %00100000              ; fragmented?
                jr      nz, hkrt_serve_loop    ; we don't reassemble

                ld      a, (packet+23)
                cp      &01                    ; ICMP?
                jr      nz, hkrt_try_udp

                ld      a, (packet+34)
                cp      &08                    ; echo request?
                jr      nz, hkrt_serve_loop
                xor     a
                ld      (packet+34), a         ; -> echo reply
                call    return_eth
                call    return_ip
                call    checksum_ip
                call    checksum_icmp
                ld      hl, packet
                call    ip_to_eth_len
                call    drv_write
                jp      hkrt_serve_loop

hkrt_try_udp:
                cp      &11                    ; UDP?
                jp      nz, hkrt_serve_loop
                ld      de, (packet+36)        ; destination port
                ld      hl, &b0ed              ; port 0xEDB0
                and     a
                sbc     hl, de
                jp      nz, hkrt_serve_loop    ; not for us

                ; unicast to our IP? (the query messages are unicast)
                ld      hl, (packet+30)        ; target IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jp      nz, hkrt_serve_loop
                ld      hl, (packet+32)        ; target IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jp      nz, hkrt_serve_loop

                ; dispatch on the first UDP data byte (packet+42).
                ld      a, (packet+42)
                cp      "?"                    ; discovery?
                jr      nz, hkrt_not_disc
                ; reply "!HR" — '!' + the tool tag, so a launcher can tell this
                ; tool from trinload (which alone answers a bare '!'; i329).
                ld      a, "!"
                ld      (packet+42), a
                ld      a, "H"
                ld      (packet+43), a
                ld      a, "R"
                ld      (packet+44), a
                ld      bc, 3
                call    ack_len
                jp      hkrt_serve_loop

hkrt_not_disc:
                cp      "R"                    ; report?
                jr      nz, hkrt_not_report
                ; reply [verdict][phase][detail LE16] — the probe's outcome.
                ld      a, (hkrt_verdict)
                ld      (packet+42), a
                ld      a, (hkrt_phase)
                ld      (packet+43), a
                ld      hl, (hkrt_detail)
                ld      (packet+44), hl
                ld      bc, 4
                call    ack_len
                jp      hkrt_serve_loop
hkrt_not_report:
                cp      "Q"                    ; quit?
                jp      nz, hkrt_serve_loop
                ; ack the quit, then EI + RET to trinload (clean, re-pushable —
                ; never a bare di;halt, which would strand the SAM and cost a
                ; power-cycle; boot_record's exit contract).
                ld      a, "q"
                ld      (packet+42), a
                ld      bc, 1
                call    ack_len
                ld      hl, hkrt_str_bye
                call    hkrt_print_str         ; CR + "DONE" + CR
                ei
                ret                            ; -> trinload's start

; ---------------------------------------------------------------------------
; hkrt_stage — record stage digit A in hkrt_phase AND print it (the sd_push
; i318 idiom: the last digit on screen localises a hang/longjmp to the stage
; that followed it). Preserves every register (dbg_char does).
; ---------------------------------------------------------------------------
hkrt_stage:
                ld      (hkrt_phase), a
                jp      dbg_char

; ---------------------------------------------------------------------------
; dbg_char / hkrt_print_str — the RST &10 print idiom (trinload/sd_push):
; dbg_char prints A preserving EVERY register; hkrt_print_str prints the
; NUL-terminated string at HL (clobbers A, HL).
; ---------------------------------------------------------------------------
dbg_char:
                push    af
                push    bc
                push    de
                push    hl
                push    ix
                push    iy
                rst     &10                    ; ROM prints A to the current channel
                pop     iy
                pop     ix
                pop     hl
                pop     de
                pop     bc
                pop     af
                ret

hkrt_print_str:
                ld      a, (hl)
                or      a
                ret     z
                call    dbg_char
                inc     hl
                jr      hkrt_print_str

; ---------------------------------------------------------------------------
; Failure paths — print the reason, show a diagnostic border, hold until Esc,
; then RET to trinload (never a raw di;halt). Only the no-network cases land
; here (phase stays '0'); a DOS-hook failure longjmps away instead (header).
; ---------------------------------------------------------------------------
hkrt_fail_cfg:
                ld      hl, hkrt_str_fail_cfg
                ld      a, 2                   ; red: no/bad network settings
                jr      hkrt_fail_show
hkrt_fail_init:
                ld      hl, hkrt_str_fail_enc
                ld      a, 1                   ; blue: ENC28J60 init failed
hkrt_fail_show:
                out     (&fe), a
                call    hkrt_print_str         ; the reason, next to the border colour
hkrt_fail_wait:
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc?
                jr      nz, hkrt_fail_wait     ; hold the border until Esc
                ret                            ; -> trinload's start

; ===========================================================================
; Data.
; ===========================================================================
hkrt_chunk_name: defm "Trinity Network "      ; the EEPROM chunk holding MAC+IP

hkrt_name:      defm "HKPROBE"                 ; the probe file's on-record name
                defb 0                          ; NUL terminator

hkrt_phase:     defb "0"                       ; last completed stage digit (ASCII)
hkrt_verdict:   defb "-"                       ; 'P' / 'F' ('-' = probe incomplete)
hkrt_detail:    defw 0                         ; &FFFF = size-check fail, else the
                                               ; first mismatch offset (0 = pass)

; Screen-status strings — NUL-terminated for hkrt_print_str; 13 = CR.
hkrt_str_banner:
                defb 13
                defm "HOOK-RT "
                defb 0
hkrt_str_bye:   defb 13
                defm "DONE"
                defb 13, 0
hkrt_str_fail_cfg:
                defb 13
                defm "NO NET CONFIG"
                defb 13, 0
hkrt_str_fail_enc:
                defb 13
                defm "ENC INIT FAIL"
                defb 13, 0

; The pattern buffers are ASSEMBLED into the binary (defs), not fixed equates:
; the composed includes put the code+data end past &9000, so i62test's fixed
; SRC_BUF &9000 / DST_BUF &A000 would overlap code here. defs placement makes
; the layout assembler-guaranteed collision-free (the 16384 fit check bounds
; the whole image); the faithful test reads both addresses from the .map.
SRC_BUF:        defs PAT_LEN                   ; pattern source (this tool's own page)
DST_BUF:        defs PAT_LEN                   ; HLOAD-back destination

packet:         defs 1518                      ; RX/TX frame buffer (drv_read fills it)

; ===========================================================================
; HKRT_CONFIG — the target-record config block (the boot_record idiom). A
; small, fixed, host-patchable region at the END of the binary so the host
; launcher (tools/trinload-push/hook-roundtrip.py) can set the record to probe
; by file offset before the program runs. An un-patched binary probes record 1.
;
; BYTE LAYOUT (the launcher patches by file offset from the HKRT_CONFIG
; symbol — the block's anchor at +0; keep STABLE):
;   +0  HKRT_CONFIG      1 byte   magic/version = HKRT_CFG_MAGIC_VAL (&5A);
;                                 the launcher sanity-checks it found the
;                                 block. Patch anchor (file offset
;                                 HKRT_CONFIG - &8000). Never written.
;   +1  HKRT_CFG_RECORD  2 bytes  the record number to probe (LE16, >= 1 — a
;                                 real card holds thousands of records).
; Total: 3 bytes.
; ===========================================================================
HKRT_CFG_MAGIC_VAL:   equ &5A                  ; the magic/version byte value

HKRT_CONFIG:          defb HKRT_CFG_MAGIC_VAL   ; +0 magic/version (patch anchor)
HKRT_CFG_RECORD:      defw 1                    ; +1 baked default: record 1 (patched)

length:               equ $ - hook_roundtrip_main  ; code length (diagnostic)
