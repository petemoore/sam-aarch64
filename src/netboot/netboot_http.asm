; netboot_http.asm — the i70 integrated HTTP fetch phase machine (the capstone).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/http/fetcher.go::Fetcher — the HTTP analogue of the TFTP
; client phase machine (netboot_client.asm). It originates the whole firmware
; self-provisioning fetch a SAM needs: ARP-for-server -> TCP active-open
; handshake -> HTTP/1.0 GET -> accumulate the streamed response -> end on the
; server's FIN (HTTP/1.0 closes after the body).
;
; It composes the already-host-verified pieces, adding no new packet arithmetic:
;   - http_get.asm  (included whole) -> tcp_conn.asm (the connection + the SYN/
;     ACK/data/FIN state machine) -> build_tcp_segment.asm + the real driver
;     encdrv.asm, plus http_get_start / http_build_request / http_parse_response,
;   - build_arp_request.asm -> the fresh ARP request (learn the server MAC).
;
; The driver is rx-driven exactly like tftp's netboot_client.asm; each phase does
; its own drv_read so the receive phase can hand the whole step to the proven
; tcp_conn_recv:
;
;   http_fetch_first:
;     fill the ARP params from the connection config, build + drv_write the
;     broadcast ARP request for the server IP. Out: BC = frame length.
;   http_fetch_onframe (dispatch on FETCH_PHASE):
;     PH_ARP       : drv_read; an ARP reply for CONN_SERVER_IP -> learn the server
;                    MAC into CONN_SERVER_MAC, phase = PH_HANDSHAKE, tcp_connect
;                    (the SYN). Any other frame -> BC = 0 (keep waiting).
;     PH_HANDSHAKE : drv_read; the SYN-ACK acking our SYN (port + flags + ack
;                    checks mirroring conn_syn_sent) -> rcvNxt = serverSeq+1,
;                    CONN_STATE = ESTABLISHED, phase = PH_RECV, then http_get_start
;                    sends the GET. The GET segment's ACK field completes the
;                    handshake AND carries the request payload (RFC 793 permits
;                    data on the handshake-completing ACK) -- one segment, so the
;                    bare handshake ACK is never sent separately.
;     PH_RECV      : tcp_conn_recv (reads + ACKs a DATA / FIN-ACKs the server FIN,
;                    accumulating the body into CONN_DATA); the FIN moves the
;                    connection to FIN_WAIT -> phase = PH_DONE.
;     PH_DONE      : BC = 0 (nothing more to send).
;
; Done is read from FETCH_PHASE (== PH_DONE), the accumulated response from
; CONN_DATA / CONN_DATA_LEN, and the parse from http_parse_response. The phase
; transitions + the seq/ack arithmetic mirror Fetcher.OnFrame step for step, and
; the frames are produced by the host-verified builders, so the wire frames are
; byte-for-byte the Go Fetcher's -- the host check netboot_http_test.go asserts.
;
; PROVENANCE: the fetch phase machine is fetcher.go (RFC 793 handshake + RFC 826
; ARP + HTTP/1.0); the framing is the included primitives (trinload-derived,
; Simon Owen BSD-style licence). VERIFICATION: host-verifiable end-to-end over the
; i80 emulation -- inject each server frame, assert the emitted ARP/SYN/GET/ACK/
; FIN-ACK on the virtual wire matches the Go Fetcher byte-for-byte. NOT
; host-verifiable: a real fetch against a live HTTP server, the B-DOS HSAVE
; write-out of the body, and the real ENC silicon -- gated on real Trinity
; (CLAUDE.md §5). Emulation-verified is not hardware-verified.

                include "http_get.asm"          ; org &8000 + tcp_conn + http_* +
                                                ; build_tcp_segment + encdrv
                include "build_arp_request.asm"  ; the fresh ARP request primitive

ARP_OP_REPLY:   equ 2                           ; ARP OPER = reply (request is in
                                                ; build_arp_request.asm)

; Fetch phases (mirror http/fetcher.go Phase).
PH_ARP:         equ 0
PH_HANDSHAKE:   equ 1
PH_RECV:        equ 2
PH_DONE:        equ 3

; ---------------------------------------------------------------------------
; http_fetch_first — broadcast the ARP request for the server IP (the first frame
; on the wire). Mirrors Fetcher.First().  Out: BC = frame length.
; ---------------------------------------------------------------------------
http_fetch_first:
                ; fill build_arp_request's params from the connection config.
                ld      hl, CONN_CLIENT_MAC
                ld      de, ARP_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CONN_CLIENT_IP
                ld      de, ARP_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, CONN_SERVER_IP
                ld      de, ARP_TARGET_IP
                ld      bc, 4
                ldir
                call    build_arp_request       ; BC = 42, frame at ARP_PACKET
                push    bc
                ld      hl, ARP_PACKET
                call    drv_write
                pop     bc
                ret

; ---------------------------------------------------------------------------
; http_fetch_onframe — process one received frame and emit the next, dispatching
; on the fetch phase. Mirrors Fetcher.OnFrame(). Out: BC = bytes transmitted (0
; if nothing); the caller reads FETCH_PHASE for done (== PH_DONE).
; ---------------------------------------------------------------------------
http_fetch_onframe:
                ld      a, (FETCH_PHASE)
                cp      PH_ARP
                jp      z, fetch_arp
                cp      PH_HANDSHAKE
                jp      z, fetch_handshake
                cp      PH_RECV
                jp      z, fetch_recv
                ; PH_DONE (or anything else): nothing to send.
fetch_none:
                ld      bc, 0
                ret

; --- PH_ARP: learn the server MAC from a matching ARP reply, then SYN ----------
fetch_arp:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, fetch_none           ; empty wire

                ; EtherType == ARP (&0806, big-endian)?
                ld      a, (RXBUF + OFF_ETHERTYPE)
                cp      ETHERTYPE_ARP >> 8
                jp      nz, fetch_none
                ld      a, (RXBUF + OFF_ETHERTYPE + 1)
                cp      ETHERTYPE_ARP & &ff
                jp      nz, fetch_none
                ; HTYPE == Ethernet (1)?
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_HTYPE)
                or      a
                jp      nz, fetch_none
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_HTYPE + 1)
                cp      ARP_HTYPE_ETH
                jp      nz, fetch_none
                ; PTYPE == IPv4 (&0800)?
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_PTYPE)
                cp      ARP_PTYPE_IPV4 >> 8
                jp      nz, fetch_none
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_PTYPE + 1)
                cp      ARP_PTYPE_IPV4 & &ff
                jp      nz, fetch_none
                ; HLEN == 6, PLEN == 4?
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_HLEN)
                cp      ARP_HLEN
                jp      nz, fetch_none
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_PLEN)
                cp      ARP_PLEN
                jp      nz, fetch_none
                ; OPER == reply (2)?
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_OPER)
                or      a
                jp      nz, fetch_none
                ld      a, (RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_OPER + 1)
                cp      ARP_OP_REPLY
                jp      nz, fetch_none

                ; sender protocol address == CONN_SERVER_IP? (4 bytes)
                ld      hl, RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_SPA
                ld      de, CONN_SERVER_IP
                ld      b, 4
fetch_arp_ipcmp:
                ld      a, (de)
                cp      (hl)
                jp      nz, fetch_none
                inc     hl
                inc     de
                djnz    fetch_arp_ipcmp

                ; learn the server MAC from the ARP sender-hardware-address.
                ld      hl, RXBUF + OFF_ARP_PAYLOAD + OFF_ARP_SHA
                ld      de, CONN_SERVER_MAC
                ld      bc, 6
                ldir
                ; phase = HANDSHAKE; originate the active open (the SYN).
                ld      a, PH_HANDSHAKE
                ld      (FETCH_PHASE), a
                jp      tcp_connect             ; sends the SYN, BC = its length

; --- PH_HANDSHAKE: validate the SYN-ACK, then send the GET (piggybacked ACK) ---
fetch_handshake:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, fetch_none

                ; dst port == our client port? (2 big-endian bytes)
                ld      a, (RXBUF + CRX_TCP_DSTPORT)
                ld      hl, CONN_CLIENT_PORT
                cp      (hl)
                jp      nz, fetch_none
                ld      a, (RXBUF + CRX_TCP_DSTPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, fetch_none
                ; src port == server port?
                ld      a, (RXBUF + CRX_TCP_SRCPORT)
                ld      hl, CONN_SERVER_PORT
                cp      (hl)
                jp      nz, fetch_none
                ld      a, (RXBUF + CRX_TCP_SRCPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, fetch_none

                ; flags carry SYN and ACK?
                ld      a, (RXBUF + CRX_TCP_FLAGS)
                ld      b, a
                and     CF_SYN
                jp      z, fetch_none
                ld      a, b
                and     CF_ACK
                jp      z, fetch_none
                ; ack == sndNxt (it acks our SYN)?
                ld      hl, RXBUF + CRX_TCP_ACK
                ld      de, CONN_SND_NXT
                call    tcp_cmp32
                jp      nz, fetch_none

                ; rcvNxt = serverSeq + 1 (their SYN consumes one).
                ld      hl, RXBUF + CRX_TCP_SEQ
                ld      de, CONN_RCV_NXT
                ld      bc, 4
                ldir
                call    tcp_inc32_rcvnxt
                ld      a, ST_ESTABLISHED
                ld      (CONN_STATE), a
                ; phase = RECV; send the GET as the single handshake-completing
                ; ACK+data segment (seq = sndNxt, ack = rcvNxt).
                ld      a, PH_RECV
                ld      (FETCH_PHASE), a
                jp      http_get_start          ; builds + sends the GET, BC = len

; --- PH_RECV: hand the step to tcp_conn_recv; the FIN completes the fetch ------
fetch_recv:
                call    tcp_conn_recv           ; reads + ACKs DATA / FIN-ACKs FIN,
                                                ; accumulates into CONN_DATA, BC = tx
                ; The server FIN moves the connection to FIN_WAIT -> done. We test
                ; only ST_FIN_WAIT (where the Go Fetcher also accepts ST_CLOSED):
                ; reaching FIN_WAIT sets PH_DONE here, so PH_RECV never runs again
                ; and ST_CLOSED (the final-ACK transition) is unreachable mid-recv.
                ld      a, (CONN_STATE)
                cp      ST_FIN_WAIT
                ret     nz                      ; still established: BC preserved
                ld      a, PH_DONE
                ld      (FETCH_PHASE), a
                ret

; ===========================================================================
; Real-hardware bootable entry (excluded from the host harness build, which has
; no EEPROM / real silicon / B-DOS store). CALL 32768 lands here on boot.
;
; http_main mirrors netboot_client.asm's client_main exactly for the
; non-host-verifiable hardware paths (the EEPROM read, the section-C source
; paging, the HSAVE write-out) — those are NOT host-verifiable (no ROM /
; SAMDOS / RST 8 in the harness) and are validated only on real Trinity
; (CLAUDE.md §5). The wire/fetch loop (http_fetch_first + http_fetch_onframe)
; IS host-verified over the i80 emulation. Emulation-verified is not
; hardware-verified.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

; http_main — read the SAM's MAC + IP from the Trinity EEPROM, fill the
; connection config (CONN_*), set the firmware-server endpoint + the output
; filename (fixed defaults), init the ENC28J60, broadcast the ARP request, then
; loop receiving until the fetch completes (FETCH_PHASE == PH_DONE); parse the
; response, stage the body into a section-C STAGING buffer, and HSAVE it to
; Trinity storage via the B-DOS seam. On any bring-up failure it sets a
; distinctive border colour and halts.
http_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk ---
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, ht_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, ht_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, ht_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into the conn config.
                ld      hl, chunk + 0
                ld      de, CONN_CLIENT_MAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONN_CLIENT_IP
                ld      bc, 4
                ldir

                ; --- the rest of the connection config (edit for your network) --
                ; our client port (an ephemeral high port), big-endian.
                ld      a, &C0
                ld      (CONN_CLIENT_PORT), a
                ld      a, &00
                ld      (CONN_CLIENT_PORT + 1), a
                ; the firmware HTTP server IP (the host serving the blob).
                ld      hl, ht_server_ip
                ld      de, CONN_SERVER_IP
                ld      bc, 4
                ldir
                ; the server port = 80 (HTTP), big-endian.
                ld      a, 80 >> 8
                ld      (CONN_SERVER_PORT), a
                ld      a, 80 & &ff
                ld      (CONN_SERVER_PORT + 1), a
                ; our initial send sequence (a fixed ISS; big-endian).
                ld      hl, ht_iss
                ld      de, CONN_ISS
                ld      bc, 4
                ldir

                ; clear the connection + fetch state.
                xor     a
                ld      (CONN_STATE), a
                ld      (FETCH_PHASE), a        ; PH_ARP

                ; --- init the ENC28J60 with the SAM's real MAC ----------
                ld      hl, CONN_CLIENT_MAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, ht_fail_init

                ; --- broadcast the ARP request, then receive until done --
                call    http_fetch_first
ht_fetch_loop:
                call    http_fetch_onframe
                ld      a, (FETCH_PHASE)
                cp      PH_DONE
                jr      nz, ht_fetch_loop

                ; --- parse the response: HTTP_BODY_OFF + HTTP_STATUS ---
                call    http_parse_response

                ; body pointer = CONN_DATA + HTTP_BODY_OFF.
                ld      hl, (HTTP_BODY_OFF)
                ld      de, CONN_DATA
                add     hl, de
                ld      (HT_BODY_PTR), hl
                ; body length = CONN_DATA_LEN - HTTP_BODY_OFF.
                ld      hl, (CONN_DATA_LEN)
                ld      de, (HTTP_BODY_OFF)
                or      a
                sbc     hl, de
                ld      (HT_BODY_LEN), hl

                ; --- stage the body into the section-C STAGING buffer ----
                ; Copy the body bytes to STAGING so the HSAVE source-paging math
                ; below (mirroring client_main's STAGING >> 14 / &3FFF | &8000)
                ; is valid for a known-addressed section-C buffer, rather than
                ; improvising paging for CONN_DATA's address.
                ld      bc, (HT_BODY_LEN)
                ld      a, b
                or      c
                jr      z, ht_no_body
                ld      hl, (HT_BODY_PTR)
                ld      de, STAGING
                ldir
ht_no_body:

                ; --- write the staged body to Trinity storage (B-DOS) ----
                ; select the mass-storage record (0 = the Trinity flat store).
                xor     a
                call    bdos_select_record
                ; build the HSAVE UIFA: name, source page/offset = STAGING, size.
                ld      hl, ht_out_name
                ld      (BD_NAME_PTR), hl
                ; STAGING lives in section C; its page/offset are the HMPR page the
                ; loader maps + the &xxxx offset. Use the staging label's address.
                ld      a, STAGING >> 14        ; the physical page of STAGING
                ld      (BD_SAVE_PAGE), a
                ld      hl, STAGING & &3FFF | &8000   ; section-C offset
                ld      (BD_SAVE_ADDR), hl
                ld      hl, (HT_BODY_LEN)       ; staged body length
                ld      (BD_SAVE_SIZE), hl
                call    bdos_fill_save_uifa
                call    bdos_save_hook          ; HSAVE (real B-DOS only)

                ; success: green border, halt (the body is on Trinity storage).
                ld      a, 4
                out     (&fe), a
                di
                halt

ht_fail_cfg:
                ld      a, 2                    ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
ht_fail_init:
                ld      a, 1                    ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

ht_chunk_name:    defm "Trinity Network "      ; the flash chunk holding MAC+IP
; The fixed firmware-fetch target — edit for your network. The HTTP path + host
; are baked into http_get.asm (HTTP_PATH / HTTP_HOST).
ht_server_ip:     defb 192, 168, 0, 1          ; the HTTP server holding the blob
ht_iss:           defb 0, 0, 4, 0              ; our initial send sequence (BE)
ht_out_name:      defm "firmware.bin"          ; the Trinity output filename
                  defb 0

; Body staging — section-C buffer the fetched body is copied into so the HSAVE
; source-paging math (STAGING >> 14 / STAGING & &3FFF | &8000) matches
; client_main's exactly. Sized to the response ceiling (16 KB, per i24).
HT_BODY_PTR:      defs 2                        ; CONN_DATA + HTTP_BODY_OFF
HT_BODY_LEN:      defs 2                        ; CONN_DATA_LEN - HTTP_BODY_OFF
STAGING:          defs 16384                    ; the staged body bytes

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; The storage seam (host-verified field arithmetic; the RST 8 hook dispatch is
; itself NETBOOT_HOSTTEST-guarded, so this include is safe in both builds) and
; the real flash config reader (real-hardware path only).
; ===========================================================================
                include "bdos_seam.asm"
                if defined(NETBOOT_HOSTTEST)==0
                include "eeprom.asm"
                endif

; Fetch state (the connection state lives in tcp_conn.asm's CONN_* block).
FETCH_PHASE:    defb PH_ARP
