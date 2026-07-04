; settle_probe.asm — the i291b trinload-pushable ENC28J60/shared-PIC settle-time probe.
;
; PURPOSE. Measure the real post-&38 SD-init settle window of the shared Trinity
; PIC: the quiet time after a heavy SD controller operation before an identity
; probe (the driver's chk_trinity: OUT &DC,8/9 + IN &DD) is honoured again. The
; emulator models this window as a conservative sdInitSettleTStates constant
; (enc28j60.go, 4x the documented ~50µs) — a pure fidelity knob that never runs on
; silicon. This probe reports, over Ethernet (unattended — no screen needed),
; whether a chk_trinity issued N T-states after the &38 reads STALE or FRESH, so a
; host bisection (the sibling leaf i291b-b2) can sweep N on real hardware, converge
; on the real settle, and set the constant to a measured value + margin.
;
; FLOW. drv_init + drv_wait_link bring the ENC up — drv_init's own chk_trinity
; passes because the PIC is quiescent (no prior &38) — then:
;   OUT (&DC),&38     arm the settle window (the heavy SD-init microcontroller wake)
;   delay N           a parameterized T-state delay (settle_delay_count, poked by
;                     the launcher / host test = the measured quantity)
;   call chk_trinity  the driver's identity read; Z if 'TR' read back (FRESH /
;                     settled), NZ if the FIRST identity select (&08) landed while
;                     the PIC was still settling (STALE — its read-back is the stale
;                     latch, not 'T'). chk_trinity's internal DJNZ delays between its
;                     two selects (~6656 T) exceed the ~1200 T window, so by the &09
;                     read the PIC has usually settled and readR reads 'R'; the
;                     DECIDING signal is therefore the first select (readT), which the
;                     Z flag tracks — readT=='T' implies readR=='R'.
; It then reports [N, readT, readR] with status 0=FRESH / 1=STALE over UDP
; (test_report SATR) + the pass/fail border (green=settled), and terminates
; (di;halt in emulation, RET to trinload on hardware).
;
; CLEAN MEASUREMENT. The probe isolates the settle by issuing a bare &38 then the
; parameterized delay — deliberately NOT the driver's post-&38 &FF settle-poll,
; which would itself wait the settle out and confound the measurement. N is the
; sole independent variable. The PIC is quiescent here (drv_init has finished), so
; no busy-poll bracket precedes the &38; if hardware shows the bare &38 is dropped,
; the bisection leaf adds one (documented there).
;
; ORDER (i242). drv_init (ENC) MUST precede the &38: the &38 leaves the PIC
; settling, so an ENC bring-up issued after it would itself read stale.
; Emulation-first (CLAUDE.md rule 7): settle_probe_test.go runs THIS binary through
; the ENC28J60 model across a sweep of settle_delay_count and asserts the reported
; status flips stale->fresh across the modelled settle boundary — proving the
; deployable probe reports stale<settle / fresh>=settle before any hardware push.

                org     &8000

TEST_ID_SETTLE:   equ 3                          ; SATR test id for the settle probe

SD_CTL:           equ &DC                        ; Trinity microcontroller select/status port
SD_INIT:          equ &38                        ; the heavy SD-init wake (arms the settle window)

                ; trinload's X packet does `out (HMPR),P; jp &8000`, landing here;
                ; the host harness invokes settle_probe_main by symbol instead.
                jp      settle_probe_main

settle_probe_main:
                ld      hl, TR_SRC_MAC          ; HL -> our MAC (test_report.asm)
                call    drv_init                ; ENC up: chk_trinity passes (PIC quiescent), RX armed
                call    drv_wait_link           ; proactive transmit -> wait for PHY link-up (i127)

                ; Arm the settle window: the heavy &38 SD-init wake. OUT (C),A with
                ; BC=&00DC — the same select idiom as chk_trinity's OUT (C),H. From
                ; here the PIC is settling and no further SD traffic follows, so the
                ; deadline anchors at this &38.
                ld      bc, &00DC               ; B=0, C=&DC (Trinity select port)
                ld      a, SD_INIT
                out     (c), a

                ; Parameterized delay = the measured quantity. 16-bit down-counter
                ; (settle_delay_count, poked by the launcher / host test). Count >= 1;
                ; count=1 is the minimal delay (one iteration). A larger count burns
                ; proportionally more T-states between the &38 and the identity read.
                ld      bc, (settle_delay_count)
sp_delay:
                dec     bc
                ld      a, b
                or      c
                jr      nz, sp_delay

                ; The driver's identity probe, issued N T-states after the &38.
                ; Within the settle window the first select (&08) is dropped and its
                ; IN &DD returns the stale latch (readT != 'T'); once settled it reads
                ; 'T'. chk_trinity's Z (readT=='T' && readR=='R') tracks that first
                ; select — readT=='T' implies readR=='R' (the &09 lands even later).
                call    chk_trinity             ; D=readT, E=readR; Z if 'TR' read back

                ; Capture the read bytes (LD preserves the chk_trinity Z flag) then
                ; classify: status 0 = FRESH (settled), 1 = STALE (still settling).
                ld      a, d
                ld      (sp_detail+2), a        ; readT
                ld      a, e
                ld      (sp_detail+3), a        ; readR
                ld      a, 1                    ; default STALE (LD preserves Z)
                jr      nz, sp_have_status      ; NZ -> stale, keep A=1
                xor     a                       ; Z  -> FRESH, A=0
sp_have_status:
                ld      (sp_status), a

                ; detail[0..1] = the N used, echoed back so a listener on the wire
                ; knows which delay produced this result.
                ld      hl, (settle_delay_count)
                ld      (sp_detail), hl

                ; Report over the network (SATR) + border, then terminate.
                ld      de, TEST_ID_SETTLE
                ld      a, (sp_status)
                ld      b, SP_DETAIL_LEN
                ld      hl, sp_detail
                call    test_report
                jp      tr_terminate            ; di;halt in emulation, RET to trinload on hardware

; ---------------------------------------------------------------------------
SP_DETAIL_LEN:    equ 4                           ; [N_lo, N_hi, readT, readR]

settle_delay_count: defw 1                         ; 16-bit delay counter (poked by launcher/host; >= 1)
sp_status:          defb 0                         ; 0 = FRESH/settled, 1 = STALE
sp_detail:          defs SP_DETAIL_LEN

                include "build_udp_frame.asm"   ; build_udp_frame + PARAMS/PACKET
                include "encdrv.asm"             ; drv_init/drv_write + chk_trinity + wait_ready
                include "enc_link.asm"           ; drv_wait_link (PHY link-up, i127)
                include "test_report.asm"        ; test_report + TR_SRC_MAC/TR_*
