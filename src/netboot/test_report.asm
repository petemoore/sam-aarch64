; test_report.asm — reusable on-SAM test-result reporter (item i225).
;
; Emits a test result two ways from ONE code path, so the SAME binary reports
; identically in the koron-go ENC28J60 emulator (result read off TXFrames) and on
; real Trinity hardware (result read off a UDP listener on the LAN):
;   1. over the network — a broadcast UDP packet carrying a "SATR" report record;
;   2. on the SAM screen — the border colour (green = pass, red = fail).
;
; The network report lets an agent read the result DIRECTLY whether the binary
; was deployed to the emulator or the real SAM, so hardware-bound tests become
; agent-autonomous (no human needed to read a screen). It reuses the
; hardware-proven ENC driver (encdrv.asm: drv_init/drv_write) and the
; build_udp_frame primitive — the includer supplies those, the org, and a prior
; drv_init (HL -> the SAM MAC).
;
; REPORT RECORD (the UDP payload), little-endian:
;   off 0  magic    4   'S','A','T','R'
;   off 4  version  1   = TR_VERSION
;   off 5  test_id  2   identifies the test (LE)
;   off 7  status   1   0 = PASS, nonzero = a test-defined FAIL code
;   off 8  dlen     1   detail length (0..TR_DETAIL_MAX)
;   off 9  detail   dlen  test-specific bytes
;
; Entry: test_report
;   DE = test_id, A = status, B = detail length, HL -> detail bytes.
; Effect: builds the record at TR_PAYLOAD, sends it as a broadcast UDP packet
;   (src = TR_SRC_MAC/TR_SRC_IP, dst = ff:ff:ff:ff:ff:ff / 255.255.255.255, dst
;   port TR_PORT), then paints the border green (pass) or red (fail).
; Clobbers: AF, BC, DE, HL.

TR_PORT:         equ 9000          ; UDP dest port for test reports (arbitrary, unused)
TR_VERSION:      equ 1
TR_DETAIL_MAX:   equ 64
TR_HDR_LEN:      equ 9             ; magic(4)+version(1)+test_id(2)+status(1)+dlen(1)
TR_BORDER_PASS:  equ 4             ; SAM border colour: green
TR_BORDER_FAIL:  equ 2             ; SAM border colour: red

EMU_DETECT_PORT: equ &7F           ; unmapped SAM port for emulation detection (i228)
TR_MODE_EMU:     equ &E0           ; tr_terminate branch markers (recorded in TR_TERM_MODE)
TR_MODE_HW:      equ &A0

test_report:
                ; Stash the entry parameters (the buffer build below needs the
                ; registers).
                ld      (tr_save_id), de        ; test_id
                ld      (tr_save_status), a      ; status
                ld      a, b
                ld      (tr_save_dlen), a        ; detail length
                ld      (tr_save_dptr), hl       ; detail pointer

                ; --- assemble the report record at TR_PAYLOAD ---
                ld      hl, tr_magic
                ld      de, TR_PAYLOAD
                ld      bc, 4
                ldir                             ; magic
                ld      a, TR_VERSION
                ld      (TR_PAYLOAD+4), a
                ld      hl, (tr_save_id)
                ld      (TR_PAYLOAD+5), hl       ; test_id (LE)
                ld      a, (tr_save_status)
                ld      (TR_PAYLOAD+7), a
                ld      a, (tr_save_dlen)
                ld      (TR_PAYLOAD+8), a
                ; detail bytes (dlen from tr_save_dptr) at TR_PAYLOAD+9
                or      a
                jr      z, tr_no_detail
                ld      c, a
                ld      b, 0
                ld      hl, (tr_save_dptr)
                ld      de, TR_PAYLOAD+TR_HDR_LEN
                ldir
tr_no_detail:
                ; payload length = TR_HDR_LEN + dlen
                ld      a, (tr_save_dlen)
                add     a, TR_HDR_LEN
                ld      l, a
                ld      h, 0
                ld      (PARAM_PAYLOAD_LEN), hl

                ; --- fill the build_udp_frame parameter block ---
                ld      hl, tr_broadcast_mac
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, TR_SRC_MAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, TR_SRC_IP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, tr_broadcast_ip
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ld      hl, tr_port_be
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, tr_port_be
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ld      hl, TR_PAYLOAD
                ld      (PARAM_PAYLOAD_PTR), hl

                call    build_udp_frame          ; frame at PACKET, BC = total length
                ld      hl, PACKET
                call    drv_write                ; transmit

                ; --- screen indicator: border green (pass) / red (fail) ---
                ld      a, (tr_save_status)
                or      a
                ld      a, TR_BORDER_PASS
                jr      z, tr_paint
                ld      a, TR_BORDER_FAIL
tr_paint:
                out     (&fe), a
                ret

; ---------------------------------------------------------------------------
; tr_terminate — end a pushed test, doing the right thing for where it runs.
;
; Reads the unmapped EMU_DETECT_PORT (i228): real hardware floats the bus high to
; 0xFF; the emulator returns a distinct marker (the koron-go IO model returns
; 0x00). Then:
;   - emulation -> di;halt, the launch-independent clean stop the harness detects;
;   - hardware  -> RET, back to trinload (which pushed its listener as our return
;     address), so trinload stays alive for the next pushed test (the autonomous
;     loop — no power-cycle between tests).
; Records the branch taken in TR_TERM_MODE so a harness test can assert both
; paths. A payload ends with `call test_report` then `jp tr_terminate`.
; ---------------------------------------------------------------------------
tr_terminate:
                ; Read with IN A,(C) and B=0 so the port address is exactly &007F
                ; — the form the i228 probe characterized (&FF on hardware). The
                ; DB-form IN A,(&7F) would put A on the high address lines (reading
                ; &xx7F, an uncharacterized port): that froze the SAM on the first
                ; hardware run, and the harness masked it (koron-go does not model
                ; A on the high address lines for IN A,(n)).
                ld      c, EMU_DETECT_PORT
                ld      b, 0
                in      a, (c)                  ; &FF on hardware; marker (!=&FF) in emulation
                inc     a                        ; &FF -> 0 (Z) only on hardware
                jr      z, tr_term_hw
                ld      a, TR_MODE_EMU
                ld      (TR_TERM_MODE), a
                di
                halt
tr_term_hw:
                ld      a, TR_MODE_HW
                ld      (TR_TERM_MODE), a
                ret

tr_magic:         defb 83,65,84,82               ; "SATR"
tr_broadcast_mac: defb &ff,&ff,&ff,&ff,&ff,&ff
tr_broadcast_ip:  defb 255,255,255,255
tr_port_be:       defb TR_PORT >> 8, TR_PORT & &ff

; The SAM's own identity. The report is a broadcast, so these only label the
; sender; the emulator ignores them and a LAN listener sees them. Hardcoded to
; the known first-light values (a later revision can read them from the Trinity
; "Trinity Network" config chunk via read_chunk).
TR_SRC_MAC:       defb &02,&54,&52,&49,&4e,&bc   ; 02:54:52:49:4e:bc
TR_SRC_IP:        defb 192,168,2,75

tr_save_id:       defs 2
tr_save_status:   defs 1
tr_save_dlen:     defs 1
tr_save_dptr:     defs 2
TR_TERM_MODE:     defs 1            ; tr_terminate branch marker (TR_MODE_EMU/HW)
TR_PAYLOAD:       defs TR_HDR_LEN+TR_DETAIL_MAX
