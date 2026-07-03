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
DBG_CLAIM_FIND_PRE:    equ &14        ; wrq_claim_record: about to bdos_find_record_for_strategy (CMD17 list reads)
DBG_CLAIM_SELECT_PRE:  equ &15        ; free record found; about to bdos_select_record (HRECORD hook)
DBG_CLAIM_SELECT_POST: equ &16        ; HRECORD select returned (the claim succeeded)
DBG_REARM_TIMEOUT:     equ &17        ; serve_rearm_enc's ENC busy-poll hit its bound (stuck shared bus = the i280b contention)
DBG_PRIOR_REARM_TIMEOUT: equ &18      ; at WRQ_ENTRY: the PRIOR WRQ's claim serve_rearm_enc timed out — latched + reported from OUTSIDE the §8e post-rearm dead ENC-TX window (i280b-b2f). &18 present ⇒ re-arm timed out (bus stayed busy); absent ⇒ re-arm succeeded but the ENC was not yet wire-ready
DBG_DATA_BLOCK:     equ &20           ; a DATA block accepted, about to sink/stage it
DBG_FLUSH_PRE:      equ &21           ; rrs_flush_sector entered: a full 512-byte sector is staged, about to write it
DBG_HWSAD_PRE:      equ &22           ; bdos_write_sector entered: about to RST 8 / DEFB 149 (B-DOS HWSAD)
DBG_HWSAD_POST:     equ &23           ; the HWSAD RST 8 returned (the per-sector SD write completed)
DBG_FINALIZE:       equ &30           ; final block: entering wd_finalize (flush+validate+claim)
DBG_FINALIZE_VALID: equ &31           ; record validated + claimed -> final ACK
DBG_FINALIZE_BAD:   equ &32           ; invalid image -> ERROR(3)
DBG_DONE_CTRL:      equ &40           ; "tftp.done" control received -> return to trinload
; i280b-b2i: runtime-paging value reports. Each is a TAG marker immediately followed
; by a SECOND marker whose code byte IS the register value (read as the UNKNOWN(&XX)
; that follows the tag). They pin the serve's real LMPR/HMPR at the HWSAD write — the
; decisive unknown for the §8k paged-pointer fix (is BD_WRITE_BUF's page nameable by
; the (H>>6)-1 encoding at write time, or is the prelude paging a different page in?).
DBG_HMPR_NEXT:      equ &50           ; the NEXT marker's code byte = HMPR (in a,(&fb)) at this point
DBG_LMPR_NEXT:      equ &51           ; the NEXT marker's code byte = LMPR (in a,(&fa)) at this point
; http_main (i70a): boot-path + per-file progress codes.
; DBG_HTTP_ENTRY fires as the FIRST marker, just after drv_init succeeds — the
; first point where the ENC is ready to transmit. All earlier markers would
; silently fail (ENC not yet initialized), so this serves as "I'm alive + past
; drv_init" confirmation. The EEPROM read already succeeded before drv_init.
DBG_HTTP_ENTRY:       equ &60         ; http_main: drv_init OK + EEPROM chunk populated
DBG_HTTP_EEPROM_OK:   equ &61         ; SD CSD read done (BD_RECORDS set) + ENC RX re-armed
DBG_HTTP_LINK_UP:     equ &62         ; PHY link up (drv_wait_link returned BC!=0)
DBG_HTTP_FILE_START:  equ &63         ; per-file fetch started (store_begin called)
DBG_HTTP_FILE_SAVED:  equ &64         ; per-file window persisted (HSAVE returned)
DBG_HTTP_FILE_VERIFY: equ &65         ; per-file verify done (store_end / conn_verify_final)
DBG_HTTP_ALL_DONE:    equ &66         ; all files fetched + persisted (ht_done)
DBG_HTTP_FAIL_CFG:    equ &70         ; fail: EEPROM chunk absent or bad (ht_fail_cfg)
DBG_HTTP_FAIL_INIT:   equ &71         ; fail: drv_init returned BC=0 (ht_fail_init)
DBG_HTTP_FAIL_LINK:   equ &72         ; fail: PHY link timeout (ht_fail_link)

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

; dbg_report_paging — emit the runtime LMPR/HMPR as two tag+value marker pairs
; (i280b-b2i). Only needed in serve debug builds (NETBOOT_SERVE_DBG, defined in
; netboot_serve.asm alongside NETBOOT_DEBUG). The serve uses it at HWSAD_PRE and
; FLUSH_PRE to pin the paged-pointer registers during raw sector writes; the
; http_main debug path has no paged writes and excluding it saves ~25 bytes that
; let http_main's debug binary stay within the 32 KB boot budget.
if defined(NETBOOT_SERVE_DBG)
dbg_report_paging:
                push    af
                ld      a, DBG_HMPR_NEXT
                call    dbg_marker
                in      a, (&fb)                 ; HMPR (section C/D page)
                call    dbg_marker
                ld      a, DBG_LMPR_NEXT
                call    dbg_marker
                in      a, (&fa)                 ; LMPR (section A/B page)
                call    dbg_marker
                pop     af
                ret
endif ; NETBOOT_SERVE_DBG

dbg_magic:          defb 83,68,66,71              ; "SDBG"
dbg_broadcast_mac:  defb &ff,&ff,&ff,&ff,&ff,&ff
dbg_broadcast_ip:   defb 255,255,255,255
dbg_port_be:        defb DBG_PORT >> 8, DBG_PORT & &ff
dbg_save_code:      defs 1
; i280b-b2f: enc_timed_out latched at the END of serve_rearm_enc and emitted at
; the NEXT WRQ_ENTRY as DBG_PRIOR_REARM_TIMEOUT. Serve-only (paired with the
; serve's serve_rearm_enc timeout path). Excluded in http_main debug builds
; (no serve_rearm_enc there) to save 1 byte.
if defined(NETBOOT_SERVE_DBG)
last_rearm_timed_out: defb 0
endif ; NETBOOT_SERVE_DBG
DBG_PAYLOAD:        defs 6
