; smoke_test.asm — the Trinity netboot bring-up smoke test (i94): the smallest
; bootable program that proves the SAM + Quazar Trinity Ethernet path comes up
; and talks on Pete's real network.
;
; It does ONE observable network action: answer an ARP request for the SAM's own
; IP with an ARP reply. So on real hardware Pete points another machine on the
; same LAN (his Pi) at the SAM's IP — `ping <sam-ip>` or `arping <sam-ip>` — and
; sees the SAM's MAC come back. That single round trip confirms: the Trinity
; board is detected, drv_init programmed the ENC28J60, the ENC receives a
; broadcast frame, and drv_write transmits a reply onto the wire. It is the
; "hello, the wire works" before the full netboot server (i86/i83).
;
; TWO entry points:
;
;   smoke_serve_once  (host-verifiable) — read one frame; if it is an ARP request
;     for SMOKE_IP, build + transmit the ARP reply. Out: BC = bytes sent (0 if
;     the frame was ignored). The host harness drives this directly with the
;     emulated Trinity (smoke_test_test.go), asserting the reply on the virtual
;     wire is byte-for-byte the Go authority smoke.Responder.OnFrame output.
;
;   smoke_main  (the bootable entry) — CALL 32768 runs: read the SAM's MAC + IP
;     from the Trinity EEPROM "Trinity Network " chunk, run drv_init, then loop
;     forever calling smoke_serve_once. The harness drives it too: with the
;     EEPROM modelled (eeprom.go) netboot_boot_test.go runs smoke_main end-to-end
;     and asserts the EEPROM-read identity answers an injected ARP. Real ENC
;     TX/RX timing + the live round trip stay hardware-gated (CLAUDE.md §5).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/smoke/smoke.go::Responder.OnFrame. The parse-request +
; build-reply dispatch mirror OnFrame step for step; the reply bytes are produced
; by the host-verified build_arp_reply primitive. So the emitted ARP reply on the
; virtual wire is byte-for-byte the Go authority output — the host check asserts
; exactly that.
;
; PROVENANCE: ARP wire format is RFC 826; the framing layout matches the Go
; `frame` package and trinload's `packet` buffer; the EEPROM config read +
; drv_init bring-up mirror trinload's own startup (trinload.asm:24-54, Simon
; Owen, BSD-style licence). The observable-action choice (answer ARP) is the
; lightest possible wire confirmation (docs/notes/trinity-capabilities.md).
;
; VERIFICATION: smoke_serve_once is host-verifiable end-to-end under the i80
; emulation — inject an ARP request, dispatch + build, drv_write, assert the
; reply on the virtual wire matches the Go Responder byte-for-byte. smoke_main
; (the EEPROM config read + drv_init + the serve loop) is host-verifiable too,
; via netboot_boot_test.go against the modelled EEPROM. Still hardware-gated:
; the real ENC28J60 TX/RX timing and the actual round trip with Pete's Pi
; (CLAUDE.md §5). Emulation-verified is not hardware-verified.

                org     &8000

                ; The boot entry (CALL 32768) must be the first instruction at
                ; &8000. The host harness invokes routines by symbol (it never
                ; CALLs 32768), so this jp is the hardware boot entry — but the
                ; harness still runs smoke_main itself (netboot_boot_test.go).
                jp      smoke_main

; ===========================================================================
; The composed state machine. It supplies the single org; the included
; primitives org only under -D NETBOOT_STANDALONE, so here they inherit this
; org. encdrv.asm and eeprom.asm are vendored drivers included after the code.
; ===========================================================================

; Received-frame field offsets (Ethernet header + ARP payload; SM_ prefix avoids
; clashing with build_arp_reply's AR_* equs included below).
SM_OFF_ETHERTYPE: equ 12
SM_ARP_BASE:      equ 14                 ; ARP payload begins after the Eth header
SM_ARP_HTYPE:     equ 14 + 0            ; 2 bytes  hardware type
SM_ARP_PTYPE:     equ 14 + 2            ; 2 bytes  protocol type
SM_ARP_HLEN:      equ 14 + 4            ; 1 byte   hardware address length
SM_ARP_PLEN:      equ 14 + 5            ; 1 byte   protocol address length
SM_ARP_OPER:      equ 14 + 6            ; 2 bytes  operation
SM_ARP_SHA:       equ 14 + 8            ; 6 bytes  sender hardware address (MAC)
SM_ARP_SPA:       equ 14 + 14           ; 4 bytes  sender protocol address (IP)
SM_ARP_TPA:       equ 14 + 24           ; 4 bytes  target protocol address (IP)

SM_ETYPE_ARP:     equ &0806
SM_HTYPE_ETH:     equ 1
SM_PTYPE_IPV4:    equ &0800
SM_HLEN:          equ 6
SM_PLEN:          equ 4
SM_OP_REQUEST:    equ 1

; ---------------------------------------------------------------------------
; smoke_serve_once — read one frame; if it is a well-formed ARP request for
; SMOKE_IP, transmit an ARP reply announcing SMOKE_MAC.
;
; In:  the emulated Trinity is attached; SMOKE_MAC / SMOKE_IP are filled and
;      drv_init has run.
; Out: BC = bytes transmitted (the reply frame length), or 0 if the frame was
;      not an ARP request for SMOKE_IP (nothing sent — keep listening).
;
; The accept tests + the learned reply destination mirror the Go authority
; smoke.Responder.OnFrame (frame.ParseARPRequest + frame.BuildARPReply): EtherType
; ARP, HTYPE=Ethernet, PTYPE=IPv4, HLEN=6, PLEN=4, OPER=request, target protocol
; address == SMOKE_IP. The reply unicasts back to the asker (its MAC from the
; request's sender-hardware-address, its IP from the sender-protocol-address).
; ---------------------------------------------------------------------------
smoke_serve_once:
                ; --- receive a frame into SM_RXBUF ----------------------
                ld      hl, SM_RXBUF
                call    drv_read               ; BC = length (0 if nothing)
                ld      a, b
                or      c
                jp      z, smoke_none          ; empty wire: nothing to do

                ; --- EtherType == ARP (&0806, big-endian)? --------------
                ld      a, (SM_RXBUF + SM_OFF_ETHERTYPE)
                cp      SM_ETYPE_ARP >> 8
                jp      nz, smoke_none
                ld      a, (SM_RXBUF + SM_OFF_ETHERTYPE + 1)
                cp      SM_ETYPE_ARP & &ff
                jp      nz, smoke_none

                ; --- HTYPE == Ethernet (1, big-endian)? -----------------
                ld      a, (SM_RXBUF + SM_ARP_HTYPE)
                or      a                      ; high byte must be 0
                jp      nz, smoke_none
                ld      a, (SM_RXBUF + SM_ARP_HTYPE + 1)
                cp      SM_HTYPE_ETH
                jp      nz, smoke_none
                ; --- PTYPE == IPv4 (&0800, big-endian)? -----------------
                ld      a, (SM_RXBUF + SM_ARP_PTYPE)
                cp      SM_PTYPE_IPV4 >> 8
                jp      nz, smoke_none
                ld      a, (SM_RXBUF + SM_ARP_PTYPE + 1)
                cp      SM_PTYPE_IPV4 & &ff
                jp      nz, smoke_none
                ; --- HLEN == 6, PLEN == 4? ------------------------------
                ld      a, (SM_RXBUF + SM_ARP_HLEN)
                cp      SM_HLEN
                jp      nz, smoke_none
                ld      a, (SM_RXBUF + SM_ARP_PLEN)
                cp      SM_PLEN
                jp      nz, smoke_none

                ; --- OPER == request (1, big-endian)? -------------------
                ld      a, (SM_RXBUF + SM_ARP_OPER)
                or      a                      ; high byte must be 0
                jp      nz, smoke_none
                ld      a, (SM_RXBUF + SM_ARP_OPER + 1)
                cp      SM_OP_REQUEST
                jp      nz, smoke_none

                ; --- target protocol address == SMOKE_IP? (4 bytes) -----
                ld      hl, SM_RXBUF + SM_ARP_TPA
                ld      de, SMOKE_IP
                ld      b, 4
smoke_ip_cmp:
                ld      a, (de)
                cp      (hl)
                jp      nz, smoke_none
                inc     hl
                inc     de
                djnz    smoke_ip_cmp

                ; --- it is asking for us: build the ARP reply -----------
                ; src MAC/IP = ours (the answer)
                ld      hl, SMOKE_MAC
                ld      de, AR_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, SMOKE_IP
                ld      de, AR_SRC_IP
                ld      bc, 4
                ldir
                ; dst MAC = the asker's (its ARP sender-hardware-address)
                ld      hl, SM_RXBUF + SM_ARP_SHA
                ld      de, AR_DST_MAC
                ld      bc, 6
                ldir
                ; dst IP = the asker's (its ARP sender-protocol-address)
                ld      hl, SM_RXBUF + SM_ARP_SPA
                ld      de, AR_DST_IP
                ld      bc, 4
                ldir

                call    build_arp_reply        ; frame at AR_PACKET, BC = length

                ; --- transmit -------------------------------------------
                push    bc                     ; save frame length for the return
                ld      hl, AR_PACKET          ; HL = frame, BC = length
                call    drv_write              ; transmit (returns BC=1 success)
                pop     bc                     ; BC = the frame length sent
                ret

smoke_none:
                ld      bc, 0
                ret

; ===========================================================================
; The bootable entry. CALL 32768 lands here on boot. It runs in the host harness
; too — netboot_boot_test.go drives it against the modelled EEPROM + ENC.
; ===========================================================================

; smoke_main — read the SAM's MAC + IP from the Trinity EEPROM "Trinity Network "
; chunk into SMOKE_MAC/SMOKE_IP, init the ENC28J60, then loop forever answering
; ARP requests for SMOKE_IP. Mirrors trinload's startup (trinload.asm:24-54).
;
; On failure (no Trinity, no settings, init failed) it sets a distinctive border
; colour and halts — Pete sees the colour and knows which stage failed (the
; encdrv driver already paints tx/rx borders, so a running smoke test shows the
; green/black rx flashes + a red/black tx flash on each answered ARP).
smoke_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk ----
                ld      a, 1
                ld      (part), a              ; part 1
                ld      (total), a             ; of 1
                ld      hl, smoke_chunk_name   ; "Trinity Network "
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index             ; find the chunk's index entry
                ld      a, (value)
                and     a
                jp      z, smoke_fail_cfg      ; settings missing
                call    read_chunk             ; read the chunk contents
                ld      a, (value)
                and     a
                jp      z, smoke_fail_cfg      ; flash read failed

                ; --- copy sam_mac (chunk+0) / sam_ip (chunk+6) into CONFIG
                ld      hl, chunk + 0
                ld      de, SMOKE_MAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, SMOKE_IP
                ld      bc, 4
                ldir

                ; --- init the ENC28J60 with the SAM's real MAC -----------
                ld      hl, SMOKE_MAC
                call    drv_init               ; BC=1 success, BC=0 failure
                ld      a, b
                or      c
                jp      z, smoke_fail_init

                ; --- forever: answer ARP requests for our IP -------------
smoke_loop:
                call    smoke_serve_once
                jr      smoke_loop

smoke_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
smoke_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

smoke_chunk_name: defm "Trinity Network "     ; the flash chunk holding MAC+IP

; ===========================================================================
; CONFIG block — the SAM's identity the smoke logic reads. smoke_main fills it
; from the EEPROM (netboot_boot_test.go exercises that path); the smoke_serve_once
; tests write it directly (SMOKE_MAC / SMOKE_IP) so the Z80 and the Go authority
; share one identity and produce identical reply bytes.
; ===========================================================================
SMOKE_MAC:        defs 6
SMOKE_IP:         defs 4

SM_RXBUF:         defs 1518                ; the received-frame buffer

; ---------------------------------------------------------------------------
; Composed primitives + the vendored drivers. build_arp_reply is the
; host-verified ARP-reply builder; encdrv.asm is the real Trinity ENC28J60
; driver; eeprom.asm is the real flash config reader (modelled by eeprom.go).
; ---------------------------------------------------------------------------
                include "build_arp_reply.asm"
                include "encdrv.asm"
                include "eeprom.asm"
