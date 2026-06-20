; netboot_dumper.asm — the i173 SAMBOOT one-shot ROM+EEPROM dumper.
;
; Pushed to the SAM via trinload (page P, offset &8000), it reads the patched
; 32 KB system ROM + the 128 KB Trinity EEPROM and serves both over TFTP so the
; host `tftp get`s them. The captured dumps unblock i87b and are the mandatory
; backup before any EEPROM flash (i135c). Controlling charter: docs/specs/
; samboot.md §6 step 2.
;
; WHY REGIONS, NOT ONE DUMP. The proven serve loop (serve_serve_once, included
; from netboot_serve.asm) streams ONE contiguous <=64 KB RAM buffer via a 16-bit
; SRC_PTR + XFER_OFFSET (send_next_data: netboot_serve.asm:472, `add hl,de`). No
; serve path pages a source bank mid-transfer, so a served file must be a
; contiguous RAM region. 32 KB ROM + 128 KB EEPROM cannot be staged at once.
; Therefore each 16 KB region is its own named TFTP file, staged into one reused
; 16 KB STAGE buffer just before its transfer (the dumper_refresh_region hook the
; serve loop calls at rrq_hit). The host concatenates:
;   ROM    -> rom0.bin + rom1.bin              -> `cat rom0.bin rom1.bin > rom.bin`
;   EEPROM -> eep0.bin .. eep7.bin (8x16 KB)   -> `cat eep0.bin .. eep7.bin > eeprom.bin`
;
; This is the SAMBOOT-side composition of the i96 serve machine (netboot_serve.asm)
; — it reuses serve_serve_once + every helper + the EEPROM reader (eeprom.asm)
; verbatim, adding only: the region STORE/SRC_TABLE templates, the per-region
; refresh hook, the EEPROM-region read loop, the ROM-paging read, and dumper_main.
;
; VERIFICATION. The EEPROM-read + serve path is host-verifiable under the i80
; emulation: dumper_test.go programs known EEPROM chunks, drives a bare RRQ for
; eep0.bin through serve_serve_once, and asserts the streamed DATA reconstructs
; the programmed 16 KB byte-for-byte. The ROM-paging read (dumper_read_rom0/rom1)
; is now ALSO emulation-exercised: since i181 the netboot harness has a real SAM
; pager (LMPR/HMPR + ROM write-protect), so dumper_rompaging_test.go loads the
; trinload build and runs both paths — rom0 copies ROM0 to STAGE and RETs clean;
; rom1 REPRODUCES the i87a crash (scratch page P-1=0 clobbers low memory; LMPR not
; restored). What stays HARDWARE-GATED is only the real patched-ROM CONTENTS (the
; captured bytes — i87a captures, i87b diffs; i190a loads the real ROM in place of
; the synthetic fixtures). The i188 redesign fixes the rom1 path and flips the
; characterization. Emulation-verified is not hardware-verified (CLAUDE.md §5).

                org     &8000

DUMPER:         equ 1                          ; arm netboot_serve.asm's hook + own-org suppression

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness invokes routines by symbol and never CALLs
                ; &8000, and dumper_main is excluded from the host-test build, so
                ; this jp is the hardware/trinload entry only.
                if defined(NETBOOT_HOSTTEST)==0
                jp      dumper_main
                endif

; The serve state machine + every helper + CONFIG/STORE/SRC_TABLE. With DUMPER
; defined it does NOT emit its own org/boot-jp/serve_main and DOES `call
; dumper_refresh_region` at rrq_hit. eeprom.asm is included by THIS file (below).
                include "netboot_serve.asm"

; ===========================================================================
; Region geometry.
; ===========================================================================
REGION_BYTES:     equ 16384                    ; one served file = 16 KB
REGION_CHUNKS:    equ 16                        ; 16 KB / 1 KB EEPROM chunk
CHUNK_BYTES:      equ 1024
STAGE:            equ &C000                    ; the reused 16 KB staging buffer (section D)

; LMPR (port &FA) bits used by the ROM read (docs/notes/sam-paging.md:99-122).
LMPR_PORT:        equ &FA
HMPR_PORT:        equ &FB
LMPR_ROM0:        equ &20                       ; bit5: 1=RAM at section A, 0=ROM0 at section A
LMPR_ROM1:        equ &40                       ; bit6: 1=ROM1 at section D, 0=RAM (HMPR+1)

; ===========================================================================
; dumper_refresh_region — the serve-loop hook (called from rrq_hit). The
; resolved filename is at (PARSE_FILENAME); resolve_src has just set SRC_PTR +
; XFER_SIZE from SRC_TABLE (both default to STAGE / REGION_BYTES). Fill STAGE
; with the requested region's bytes; for rom1.bin override SRC_PTR to a
; section-A scratch buffer. By block-1 time the buffer holds the region and the
; serve loop streams it normally.
; ===========================================================================
dumper_refresh_region:
                ld      hl, (PARSE_FILENAME)

                ; eep0.bin .. eep7.bin -> EEPROM region (emulation-verifiable).
                ; Filename shape: "eepN.bin" — match the "eep" prefix, take N.
                ld      de, rgn_eep_prefix
                call    dr_streq3              ; CY set if (HL) begins "eep"
                jr      c, dr_eep

                ; rom0.bin / rom1.bin -> ROM region (hardware-first).
                ld      hl, (PARSE_FILENAME)
                ld      de, rgn_rom_prefix
                call    dr_streq3              ; CY set if (HL) begins "rom"
                ret     nc                     ; unknown name: leave STAGE as-is

                ; HL points just past "rom"; the next char is '0' or '1'.
                ld      a, (hl)
                cp      "1"
                jp      z, dumper_read_rom1    ; rom1.bin: ROM1, section D (overrides SRC_PTR)
                jp      dumper_read_rom0       ; rom0.bin: ROM0, section A

dr_eep:
                ; HL points just past "eep"; the next char is the region digit N.
                ld      a, (hl)
                sub     "0"                    ; A = region index N (0..7)
                jp      dumper_read_eeprom_region

; dr_streq3 — does the NUL-terminated string at HL begin with the 3-char prefix
; at DE? Out: CY set + HL advanced past the 3 prefix chars if it matches; CY
; clear + HL undefined otherwise.
dr_streq3:
                ld      b, 3
dr_s3_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, dr_s3_no
                inc     hl
                inc     de
                djnz    dr_s3_loop
                scf
                ret
dr_s3_no:
                or      a
                ret

rgn_eep_prefix:   defm "eep"
rgn_rom_prefix:   defm "rom"

; ===========================================================================
; EEPROM region read (EMULATION-VERIFIABLE — reuses eeprom.asm read_chunk).
;
; Region N (16 KB) = 16 EEPROM chunks. eeprom.asm get_chunk maps `value` to flat
; EEPROM address (28 + value*4)<<8, so chunk `value` lives at value*1024 above
; value 1's base (0x2000); chunks are contiguous in value order. We map region N,
; in-region chunk i (0..15) to value = N*16 + i + 1, so `cat eep0..eep7`
; reconstructs the raw 128 KB in chunk-value order (plan §D, chunk K base =
; 8192+(K-1)*1024 with K = value).
;
; In: A = region index N (0..7). Fills STAGE[0..16383] with the region's bytes.
; ===========================================================================
dumper_read_eeprom_region:
                ; first chunk value = N*16 + 1.
                add     a, a                    ; N*2
                add     a, a                    ; N*4
                add     a, a                    ; N*8
                add     a, a                    ; N*16
                inc     a                       ; +1 (chunk values are 1-based)
                ld      (dr_chunk_value), a

                ld      hl, STAGE
                ld      (dr_stage_ptr), hl
                ld      b, REGION_CHUNKS
dr_eep_loop:
                push    bc
                ; read this chunk number into eeprom.asm's `chunk` buffer.
                ld      a, (dr_chunk_value)
                ld      (value), a
                call    read_chunk             ; eeprom.asm:312 — chunk[0..1023] filled

                ; copy the 1024-byte chunk to STAGE + i*1024.
                ld      hl, chunk
                ld      de, (dr_stage_ptr)
                ld      bc, CHUNK_BYTES
                ldir
                ld      (dr_stage_ptr), de     ; DE = STAGE + (i+1)*1024 after ldir

                ; advance to the next chunk value.
                ld      a, (dr_chunk_value)
                inc     a
                ld      (dr_chunk_value), a

                pop     bc
                djnz    dr_eep_loop

                ; SRC_PTR/XFER_SIZE already default to STAGE/REGION_BYTES; nothing
                ; to override for the EEPROM regions.
                ret

dr_chunk_value:   defb 0
dr_stage_ptr:     defw 0

; ===========================================================================
; ROM region reads (the PAGING is emulation-verified since i181/i188; the real
; ROM *contents* stay hardware-gated).
;
; Since i181 the netboot harness has a faithful SAM pager, so these `ldir`s run
; under emulation (dumper_rompaging_test.go) with synthetic ROM fixtures: rom0
; copies ROM0->STAGE and the paging save/restore is asserted; rom1 was the i87a
; hardware crash and is now fixed + asserted here. What remains hardware-gated is
; only the patched ROM's real bytes (i87a captures them, i87b diffs them; i190a
; loads them in place of the fixtures). Guarded out of the host-test build only
; because that build stubs the ROM path; the trinload build runs it.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

; dumper_read_rom0 — copy ROM0 (&0000-&3FFF, section A) into STAGE (&C000-&FFFF,
; section D). ROM0 (section A) and STAGE (section D) are different sections, so a
; single ldir reads one and writes the other with no scratch needed. The entry
; LMPR is saved in memory and restored before the RET, so section B (trinload)
; and the stack are never disturbed. Asserted by TestDumperReadROM0CopiesToStage.
dumper_read_rom0:
                in      a, (LMPR_PORT)
                ld      (dr_save_lmpr), a
                di
                ; Clear bit5 -> ROM0 at section A (&0000-&3FFF); clear bit6 -> RAM
                ; (HMPR+1) at section D, so STAGE stays writable. Reads ROM0 low 16 KB.
                and     ~(LMPR_ROM0 | LMPR_ROM1) & &ff   ; bit5=0 ROM0 on, bit6=0 ROM1 off
                out     (LMPR_PORT), a
                ld      hl, &0000
                ld      de, STAGE
                ld      bc, REGION_BYTES
                ldir
                ld      a, (dr_save_lmpr)
                out     (LMPR_PORT), a          ; restore the entry LMPR (ROM/RAM map)
                ei
                ; STAGE now holds ROM0; SRC_PTR/XFER_SIZE default to STAGE/16384.
                ret

; dumper_read_rom1 — ROM1 (&C000-&FFFF) maps at section D, the SAME logical window
; as STAGE, so ROM1 and STAGE cannot both sit at &C000 at once. Read ROM1 into
; STAGE's OWN physical page (P+1) via section A, then leave it in STAGE: with ROM1
; off, section D = HMPR+1 = page P+1, so STAGE now holds ROM1 and serves via the
; default SRC_PTR — no override, identical to every other region.
;
; STAGE's page (P+1) is the scratch precisely because it is provably free: it is
; the dumper's own high buffer, the page the EEPROM + rom0 captures stage into
; successfully on real hardware. (The i87a crash used P-1, which was page 0 — the
; SAM's low memory — clobbering it AND, because the copy clobbered C, leaving LMPR
; at &00 with section B remapped off trinload. This redesign touches only sections
; A and D, under DI, and restores the entry LMPR from memory — never a register
; the ldir trashes — before the RET, so page 0, the stack, and trinload's section B
; are all untouched. i188; asserted by TestDumperReadROM1StagesToStage.)
dumper_read_rom1:
                in      a, (LMPR_PORT)
                ld      (dr_save_lmpr), a       ; entry LMPR (ROM1 off; section D = STAGE)
                in      a, (HMPR_PORT)          ; A = P (section-C page = HMPR low5)
                inc     a                       ; A = P+1 (STAGE's physical page)
                and     &1f                     ; low5 = STAGE page; clear bit5/6/7
                or      LMPR_ROM0 | LMPR_ROM1   ; bit5=1 RAM at section A, bit6=1 ROM1 at section D
                di
                out     (LMPR_PORT), a          ; section A = STAGE page, section D = ROM1
                ld      hl, &C000               ; ROM1 source (section D)
                ld      de, &0000               ; STAGE page, mapped at section A (dest)
                ld      bc, REGION_BYTES
                ldir
                ld      a, (dr_save_lmpr)
                out     (LMPR_PORT), a          ; restore entry LMPR: section D = STAGE = the dump; section B = trinload
                ei
                ; STAGE now holds ROM1; SRC_PTR/XFER_SIZE default to STAGE/16384.
                ret

dr_save_lmpr:     defb 0

                endif  ; !NETBOOT_HOSTTEST

; In the host-test build the ROM-paging entry points are unreachable (no rom*.bin
; RRQ is driven), but dumper_refresh_region references them by label, so provide
; inert stubs so the host binary links. They never run under the harness.
                if defined(NETBOOT_HOSTTEST)
dumper_read_rom0:
                ret
dumper_read_rom1:
                ret
                endif

; ===========================================================================
; Bootable / trinload entry (excluded from the host harness build — no EEPROM,
; no real silicon). dumper_main reads the SAM's MAC/IP from the "Trinity Network "
; flash chunk, provisions the region STORE/SRC_TABLE, inits the ENC28J60, then
; loops serve_serve_once with an Esc-to-RET exit so it can be re-pushed.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

dumper_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk -----
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, dm_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, dm_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, dm_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into CONFIG.
                ld      hl, chunk + 0
                ld      de, CONFIG_SERVERMAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONFIG_SERVERIP
                ld      bc, 4
                ldir

                ; fixed transfer source TID (an ephemeral high port), big-endian.
                ld      a, 40136 >> 8
                ld      (CONFIG_SERVERTID), a
                ld      a, 40136 & &ff
                ld      (CONFIG_SERVERTID + 1), a

                xor     a
                ld      (XFER_ACTIVE), a
                ld      (XFER_JUST_OACKED), a

                ; --- provision the region STORE + SRC_TABLE ---------------
                call    dumper_provision

                ; --- init the ENC28J60 with the SAM's real MAC ------------
                ld      hl, CONFIG_SERVERMAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, dm_fail_init

dm_serve_loop:
                ; Esc-to-exit (trinload.asm:89-92): poll the keyboard; on Esc,
                ; RET to trinload's `start` (it pushed start as our return addr)
                ; so the dumper can be re-pushed for another capture. trinload set
                ; up section B (&6000); we never repage it, so RET lands cleanly.
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc pressed?
                ret     z                      ; -> trinload's start

                call    serve_serve_once
                jr      dm_serve_loop

dm_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
dm_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

; dumper_provision — copy the region STORE + SRC_TABLE templates into the live
; tables resolve + resolve_src walk. The dumper does NOT fill STAGE here: each
; region is staged on demand by dumper_refresh_region at its RRQ.
dumper_provision:
                ld      hl, dump_store_tmpl
                ld      de, STORE
                ld      bc, dump_store_tmpl_end - dump_store_tmpl
                ldir
                ld      hl, dump_src_tmpl
                ld      de, SRC_TABLE
                ld      bc, dump_src_tmpl_end - dump_src_tmpl
                ldir
                ret

dm_chunk_name:    defm "Trinity Network "     ; the flash chunk holding MAC+IP

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; Region STORE + SRC_TABLE templates (mirror provision_demo's, netboot_serve.asm:
; 968-992). Both are needed in the host-test build too: dumper_test.go provisions
; them directly so resolve / resolve_src match the region names.
;   STORE:     name\0 | 4-byte LE size, then a 0 sentinel.
;   SRC_TABLE: name\0 | 2-byte LE source ptr | 4-byte LE size, then a 0 sentinel.
; Every region defaults to STAGE / REGION_BYTES; dumper_refresh_region overrides
; SRC_PTR for rom1.bin only. The entries are written out longhand (pyz80 macros
; are parameterless) — one defm/defb/defw triple per region, every size REGION_BYTES.
; ===========================================================================
dump_store_tmpl:
                  defm "rom0.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0                        ; size high word
                  defm "rom1.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep0.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep1.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep2.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep3.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep4.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep5.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep6.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defm "eep7.bin"
                  defb 0
                  defw REGION_BYTES
                  defw 0
                  defb 0                        ; end-of-store sentinel
dump_store_tmpl_end:

dump_src_tmpl:
                  defm "rom0.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "rom1.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep0.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep1.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep2.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep3.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep4.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep5.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep6.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defm "eep7.bin"
                  defb 0
                  defw STAGE
                  defw REGION_BYTES
                  defw 0
                  defb 0                        ; end-of-table sentinel
dump_src_tmpl_end:

; ===========================================================================
; The Trinity flash reader. netboot_serve.asm suppresses its own eeprom.asm
; include under DUMPER (above), so the dumper owns it — and includes it in EVERY
; build (host-test too): the EEPROM read is the emulation-verified path, so it is
; never carved out. find_index / read_chunk + the value/chunk/name/part/total
; storage all come from here.
; ===========================================================================
                include "eeprom.asm"
