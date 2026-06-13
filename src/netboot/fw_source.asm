; fw_source.asm — the firmware-source URL builder for the i100 downloader.
;
; Builds the cdn.githubraw.com request path for a commit-pinned firmware file:
;   /<owner>/<repo>/<sha>/<path>   e.g. /raspberrypi/firmware/<sha>/boot/start4.elf
; cdn.githubraw.com is a plain-HTTP, commit-SHA-addressable proxy for
; raw.githubusercontent.com (q15 option c), so the SAM can fetch an exact pinned
; firmware revision over plain HTTP — no on-SAM TLS. Integrity is supplied
; separately by the SHA-256 verify (the proxy is untrusted), see sha256.asm +
; tcp_conn.asm's conn_verify_*. Reference: item i100 and
; docs/specs/phase3-delivery-design.md §7.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/fw_source_test.go assembles this file, runs
; fw_build_path under the koron-go/z80 harness, reads the FW_* config strings
; back out of the binary and feeds them to the Go authority http.GithubRawPath,
; then asserts FW_PATH equals it byte-for-byte — the same one-source-of-truth
; pattern as http_get's HTTP_PATH/HTTP_HOST.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Output buffer + the configured target (the harness reads these back).  Data
; first so every label is defined before the code references it.
; ===========================================================================
FW_PATH:        defs 256                ; the built request path (NUL-terminated)

; The pinned firmware source.  Reference: spectrum4's firmware/Tupfile pins this
; exact revision — the last-working RPi firmware before issue #1979 broke the
; next.  These are the user-selectable inputs (revision + file) the downloader
; UX will drive later; for now they hold the reference values so the builder is
; exercised + host-verified.
FW_OWNER:       defm "raspberrypi"
                defb 0
FW_REPO:        defm "firmware"
                defb 0
FW_SHA:         defm "a43df3a002f60c4c2243a416d045eb5937585e8b"
                defb 0
FW_REPOPATH:    defm "boot/start4.elf"
                defb 0

; ===========================================================================
; fw_build_path — build /<owner>/<repo>/<sha>/<path> into FW_PATH, NUL-terminated.
; Mirrors the Go authority http.GithubRawPath byte-for-byte.
; Out: HL = FW_PATH, BC = path length (excl. NUL).  Clobbers A, DE, HL.
; ===========================================================================
fw_build_path:
                ld      de, FW_PATH             ; running dest pointer
                ld      hl, FW_OWNER
                call    fw_emit_seg             ; "/" + owner
                ld      hl, FW_REPO
                call    fw_emit_seg             ; "/" + repo
                ld      hl, FW_SHA
                call    fw_emit_seg             ; "/" + sha
                ld      hl, FW_REPOPATH
                call    fw_emit_seg             ; "/" + path
                xor     a
                ld      (de), a                 ; NUL-terminate
                ; BC = DE (end) - FW_PATH (start), the length excluding the NUL.
                ex      de, hl                  ; HL = end pointer
                ld      de, FW_PATH
                or      a
                sbc     hl, de                  ; HL = length
                ld      b, h
                ld      c, l
                ld      hl, FW_PATH             ; HL = the built path
                ret

; fw_emit_seg — emit a '/' separator then copy the NUL-terminated string at HL to
; DE (excluding the NUL), advancing DE past the copied bytes.
; In: HL=src, DE=dest.  Clobbers A.
fw_emit_seg:
                ld      a, &2F                  ; "/"
                ld      (de), a
                inc     de
fw_emit_loop:
                ld      a, (hl)
                or      a
                ret     z
                ld      (de), a
                inc     hl
                inc     de
                jr      fw_emit_loop
