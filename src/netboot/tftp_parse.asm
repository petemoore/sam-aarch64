; tftp_parse.asm — the i83 TFTP server's request-side logic: parse an incoming
; RRQ and resolve its filename against the flat Trinity store.
;
; These are the SAM-side Z80 ports of the netboot-oracle Go authorities
; tools/netboot-oracle/tftp/tftp.go::ParseRequest and
; tools/netboot-oracle/tftp/server.go::Resolve (+ isSerialSubdir), each verified
; against the real Pi 400 capture's RRQs (TFTPRrqSerial / TFTPRrqRoot1024 /
; TFTPRrqRoot1468) via the host harness.
;
; A TFTP RRQ payload is a 2-byte big-endian opcode (1 = RRQ) followed by
; NUL-terminated C-strings: filename, mode ("octet"), then zero or more option
; name/value pairs ("tsize\0" "0\0" "blksize\0" "1024\0" ...). Because every
; field is already NUL-terminated in place, the parser returns *pointers* into
; the payload buffer (mirroring the Go readCStr returning slices) — no copying.
;
; Resolve decides how the server answers one RRQ (oracle §2-§3):
;   - 404 any serial-subdir prefix ("<hex>/start4.elf") — the Pi retries at root.
;   - serve a flat-root hit via OACK (returns the stored size).
;   - ERROR(1) every miss, and keep serving (the single most important
;     robustness requirement — the boot ROM probes a long list of optional
;     files and proceeds on not-found).
;
; PROVENANCE: TFTP wire format (RFC 1350 + 2347 OACK). The serve-by-name /
; ERROR(1)-on-miss / serial-subdir-404 behaviours are
; docs/notes/pi-netboot-capture-analysis.md §2-§3.
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; feeds it each captured RRQ payload, and asserts parse_request decodes the
; filename/mode/options the Go authority does and resolve returns the matching
; action/size. The wire receive (ENC28J60 I/O) is NOT host-verifiable (gated on
; i80 / real Trinity).

                ; org only when assembled standalone (the host harness builds
                ; this file on its own with -D NETBOOT_STANDALONE=1); when a
                ; state-machine file `include`s it, that file supplies the org.
                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

OP_RRQ:           equ 1
OP_WRQ:           equ 2

ACTION_OACK:      equ 0                  ; mirror tftp.ActionOACK
ACTION_ERROR404:  equ 1                  ; mirror tftp.ActionError404

; ASCII constants (pyz80 has no character literals).
CH_SLASH:         equ &2F                ; '/'
CH_0:             equ &30                ; '0'
CH_9:             equ &39                ; '9'
CH_A_UP:          equ &41                ; 'A'
CH_F_UP:          equ &46                ; 'F'
CH_A_LO:          equ &61                ; 'a'
CH_F_LO:          equ &66                ; 'f'

; ---------------------------------------------------------------------------
; parse_request — decode an RRQ/WRQ payload into pointer fields.
;
; In:  RRQ_IN       the payload bytes (opcode + C-strings), filled by the caller
;      RRQ_IN_LEN   2 bytes  payload length
; Out: PARSE_OK         1 byte   1 = a valid RRQ/WRQ was parsed, 0 = not
;      PARSE_OPCODE     2 bytes  the opcode (big-endian as on the wire)
;      PARSE_FILENAME   2 bytes  pointer to the NUL-terminated filename
;      PARSE_MODE       2 bytes  pointer to the NUL-terminated mode
;      PARSE_OPTS       2 bytes  pointer to the first option name (or past-end)
;      PARSE_OPT_COUNT  2 bytes  number of option name/value pairs
;
; A truncated field (missing NUL, or an option name with no value) sets
; PARSE_OK = 0, matching the Go ParseRequest error returns.
; ---------------------------------------------------------------------------
parse_request:
                xor     a
                ld      (PARSE_OK), a
                ld      (PARSE_OPT_COUNT), a
                ld      (PARSE_OPT_COUNT+1), a

                ; need at least 2 bytes for the opcode
                ld      hl, (RRQ_IN_LEN)
                ld      de, 2
                or      a
                sbc     hl, de
                jr      c, parse_fail          ; len < 2

                ; opcode (big-endian) -> PARSE_OPCODE; accept only RRQ or WRQ
                ld      a, (RRQ_IN)            ; high byte
                ld      (PARSE_OPCODE), a
                ld      hl, RRQ_IN+1
                ld      a, (hl)                ; low byte
                ld      (PARSE_OPCODE+1), a
                ; high byte must be 0, low byte must be 1 (RRQ) or 2 (WRQ)
                ld      a, (RRQ_IN)
                or      a
                jr      nz, parse_fail
                ld      a, (RRQ_IN+1)
                cp      OP_RRQ
                jr      z, parse_op_ok
                cp      OP_WRQ
                jr      nz, parse_fail
parse_op_ok:
                ; END = RRQ_IN + RRQ_IN_LEN  (one past the last byte)
                ld      hl, RRQ_IN
                ld      bc, (RRQ_IN_LEN)
                add     hl, bc
                ld      (PARSE_END), hl

                ; cursor starts past the opcode
                ld      hl, RRQ_IN+2

                ; filename = cursor; advance past its NUL
                ld      (PARSE_FILENAME), hl
                call    scan_cstr             ; HL -> after NUL, CY=fail
                jr      c, parse_fail

                ; mode = cursor; advance past its NUL
                ld      (PARSE_MODE), hl
                call    scan_cstr
                jr      c, parse_fail

                ; options begin here
                ld      (PARSE_OPTS), hl
parse_opt_loop:
                ; stop when the cursor reaches END
                ld      de, (PARSE_END)
                or      a
                sbc     hl, de                ; HL = cursor - END
                add     hl, de                ; restore HL
                jr      nc, parse_opts_done   ; cursor >= END: no more options

                ; option name; advance past its NUL
                call    scan_cstr
                jr      c, parse_opts_done    ; a dangling name with no NUL ends the loop (Go: break)

                ; option value must follow (Go: error if missing)
                call    scan_cstr
                jr      c, parse_fail

                ; one full pair parsed
                ld      de, (PARSE_OPT_COUNT)
                inc     de
                ld      (PARSE_OPT_COUNT), de
                jr      parse_opt_loop

parse_opts_done:
                ld      a, 1
                ld      (PARSE_OK), a
                ret
parse_fail:
                xor     a
                ld      (PARSE_OK), a
                ret

; ---------------------------------------------------------------------------
; scan_cstr — advance HL past a NUL-terminated string, bounded by PARSE_END.
;
; In:  HL = cursor (start of the string)
; Out: HL = one past the terminating NUL (the next field), CY clear on success;
;      CY set (and HL unspecified) if no NUL is found before PARSE_END.
; Clobbers: A.
; ---------------------------------------------------------------------------
scan_cstr:
                push    de
sc_loop:
                ld      de, (PARSE_END)
                or      a
                sbc     hl, de                ; HL = cursor - END
                add     hl, de                ; restore HL
                jr      nc, sc_overrun        ; cursor >= END: ran out
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, sc_loop
                ; found the NUL; HL already points past it
                pop     de
                or      a                     ; clear CY
                ret
sc_overrun:
                pop     de
                scf
                ret

; ---------------------------------------------------------------------------
; resolve — decide how the server answers an RRQ for the filename at
; RESOLVE_NAME_PTR, against the flat store at STORE.
;
; Port of server.go::Resolve (+ isSerialSubdir).
;
; In:  RESOLVE_NAME_PTR  2 bytes  pointer to the NUL-terminated filename
;      STORE             the flat store (see below)
; Out: RESOLVE_ACTION  1 byte   ACTION_OACK (0) or ACTION_ERROR404 (1)
;      RESOLVE_SIZE    4 bytes  little-endian size (meaningful only for OACK)
;
; STORE layout (the host harness fills it; on hardware it is the B-DOS flat
; name->{record,size} index, plan §3.3):
;   repeated entries, each = name bytes, a NUL, then a 4-byte little-endian size;
;   a single leading NUL (empty name) terminates the store.
; ---------------------------------------------------------------------------
resolve:
                ; default to ERROR404 with size 0
                ld      a, ACTION_ERROR404
                ld      (RESOLVE_ACTION), a
                xor     a
                ld      (RESOLVE_SIZE), a
                ld      (RESOLVE_SIZE+1), a
                ld      (RESOLVE_SIZE+2), a
                ld      (RESOLVE_SIZE+3), a

                ; serial-subdir prefix -> 404 (already the default)
                call    is_serial_subdir
                ret     c                     ; CY set = is a serial subdir

                ; walk the store looking for an exact name match
                ld      ix, STORE
resolve_store_loop:
                ld      a, (ix+0)
                or      a
                ret     z                     ; empty name = end of store: miss (404)

                ; compare the candidate (at IX) with the request (RESOLVE_NAME_PTR)
                ld      hl, (RESOLVE_NAME_PTR)
                push    ix
                pop     de                    ; DE = candidate name pointer
rs_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, rs_next           ; mismatch -> skip this entry
                or      a                     ; both bytes equal; was it the NUL?
                jr      z, rs_match           ; matched through the terminating NUL
                inc     hl
                inc     de
                jr      rs_cmp
rs_match:
                ; DE points at the candidate's terminating NUL; the size follows.
                inc     de                    ; DE -> 4-byte LE size
                ld      hl, RESOLVE_SIZE
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                ld      a, (de)
                ld      (hl), a
                ld      a, ACTION_OACK
                ld      (RESOLVE_ACTION), a
                ret

rs_next:
                ; advance IX past this entry: skip the name's NUL, then 4 size bytes.
                ; IX currently points at the entry's first name byte; scan to NUL.
rs_skip_name:
                ld      a, (ix+0)
                inc     ix
                or      a
                jr      nz, rs_skip_name
                ; IX now points past the NUL, at the 4-byte size; skip it.
                inc     ix
                inc     ix
                inc     ix
                inc     ix
                jr      resolve_store_loop

; ---------------------------------------------------------------------------
; is_serial_subdir — does the name at RESOLVE_NAME_PTR begin with a serial-
; number subdir ("<hexprefix>/...") of >= 4 hex digits before the first '/'?
;
; Port of server.go::isSerialSubdir: find the first '/', require the prefix to
; be non-empty, >= 4 chars, and entirely hex digits ([0-9a-fA-F]).
;
; Out: CY set if it is a serial subdir; CY clear otherwise.
; Clobbers: A, BC, HL.
; ---------------------------------------------------------------------------
is_serial_subdir:
                ld      hl, (RESOLVE_NAME_PTR)
                ld      b, 0                  ; B = count of hex chars before '/'
iss_loop:
                ld      a, (hl)
                or      a
                jr      z, iss_no             ; reached end with no '/'
                cp      CH_SLASH
                jr      z, iss_slash
                ; must be a hex digit to still qualify
                call    is_hex_digit
                jr      nc, iss_no            ; non-hex before '/': not a serial subdir
                inc     b
                inc     hl
                jr      iss_loop
iss_slash:
                ; '/' found. prefix length = B; require B >= 4 and B > 0.
                ld      a, b
                cp      4
                jr      c, iss_no             ; < 4 chars: not a serial subdir
                scf                           ; is a serial subdir
                ret
iss_no:
                or      a                     ; clear CY
                ret

; ---------------------------------------------------------------------------
; is_hex_digit — is A one of 0-9, a-f, A-F?
; Out: CY set if hex, clear otherwise. A preserved.
; ---------------------------------------------------------------------------
is_hex_digit:
                cp      CH_0
                jr      c, ihd_no
                cp      CH_9+1
                jr      c, ihd_yes            ; '0'..'9'
                cp      CH_A_UP
                jr      c, ihd_no
                cp      CH_F_UP+1
                jr      c, ihd_yes            ; 'A'..'F'
                cp      CH_A_LO
                jr      c, ihd_no
                cp      CH_F_LO+1
                jr      c, ihd_yes            ; 'a'..'f'
ihd_no:
                or      a                     ; clear CY
                ret
ihd_yes:
                scf
                ret

; ===========================================================================
; Data region — parse outputs, resolve outputs, and the input buffers.
; ===========================================================================
PARSE_OK:         defs 1
PARSE_OPCODE:     defs 2                 ; big-endian on the wire
PARSE_FILENAME:   defs 2                 ; pointer into RRQ_IN
PARSE_MODE:       defs 2                 ; pointer into RRQ_IN
PARSE_OPTS:       defs 2                 ; pointer into RRQ_IN
PARSE_OPT_COUNT:  defs 2
PARSE_END:        defs 2                 ; scratch: one past the payload
RRQ_IN_LEN:       defs 2

RESOLVE_ACTION:   defs 1
RESOLVE_SIZE:     defs 4                 ; little-endian
RESOLVE_NAME_PTR: defs 2

RRQ_IN:           defs 512               ; the input RRQ payload
STORE:            defs 1024              ; the flat name->size store (harness-filled)
