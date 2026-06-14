; tftp_client.asm — the i82 TFTP client's request side: build a read request
; (RRQ) and parse the server's option-acknowledgement (OACK).
;
; These are the SAM-side Z80 ports of the netboot-oracle Go authorities
; tools/netboot-oracle/tftp/tftp.go::BuildRRQ and ::ParseOACK. The client
; originates an RRQ to a server's port 69 (octet mode), requesting the settled
; option set (blksize=1428, tsize=0, timeout=2, windowsize=4 — research note
; §5.7), then parses the OACK to learn the negotiated blksize and the file's
; tsize (which sizes the B-DOS pre-allocation).
;
; An RRQ payload is: a 2-byte big-endian opcode (1 = RRQ), then NUL-terminated
; C-strings: filename, mode, then each option name NUL value NUL. An OACK is a
; 2-byte big-endian opcode (6) then the option name/value pairs.
;
; build_rrq frames a *pre-formatted* option blob (each "name\0value\0"), exactly
; as build_oack does for the server — the option strings are the client's
; business (the fixed ClientOptionSet on hardware), and framing them this way
; keeps the routine byte-exact against the captured wire bytes.
;
; PROVENANCE: TFTP wire format (RFC 1350 + 2347 OACK + 2348 blksize + 2349
; tsize/timeout + 7440 windowsize). The client option set is research note §5.7.
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs build_rrq with a captured RRQ's filename/mode/options and asserts the
; emitted RRQ matches the captured payload byte-for-byte (mirroring the Go
; TestRRQBuilderByteExact), and runs parse_oack on the captured OACK and asserts
; the negotiated blksize/tsize match the Go authority (TestOACKParse). The wire
; send/receive (ENC28J60 I/O) is NOT host-verifiable (gated on i80 / real
; Trinity).

                org     &8000

OP_RRQ:           equ 1
OP_OACK:          equ 6

; ---------------------------------------------------------------------------
; build_rrq — opcode 1, filename NUL, mode NUL, then the option bytes verbatim.
;
; In:  RRQ_FILENAME_PTR  2 bytes  pointer to the NUL-terminated filename
;      RRQ_MODE_PTR      2 bytes  pointer to the NUL-terminated mode ("octet")
;      RRQ_OPTS          option bytes (each "name\0value\0"), pre-formatted
;      RRQ_OPTS_LEN      2 bytes  number of option bytes
; Out: packet at CRBUF; BC = total length (2 + filename+NUL + mode+NUL + opts).
; ---------------------------------------------------------------------------
build_rrq:
                ld      hl, CRBUF
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_RRQ           ; HL = CRBUF+1
                inc     hl                     ; HL = CRBUF+2, dest cursor

                ; copy the filename up to and including its NUL
                ld      de, (RRQ_FILENAME_PTR)
                call    copy_cstr              ; HL advanced past the copied NUL

                ; copy the mode up to and including its NUL
                ld      de, (RRQ_MODE_PTR)
                call    copy_cstr

                ; copy the pre-formatted option bytes (may be empty)
                ld      bc, (RRQ_OPTS_LEN)
                ld      a, b
                or      c
                jr      z, rrq_done
                ex      de, hl                 ; DE = dest cursor
                ld      hl, RRQ_OPTS           ; source
                ldir                           ; copies BC bytes, leaves DE past dest
                ex      de, hl                 ; HL = dest cursor again
rrq_done:
                ; length = HL - CRBUF
                ld      de, CRBUF
                and     a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; copy_cstr — copy a NUL-terminated string from DE to HL, up to and including
; the terminating NUL. On return HL points just past the copied NUL and DE just
; past the source NUL.
; Clobbers: A.
; ---------------------------------------------------------------------------
copy_cstr:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                or      a
                jr      nz, copy_cstr
                ret

; ---------------------------------------------------------------------------
; parse_oack — decode an OACK payload into a pointer to the first option pair
; plus the pair count, mirroring the Go ParseOACK option loop.
;
; In:  OACK_IN      the OACK payload bytes, filled by the caller
;      OACK_IN_LEN  2 bytes  payload length
; Out: OACK_OK         1 byte  1 = a valid OACK was parsed, 0 = not
;      OACK_OPTS_PTR   2 bytes pointer to the first option name (or past-end)
;      OACK_OPT_COUNT  2 bytes number of option name/value pairs
;
; A truncated option (a name with no following value) sets OACK_OK = 0,
; matching the Go ParseOACK error return; a dangling name with no NUL simply
; ends the loop (Go: break).
; ---------------------------------------------------------------------------
parse_oack:
                xor     a
                ld      (OACK_OK), a
                ld      (OACK_OPT_COUNT), a
                ld      (OACK_OPT_COUNT+1), a

                ; need at least 2 bytes for the opcode
                ld      hl, (OACK_IN_LEN)
                ld      de, 2
                or      a
                sbc     hl, de
                jr      c, oack_fail           ; len < 2

                ; opcode must be OACK (high byte 0, low byte 6)
                ld      a, (OACK_IN)
                or      a
                jr      nz, oack_fail
                ld      a, (OACK_IN+1)
                cp      OP_OACK
                jr      nz, oack_fail

                ; END = OACK_IN + OACK_IN_LEN
                ld      hl, OACK_IN
                ld      bc, (OACK_IN_LEN)
                add     hl, bc
                ld      (OACK_END), hl

                ; options begin past the opcode
                ld      hl, OACK_IN+2
                ld      (OACK_OPTS_PTR), hl
oack_opt_loop:
                ; stop when the cursor reaches END
                ld      de, (OACK_END)
                or      a
                sbc     hl, de
                add     hl, de
                jr      nc, oack_done          ; cursor >= END: no more options

                call    oack_scan_cstr         ; option name
                jr      c, oack_done           ; dangling name with no NUL: break

                call    oack_scan_cstr         ; option value
                jr      c, oack_fail           ; name with no value: error

                ld      de, (OACK_OPT_COUNT)
                inc     de
                ld      (OACK_OPT_COUNT), de
                jr      oack_opt_loop
oack_done:
                ld      a, 1
                ld      (OACK_OK), a
                ret
oack_fail:
                xor     a
                ld      (OACK_OK), a
                ret

; ---------------------------------------------------------------------------
; oack_scan_cstr — advance HL past a NUL-terminated string, bounded by
; OACK_END. CY clear on success (HL past the NUL), CY set on overrun.
; Clobbers: A.
; ---------------------------------------------------------------------------
oack_scan_cstr:
                push    de
osc_loop:
                ld      de, (OACK_END)
                or      a
                sbc     hl, de
                add     hl, de
                jr      nc, osc_overrun        ; cursor >= END
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, osc_loop
                pop     de
                or      a                      ; clear CY
                ret
osc_overrun:
                pop     de
                scf
                ret

; ---------------------------------------------------------------------------
; find_option — locate a named option's value in a parsed OACK.
;
; Walks the option pairs starting at OACK_OPTS_PTR (set by parse_oack), bounded
; by OACK_END, comparing each name against the NUL-terminated name at
; FIND_NAME_PTR. Mirrors the Go OptionUint/Option lookups (the harness reads the
; value string and parses the integer, as the Go side does).
;
; In:  FIND_NAME_PTR  2 bytes  pointer to the NUL-terminated option name to find
;      OACK_OPTS_PTR / OACK_END  set by parse_oack
; Out: FIND_OK     1 byte   1 = found, 0 = not found
;      FIND_VALUE_PTR 2 bytes pointer to the NUL-terminated value (if found)
; ---------------------------------------------------------------------------
find_option:
                xor     a
                ld      (FIND_OK), a

                ld      hl, (OACK_OPTS_PTR)    ; cursor over the option region
fo_loop:
                ; stop at END
                ld      de, (OACK_END)
                or      a
                sbc     hl, de
                add     hl, de
                ret     nc                     ; cursor >= END: not found (FIND_OK=0)

                ; compare the option name at HL with the wanted name
                push    hl                     ; remember the name start
                ld      de, (FIND_NAME_PTR)
fo_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, fo_mismatch
                or      a                      ; equal bytes; was it the NUL?
                jr      z, fo_match            ; both reached NUL together: match
                inc     hl
                inc     de
                jr      fo_cmp
fo_match:
                ; HL points at the name's terminating NUL; value starts after it.
                inc     hl
                ld      (FIND_VALUE_PTR), hl
                ld      a, 1
                ld      (FIND_OK), a
                pop     bc                     ; discard the saved name start
                ret

fo_mismatch:
                ; not this option: skip its name's NUL, then skip its value.
                pop     hl                     ; restore the name start
fo_skip_name:
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, fo_skip_name       ; HL now past the name's NUL
fo_skip_value:
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, fo_skip_value      ; HL now past the value's NUL
                jr      fo_loop

; ===========================================================================
; Data region — parameter blocks, parse outputs, and the output RRQ buffer.
; ===========================================================================
RRQ_FILENAME_PTR: defs 2
RRQ_MODE_PTR:     defs 2
RRQ_OPTS_LEN:     defs 2

OACK_IN_LEN:      defs 2
OACK_OK:          defs 1
OACK_OPTS_PTR:    defs 2                 ; pointer into OACK_IN
OACK_OPT_COUNT:   defs 2
OACK_END:         defs 2                 ; scratch: one past the OACK payload

FIND_NAME_PTR:    defs 2
FIND_OK:          defs 1
FIND_VALUE_PTR:   defs 2                 ; pointer into OACK_IN

RRQ_OPTS:         defs 256               ; caller-formatted RRQ option bytes
CRBUF:            defs 512               ; the output RRQ payload buffer
OACK_IN:          defs 512               ; the input OACK payload
