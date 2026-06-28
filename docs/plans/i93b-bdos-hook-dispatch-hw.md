# i93b — B-DOS RST 8 hook dispatch on real Trinity (HGTHD/HLOAD/HSAVE/HRECORD)

Registry item **i93b** (gates i70, i88c, i95b). This plan is deleted in the PR
that completes the work.

## What this proves

The full whole-file hook round-trip — HRECORD select → HSAVE → HGTHD → HLOAD →
byte-compare — dispatched via real `RST 8` against real B-DOS **1.5t** on real
Trinity hardware. Today this round-trip is proven only on emulated Atom Lite
B-DOS **1.5a** (`tools/i62-bdos-experiment/`, registry i62); the 1.5t leg is
verified-static only (`docs/notes/bdos-version-landscape.md` §"runtime
confirmation on real Trinity hardware remains outstanding"). HRECORD (+ALHK)
alone is already hardware-proven via boot_record (i316/i319 shots).

## Established facts the implementation leans on (do not re-derive)

- **HSAVE (hook 132) manages HMPR itself** and restores it before returning —
  no trampoline; just a correct UIFA (`docs/specs/samdos-file-io.md` §"WRITE —
  HSAVE needs no trampoline").
- **HLOAD (hook 130) never touches HMPR.** Loading into the page **already
  mapped at section C** therefore needs no trampoline either — the exact
  simplification `tools/i62-bdos-experiment/i62test.asm` uses (its step 5).
- **Calling `rst 8` from section C (org &8000) is safe**: boot_record does
  HRECORD+ALHK from &8000 on real 1.5t hardware (i319a-b2 shot, 2026-07-02);
  i62test did HRECORD/HSAVE/HGTHD/HLOAD from &8000 on emulated 1.5a.
- **Trinload's SP is RST-8-safe** (boot_record precedent). Do NOT switch SP.
- **DOS errors longjmp** into BASIC's error path — HGTHD "file not found",
  HRECORD "Invalid record" (rep81) etc. never return (registry i25;
  `src/netboot/bdos_seam.asm` bdos_lookup_hook header). The tool only ever
  HGTHDs the file it just HSAVEd, into a record it was explicitly given.
- **HRECORD validates the record's "BDOS" stamp** (body +232, first dir
  sector) — a record pushed by sd_push carries it (patched on write;
  memory `feedback_bdos_record_header_vs_disk_body`).
- RST 8 does `EI` and leaves IX at `dchan` — reload IX after every hook
  (`src/loader.asm` header notes).
- UIFA at `&4B00`, DIFA deposited at `&4B50`; identical on 1.5t
  (`docs/notes/bdos-version-landscape.md` §1.5t). The seam's
  `bdos_fill_save_uifa` / `bdos_name_to_uifa` / `bdos_difa_to_size` are
  host-verified — use them, don't reimplement.
- DIFA `+34` = pages, `+35..36` = lengthMod16K with **bit 7 of the high byte
  set as the length marker — clear it** before using as DE
  (`docs/specs/samdos-file-io.md` §"The trampoline contract").

## Deliverables

1. **`src/netboot/hook_roundtrip.asm`** — a trinload-pushable tool (tag `!HR`).
2. **`bdos_load_hook` added to `src/netboot/bdos_seam.asm`** — the missing
   HLOAD dispatch (the seam has HGTHD/HSAVE/HRECORD but no HLOAD).
3. **Faithful emulation gate** —
   `tools/netboot-oracle/z80/hook_roundtrip_faithful_test.go`: the whole
   round-trip against Colin's real ROM + B-DOS 1.5t + an emulated SD record,
   plus the i327 come-up registration.
4. **Host launcher** — `tools/trinload-push/hook-roundtrip.py`.
5. **Makefile target** — `netboot-hook-roundtrip` (+ fit check ≤16384).
6. The **hardware shot** (orchestrator-driven, not part of the implementer's
   scope): sd-push a scratch record → run the tool → PASS → delete the record.

## 1. The tool — `src/netboot/hook_roundtrip.asm`

Follow `src/netboot/boot_record.asm` (structure, header-comment style, exit
contract) and `src/netboot/sd_push.asm` (serve-loop framing, `?` tagging,
screen stage digits i318). Structure:

```
org &8000
jp hkrt_main
include "bdos_seam.asm"
; + the minimal ENC/network includes the serve loop needs (mirror sd_push's
;   include set MINUS sd_csd/raw-SPI: this tool talks to the card ONLY via
;   RST 8 — no NETBOOT_REAL_LISTREAD, no NETBOOT_WANT_CLAIM, no CMD17/24).
```

Config block at the **end** of the binary (mirror boot_record's BOOT_CONFIG,
magic `&5A`): `HKRT_CFG_RECORD` (LE16) — the target record number, host-patched.

Constants (mirror i62test where sensible):

```
SRC_BUF: equ &9000       ; pattern source — inside this tool's own page
DST_BUF: equ &A000       ; HLOAD-back destination — same page
PAT_LEN: equ 1553        ; 3 sectors + 17 bytes: crosses sector boundaries, odd tail
                         ; file name: "HKPROBE" (NUL-terminated)
```

`hkrt_main` flow — after each phase, print the stage digit to the screen
(sd_push i318 idiom) AND record it in a `hkrt_phase` byte; phases:

- **'1' come-up**: EEPROM config read + ENC init exactly as sd_push's come-up
  (the parts the i327 gate exercises), MINUS the SD/CSD/list-scan stages —
  B-DOS owns the card in this tool. Then broadcast-capable network up.
- **'2' HRECORD**: `ld a,(HKRT_CFG_RECORD)`-style load of the config word →
  `bdos_select_record`.
- **'3' HSAVE**: fill SRC_BUF with the deterministic pattern (i62test's
  generator: `(i*7+i>>8) & 0xFF` or equivalent — any fixed formula; the
  faithful test must use the same one). `BD_NAME_PTR`→"HKPROBE",
  `BD_SAVE_PAGE` = `in a,(251)` AND &1F (this tool's own physical page),
  `BD_SAVE_ADDR` = SRC_BUF, `BD_SAVE_SIZE` = PAT_LEN →
  `bdos_fill_save_uifa` → `bdos_save_hook`.
- **'4' HGTHD**: `bdos_name_to_uifa`("HKPROBE") → `bdos_lookup_hook` →
  `bdos_difa_to_size` → compare against PAT_LEN (32-bit compare; store
  FAIL detail on mismatch, skip to '6' verdict as FAIL).
- **'5' HLOAD**: new seam routine `bdos_load_hook` (below) with HL=DST_BUF,
  B = own page (same `in a,(251)` AND &1F), C = DIFA+34 pages,
  DE = DIFA+35..36 with bit 7 of D cleared, IX=&4B00.
- **'6' verdict**: byte-compare SRC_BUF vs DST_BUF over PAT_LEN. Verdict
  `'P'`/`'F'` + 16-bit fail detail (first mismatch offset, or &FFFF for the
  size-check failure). Print `P`/`F` on screen.
- **serve loop** (`hkrt_serve_loop` — the i327 gate needs this exact-ish
  symbol; check what `tool_comeup_faithful_test.go` expects and match):
  - `'?'` → reply `"!HR"` (3 bytes — mirror sd_push's `sp_serve_loop`
    discovery arm verbatim).
  - `'R'` → reply `[verdict]['0'+phase][detail LE16]` (4 bytes).
  - `'Q'` → `ei` / `ret` back to trinload (boot_record's exit-contract
    comment explains why never `di;halt`).
  - plus the ARP-request + ICMP-echo reply arms, ported the same way
    sd_push ports them from trinload.

If a DOS error longjmps mid-probe the tool is simply gone (BASIC error
screen): the host sees `'?'` unanswered, and the on-screen stage digit
localises the failing hook. Do NOT attempt a `&5BC0` error-vector trap in v1
— it is unproven on 1.5t and adds a mechanism to debug; the stage digits +
faithful gate carry the diagnosability.

## 2. The seam addition — `bdos_load_hook`

In `src/netboot/bdos_seam.asm`, next to `bdos_save_hook`, following the
existing comment style (present tense, cite the register contract):

```
; bdos_load_hook — copy the built UIFA to &4B00, issue HLOAD (hook 130).
; In: HL = section-C destination (&8000..&BFFF), B = target physical page,
;     C = pages count (DIFA+34), DE = lengthMod16K (DIFA+35, bit 7 of D clear).
; HLOAD never touches HMPR: with B = the page already mapped at section C the
; load lands in this tool's own page and no trampoline is needed
; (docs/specs/samdos-file-io.md; the i62test step-5 simplification).
; Longjmps on DOS error (registry i25). RST 8 does EI and leaves IX at dchan.
bdos_load_hook:
                push    hl / push de / push bc   ; (UIFA copy clobbers these)
                ld      hl, BD_UIFA
                ld      de, BD_UIFA_ADDR
                ld      bc, BD_UIFA_LEN
                ldir
                pop     bc / pop de / pop hl
                ld      ix, BD_UIFA_ADDR
                rst     8
                defb    130
                ret
```

(Exact push/pop ordering as needed — the above is the shape, not verbatim.)
The flat harness's `bdos_store.go` models neither HGTHD nor HLOAD — that is
fine: no flat-harness test may call `bdos_load_hook`; only the faithful tier
runs it. Do not add a flat model.

## 3. The faithful gate — `tools/netboot-oracle/z80/hook_roundtrip_faithful_test.go`

Two tests, both `SKIP_PRIVATE_TESTS`-gated (`requirePrivateCapture`):

- **`TestHookRoundtripComesUpFaithful`** — register the tool with the i327
  gate exactly as `TestSDPushComesUpFaithful` / `TestListRecordsComesUpFaithful`
  do in `tool_comeup_faithful_test.go` (entry symbol, serve-loop symbol, tag
  `"HR"`). NOTE: the shared `comeUpFaithful` helper may assert SD/CSD stages
  this tool doesn't perform — if so, parameterise or write the analogous
  bring-up inline; the assertion that matters is entry→serve-loop + exactly
  one `!HR` reply to `'?'`.
- **`TestHookRoundtripFaithful`** — the i93b emulation gate:
  1. Boot real ROM + 1.5t to editor idle with SD+ENC attached —
     `bootToEditorIdleSDENC` (`bdos_save_writes_record_test.go`).
  2. Seed a scratch record: an all-zeros 819200-byte .mgt via
     `seedRecordFromMGT(sd, N, mgt, "hkscratch")` + `seedRecordList` (the
     `trinload_idle_faithful_test.go` helpers). Confirm the seeded record
     carries the +232 "BDOS" body stamp (check what seedRecordFromMGT does;
     if it doesn't stamp, set bytes 232..235 of the .mgt's first dir sector
     to "BDOS" in the test fixture — that is what sd_push patches on write).
  3. Load `build/hook_roundtrip.bin` to page 1 per the deployment contract
     (`tool_comeup_faithful_test.go` `comeUpFaithful`), patch the config
     word to the seeded record number (find the `&5A` magic offset from the
     .map exactly as `boot-record.py` / `stageBootRecord` does).
  4. Run entry → serve loop (bounded cycles).
  5. Assert: phase byte reached '6', verdict 'P' — read them via the 'R'
     verb (inject the UDP packet like the come-up test injects `'?'`) or
     directly from the tool's RAM via the .map symbols (simpler, fine).
  6. Assert the HSAVE physically landed: the pattern is findable in the SD
     model's written blocks within the scratch record's LBA range
     (`storeBlocks` / `findPatternBlock` idiom from
     `bdos_save_writes_record_test.go`).

Run: `cd tools/netboot-oracle && go test -count=1 -run 'HookRoundtrip' ./z80/`.

## 4. Host launcher — `tools/trinload-push/hook-roundtrip.py`

Mirror `boot-record.py` (config patch by `&5A`-magic offset + push via the
shared `trinpush.py` lib) and `sd-push.py` (post-push protocol chat):

```
hook-roundtrip.py <ip> <record-number>
```

1. Patch `HKRT_CFG_RECORD` in a copy of `build/hook_roundtrip.bin`.
2. Stage-1 discovery guard (i329): probe `'?'` — proceed only on a bare `'!'`
   (trinload) exactly as the other launchers do.
3. Push + run.
4. Poll `'?'` until `"!HR"` (the probe phases take well under a second;
   timeout ~30 s), then send `'R'`, print the decoded verdict/phase/detail,
   send `'Q'`, exit 0 on 'P' / 1 otherwise.

## 5. Makefile

Target `netboot-hook-roundtrip` mirroring `netboot-boot-record` (no
NETBOOT_REAL_LISTREAD, no NETBOOT_WANT_CLAIM — RST 8 only):
`pyz80 --obj=build/hook_roundtrip.bin --mapfile=build/hook_roundtrip.map
src/netboot/hook_roundtrip.asm` + `tools/netboot-boot-fit-check.sh` at 16384.
Add the tool to whatever aggregate target the other pushables hang off
(check how sd_push/boot_record are reached from `all`/CI).

## 6. Verification ladder (implementer runs 1–3; orchestrator runs 4)

1. `make netboot-hook-roundtrip` — builds + fits.
2. `cd tools/netboot-oracle && go test -count=1 -run 'HookRoundtrip' ./z80/` — both faithful tests green.
3. `go test -count=1 ./...` in tools/netboot-oracle + `make registry-sync-check` — no regressions.
4. Hardware shot (orchestrator): tapo on → verify trinload (`'?'`→bare `'!'`)
   → `sd-push.py` an all-zeros scratch .mgt (name "hkscratch") → record N from
   the 'D' reply (fallback `list-records.py`, i335) → `hook-roundtrip.py <ip> N`
   → expect PASS → `delete-record.py N` → `list-records.py` confirms freed →
   tapo off. `DEPLOY_CHECKED=1` on every deploy step
   (`docs/notes/netboot-trinity-testing.md`).
