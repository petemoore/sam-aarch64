; render_disk_boot.asm — the i365d-b2a bootable: a CODE-auto Trinity-record
; vessel that boots, loads release-unstripped.tbn (DOS 'IN') from the boot
; record into the render IN pages 8..30 and disasm.bin into page 31, renders
; release.tbn -> release.src streamed straight to the record via the raw-CMD24
; sink (render_disk_sink, i365d-b1), then idles. Wall-1 of the demo — see
; docs/specs/i365-demo-architecture.md.
;
; It FUSES two proven bootables:
;   * the render_disk_probe come-up + sink (EEPROM/ENC/CSD + render_disk_sink,
;     src/netboot/render_disk_probe.asm), and
;   * the tbn_render_driver render engine (reader + tbn_render + paged_call,
;     src/tbn_render_driver.asm),
; adding the ONE genuinely new piece: loading the 371 KB IN file from disk into
; the render's contiguous IN run (pages 8..30) at boot, where the standalone
; driver assumes the harness pre-stages them. A single B-DOS HLOAD cannot: its
; per-16 KB HMPR advance is flag-gated (SAMDOS ctas bitf6 / flags@&5C3B) and is
; not engaged for a DOS-file HLOAD here, so a 23-page load runs past &FFFF and
; wraps into ROM. So rdb_load_tbn follows the file's MGT sector chain with raw
; CMD17 reads (bd_record_read_hw) and re-blocks it flat — the inverse of
; render_disk_sink. disasm.bin (single page, ≤32 KB) still loads via HLOAD.
;
; MEMORY LAYOUT: the render engine's ~10 KB of write-before-read scratch buffers
; (tbn_render_editor.asm) are placed LAST so pyz80 trims them as trailing `defs`,
; keeping the loaded file below the ROM1 boot-continuation region (&E274) — the
; ~25 KB record-vessel ceiling (a file overrunning &E274 corrupts the boot
; continuation mid-load and never runs). The reader's STAGING_BUF + SECT_BUF +
; the relocated render stack (&FFFE) all sit in RAM above the loaded image.
;
; PAGING (mirrors tbn_render_driver.asm): section C/D (HMPR) = this vessel's two
; physical pages throughout the render; the sink (render_disk_sink_byte +
; bd_record_write_hw + the SPI path) is co-resident there so it can flush a
; sector mid-render. Off-axis: 7 = paged_call, 8..30 = IN (release.tbn), 31 =
; disasm.bin. render_run toggles LMPR (section A/B) but never HMPR.
;
; VERIFICATION (CLAUDE.md rule 7): render_disk_boot_faithful_test.go boots this
; on the faithful rig (Colin's real ROM + B-DOS 1.5t + SPI SD), seeds a record
; with this vessel + release-unstripped.tbn + disasm.bin, runs the render, then
; reconstructs release.src from the record's contiguous sectors and byte-compares
; to render.Emit(release-unstripped.tbn). NOT the BDOSStore mock (i356).
;
; Built with -D NETBOOT_WANT_RECORD_WRITE=1 (the sd_csd raw-CMD24 write path +
; render_disk_sink). One build, no NETBOOT_HOSTTEST carve-out (i231).

                org     &8000

                ; trinload's X packet does `out (HMPR),P; jp &8000`; a record
                ; boot's ALHK runs this AUTO* CODE file at its load address.
                jp      rdb_main

; ===========================================================================
; Render-engine paging constants (from tbn_render_driver.asm).
; ===========================================================================
                include "../disasm_comm.inc"       ; DISASM_COMM_MNEM/OPS/PC etc.
                include "../tbn_constants.inc"      ; REC_KIND_* (Go-generated)

DISASM_ENTRY:           equ     &8000
DISASM_PAGE:            equ     31

    if defined(DEMO_CHAIN)
; i365d-b2c: the B-DOS 1.5t DOS code is resident at DOSFLG (&5BC2) = page 29. The
; IN reblock must NOT overwrite it — else the assemble-chain's B-DOS ops (which
; page the DOS in via LMPR <- DOSFLG-1) dispatch into render garbage and wedge.
; Shift the 23-page IN run to pages 4..26 and paged_call to 28, leaving page 29
; (DOS), 30, and 31 (disasm) clear. b2a keeps 8..30 (it DI;HALTs, no B-DOS after).
IN_PAGE:                equ     4                       ; IN run = pages 4..26
PAGED_CALL_PAGE:        equ     28                      ; clear of the DOS page (29)
    else
IN_PAGE:                equ     8                       ; IN run = pages 8..30
PAGED_CALL_PAGE:        equ     7
    endif
IN_FIRST_LMPR:          equ     &20 | IN_PAGE           ; RAM0 | IN_PAGE
LMPR_DECODE:            equ     &20 | (PAGED_CALL_PAGE - 1)

; paged_call installation slots (section B; mirror tbn_render_driver.asm).
TRAMPOLINE_DST:         equ     &7E00
PAGED_CALL_DST:         equ     TRAMPOLINE_DST + &40    ; = &7E40 (section B)
PAGED_CALL_HMPR_SAVE:   equ     TRAMPOLINE_DST + &D0    ; = &7ED0
PAGED_CALL_SP_SAVE:     equ     TRAMPOLINE_DST + &D1    ; = &7ED1
TRAMP_SAFE_SP:          equ     TRAMPOLINE_DST + 256    ; = &7F00
paged_call:             equ     PAGED_CALL_DST

; My HLOAD trampoline slots (section B, below the paged_call slots so the two
; never overlap; the HLOAD runs during come-up, paged_call only during render).
RDB_TRAMP_DST:          equ     TRAMPOLINE_DST          ; = &7E00 (my body)
RDB_HMPR_SAVE:          equ     TRAMPOLINE_DST + &30    ; = &7E30
RDB_SP_SAVE:            equ     TRAMPOLINE_DST + &32    ; = &7E32

; Reader header-table pass gate (seed the label/local tables on pass 1).
PASS_PASS1:             equ     1

; IM 2 plumbing for the render phase (netboot_server idiom). A 257-byte table at
; a 256-aligned section-D address, every byte RDB_IM2_VEC, so the ack-time vector
; word reads (VEC,VEC) = the 3-byte ei;reti handler. Placed above the render
; buffers (render_body_buf ends ~&F8AA) and below the &FFFE stack, in the vessel's
; own page 2 (section D, HMPR-stable at HMPR=1 — where all interrupts fire).
RDB_IM2_TABLE:          equ     &F900          ; 257 bytes: &F900..&FA00 inclusive
RDB_IM2_VEC:            equ     &F9            ; every table byte
RDB_IM2_HANDLER:        equ     &F9F9          ; = RDB_IM2_VEC * &101 (ei ; reti)

; --- i365d-b2c overlay-chain slots (section B, clear of B-DOS sysvars <=&5FFF and
;     of the &7E00+ trampoline area render already owns). The chain-stub is copied
;     here and run from OUTSIDE the &8000 window it HLOADs the next overlay into.
OVL_STUB_DST:           equ     &7C00          ; the straight-line loader stub body
OVL_STUB_SP:            equ     &7DF0          ; the stub's HLOAD scratch stack (grows down);
                                               ; also the server overlay's come-up + serve stack
                                               ; (section B; ~7 KB down to &6000, clear of the
                                               ; B-DOS sysvars <=&5FFF the server's B-DOS walk uses)
OVL_STUB_HMPR:          equ     &7DFC          ; 1-byte slot: the boot HMPR (exec window), saved
                                               ; across the 2-page server HLOAD (above OVL_STUB_SP)

; STAGING_BUF is a LABEL placed after all code (see the end of the file), not
; the driver's fixed &E000 — the come-up/sink code occupies what would be the
; driver's &D316..&E000 gap, so the reader's staging must live above it.

; ===========================================================================
; Come-up + sink includes (data-first; every symbol resolves before the code
; below references it). NETBOOT_WANT_RECORD_WRITE gates the sd_csd write path.
; ===========================================================================
                include "encdrv.asm"            ; drv_init / drv_read / drv_write
                include "eeprom.asm"            ; find_index / read_chunk + chunk/name/part/total
                include "bdos_seam.asm"         ; bdos_name_to_uifa / bdos_lookup_hook / bdos_id_str / BD_*
                include "sd_csd.asm"            ; csd_set_bd_records / bd_record_write_hw / csd_base
                include "render_disk_sink.asm"  ; render_disk_sink_reset / _byte / _finish

sam_mac:        equ     chunk+0                 ; SAM MAC, from the loaded flash chunk
sam_ip:         equ     chunk+6                 ; SAM IP

; ===========================================================================
; render_run — set up the render window, install paged_call, point the reader
; at the staged `.tbn`, and render it. VERBATIM from tbn_render_driver.asm: the
; render half of the fusion is unchanged; only the input staging (HLOAD from
; disk) and output sink (RENDER_SINK_VEC) differ, and those are wired in
; rdb_main around this call.
; ===========================================================================
render_run:
                in      a, (250)
                ld      (render_saved_lmpr), a
                ld      hl, 0
                add     hl, sp
                ld      (render_saved_sp), hl
                ld      sp, &FFFE               ; section D (HMPR-stable) stack top

                ld      a, LMPR_DECODE
                out     (250), a
                ld      hl, paged_call_body
                ld      de, PAGED_CALL_DST
                ld      bc, paged_call_body_end - paged_call_body
                ldir

                ld      a, IN_FIRST_LMPR
                ld      (IN_BASE_LMPR), a
                ld      (IN_POS_PAGE), a
                ld      hl, 0
                ld      (IN_POS_OFFSET), hl

                call    render_emit

                ld      a, (render_saved_lmpr)
                out     (250), a
                ld      hl, (render_saved_sp)
                ld      sp, hl
                ret

render_saved_lmpr:      defb    0
render_saved_sp:        defw    0

; ===========================================================================
; IN-paging window helpers (single-page IN, section A). VERBATIM from
; tbn_render_driver.asm — the reader tail-calls these across the multi-page run.
; ===========================================================================
in_map_current:
                ld      a, (IN_POS_PAGE)
                out     (250), a
                ret

in_persist_hl:
                ld      (IN_POS_OFFSET), hl
                in      a, (250)
                ld      (IN_POS_PAGE), a
                ret

in_normalise_hl:
in_normalise_loop:
                ld      a, h
                cp      &40
                ret     c
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a
                out     (250), a
                jr      in_normalise_loop

enctab_map_in:
                di
                ld      a, LMPR_DECODE
                out     (250), a
                ret

fail:
                di
                halt

; ===========================================================================
; Reader header-table seed callbacks (VERBATIM from tbn_render_driver.asm).
; ===========================================================================
symbol_insert:          jp      render_store_label
local_def_append:       jp      render_store_local

PASS_MODE:              defb    PASS_PASS1
symbol_value_buf:       defs    4
local_label_pc_buf:     defs    4

IN_BASE_LMPR:           defb    0
IN_POS_PAGE:            defb    0
IN_POS_OFFSET:          defw    0
IN_END_PAGE:            defb    0
IN_END_OFFSET:          defw    0

; ===========================================================================
; rdb_main — the bootable / trinload entry: come-up, load the render inputs
; from disk, render release.tbn -> release.src straight to the boot record,
; then idle.
; ===========================================================================
rdb_main:
                di
    if defined(DEMO_CHAIN)
                ; Capture the pristine B-DOS boot LMPR (ROM0 in section A + the
                ; B-DOS sysvar page in section B, as ALHK entered us) for the b2c
                ; chain's B-DOS lookup. rdb_boot_lmpr (captured later in rdb_load_tbn)
                ; only guarantees ROM0 in section A for the raw SD reads — its
                ; section B is a data page, which HGTHD's sysvar access needs correct.
                in      a, (250)
                ld      (rdb_bdos_lmpr), a
                in      a, (251)               ; the pristine boot HMPR (the &8000..&FFFF
                ld      (rdb_boot_hmpr), a     ; exec window ALHK set) for the overlay chain
    endif
                ; No ROM screen work at come-up (CLSLOWER / RST &10) — under an ALHK
                ; record boot that context is not yet safe (it hangs, unlike the
                ; trinload-booted probe). b2a's gate reads rdb_phase from memory;
                ; the on-screen demo messages are b2c's job.

                ; --- EEPROM "Trinity Network " chunk -> MAC/IP (drv_init needs it) -
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, rdb_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, rdb_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, rdb_fail_cfg

                ; --- ENC28J60 init (before any SD work, i242) ---------------------
                ld      hl, sam_mac
                call    drv_init
                ld      a, b
                or      c
                jp      z, rdb_fail_init

                ; --- read the CSD -> csd_base / csd_blocks / BD_RECORDS ------------
                call    csd_set_bd_records
                ld      hl, (BD_RECORDS)
                ld      a, h
                or      l
                jp      z, rdb_fail_init
                               ; come-up (EEPROM/ENC/CSD) done

                ; --- install my section-B HLOAD trampoline (once) -----------------
                ld      hl, rdb_tramp_src
                ld      de, RDB_TRAMP_DST
                ld      bc, rdb_tramp_src_end - rdb_tramp_src
                ldir

                ; --- load disasm.bin into page 31 (the render's decode engine) ----
                ld      hl, rdb_name_disasm
                ld      (BD_NAME_PTR), hl
                ld      a, DISASM_PAGE
                call    rdb_hload_page
                ld      a, "D"
                ld      (rdb_phase), a
                               ; disasm.bin HLOADed to page 31

                ; --- load release.tbn (whole) into the IN run, pages 8..30 --------
                ; A single B-DOS HLOAD cannot do this: its per-16 KB HMPR advance is
                ; flag-gated (SAMDOS ctas bitf6 / flags@&5C3B) and not engaged for a
                ; DOS-file HLOAD here, so a 23-page load runs past &FFFF and wraps
                ; into ROM. Instead follow the file's MGT sector chain with raw CMD17
                ; reads and re-block it FLAT across pages 8..30 — the inverse of
                ; render_disk_sink (strip the 510+2 links + the 9-byte CODE header).
                call    rdb_load_tbn
                ld      a, "I"
                ld      (rdb_phase), a
                               ; release.tbn re-blocked into pages 8..30

                ; --- configure the render disk sink: RELEASESRC on the boot record
                ld      hl, rdb_name_out
                ld      de, RDS_NAME
                ld      bc, 10
                ldir
                ld      hl, &8000
                ld      (RDS_LOAD), hl
                ld      hl, (RDB_CFG_RECORD)
                ld      (RDS_RECORD), hl
                call    render_disk_sink_reset

                ; --- own the maskable interrupt for the render (IM 2 -> ei;reti) --
                ; The SD driver's deselect re-enables interrupts after every sink
                ; flush (sd_csd.asm ei), and the render runs with ROM0 OUT of section
                ; A, so a frame interrupt to ROM &0038 would hit RAM garbage. A benign
                ; IM 2 handler in section D (HMPR-stable page 2 throughout the render;
                ; paged_call DIs during its HMPR=31 window, so interrupts only fire at
                ; HMPR=1) neutralizes that (the i280b class, netboot_server idiom).
                di
                ld      hl, RDB_IM2_TABLE
                ld      de, RDB_IM2_TABLE + 1
                ld      bc, 256
                ld      a, RDB_IM2_VEC
                ld      (hl), a
                ldir                           ; 257 bytes, all RDB_IM2_VEC
                ld      a, &FB                 ; ei
                ld      (RDB_IM2_HANDLER), a
                ld      a, &ED                 ; reti (ED 4D)
                ld      (RDB_IM2_HANDLER + 1), a
                ld      a, &4D
                ld      (RDB_IM2_HANDLER + 2), a
                ld      a, RDB_IM2_TABLE / 256
                ld      i, a
                im      2

                ; --- point the render sink at the disk sink, then render ----------
                ld      hl, render_disk_sink_byte
                ld      (RENDER_SINK_VEC), hl
                call    render_run
                ld      a, "R"
                ld      (rdb_phase), a
                               ; render_run returned

                ; --- finish: flush the tail + write the directory entry -----------
                call    render_disk_sink_finish
                ld      a, "F"
                ld      (rdb_phase), a
                               ; sink finished (dir entry written)

                ; --- verdict: RDS_ERR set on any write/guard failure --------------
                ld      a, (RDS_ERR)
                or      a
                jr      nz, rdb_bad
                ld      a, "P"
                jr      rdb_verdict_set
rdb_bad:
                ld      a, "F"
rdb_verdict_set:
                ld      (rdb_verdict), a
                ld      a, "5"
                ld      (rdb_phase), a

    if defined(DEMO_CHAIN)
                ; i365d-b2c Phase B: RELEASESRC is on the record (RELEASEIMG was
                ; HSAVE'd by the assembler before it chained here). Hand the machine
                ; to the netboot SERVER overlay (nbsrv) — load it over the &8000
                ; window and run it; it comes up (EEPROM/ENC/CSD, B-DOS store walk)
                ; and serves BOTH files disk-backed over TFTP forever. See
                ; rdb_chain_next.
                jp      rdb_chain_next
    endif

; ===========================================================================
; rdb_done — the completion barrier: DI;HALT. The faithful test runs to the halt
; and reads rdb_phase/rdb_verdict from memory. A StopPC would be unreliable here —
; the disasm engine runs at page 31 with PCs across &8000..&AA7E, so any vessel PC
; in that range aliases under a page-agnostic StopPC. b2c replaces this with the
; i365c large-file serve loop. rdb_phase='5' + rdb_verdict='P' == success.
; ===========================================================================
rdb_done:
                di
                halt

    if defined(DEMO_CHAIN)
; ===========================================================================
; rdb_chain_next (i365d-b2c Phase B) — hand the machine to the netboot SERVER
; overlay (nbsrv). The render is done and RELEASESRC is on the record. We restore
; a clean B-DOS runtime (boot LMPR = ROM0 in section A + the B-DOS sysvar page in
; section B, IM 1, DI), arm the HGTHD lookup of the server CODE file, then LDIR a
; tiny straight-line loader stub to section-B low RAM (OUTSIDE the &8000 window)
; and JP it. The stub HLOADs nbsrv over &8000 (2 pages, ~19 KB fills sections C+D)
; and enters it. The server is TERMINAL — its own come-up (EEPROM/ENC/CSD, the
; B-DOS store walk that indexes RELEASESRC + RELEASEIMG + the NBMANIFEST long-name
; map) then nb_serve_loop forever — so no return frame is needed; the stub leaves
; SP in section B (OVL_STUB_SP) for the server's come-up + serve stack.
;
; render's IN reblock ran at IN=4..26 (DEMO_CHAIN), so B-DOS's DOS code page (29)
; survives intact for the server's post-render B-DOS store walk (HRSAD/HGTHD/HLOAD).
; ===========================================================================
rdb_chain_next:
                di
                im      1
                ld      a, (rdb_bdos_lmpr)     ; ROM0 in A + B-DOS sysvar page in B
                out     (250), a               ; (the pristine boot LMPR, from rdb_main)
                ld      a, (rdb_boot_hmpr)     ; restore the boot exec window so the next
                out     (251), a               ; overlay loads to &8000 into the RAM page pair
                                               ; ALHK set (render left HMPR at the
                                               ; paged_call/IN window during the render)

                ; --- arm HGTHD for nbsrv -> BD_DIFA (pages, length) -------------
                ; render_disk_write_dirent appended RELEASESRC into the first FREE
                ; dir slot (no type-0 gap before nbsrv), so B-DOS's directory scan
                ; reaches nbsrv (whose on-record body sectors sit above RELEASESRC's
                ; base-40 write band — placed LAST on the record). This HGTHD
                ; dispatches to REAL B-DOS (the DOS page at DOSFLG survives the
                ; IN=4..26 reblock).
                ld      hl, rdb_name_nbsrv
                ld      (BD_NAME_PTR), hl
                call    bdos_name_to_uifa
                call    bdos_lookup_hook       ; UIFA->&4B00, HGTHD arms svde, DIFA->BD_DIFA

                ; --- copy the loader stub to low RAM and enter it ---------------
                ld      hl, rdb_chain_stub_src
                ld      de, OVL_STUB_DST
                ld      bc, rdb_chain_stub_end - rdb_chain_stub_src
                ldir
                jp      OVL_STUB_DST

; rdb_chain_stub_src — the straight-line overlay loader, copied to OVL_STUB_DST
; and run from section B (LMPR-stable, outside the &8000 window it overwrites).
; Absolute addresses only (no self-relative control flow) so it runs correctly
; at OVL_STUB_DST regardless of where it is assembled. Reads the armed BD_DIFA
; (still mapped in section C until the HLOAD) for the page count + length, HLOADs
; the whole server to &8000, then enters it with SP in section B.  The 2-page
; (~19 KB) HLOAD to &8000 ADVANCES HMPR per 16 KB (the data lands correctly across
; sections C+D, no wrap into ROM), so the stub SAVES the boot HMPR first and
; RESTORES it after — else the server enters at page 2 (section C = its 2nd half).
rdb_chain_stub_src:
                ld      sp, OVL_STUB_SP
                in      a, (251)               ; save the boot exec window (section C = page 1)
                ld      (OVL_STUB_HMPR), a
                ld      a, (BD_DIFA + BD_OFF_PAGES)
                ld      c, a                   ; C = whole-file page count
                ld      a, (BD_DIFA + BD_OFF_LENGTH)
                ld      e, a
                ld      a, (BD_DIFA + BD_OFF_LENGTH + 1)
                and     BD_DIFA_MARKER         ; clear the +36 bit-7 marker
                ld      d, a                   ; DE = length-mod-16K
                ld      hl, &8000              ; HLOAD dest = the current window (section C)
                rst     8
                defb    BD_HOOK_HLOAD          ; load nbsrv to &8000 (longjmps on read error)
                di                             ; PTDOS EIs inside RST 8
                ld      a, (OVL_STUB_HMPR)     ; restore the boot exec window so the server
                out     (251), a               ; enters at section C = page 1 (jp netboot_main)
                ld      sp, OVL_STUB_SP        ; the server's come-up + serve stack (section B)
                jp      &8000
rdb_chain_stub_end:
    endif

; ===========================================================================
; rdb_hload_page — HGTHD + trampoline-HLOAD a named CODE file into a target
; physical page. Used for the SINGLE-page disasm.bin payload; a whole-file HLOAD
; only pages correctly for a file that fits the 32 KB HMPR window (see the tbn
; note in rdb_main — the 23-page tbn goes via rdb_load_tbn, not this).
;
; In: (BD_NAME_PTR) = ptr to the NUL-terminated file name; A = target page.
; Clobbers: AF, BC, DE, HL, IX. The boot record stays HRECORD-selected (#813),
; so no re-select is needed before the lookup.
; ===========================================================================
rdb_hload_page:
                push    af                     ; save the target page
                call    bdos_name_to_uifa      ; build the UIFA name block
                call    bdos_lookup_hook       ; UIFA->&4B00, HGTHD (arms svde), DIFA->BD_DIFA
                ld      a, (BD_DIFA + BD_OFF_LENGTH)
                ld      e, a
                ld      a, (BD_DIFA + BD_OFF_LENGTH + 1)
                and     BD_DIFA_MARKER         ; clear the +36 bit-7 marker
                ld      d, a                   ; DE = length-mod-16K
                ld      a, (BD_DIFA + BD_OFF_PAGES)
                ld      c, a                   ; C = whole-file page count
                pop     af
                ld      b, a                   ; B = target page
                ld      hl, &8000              ; HLOAD dest (section C, DOS requirement)
                call    RDB_TRAMP_DST          ; HMPR-swing HLOAD (my section-B body)
                ret

; rdb_tramp_src — the section-B HLOAD trampoline body, copied to RDB_TRAMP_DST
; at come-up. It swings HMPR to the target page (B) around RST 8 HLOAD so the
; file lands in a page OTHER than this vessel's, then restores HMPR/SP. Runs
; from section B (LMPR-stable across the HMPR change). Mirrors trampoline.asm's
; trampoline_body; HMPR_SAVE/SP_SAVE are section-B statics (LMPR-stable).
; In: B = target page, HL = &8000 dest, C = pages, DE = length-mod-16K.
rdb_tramp_src:
                in      a, (251)
                ld      (RDB_HMPR_SAVE), a
                ld      (RDB_SP_SAVE), sp
                ld      sp, TRAMP_SAFE_SP
                ld      a, b
                out     (251), a               ; HMPR = target page
                rst     8
                defb    BD_HOOK_HLOAD          ; 130 — longjmps on read error
                ex      af, af'
                ld      a, (RDB_HMPR_SAVE)
                out     (251), a               ; HMPR restored
                ld      sp, (RDB_SP_SAVE)
                ex      af, af'
                di                             ; PTDOS does EI inside RST 8
                ret
rdb_tramp_src_end:

; ===========================================================================
; rdb_load_tbn — load the whole "IN" file (release.tbn, 371 KB / 23 pages) FLAT
; into the render IN run (pages IN_PAGE..IN_PAGE+22) by following its MGT sector
; chain with raw CMD17 reads and re-blocking, since a single B-DOS HLOAD cannot
; page across 23 pages here (see the caller). The inverse of render_disk_sink:
;   * scan the record directory (linearSec 0..39, two 256-byte entries/sector)
;     for the CODE entry named "IN" -> its first (track,sector);
;   * follow the chain (each sector = 510 data + [next-track][next-sector]),
;     reading via bd_record_read_hw (BD_REC_LINEAR = mgt_ts_to_record_linear);
;   * strip the 9-byte CODE header (first sector only) and write the data bytes
;     flat to pages IN_PAGE.. via LMPR section A, exactly where the reader reads.
; A read failure or a missing "IN" stops loud at rdb_load_fail.
; The boot record stays HRECORD-selected (#813); BD_REC_REC drives the reads.
; ===========================================================================
rdb_load_tbn:
                ; The flat write toggles LMPR (section A = the destination page)
                ; per byte, so the stack must live in HMPR-stable section D — else
                ; a push under one LMPR / pop under another corrupts the return
                ; frame. Relocate SP to the top of section D for the duration.
                ld      (rdb_saved_sp), sp
                ld      sp, &FFFE

                ; Capture the boot LMPR (ROM0 in section A) so it can be restored
                ; before every bd_record_read_hw — the flat write leaves LMPR
                ; mapping a data page (ROM0 out), and the SD read path needs ROM0.
                in      a, (250)
                ld      (rdb_boot_lmpr), a

                ld      hl, (RDB_CFG_RECORD)
                ld      (BD_REC_REC), hl        ; raw reads target the boot record

                ; --- find the "IN" directory entry -> first (track,sector) ------
                ld      hl, 0
                ld      (rdb_dir_lin), hl
rdb_dir_loop:
                ld      hl, (rdb_dir_lin)
                ld      (BD_REC_LINEAR), hl
                ld      hl, SECT_BUF
                call    bd_record_read_hw
                jp      c, rdb_load_fail
                ld      hl, SECT_BUF            ; entry 0
                call    rdb_match_in
                jr      z, rdb_found
                ld      hl, SECT_BUF + 256      ; entry 1
                call    rdb_match_in
                jr      z, rdb_found
                ld      hl, (rdb_dir_lin)
                inc     hl
                ld      (rdb_dir_lin), hl
                ld      de, 40
                or      a
                sbc     hl, de
                jr      c, rdb_dir_loop         ; linearSec < 40 -> keep scanning
                jp      rdb_load_fail           ; "IN" not found in the directory
rdb_found:
                push    hl
                pop     ix                      ; IX = matched 256-byte entry
                ld      d, (ix + &0D)           ; first-sector track_byte
                ld      e, (ix + &0E)           ; first-sector sector (1-based)
                ld      (rdb_cur_ts), de
                               ; "IN" dir entry located; rdb_cur_ts set

                ; --- init the flat write cursor + first-sector header skip ------
                ld      a, IN_PAGE
                ld      (rdb_dst_page), a
                ld      hl, 0
                ld      (rdb_dst_off), hl
                ld      a, RDS_HDR_LEN          ; skip the 9-byte CODE header once
                ld      (rdb_hdr_skip), a

rdb_chain_loop:
                ld      a, (rdb_boot_lmpr)      ; ROM0 back in section A for the SD read
                out     (250), a
                ld      de, (rdb_cur_ts)
                call    mgt_ts_to_record_linear ; HL = record-linear (side-major)
                ld      (BD_REC_LINEAR), hl
                ld      hl, SECT_BUF
                call    bd_record_read_hw
                jp      c, rdb_load_fail

                ; copy [hdr_skip .. 509] of SECT_BUF (510-skip bytes, 16-bit) to
                ; the flat cursor.
                ld      a, (rdb_hdr_skip)
                ld      e, a
                ld      d, 0                    ; DE = skip (0 or 9)
                ld      hl, SECT_BUF
                add     hl, de                  ; HL = SECT_BUF + skip (source)
                push    hl
                ld      hl, RDS_PAYLOAD         ; 510
                or      a
                sbc     hl, de                  ; HL = 510 - skip (bytes to copy)
                ld      b, h
                ld      c, l                    ; BC = count
                pop     hl                      ; HL = source
                di                              ; the flat write maps ROM0 OUT of
                                                ; section A, so a frame interrupt to
                                                ; ROM &0038 would land in RAM garbage;
                                                ; the SD read path (RST 8 / PTDOS EI)
                                                ; may have re-enabled interrupts.
                call    rdb_flat_copy
                xor     a
                ld      (rdb_hdr_skip), a       ; header present only in sector 0

                ; advance to the next chain sector (link at SECT_BUF+510/511).
                ld      a, (SECT_BUF + RDS_PAYLOAD)
                ld      d, a
                ld      a, (SECT_BUF + RDS_PAYLOAD + 1)
                ld      e, a
                ld      (rdb_cur_ts), de
                or      d                       ; next == (0,0) -> terminal
                jr      nz, rdb_chain_loop
                ; The come-up SP lives in section B (LMPR-controlled); the flat
                ; write left LMPR mapping a data page, so restore the boot LMPR
                ; before touching the caller stack, else RET reads the wrong page.
                ld      a, (rdb_boot_lmpr)
                out     (250), a
                ld      sp, (rdb_saved_sp)      ; restore the caller SP + return
                ret

; ---------------------------------------------------------------------------
; rdb_match_in — is the 256-byte directory entry at (HL) the CODE file "IN"?
; Out: Z set on match (HL preserved). Clobbers AF, BC, DE (HL preserved).
; ---------------------------------------------------------------------------
rdb_match_in:
                ld      a, (hl)
                cp      RDS_TYPE_CODE           ; &13 = CODE
                ret     nz                      ; Z clear
                push    hl
                inc     hl                      ; HL = name field (+1)
                ld      de, rdb_name_in_padded
                ld      b, 10
rmi_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, rmi_no
                inc     hl
                inc     de
                djnz    rmi_loop
                pop     hl
                xor     a                       ; Z set = match
                ret
rmi_no:
                pop     hl
                or      1                       ; Z clear = no match
                ret

; ---------------------------------------------------------------------------
; rdb_flat_copy — copy BC bytes from (HL) to pages IN_PAGE.. at the running flat
; cursor (rdb_dst_page, rdb_dst_off) via LMPR section A. Handles the 16384 page
; boundary (byte-at-a-time — the render's 25M-step budget dwarfs this). The
; source (HL) is in section C/D (HMPR-stable, unaffected by the LMPR swings).
; Clobbers AF, BC, DE, HL. Leaves LMPR mapping the last dst page (ROM0 out of
; section A) — safe: nothing below issues RST 8, and render_run re-saves LMPR.
; ---------------------------------------------------------------------------
rdb_flat_copy:
rfc_loop:
                ld      a, b
                or      c
                ret     z                       ; BC == 0 -> done
                ld      a, (hl)                 ; source byte (section C/D)
                ld      (rfc_byte), a
                push    hl
                push    bc
                ld      a, (rdb_dst_page)
                or      &20                     ; RAM0 | page -> section A = that page
                out     (250), a
                ld      hl, (rdb_dst_off)       ; section-A address &0000+off
                ld      a, (rfc_byte)
                ld      (hl), a
                ; advance the cursor; wrap page at 16384.
                ld      hl, (rdb_dst_off)
                inc     hl
                ld      de, 16384
                or      a
                sbc     hl, de
                jr      nz, rfc_no_wrap
                ld      hl, 0                   ; page full -> next page, offset 0
                ld      (rdb_dst_off), hl
                ld      a, (rdb_dst_page)
                inc     a
                ld      (rdb_dst_page), a
                jr      rfc_next
rfc_no_wrap:
                add     hl, de                  ; restore off+1 (undo the sbc)
                ld      (rdb_dst_off), hl
rfc_next:
                pop     bc
                pop     hl
                inc     hl                      ; next source byte
                dec     bc
                jr      rfc_loop

rfc_byte:       defb    0

; ---------------------------------------------------------------------------
; mgt_ts_to_record_linear — map an MGT (track_byte, sector) to the side-major
; record-linear sector (replicated from netboot_server.asm, i365c):
;   record_linear = ((track&0x80)>>7)*800 + (track&0x7F)*10 + (sector-1)
; In: D = track_byte, E = sector (1-based). Out: HL = record_linear (0..1599).
; Clobbers AF, BC, HL (D/E preserved).
; ---------------------------------------------------------------------------
mgt_ts_to_record_linear:
                ld      a, d
                and     &7F
                ld      l, a
                ld      h, 0
                add     hl, hl                  ; 2*cyl
                ld      c, l
                ld      b, h
                add     hl, hl                  ; 4*cyl
                add     hl, hl                  ; 8*cyl
                add     hl, bc                  ; 10*cyl
                bit     7, d
                jr      z, mtr_addsec
                ld      bc, 800
                add     hl, bc                  ; += side*800
mtr_addsec:
                ld      a, e
                dec     a
                ld      c, a
                ld      b, 0
                add     hl, bc                  ; += sector-1
                ret

rdb_load_fail:
                ld      a, "L"
                ld      (rdb_phase), a
                ld      a, 4                    ; green: tbn chain read failed / IN absent
                out     (&fe), a
                di
                halt

; ---------------------------------------------------------------------------
; Failure paths — a distinctive border colour + spin (a DOS-hook failure
; longjmps away; a come-up failure stops here). No ROM screen work, so the
; ALHK-record-boot context stays safe. rdb_phase records which stage failed.
; ---------------------------------------------------------------------------
rdb_fail_cfg:
                ld      a, "c"
                ld      (rdb_phase), a
                ld      a, 2                   ; red: no/bad network settings
                jr      rdb_fail_wait
rdb_fail_init:
                ld      a, "e"
                ld      (rdb_phase), a
                ld      a, 1                   ; blue: ENC/CSD init failed
rdb_fail_wait:
                out     (&fe), a
                di
                halt                           ; halts like rdb_done, but rdb_phase is
                                               ; left at the failing stage (c/e/L), so
                                               ; the test tells this from a clean run

; ===========================================================================
; Data.
; ===========================================================================
rdb_chunk_name: defm    "Trinity Network "
rdb_name_out:   defm    "RELEASESRC"           ; 10-char on-record output name
                defb    0
rdb_name_disasm: defm   "disasm"               ; the render's decode engine payload
                defb    0
    if defined(DEMO_CHAIN)
rdb_name_nbsrv: defm    "nbsrv"                 ; i365d-b2c Phase B: the netboot server overlay
                defb    0
    endif
rdb_name_in_padded:                            ; the 10-char on-record name of the
                defm    "IN        "           ; DOS 'IN' file (2 + 8 spaces), for
                                               ; the directory scan in rdb_load_tbn

rdb_phase:      defb    "0"                    ; progress marker (memory-read by the test)
rdb_verdict:    defb    "-"

; rdb_load_tbn cursor state (the MGT-chain re-block into pages IN_PAGE..).
rdb_dir_lin:    defw    0                      ; directory linearSec being scanned (0..39)
rdb_cur_ts:     defw    0                      ; current chain sector (D=track_byte, E=sector)
rdb_dst_page:   defb    0                      ; flat write cursor: physical page (IN_PAGE..)
rdb_dst_off:    defw    0                      ; flat write cursor: offset within the page (0..16383)
rdb_hdr_skip:   defb    0                      ; bytes to skip at the stream start (9, then 0)
rdb_saved_sp:   defw    0                      ; caller SP parked across the LMPR-toggling load
rdb_boot_lmpr:  defb    0                      ; boot LMPR (ROM0 in section A), restored per read
    if defined(DEMO_CHAIN)
rdb_bdos_lmpr:  defb    0                      ; pristine B-DOS boot LMPR (ROM0 A + sysvar B), for the chain
rdb_boot_hmpr:  defb    0                      ; pristine boot HMPR (the &8000 exec window), for the chain
    endif

; ===========================================================================
; RDB_CONFIG — host/test-patchable config block (boot_record idiom), patched by
; file offset from RDB_CONFIG. Keep the layout STABLE.
;   +0  RDB_CONFIG       1 byte   magic = &5A (patch anchor; never written)
;   +1  RDB_CFG_RECORD   2 bytes  target record number (LE16, >= 1)
; ===========================================================================
RDB_CFG_MAGIC_VAL:    equ &5A
RDB_CONFIG:           defb RDB_CFG_MAGIC_VAL    ; +0 magic (patch anchor)
RDB_CFG_RECORD:       defw 1                    ; +1 record (patched)

; RDB_SCRATCH — a 1 KB scratch buffer placed LOW (right after the vessel code,
; below the render buffers and both stacks). SECT_BUF (the rdb_load_tbn CMD17 read
; destination) and the reader's STAGING_BUF share it — they are time-disjoint
; (SECT_BUF during the come-up load, STAGING_BUF during the render). Sharing keeps
; STAGING_BUF off the top of RAM so the render's relocated stack (&FFFE) has room
; for the disk-sink flush's deep call chain (sdc_init_ladder), which the harness
; render — driving the plain OUT-port sink — never exercises.
RDB_SCRATCH:          defs 1024
SECT_BUF:             equ  RDB_SCRATCH

; ===========================================================================
; Render engine (the tbn_render_driver.asm include set). tbn_render_editor.asm
; is deliberately LAST: it ends with ~10 KB of write-before-read scratch buffers
; (render_names_buf / render_body_buf / render_name_ptrs …), so putting it after
; paged_bodies.asm (which has code) makes those buffers + STAGING_BUF the file's
; trailing `defs`, which pyz80 trims. That trim keeps the CODE-auto vessel's file
; below the ROM1 boot-continuation region (&E274): a record boot loads the AUTO
; file to &8000 upward, and a file overrunning &E274 corrupts the continuation's
; own workspace mid-load and never runs (the ~25 KB record-vessel ceiling; all
; proven vessels — AUTOasm 20 KB, AUTOnbsrv 19 KB — sit below it). The buffers are
; populated from the tbn before they are read, so an un-zeroed load is safe (the
; faithful byte-compare gate proves it).
; ===========================================================================
                include "../reader.asm"
                include "../tbn_render.asm"
                include "../paged_bodies.asm"
                include "../tbn_render_editor.asm"

; ===========================================================================
; STAGING_BUF — the reader's 1 KB record staging area, sharing RDB_SCRATCH (low
; RAM, below the render buffers) with SECT_BUF. Keeping it OFF the top of RAM
; leaves the render's &FFFE stack the headroom the disk-sink flush needs.
; ===========================================================================
STAGING_BUF:            equ     RDB_SCRATCH
STAGING_BUF_END:        equ     RDB_SCRATCH + &400

rdb_length:             equ     $ - rdb_main
