# i135d — emulation-prototype the SAMBOOT bootblock injection (build plan)

Ephemeral plan (deleted by the completing PR). Charter: `docs/specs/samboot.md`
§4/§6 step 5. Bootblock structure authority: `docs/notes/samboot-bootblock-analysis.md`
(§2 stripes, §3 the TODO hook site). **This PR also completes i112** (the stripes
redraw is folded in).

## Scope (SMALL — pure glue over two tested primitives)

Prototype the patched bootblock's **decision + dispatch** logic that attaches at
the analysis §3 TODO hook (between `CALL stripes` and `restore:`): redraw stripes
unconditionally → read the SAMBOOT BIOS config (i176) → auto-boot the configured
record (i122a) or fall through to BASIC. Both primitives already exist and are
harness-tested, so this is control flow + a stripes stub + a test. No Go authority.

## B. The Z80 routine — `src/netboot/samboot_inject.asm`

```asm
; samboot_inject.asm — i135d emulation prototype of the patched-bootblock
; decision+dispatch (analysis §3 TODO hook). Reuses i176 (samboot_read_config)
; + i122a (bdos_boot_record) verbatim. The reset->ROM->bootblock->B-DOS-load
; chain and the restore/JP-4143 exit are hardware (i135c); this models only the
; injected segment the flash adds.

samboot_inject:
                call    samboot_stripes         ; ALWAYS redraw the MGT stripes (i112 fold)
                call    samboot_read_config     ; i176: A=1/HL=record (CY set) or A=0 (CY clr)
                ret     nc                       ; A=0: no auto-boot -> fall through to BASIC
                ld      a, l                     ; BD_BOOT_RECORD is 1-byte (record <=255)
                ld      (BD_BOOT_RECORD), a
                jp      bdos_boot_record         ; i122a: HRECORD-select + ALHK-boot, no keypress

; samboot_stripes — redraw the MGT opening stripes (i112). Decision-free MODE-3/2
; palette+banner blit is hardware-gated (the picker_render precedent,
; bdos_picker.asm:563). The host test asserts only that this is CALLED (the probe
; counter), in BOTH branches — proving the unconditional redraw + no wait_for_key.
samboot_stripes:
                ld      hl, SAMBOOT_STRIPES_CALLS
                inc     (hl)
                if defined(NETBOOT_HOSTTEST)==0
                ; Real-hardware stripes (analysis §2): clearscn, PALTAB(&55D8)->
                ; LINICOLS(&5600) rainbow stepping &0B/line to &A6, MGT banner via
                ; RST 16. Hardware-gated, drawn for real at i135c.
                endif
                ret

                include "samboot_config.asm"     ; samboot_read_config (+ eeprom.asm, org &8000)
                include "bdos_seam.asm"           ; bdos_boot_record + BD_BOOT_RECORD

SAMBOOT_STRIPES_CALLS:  defs 1                    ; host-test probe: stripes call count
```

Notes:
- Do NOT define `NETBOOT_STANDALONE` — let `samboot_config.asm`'s `org &8000`
  stand as the only org. Assemble once and confirm from the `.map` that there is a
  single org and no double-include (neither include is pulled twice here, so no
  pyz80 `-D` dedupe needed — but verify).
- `samboot_read_config` contract (i176): In nothing; Out A=0 (no boot) / A=1 +
  HL=record, CY mirrors A. `bdos_boot_record` (i122a, `bdos_seam.asm:678-700`):
  HRECORD-select + ALHK-boot; ALHK never returns on hardware (harness captures it).
- `BD_BOOT_RECORD` is 1 byte (`bdos_seam.asm:743`); pass `L`. Record >255 would
  need `bdos_select_record` widening — out of scope; document inline (real cards
  have <256 records).

## C. Harness test — `tools/netboot-oracle/z80/samboot_inject_test.go`

Combine `samboot_config_test.go` (programs the config chunk via
`enc.ProgramNamedChunk(slot, samboot.ChunkName, samboot.Boot(N).Encode())`) and
`bdos_boot_test.go` (`store := NewBDOSStore(); mac.AttachBDOS(store)`; assert
`store.Boots()`/`store.Selected()`). Load `build/samboot_inject.bin`; `CallEntry("samboot_inject")`.

| Test | Config | Assert |
|---|---|---|
| AutoBoot | `samboot.Boot(7)` | `store.Selected()==7`, `store.Boots()==[7]`, stripes-probe==1 |
| SecondRecord | `samboot.Boot(0x12)` | `store.Boots()==[0x12]` |
| NoneMode | `samboot.None()` | `store.Boots()` empty, `Selected()==-1`, stripes-probe==1 |
| AbsentChunk | (none programmed) | `store.Boots()` empty, stripes-probe==1 |
| BadVersion | `Boot(9).Encode()` with `data[0]=0xFF` | `store.Boots()` empty |

Read the probe via `mac.Read(symAddr(t, mac, "SAMBOOT_STRIPES_CALLS"), 1)` (the
`symAddr` helper is used across the bdos tests). All harness hooks already exist
(`bdHookHRECORD`/`bdHookALHK`/`ProgramNamedChunk`) — zero Go harness changes.

## D. i112 fold
The unconditional `samboot_stripes` call IS i112 (restore the trampled opening
screen). Emulation-testable: it is *called* in every branch (probe==1). Pixels are
hardware-gated (picker_render precedent). **Mark i112 DONE in this PR too**
(`set-status --id i112 --status DONE --pr <N>`); one PR completes i135d + i112.

## E. Hardware-first (i135c, Pete) — state explicitly, ship nothing un-emulated silently
Real reset→ROM-chip→bootblock entry; real B-DOS load from chunks 2–13 + restore/
JP-4143 exit; real stripes pixels; real record auto-boot (ALHK runs the AUTO file
on hardware); flashing chunk-1 via `write_chunk` (i135c, with the i87a backup);
the analysis §5 unknowns (chunk-1 byte-match, `&804C` entry, `OUT &F8` poke) —
confirmed against captured `eeprom.bin`/`rom.bin` (i87a/i87b).

## F. Build / wiring
- New `src/netboot/samboot_inject.asm`; one row in `src/netboot/README.md` map.
- Makefile: a `netboot-samboot-inject` target (copy the `netboot-samboot-config`
  block, ~lines 747-764, `-D NETBOOT_HOSTTEST=1`). Add to `.PHONY` (line 76) +
  `netboot-z80-routines` so `ci-netboot-z80` builds it before `go test`.
- No Go authority (control flow over already-ported primitives). No disk/loader
  wiring (host-test routine; the bootable form is the i135c EEPROM flash).

## G. Open questions → none blocking
16-bit record truncation (document inline); org hygiene (verify from `.map`); the
i135c hardware confirmations are already tracked. Nothing needs Pete to start.
