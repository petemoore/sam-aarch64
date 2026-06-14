; tftp_build.asm — the i83 TFTP server's reply-packet builders: OACK, DATA,
; and ERROR. These are the SAM-side Z80 ports of the netboot-oracle Go
; authorities tools/netboot-oracle/tftp/tftp.go::BuildOACK / BuildDATA /
; BuildError, each verified byte-for-byte against the real Pi 400 capture.
;
; A TFTP packet is a 2-byte big-endian opcode followed by the body:
;   OACK  (6): each option "name\0value\0" pair, concatenated.
;   DATA  (3): a 2-byte big-endian block number, then up to blksize data bytes.
;   ERROR (5): a 2-byte big-endian error code, then a NUL-terminated message.
;
; PROVENANCE: TFTP wire format (RFC 1350 + 2347 OACK). The captured OACK/DATA/
; ERROR are docs/notes/pi-netboot-capture-analysis.md §2.
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs each builder, and asserts the emitted packet matches the captured /
; Go-authority bytes exactly. The wire send (ENC28J60) is NOT host-verifiable
; (gated on i80 / real Trinity).

                org     &8000

OP_DATA:          equ 3
OP_ERROR:         equ 5
OP_OACK:          equ 6

; ---------------------------------------------------------------------------
; build_oack — opcode 6 then the pre-formatted option bytes verbatim.
;
; The caller assembles the option pairs (each "name\0value\0") into OACK_OPTS
; with the byte count in OACK_OPTS_LEN — exactly what the server negotiated
; (e.g. blksize\0<n>\0 tsize\0<size>\0). This routine just frames them; the
; option *strings* are the server's business (it echoes the accepted blksize
; and reports the real tsize).
;
; Out: packet at TBUF; BC = total length (2 + option bytes).
; ---------------------------------------------------------------------------
build_oack:
                ld      hl, TBUF
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_OACK          ; HL = TBUF+1
                inc     hl                     ; HL = TBUF+2 (dest)
                ld      bc, (OACK_OPTS_LEN)
                ld      a, b
                or      c
                jr      z, oack_done
                ex      de, hl                 ; DE = TBUF+2 dest
                ld      hl, OACK_OPTS          ; source
                ldir                           ; copy BC option bytes
oack_done:
                ld      hl, (OACK_OPTS_LEN)
                ld      bc, 2
                add     hl, bc
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; build_data — opcode 3, block number (big-endian), then the data bytes.
;
; In:  DATA_BLOCK   2 bytes  block number (big-endian as it goes on the wire)
;      DATA_PTR     2 bytes  pointer to the data bytes
;      DATA_LEN     2 bytes  number of data bytes (0..blksize)
; Out: packet at TBUF; BC = total length (4 + DATA_LEN).
; ---------------------------------------------------------------------------
build_data:
                ld      hl, TBUF
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_DATA
                inc     hl
                ; block number (big-endian) <- DATA_BLOCK (stored big-endian)
                ld      a, (DATA_BLOCK)
                ld      (hl), a
                inc     hl
                ld      a, (DATA_BLOCK+1)
                ld      (hl), a
                inc     hl                     ; HL = TBUF+4
                ; copy DATA_LEN data bytes
                ld      de, (DATA_LEN)
                ld      a, d
                or      e
                jr      z, data_done
                ld      d, h
                ld      e, l                   ; DE = dest TBUF+4
                ld      hl, (DATA_PTR)         ; HL = source
                ld      bc, (DATA_LEN)
                ldir
data_done:
                ld      hl, (DATA_LEN)
                ld      bc, 4
                add     hl, bc
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; build_error — opcode 5, error code (big-endian), message, NUL.
;
; In:  ERR_CODE  2 bytes  error code (big-endian as on the wire)
;      ERR_MSG_PTR 2 bytes pointer to the NUL-terminated message
; Out: packet at TBUF; BC = total length (4 + strlen(msg) + 1).
;      The message (incl. its terminating NUL) is copied verbatim.
; ---------------------------------------------------------------------------
build_error:
                ld      hl, TBUF
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_ERROR
                inc     hl
                ld      a, (ERR_CODE)
                ld      (hl), a
                inc     hl
                ld      a, (ERR_CODE+1)
                ld      (hl), a
                inc     hl                     ; HL = TBUF+4, dest cursor
                ; copy the message up to and including its NUL.
                ld      de, (ERR_MSG_PTR)      ; DE = source
err_copy:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                or      a
                jr      nz, err_copy
                ; HL now points just past the copied NUL; length = HL - TBUF.
                ld      de, TBUF
                and     a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ret

; ===========================================================================
; Data region — parameter blocks and the output packet buffer.
; ===========================================================================
OACK_OPTS_LEN:    defs 2
DATA_BLOCK:       defs 2                 ; big-endian on the wire
DATA_PTR:         defs 2
DATA_LEN:         defs 2
ERR_CODE:         defs 2                 ; big-endian on the wire
ERR_MSG_PTR:      defs 2

OACK_OPTS:        defs 256               ; caller-formatted option bytes
TBUF:             defs 1518              ; the output packet buffer
