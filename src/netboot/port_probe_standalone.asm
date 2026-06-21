; port_probe_standalone.asm — hardware port-characterization probe (item i228,
; step A; second client of test_report.asm).
;
; PURPOSE: find a port that is NOT mapped to any SAM peripheral, so a test binary
; can IN it to tell emulation from real hardware at runtime (i228): on real
; hardware an unmapped port returns a stable floating-bus value; the emulator IO
; model is then made to return a DISTINCT marker on that same port, so the
; terminator can branch (emulator-marker -> di;halt; else -> RET to trinload).
;
; This probe reads a list of candidate ports via IN A,(C) with B=0 (so the read
; address is just the low-byte port, high byte 0 — consistent with how the
; eventual detection will read it) and reports each [port, value] pair over the
; network via test_report (SATR, test_id 2). Read it off the wire to characterize
; the SAM, then pick a port + a non-colliding emulator marker.
;
; The candidate ports are all in &00..&A0, below the SAM system/ASIC ports
; (&F8..&FF), the keyboard/border port (&FE), and the Trinity ports (&DC..&DF) —
; chosen to be side-effect-free reads. (Pete blesses the list before any hardware
; read.) Reports always status 0; the payload IS the detail bytes.

                org     &8000

TEST_ID_PORTPROBE: equ 2

port_probe_main:
                ld      hl, TR_SRC_MAC          ; HL -> our MAC (test_report.asm)
                call    drv_init
                call    drv_wait_link           ; proactive transmit -> wait for link (i127)

                ; Sweep the candidate ports, packing [port, value] pairs.
                ld      hl, pp_ports
                ld      de, pp_detail
                ld      b, PP_NPORTS
pp_loop:
                push    bc                      ; save the loop count (IN needs B=0)
                ld      a, (hl)                 ; candidate port (low byte)
                ld      c, a
                ld      (de), a                 ; record the port
                inc     de
                ld      b, 0                    ; high address byte = 0
                in      a, (c)                  ; read port &00<<8 | port
                ld      (de), a                 ; record its value
                inc     de
                inc     hl
                pop     bc
                djnz    pp_loop

                ; Report the [port,value] table over the network.
                ld      de, TEST_ID_PORTPROBE
                xor     a                       ; status 0 (the data is the detail)
                ld      b, PP_NPORTS*2          ; detail length
                ld      hl, pp_detail
                call    test_report
                jp      tr_terminate            ; di;halt in emulation, RET to trinload on hardware (i228)

; ---------------------------------------------------------------------------
; Candidate ports to characterize (Pete-blessed, side-effect-free reads, all
; below the SAM peripheral range). PP_NPORTS*2 must stay <= TR_DETAIL_MAX (64).
; ---------------------------------------------------------------------------
pp_ports:         defb &00,&08,&10,&18,&20,&28,&30,&38
                  defb &40,&50,&60,&70,&7F,&80,&90,&A0
PP_NPORTS:        equ 16

                include "build_udp_frame.asm"   ; build_udp_frame + PARAMS/PACKET
                include "encdrv.asm"             ; drv_init/drv_write + wait_ready
                include "enc_link.asm"           ; drv_wait_link (PHY link-up, i127)
                include "test_report.asm"        ; test_report + TR_SRC_MAC/TR_*

pp_detail:        defs PP_NPORTS*2               ; [port, value] pairs
