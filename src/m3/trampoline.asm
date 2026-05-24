; trampoline.asm — paged-RAM trampoline machinery for HLOAD and ENCTAB
; runtime reads.
;
; Per docs/specs/2026-05-27-samdos-load-idiom.md (the design source).
;
; Purpose
; -------
; The SAMDOS HLOAD hook requires HL ∈ &8000..&BFFF (section C) and the
; destination physical page mapped via HMPR.  When the caller wants the
; file to land somewhere OTHER than the caller's own current section-C
; page (i.e. where the running code lives), the canonical SAM pattern
; is a "trampoline" that temporarily reprograms HMPR around the RST 8.
; COMET (a contemporary SAM Coupé assembler) uses exactly this pattern;
; we follow it verbatim modulo small adjustments forced by our memory
; layout.
;
; Why does the trampoline live OUTSIDE section C?
; -----------------------------------------------
; HMPR controls section C (and, automatically, section D = HMPR+1).
; The trampoline issues `out (251), a` to swing section C onto the
; target page — but the trampoline's OWN instruction stream is being
; fetched from somewhere in the 64 KB address map.  If the trampoline
; lived in section C, the very next instruction fetch after the
; `out (251), a` would come from the NEW physical page (the load
; target), which holds whatever undefined bytes happen to be there —
; a guaranteed crash.
;
; Therefore the trampoline body must run from section A (LMPR low 5
; bits) or section B (LMPR low 5 bits + 1).  Both are LMPR-controlled
; and unaffected by HMPR changes.
;
; Why we copy into section B at TRAMPOLINE_DST instead of section A
; -----------------------------------------------------------------
; Section A normally holds ROM0 (LMPR bit 5 = 0).  `RST 8` jumps to
; &0008 (in ROM0), so SAMDOS hooks REQUIRE ROM0 to be in section A
; during dispatch.  Replacing section A with the trampoline page would
; break every subsequent SAMDOS hook call.  Section B (page 1 = BASIC
; system page in the default LMPR=&00 state) is free for us to
; overwrite once we've finished with BASIC — which we have, by the
; time `enctab_trampoline_setup` runs (BASIC's only job was the AUTO
; `LOAD CODE 32768; CALL 32768`; we never call back into it).
;
; The 14-byte trampoline body fits easily near the top of section B
; (TRAMPOLINE_DST = &7E00) without disturbing anything we care about.
;
; Stack handling — static-save-in-section-B pattern
; --------------------------------------------------
; The design note (`docs/specs/2026-05-27-samdos-load-idiom.md`
; §"Pre-built trampoline reference") shows a `push af / ... / pop af`
; pair BRACKETING the HMPR change (push BEFORE the change, pop AFTER).
; That works only if SP points into LMPR-stable memory (section A or
; B).  In our memory layout SP points into section D (HMPR+1; the
; default stack at &C100 grows down into section D) — and section D
; IS paged out by the HMPR change.  Push-before-pop-after would write
; to one physical page and read from another, returning garbage.
;
; COMET avoids this by switching SP into a section-A/B scratch area
; via `LD SP, (sproom)` at the call site (`comet.asm:1189`).  We avoid
; it differently: we save the HMPR byte in a STATIC LOCATION in
; section B, right next to the trampoline body.  Section B is
; LMPR-stable across the trampoline's HMPR change, so the byte
; survives.
;
; We must NOT save it in a register, because:
;   - B/C are clobbered by ROM PTDOS (B = LMPR save, C = port number)
;   - HL is clobbered by ROM PTDOS (ADD HL, SP)
;   - A, D, E, F are clobbered by SAMDOS hook body or RFHK epilogue
;   - IX is left at SAMDOS's `dchan` (the dispatcher saves it but
;     does not restore)
;   - IY happens to survive, but we don't want to depend on undocumented
;     behaviour
;
; We also must NOT save it via stack push BEFORE the HMPR change and
; pop AFTER, because that crosses the HMPR window (push goes to
; orig+1 physical, pop reads target+1 physical — different memory).
;
; The static-save approach addresses these issues cleanly: one byte
; of section B holds the original HMPR across the RST 8.
;
; Position independence + the static-save absolute address
; --------------------------------------------------------
; The trampoline body uses absolute addressing for the static save
; (`ld (HMPR_SAVE), a` / `ld a, (HMPR_SAVE)`).  HMPR_SAVE is defined
; as a SECTION-B address (TRAMPOLINE_DST + offset).  So when pyz80
; assembles the source-side `trampoline_body` (in section C), the
; absolute address baked into the LD instructions ALREADY references
; section B.  After LDIR copies the body to TRAMPOLINE_DST, the COPY
; reads/writes the section-B byte at HMPR_SAVE — independent of
; where the COPY itself happens to live, and independent of HMPR
; (which controls section C/D only).
;
; All other instructions in the body use register-relative addressing
; (caller-supplied registers, RST 8's stack push), so they're
; trivially position-independent.
;
; ENCTAB runtime access — LMPR-swap pattern
; -----------------------------------------
; Once ENCTAB is loaded into physical page 4, the encoder + form_lookup
; need to READ from it during the assemble passes.  Approach: map page
; 4 into section A by setting LMPR = LMPR_ENCTAB (&24 = RAM0 bit + low
; 5 bits = 4).  The encoder reads ENCTAB at base address &0000 via
; section A.
;
; This is bracketed at a coarse grain in `main_assemble`:
; `enctab_map_in` is called after `load_in_file` (which needs ROM in
; section A for its RST 8), and `enctab_map_out` is called at the end
; of main_assemble before save_out_file.  Between those calls the
; encoder + form_lookup may read ENCTAB freely via direct addressing.
;
; LMPR = LMPR_ENCTAB means section A = page 4 (ENCTAB, RAM-replaces-
; ROM) and section B = page 5 (currently unused — we never write or
; read here while LMPR = LMPR_ENCTAB).  The trampoline copy at
; TRAMPOLINE_DST lives in section B under LMPR = LMPR_DEFAULT; with
; LMPR = LMPR_ENCTAB section B = page 5, so the trampoline copy is
; INVISIBLE — but we never call it during the LMPR = LMPR_ENCTAB
; window, so this is fine.
;
;
; ===========================================================================
; HOW TO EXTEND THIS PATTERN FOR IN AND OUT BUFFERS
; ===========================================================================
;
; The current code budget pressure (M5) was solved by paging ENCTAB
; out of section C — the largest single block, biggest payoff.  Later
; milestones (M6: large spectrum4 sources, ~20 KB input, ~22 KB
; output) will face the same pressure on IN and OUT buffers, and will
; need an analogous extension of this machinery.
;
; The PATTERN is the same:
;
; 1. **HSAVE (hook 132) trampoline.**  Mirror `trampoline_hload` but
;    with `defb 132` instead of `defb HOOK_HLOAD`.  Verify the exact
;    byte against samdos/src/b.s::samhk — HSAVE is samhk[4] = code
;    132 (`samdos/src/b.s:501 defw hsave ;132`).  HSAVE reads the
;    source address from UIFA byte 32 and the length from UIFA byte
;    35/36; the trampoline's job is to set HMPR so the SAMDOS reads
;    through section C land in the right physical page.
;
; 2. **Runtime read/write via section A.**  Parse-eval-encode-emit
;    interleaves reads from IN with reads from ENCTAB with writes to
;    OUT.  Three buffers all mapped via section A can't all be active
;    at once — only one page maps to A at a time.  Options:
;    (a) Snapshot a window of IN into a small section-C scratch
;        before encoding, encode from the scratch, write OUT to a
;        section-C scratch, then HSAVE the scratch.  Compatible with
;        the current encoder structure.
;    (b) Keep ENCTAB in section A (current LMPR = LMPR_ENCTAB setup),
;        keep IN in section B (via a second LMPR value), keep OUT in
;        section D (via HMPR change).  Three different paging
;        registers, complex bracketing.
;    (c) Split the encoder into phases: read-IN phase (LMPR=IN page),
;        encode-with-ENCTAB phase (LMPR=ENCTAB page), emit-to-OUT
;        phase (HMPR=OUT page).  Buffers the data in section D
;        scratch between phases.
;
;    No single option is obviously best — needs design analysis
;    informed by COMET's OUT-side write pattern (HSAVE assembles to
;    disk so COMET must have solved this).
;
; 3. **Survey-and-design BEFORE implementation.**  See the deferred-
;    work item in `docs/ROADMAP.md` flagged "M6 prerequisite —
;    trampoline extension for IN/OUT buffers".  The expected
;    workflow:
;
;    a) Read `~/git/comet/` (or `reference/comet-decoded/comet.asm`)
;       for HSAVE / save-side patterns.  COMET likely has a
;       counterpart to the HLOAD trampoline at lines 1265-1284.
;       Find it, document its calling convention.
;    b) If COMET's pattern doesn't translate cleanly, scan
;       `~/sam-corpus/disks/` for other SAM-era assemblers /
;       large-output programs (the COMET trampoline study found the
;       pattern in COMET because we already had its source; the
;       broader corpus may have other examples).
;    c) Write the design up alongside
;       `docs/specs/2026-05-27-samdos-load-idiom.md` (a follow-up
;       note in the same `docs/specs/` directory).
;    d) Open the implementation PR AFTER the design note is in.
;
; The architectural foundation for paged IN/OUT is already here — the
; trampoline + LMPR-swap pattern below generalises directly.  Don't
; re-derive it from scratch; extend it.


; -----------------------------------------------------------------------
; Page allocation constants.
; -----------------------------------------------------------------------
;
; SAM Coupé physical-page layout on a 256 KB machine after SAMDOS load
; (Tech Manual v3.0, page 57, "PAGE ALLOCATION TABLE" at &5100):
;
;   pages 0..3   BASIC program        (40H)
;   pages 4..12  Unused              (00H)   <-- we pick from here
;   page  13     DOS                 (60H)
;   pages 14..15 Screen              (C0H)
;   pages 16+    FFH                 (non-existent on 256 KB)
;
; We pick page 4 for ENCTAB: lowest free page, far from DOS / screen /
; BASIC defaults, no risk of stomping anything SAMDOS uses internally.
; (Tech Manual §"PAGE ALLOCATION TABLE": "Free pages can be used as
; temporary workspaces, provided you are sure that nothing is going
; to overwrite the page while you are using it.  (Interrupts do not
; do this, but the DOS might).")  Since our DOS use is HGTHD / HLOAD
; / HSAVE only — none of which use page 4 — this is safe.
;
; A formal ALLOCT update (marking page 4 as 20H "utilities" or
; similar) is NOT required for our case because BASIC will never run
; again after our `di; halt` exit.  If a future milestone returns
; control to BASIC, ALLOCT bookkeeping will be needed.

ENCTAB_PAGE:    equ     4              ; physical page holding ENCTAB
ENCTAB_BASE:    equ     &0000          ; section-A base address when
                                       ; LMPR = LMPR_ENCTAB (page 4
                                       ; mapped in via RAM0 bit + low
                                       ; 5 bits = 4)

; Note: there's no compile-time LMPR_DEFAULT constant.  Section B's
; page depends on what BASIC's CALL left LMPR at, and that varies
; (commonly &1F on 256 KB Coupé → section B = page 0 = BASIC sys
; page).  We capture the real boot value at startup into
; LMPR_DEFAULT_RUNTIME and use that for restoration.  See
; assembler.asm `start:`.
LMPR_ENCTAB:    equ     &20 + ENCTAB_PAGE
                                       ; RAM0 bit (=&20) + page 4 →
                                       ; section A = page 4.  We
                                       ; deliberately use a known
                                       ; absolute LMPR value for
                                       ; enctab_map_in, because the
                                       ; bit-5 RAM0 toggle has to
                                       ; happen unconditionally (the
                                       ; boot LMPR has bit 5 = 0; we
                                       ; need it = 1 to swap ROM out).

; OUT-buffer paged-output constants.
;
; Per docs/specs/2026-05-27-m6-paged-out-design.md.  OUT lives in
; physical pages 5..6 across two zones:
;
;   Low zone  (bytes 0..16383)   — section B during LMPR_ENCTAB window;
;                                  page 5 reached for free as
;                                  LMPR_ENCTAB+1 = &25 (Tech Manual
;                                  tech-man_v3-0.txt:908-910).
;   High zone (bytes 16384..32767) — LMPR brackets each emit to LMPR_OUT_HIGH,
;                                    placing page 6 in section B.
;
; HSAVE at end of pass 2 reads via section C with UIFA[31]=OUT_BASE_PAGE
; (= 5), HMPR auto-paging at &C000 to reach page 6 (see
; docs/specs/2026-05-27-samdos-save-idiom.md).

OUT_BASE_PAGE:  equ     5              ; first physical page of OUT
LMPR_OUT_HIGH:  equ     &25            ; RAM0 + low5=5; A=page 5, B=page 6


TRAMPOLINE_DST: equ     &7E00          ; section-B copy destination
                                       ; (under LMPR_DEFAULT, section B
                                       ; = page 1).  Near the top of
                                       ; section B so we don't
                                       ; interfere with anything we
                                       ; might read from page 1 —
                                       ; which is nothing, since BASIC
                                       ; is dead by the time we copy.

; Absolute address of the static HMPR-save byte in section B.  The
; trampoline body's `ld (HMPR_SAVE), a` and `ld a, (HMPR_SAVE)`
; instructions encode THIS address; after the LDIR copy, those
; instructions access section B (LMPR-stable across the HMPR change)
; regardless of where the trampoline copy itself lives.
;
; Placed at TRAMPOLINE_DST + 32 to leave plenty of headroom for the
; trampoline body (currently 15 bytes; a future extension might
; lengthen it, e.g. to do an HSAVE-side mirror).
HMPR_SAVE:      equ     TRAMPOLINE_DST + 32


; -----------------------------------------------------------------------
; enctab_trampoline_setup — copy the trampoline body into section B.
;
; Must be called ONCE at startup, BEFORE the first call to
; trampoline_hload.  Idempotent (safe to call multiple times).
;
; Input:  none.
; Output: trampoline body installed at TRAMPOLINE_DST in section B.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
enctab_trampoline_setup:
                ld      hl, trampoline_body
                ld      de, TRAMPOLINE_DST
                ld      bc, trampoline_body_end - trampoline_body
                ldir
                ret


; -----------------------------------------------------------------------
; trampoline_body / trampoline_body_end — the bytes that get copied
; into section B at startup.  The CALLER does NOT call this label;
; they call TRAMPOLINE_DST, which holds the COPY in section B (which
; is LMPR-stable across the HMPR change the body issues).
;
; The static-save absolute address (HMPR_SAVE) is in section B, so
; the LD (HMPR_SAVE), A and LD A, (HMPR_SAVE) instructions baked into
; the body access section B regardless of where the body's COPY runs
; from — making the body fully position-correct once LDIRed to
; TRAMPOLINE_DST.
;
; Critical: we MUST NOT clobber the caller's B/C/D/E/H/L between
; reading HMPR and the RST 8, because the SAMDOS hook dispatcher saves
; HL/BC/DE from the caller's MAIN register set at hook entry — and
; HLOAD's dschd then reads them back as the destination and pages-
; count.  Clobbering C (for example) would make HLOAD think the file
; spans more 16K pages than it actually does, and the load would
; over-read past EOF.
;
; Input (set up by caller before CALLing TRAMPOLINE_DST):
;   HL = &8000..&BFFF (HLOAD's section-C window)
;   B  = target physical page (5-bit, only low 5 bits used)
;   IX = UIFA pointer
;   C  = pages count (from DIFA+34)
;   DE = length modulo 16K (from DIFA+35, with bit 7 of D cleared)
;
; Output: HMPR restored to its entry value; HL/BC/DE/AF clobbered;
;         AF carries HLOAD's status (CY = error — but HLOAD longjmps
;         on error so CY in practice means "didn't longjmp = success").
;
; Caller must ensure interrupts are disabled BEFORE calling (the
; trampoline runs DI internally at the end but does not gate the
; entry — and PTDOS does `DI / OUT (250), A / LD SP, &8000 / EI`
; around its own work, so an interrupt during the HMPR-set window
; could see a deranged section C/D map).
trampoline_body:
                in      a, (251)                ; A = current HMPR
                ld      (HMPR_SAVE), a          ; save to absolute section-B
                                                ; address (LMPR-stable across
                                                ; the upcoming HMPR change)
                ld      a, b                    ; A = target page
                out     (251), a                ; HMPR = target
                                                ; (section C/D now points at
                                                ;  target page)
                rst     8
                defb    HOOK_HLOAD              ; 130 — longjmps on read error
                ex      af, af'                 ; preserve HLOAD's AF (CY = error)
                ld      a, (HMPR_SAVE)          ; A = saved HMPR (section B
                                                ; READ is LMPR-stable; AF
                                                ; flags from this LD don't
                                                ; matter because we restore
                                                ; AF below)
                out     (251), a                ; HMPR restored
                ex      af, af'                 ; restore HLOAD's AF
                di                              ; PTDOS does EI inside RST 8;
                                                ; restore the no-interrupts
                                                ; invariant
                ret
trampoline_body_end:


; -----------------------------------------------------------------------
; enctab_map_in — map ENCTAB (physical page 4) into section A.
;
; LMPR = LMPR_ENCTAB (&24): RAM0 bit set + low 5 bits = 4.  After this
; call, reads from &0000..&3FFF return ENCTAB bytes; the ROM is paged
; out of section A.  RST 8 calls will FAULT if attempted in this
; state — and so would the ROM interrupt handlers at &0038 / &0066,
; so we DI before the swap and leave interrupts disabled.
;
; Must be balanced by enctab_map_out before any RST 8 call.
;
; Clobbers: A.  Disables interrupts.
; -----------------------------------------------------------------------
enctab_map_in:
                di                              ; ROM handlers at &0038 / &0066
                                                ; would execute from section A,
                                                ; which is about to become ENCTAB
                                                ; data — must block interrupts.
                ld      a, LMPR_ENCTAB
                out     (250), a
                ret


; -----------------------------------------------------------------------
; enctab_map_out — restore the LMPR captured at boot.
;
; Reverse of enctab_map_in.  After this call, RST 8 (and any other ROM
; entry) works as it did under BASIC's original LMPR.  Interrupts
; remain DISABLED — the caller is responsible for re-enabling them if
; needed (the M3 assembler runs with interrupts disabled throughout;
; SAMDOS hooks internally EI/DI around their own dispatch).
;
; The restore value comes from LMPR_DEFAULT_RUNTIME, populated by
; assembler.asm's `start:` from `IN A, (250)` at the very first
; opportunity.  Hard-coding LMPR_DEFAULT=&00 (RAM0 off, low bits 0)
; is WRONG: BASIC's CALL 32768 typically leaves LMPR at &1F (low bits
; = 31 → section B = page 0 = BASIC sys page).  Restoring &00 instead
; would silently shift section B to page 1 (different memory) and the
; next RST 8's UIFA-via-section-B reads would target wrong addresses
; (or, more visibly, the SAMDOS hook would hang on the SAMDOS-side
; stack switch because PTDOS's `IN B,(250)` and later `OUT (250),B`
; restore-pair would round-trip the WRONG initial value).
;
; Clobbers: A.  Leaves interrupts in their current state (typically
; disabled — see above).
; -----------------------------------------------------------------------
enctab_map_out:
                ld      a, (LMPR_DEFAULT_RUNTIME)
                out     (250), a
                ret


; -----------------------------------------------------------------------
; Static storage — captured at boot, used by enctab_map_out.
; Lives in section C alongside the rest of the trampoline source.
; Read/written via absolute addressing; section C is HMPR-managed but
; nothing in our flow changes HMPR while these are being touched
; (other than the trampoline itself, which lives entirely in section B
; and doesn't read this slot).
; -----------------------------------------------------------------------
LMPR_DEFAULT_RUNTIME:   defb    0
