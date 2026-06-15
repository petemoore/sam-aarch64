; fw_source.asm — the firmware-source URL builder + manifest for the i100 downloader.
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
; Three layers, each mirroring a Go authority:
;   * fw_build_path / fw_build_path_for — the path builder (http.GithubRawPath):
;     fw_build_path_for takes any in-repo path pointer; fw_build_path is the
;     fixed-FW_REPOPATH wrapper.  FW_HOST is the request Host (http.GithubRawHost).
;   * FW_MANIFEST / fw_manifest_entry — the pinned file list (http.RPiFirmware).
;   * fw_plan_path — build the cdn path for manifest file N (http.Manifest.Plan's
;     per-spec Path), the per-file fetch loop's "build each selected file's URL"
;     step; the spec's other fields come straight off the record (name ptr, the
;     32-byte SHA-256) and the FW_HOST constant.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/fw_source_test.go assembles this file, runs the
; builders under the koron-go/z80 harness, reads the FW_* config back out of the
; binary, and asserts FW_PATH / FW_HOST / the per-file plan paths equal the Go
; authorities (http.GithubRawPath / GithubRawHost / Manifest.Plan) byte-for-byte —
; the same one-source-of-truth pattern as http_get's HTTP_PATH/HTTP_HOST.

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

; The request Host for every firmware fetch (mirrors http.GithubRawHost).  Fixed
; — only the path varies per file/revision.
FW_HOST:        defm "cdn.githubraw.com"
                defb 0

; ===========================================================================
; fw_build_path — build /<owner>/<repo>/<sha>/<path> for the fixed FW_REPOPATH
; (the configured single file) into FW_PATH.  Thin wrapper over fw_build_path_for.
; Out: HL = FW_PATH, BC = path length (excl. NUL).  Clobbers A, DE, HL.
; ===========================================================================
fw_build_path:
                ld      hl, FW_REPOPATH
                ; fall through to fw_build_path_for

; ===========================================================================
; fw_build_path_for — build /<owner>/<repo>/<sha>/<path> into FW_PATH,
; NUL-terminated, taking the in-repo <path> from a caller-supplied pointer (so the
; per-file fetch loop can build any manifest file's URL, not just FW_REPOPATH).
; Mirrors the Go authority http.GithubRawPath byte-for-byte with path = *HL.
; In:  HL = pointer to a NUL-terminated in-repo path (e.g. "boot/start4.elf").
; Out: HL = FW_PATH, BC = path length (excl. NUL).  Clobbers A, DE, HL.
; ===========================================================================
fw_build_path_for:
                push    hl                      ; save the in-repo path pointer
                ld      de, FW_PATH             ; running dest pointer
                ld      hl, FW_OWNER
                call    fw_emit_seg             ; "/" + owner
                ld      hl, FW_REPO
                call    fw_emit_seg             ; "/" + repo
                ld      hl, FW_SHA
                call    fw_emit_seg             ; "/" + sha
                pop     hl                      ; the caller's in-repo path pointer
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

; ===========================================================================
; The pinned firmware manifest — the i100 downloader's file list.  The six files
; from spectrum4's firmware/Tupfile at revision FW_SHA, each with its output
; name, its in-repo path, and its pinned SHA-256 (the trusted hash the fetch
; verifies the proxy-served bytes against — cdn.githubraw.com is untrusted).  The
; SHA-256s are the content of each file AT THIS COMMIT; a fetch whose bytes hash
; to anything else is rejected.  The Go authority is http.RPiFirmware.
; ===========================================================================
FW_FILE_COUNT:  equ 6
FW_REC_LEN:     equ 36          ; per record: name ptr(2) + path ptr(2) + hash(32)

; Strings first, so the table's pointers are backward references (no forward data
; reference for pyz80's two-pass sizing).
fw_nm_licence:  defm "LICENCE.broadcom"
                defb 0
fw_pp_licence:  defm "boot/LICENCE.broadcom"
                defb 0
fw_nm_bootcode: defm "bootcode.bin"
                defb 0
fw_pp_bootcode: defm "boot/bootcode.bin"
                defb 0
fw_nm_start:    defm "start.elf"
                defb 0
fw_pp_start:    defm "boot/start.elf"
                defb 0
fw_nm_start4:   defm "start4.elf"
                defb 0
fw_pp_start4:   defm "boot/start4.elf"
                defb 0
fw_nm_fixup:    defm "fixup.dat"
                defb 0
fw_pp_fixup:    defm "boot/fixup.dat"
                defb 0
fw_nm_fixup4:   defm "fixup4.dat"
                defb 0
fw_pp_fixup4:   defm "boot/fixup4.dat"
                defb 0

; The record array: { name ptr (defw), path ptr (defw), pinned SHA-256 (32 B) }.
FW_MANIFEST:
                defw fw_nm_licence
                defw fw_pp_licence
                ; LICENCE.broadcom  sha256
                defb &c7,&28,&3f,&f5,&1f,&86,&3d,&93,&a2,&75,&c6,&6e,&3b,&4c,&b0,&80
                defb &21,&a5,&dd,&4d,&8c,&1e,&7a,&cc,&47,&d8,&72,&fb,&e5,&2d,&3d,&6b
                defw fw_nm_bootcode
                defw fw_pp_bootcode
                ; bootcode.bin  sha256
                defb &af,&60,&3e,&bd,&97,&e7,&b6,&92,&c3,&01,&95,&56,&3f,&7b,&25,&65
                defb &6e,&b0,&5d,&57,&83,&8c,&f1,&a7,&15,&eb,&b4,&70,&d1,&61,&4c,&e4
                defw fw_nm_start
                defw fw_pp_start
                ; start.elf  sha256
                defb &dd,&9b,&42,&04,&1b,&56,&6d,&8b,&94,&52,&9a,&6e,&da,&68,&de,&d1
                defb &47,&fd,&18,&c6,&a4,&b5,&d6,&b9,&74,&32,&26,&08,&21,&14,&ec,&84
                defw fw_nm_start4
                defw fw_pp_start4
                ; start4.elf  sha256
                defb &e1,&ee,&99,&39,&c2,&3d,&25,&3e,&c2,&78,&a1,&19,&54,&d7,&a3,&57
                defb &62,&d6,&be,&0d,&9f,&1d,&2d,&35,&a6,&56,&fe,&1a,&e3,&a0,&30,&4e
                defw fw_nm_fixup
                defw fw_pp_fixup
                ; fixup.dat  sha256
                defb &d8,&b5,&5a,&35,&20,&26,&84,&52,&7a,&97,&3e,&e4,&71,&40,&37,&6f
                defb &44,&0f,&ee,&00,&d9,&67,&9d,&c4,&a8,&52,&ed,&0f,&22,&eb,&1b,&be
                defw fw_nm_fixup4
                defw fw_pp_fixup4
                ; fixup4.dat  sha256
                defb &5f,&2d,&92,&2a,&87,&bb,&3a,&75,&f9,&ae,&7b,&50,&78,&f5,&2a,&83
                defb &d6,&b8,&3a,&2a,&86,&62,&23,&a6,&4d,&32,&83,&41,&e5,&b4,&35,&1e

; ===========================================================================
; fw_manifest_entry — point at one manifest record by index.
; In:  BC = file index (0..FW_FILE_COUNT-1).
; Out: BC = pointer to the record (read fields at offsets 0 name-ptr, 2 path-ptr,
;      4 the 32-byte SHA-256).  Clobbers A, DE, HL.
; ===========================================================================
fw_manifest_entry:
                ld      hl, FW_MANIFEST
                ld      de, FW_REC_LEN
fw_me_loop:
                ld      a, b
                or      c
                jr      z, fw_me_done
                add     hl, de
                dec     bc
                jr      fw_me_loop
fw_me_done:
                ld      b, h
                ld      c, l
                ret

; ===========================================================================
; fw_plan_path — build the cdn.githubraw.com path for manifest file BC into
; FW_PATH.  The Z80 analogue of http.Manifest.Plan's per-spec Path (=
; PathFor(Files[index])): it composes fw_manifest_entry (index -> record) with
; fw_build_path_for (build the path from that record's in-repo path), so the
; per-file fetch loop can build each selected file's request path.  The spec's
; other fields are reached directly: the output name pointer is at record+0, the
; pinned SHA-256 at record+4, and the Host is the FW_HOST constant.
; In:  BC = file index (0..FW_FILE_COUNT-1).
; Out: HL = FW_PATH, BC = path length (excl. NUL).  Clobbers A, DE, HL.
; ===========================================================================
fw_plan_path:
                call    fw_manifest_entry       ; BC = pointer to record[index]
                ld      h, b
                ld      l, c                    ; HL = record pointer
                inc     hl
                inc     hl                      ; HL -> the path-pointer field (record+2)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = the in-repo path pointer
                ex      de, hl                  ; HL = the in-repo path pointer
                jp      fw_build_path_for       ; build /owner/repo/sha/<path>
