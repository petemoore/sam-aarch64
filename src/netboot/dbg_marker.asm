; dbg_marker.asm — i271 network debug step-markers for the SAM netboot programs.
;
; A network debug channel so an AGENT (not a present human) can see how far a
; netboot program got on real Trinity hardware: instead of an RST &10 screen dump
; or a border colour only a watcher at the SAM can read, each instrumented step
; broadcasts a tiny UDP packet that the agent reads off the wire (tcpdump, or a
; small UDP listener). This is the i270 debug bottleneck removed — the WRQ/SD-write
; path emits a marker per step, so a hang localizes to the LAST marker seen.
;
; Compiled in only under -D NETBOOT_DEBUG (additive — production builds are byte-
; identical, no extra TX, no risk to the proven serve path). It reuses the
; hardware-proven TX primitives the bootable serve already links: build_udp_frame
; (build_udp_frame.asm), drv_write (encdrv.asm), and the SAM identity TR_SRC_MAC /
; TR_SRC_IP (test_report.asm) — so NETBOOT_DEBUG is only valid alongside the
; bootable serve build (NETBOOT_HOSTTEST undefined), where those are present.
;
; MARKER PACKET (the UDP payload), broadcast to 255.255.255.255:DBG_PORT:
;   off 0  magic    4   'S','D','B','G'
;   off 4  version  1   = DBG_VERSION
;   off 5  marker   1   = the step code (DBG_* below)
;
; Entry: dbg_marker — A = marker code. PRESERVES ALL REGISTERS, so a call can be
;   dropped at any handler boundary without disturbing the instrumented code.
; TX-ONLY: it only transmits (drv_write), never receives, so it is reliable even
;   after an SD transaction has disturbed the ENC RX path (RX is re-armed
;   elsewhere; TX is independent). The ONE constraint: never call it WHILE an SD
;   chip-select is asserted (between an SD command and its release) — the SD and
;   the ENC share the one-PIC Trinity controller. All call sites are at the
;   handler level, outside the SD transactions.

DBG_PORT:           equ 9001          ; UDP dest port for debug markers (distinct from TR_PORT 9000)
DBG_VERSION:        equ 1

; Step codes. Grouped by phase so a listener reads the pipeline at a glance.
DBG_WRQ_ENTRY:      equ &10           ; handle_wrq entered
DBG_WRQ_CLAIMED:    equ &11           ; free record claimed + ENC re-armed
DBG_WRQ_NOFREE:     equ &12           ; no free record -> ERROR(3)
DBG_WRQ_HANDSHAKE:  equ &13           ; about to send the OACK / ACK-0 handshake
DBG_DATA_BLOCK:     equ &20           ; a DATA block accepted, about to sink/stage it
DBG_FINALIZE:       equ &30           ; final block: entering wd_finalize (flush+validate+claim)
DBG_FINALIZE_VALID: equ &31           ; record validated + claimed -> final ACK
DBG_FINALIZE_BAD:   equ &32           ; invalid image -> ERROR(3)
DBG_DONE_CTRL:      equ &40           ; "tftp.done" control received -> return to trinload

dbg_marker:
                push    af
                push    bc
                push    de
                push    hl
                push    ix
                push    iy
                ld      (dbg_save_code), a

                ; --- assemble the marker record at DBG_PAYLOAD ---
                ld      hl, dbg_magic
                ld      de, DBG_PAYLOAD
                ld      bc, 4
                ldir                             ; magic "SDBG"
                ld      a, DBG_VERSION
                ld      (DBG_PAYLOAD+4), a
                ld      a, (dbg_save_code)
                ld      (DBG_PAYLOAD+5), a
                ld      hl, 6                     ; payload length = magic(4)+version(1)+marker(1)
                ld      (PARAM_PAYLOAD_LEN), hl

                ; --- fill the build_udp_frame parameter block (broadcast) ---
                ld      hl, dbg_broadcast_mac
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
                ld      hl, dbg_broadcast_ip
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ld      hl, dbg_port_be
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, dbg_port_be
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ld      hl, DBG_PAYLOAD
                ld      (PARAM_PAYLOAD_PTR), hl

                call    build_udp_frame          ; frame at PACKET, BC = total length
                ld      hl, PACKET
                call    drv_write                ; transmit (TX only)

                pop     iy
                pop     ix
                pop     hl
                pop     de
                pop     bc
                pop     af
                ret

dbg_magic:          defb 83,68,66,71              ; "SDBG"
dbg_broadcast_mac:  defb &ff,&ff,&ff,&ff,&ff,&ff
dbg_broadcast_ip:   defb 255,255,255,255
dbg_port_be:        defb DBG_PORT >> 8, DBG_PORT & &ff
dbg_save_code:      defs 1
DBG_PAYLOAD:        defs 6
