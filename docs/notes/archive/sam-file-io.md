# SAM disk file I/O API (M0 Task 6 spike)

Status: **research spike, M0 Task 6**. Output: this document, plus a
skeleton include file at `src/sam_io.inc` (TODO bodies; Task 7 fills
them).

## TL;DR

- The SAM Coupé's normal application-level disk API is **SAMDOS hook
  codes** invoked via `RST 8` followed by an immediate byte. Standard
  SAMDOS, MasterDOS and Pro-DOS all expose the same hook-code numbers
  for the file primitives we need; the COMET manual confirms it
  ("COMET works with SAMDOS or MASTERDOS"). We target SAMDOS 2 — that
  is what SimCoupé substitutes by default and what is on every COMET
  disk.
- The four primitives we need (open input, read byte, open output,
  write byte) are exposed as hook codes 158, 159, 147, 148; close-on-
  output is 152. There is **no explicit close-on-input** in SAMDOS —
  the input buffer is reused on the next open. Concrete byte offsets
  in the SAMDOS source are in the ["Hook code reference"](#hook-code-reference)
  table below.
- **The disk we build in Task 5 does *not* need SAMDOS injected onto
  it** to run on SimCoupé. SimCoupé has a `-dosboot 1` option (default
  yes) that detects an unbootable disk and temporarily swaps in
  `samdos2.sbt` to install SAMDOS in RAM, then continues the user's
  boot. So Task 7 can rely on `RST 8` working out of the box, with
  zero changes to `tools/build-disk.sh`. (Real hardware lacks this
  trick — flagged as a future-portability concern below.)
- COMET's source corroborates the calling convention but uses higher-
  level "load whole file" hooks (HGTHD = 129, HLOAD = 130) rather than
  the byte-stream API. For our streaming-aware stub we use the byte
  primitives; the calling protocol (IX → UIFA at `&4B00`, name +
  type pre-filled) is identical.

## Calling convention

SAMDOS routines are **hook-coded subroutines** entered via `RST 8`
followed by an inline byte. The convention is established in the SAM
ROM and intercepted by SAMDOS once it loads.

```asm
ld   ix, &4B00          ; UIFA buffer
; ... fill UIFA: type byte + 10-char name + ... + load addr + length
rst  8                  ; gateway into DOS / ROM error reporter
defb 158                ; hook code: HGFLE (open file for reading)
; control returns here on success;
; on error, SAMDOS prints "File not found" and longjmps to BASIC
; (see "Error handling" below).
```

The dispatcher is `samdos/src/b.s::hook` (line 439). It saves the
current registers (notably IX, which is the caller's UIFA pointer)
into `(svhdr)`, then indexes into `samhk` (b.s line 497, the table of
function pointers per hook code) and invokes the matching routine.
Return is via `rfhk` (b.s line 475) which clears `flag3` and returns
to the instruction *after* the `defb`.

### What lives in the UIFA buffer

The UIFA ("user information area") is a 48-byte struct at `&4B00`
that the caller pre-fills with the file metadata. SAMDOS reads it via
the `nrread` ROM call from the saved IX address, into its own working
copy at `uifa` (a.s line 119, in bank 1). Layout:

| Offset | Bytes | Field           | Notes                                             |
| ------ | ----- | --------------- | ------------------------------------------------- |
| 0      | 1     | type            | 19 = code, 16 = SAM BASIC, etc. (see drtab f.s 322) |
| 1–10   | 10    | name            | space-padded                                       |
| 11–14  | 4     | extension       | space-padded; "    " is fine                       |
| 15–30  | 16    | filler / hidden | initialised to `&FF`                               |
| 31     | 1     | start page      | for code files: page number mod 32                 |
| 32–33  | 2     | load addr       | LE; for code: target address mod 16K               |
| 34     | 1     | pages           | high byte of file length (full 16K pages)          |
| 35–36  | 2     | length mod 16K  | LE; for code: length within last page              |
| 37–47  | 11    | reserved        | leave as `&FF`                                     |

`evfile` (h.s line 377) is SAMDOS's parser that walks the user's
filename string into UIFA. We bypass that — we write the binary form
directly, the same way COMET does (`comet.asm` line 194–205).

The `&4B00` address is a SAM-specific scratch area in bank 1
(page 1, base of the gnd / "ground" workspace). It is not paged in
by default; SAMDOS's `nrread`/`nrrite` helpers handle the paging
during hook entry. As applications, we just write to `&4B00..&4B2F`
in our own context — at boot SimCoupé's RAM has bank 1 paged in at
that address by default (see `b.s` line 17: `gnd: equ &4000`).

### Filename format on disk

The on-disk directory entry stores the name **exactly** as the UIFA
caller writes it: 10 bytes, space-padded, case-as-given. There is no
extension stored separately on disk — the 4-byte UIFA "extension"
field is purely a UIFA convention; it does not appear in the
directory. Wildcard matching in `cknam` (c.s line 1148) uppercases
both sides via XOR with `&DF`, so case is irrelevant for lookup.
Names longer than 10 chars are an error (`evnam`, f.s line 829, →
report 8 "Invalid file name").

For our purposes:

- Input file: name `IN        ` (`I N` + 8 spaces).
- Output file: name `OUT       ` (`O U T` + 7 spaces).
- Extension field: 4 spaces.

## Hook code reference

Codes are documented at `samdos/src/b.s` line 497 (`samhk` table).
Below: the file-I/O subset, with each routine's primary entry point
and the line in the SAMDOS source where the body lives.

| Hook | Mnemonic | File   | Line | Purpose                                     |
| ---- | -------- | ------ | ---- | ------------------------------------------- |
| 129  | HGTHD    | h.s    | 59   | Get header (read directory entry only)      |
| 130  | HLOAD    | h.s    | 70   | Load whole file into memory (used by COMET) |
| 132  | HSAVE    | h.s    | 132  | Save block as a complete file               |
| 134  | HOPEN    | h.s    | 162  | OPEN — *no-op stub in SAMDOS 2*             |
| 135  | HCLOS    | h.s    | 164  | CLOSE — *no-op stub in SAMDOS 2*            |
| **147** | **HOFLE** | h.s | 242  | **Open new file for streaming write**     |
| **148** | **SBYT**  | c.s | 533  | **Save one byte to current open file**    |
| **152** | **CFSM**  | c.s | 1306 | **Close (flush) current output file**     |
| **158** | **HGFLE** | h.s | 252  | **Open existing file for streaming read** |
| **159** | **LBYT**  | c.s | 557  | **Load one byte from current open file**  |
| 166  | HERAZ    | h.s    | 262  | Erase a file                                |

(Bold rows are the five we'll call in Task 7.)

`HOPEN` (134) and `HCLOS` (135) — the obviously-named entry points —
are bare `ret` instructions in SAMDOS 2 (h.s lines 162 and 164). They
are vestigial slots in the hook table from an earlier API and **must
not be used**. The actual file-streaming hooks are `HOFLE` /
`HGFLE` for open and `CFSM` for close-on-output. (MasterDOS may
populate HOPEN/HCLOS — out of scope for M0.)

### Read pipeline (open / read byte / close)

SAMDOS-side:

- **HGFLE** (h.s line 252):
  ```asm
  hgfle:  call rxhed       ; copy 48 bytes from caller's IX to uifa.
          call gtfle       ; resolve UIFA → directory entry; fills FCB at dchan.
          ld   de,(svde)   ; first track/sector of the file's data block.
          call rsad        ; read that sector into the DOS buffer.
          call ldhd        ; consume the 9-byte file header (LBYT × 9).
          ret
  ```
  After return: the directory entry is loaded, the first 512-byte data
  sector is in the DOS buffer at `dram` (a.s line 147), the in-buffer
  read pointer (`rptl`/`rpth` in `dchan`, see fcb layout below) sits
  immediately past the 9-byte SAMDOS file header — i.e. at the start of
  user payload.

- **LBYT** (c.s line 557): one byte from the buffer at IX-relative
  pointer; if the buffer is exhausted, walk to the next track/sector
  via the link bytes at offset 510–511 and call `rsad` to read it in.
  Returns `A` = byte; CY undefined unless EOF (in which case
  SAMDOS errors out via `rep26`, "End of file"; longjmp).

- **No close-on-read**: SAMDOS does not provide one — see the no-op
  `hclos` stub above. The next `HGFLE` reuses the buffer and FCB. Our
  wrapper `close_input` is therefore an empty `ret` for SAMDOS, kept
  as a named entry point so the API surface is symmetric and so
  MasterDOS (which does have one) can be slotted in later.

### Write pipeline (open / write byte / close)

- **HOFLE** (h.s line 242):
  ```asm
  hofle:  call rxhed       ; copy caller's IX → uifa.
          call ofsm        ; "open file sector map": find a free directory
                           ; entry, allocate sectors, build sector-address-
                           ; map in-memory.
          ret c            ; CY = name conflict / disk full / dir full.
          call svhd        ; SBYT × 9 — write the 9-byte file header.
          ret
  ```
  After return: directory entry reserved, header written, buffer
  ready to receive payload bytes.

- **SBYT** (c.s line 533): write one byte into the buffer at the
  IX-relative pointer; if the buffer is full, allocate the next
  free sector, link it from the previous sector's tail bytes, and
  flush the previous sector via `wsad`. The directory's sector map
  grows as sectors are consumed.

- **CFSM** (c.s line 1306): zero-pad the rest of the current sector,
  flush it via `wsad`, finalise the directory entry by copying the
  in-memory sector-address map back to the directory sector via
  `fdhr` and `wsad`. *Without this call the directory entry is left
  in the "open" state and the file is unreadable* — every test that
  exercises write must call CFSM.

### Register conventions

Across all five hooks:

| Hook   | In: IX        | In: HL/DE/BC                       | Out: A      | Out: CY                    |
| ------ | ------------- | ---------------------------------- | ----------- | -------------------------- |
| HGFLE  | UIFA pointer  | (UIFA fields drive everything)     | unspecified | (errors longjmp; see below)|
| LBYT   | (preserved)   | (preserved)                        | byte read   | (errors longjmp)           |
| HOFLE  | UIFA pointer  | (UIFA drives name + type only)     | unspecified | set if name conflict / full|
| SBYT   | (preserved)   | (preserved)                        | A unchanged | (errors longjmp)           |
| CFSM   | (preserved)   | (preserved)                        | unspecified | (errors longjmp)           |

(SAMDOS preserves the alt-register set; main-set BC/DE/HL clobbered
inside the hooks but not used as parameters.)

### Error handling — the longjmp gotcha

Most SAMDOS errors **do not return to the caller**. The dispatcher
saves the entry SP at `entsp` (b.s line 355), and on error
`derr` (d.s line 430) reloads it, restores AF on the original frame
to indicate "no result", and jumps to the BASIC error handler. From
the application's perspective, a missing file or full disk causes
control to **never return from `RST 8`** — instead, BASIC's
`Ready` prompt appears with an error message printed.

Implications for the M0 stub:

1. **For pre-existence checks** (e.g. is `IN` actually on the disk?),
   we must rely on the build harness to ensure `IN` is present. We
   cannot probe-and-recover at runtime.
2. **For disk-full / name-in-use during create**, HOFLE is one of
   the few hooks that *does* honour the SAMDOS interactive overwrite
   prompt instead of longjmp-ing — see `ofsm` (c.s line 1185), which
   prompts "OVERWRITE?" via beep + `cyes` if the name exists. For
   our headless harness, we should erase any old `OUT` first
   (HERAZ = 166) then HOFLE; that turns the path deterministic.
3. **For mid-stream errors (EOF, disk full)**, the longjmp lands us
   at BASIC's `Ready` prompt. The Z80 keeps running there, *not* in
   our stub. SimCoupé's `-exitonhalt` won't fire because we never
   reach `HALT`. Task 7's stub should therefore (a) erase OUT first,
   (b) not call LBYT past the known input length, and (c) call
   CFSM cleanly on the way out.

A more defensive option is to install our own `(hksp)` handler
(b.s line 160 — a stack pointer SAMDOS jumps to on error if non-zero)
to catch errors and report them via a chosen exit code. This is *not*
needed for M0 (Task 7's stub processes a fixed-size known input) but
is worth flagging for M1+.

## DOS we are targeting: SAMDOS 2

The target is **SAMDOS 2** (specifically the version cloned from
`stefandrissen/samdos`, `samdos.s` v1.2 dated 1990-01-24, signature
"SAMDOS 1.1" in the error table d.s line 465 — yes the file headers
say "1.2" while the printed error banner says "1.1"; both are SAMDOS
2 in modern parlance).

Why SAMDOS 2 and not MasterDOS:

- It's the de facto standard, included in the SAM ROM image set, and
  what SimCoupé substitutes by default (`-dosdisk` blank → built-in
  `samdos2.sbt`).
- COMET's manual: "COMET works with SAMDOS or MASTERDOS" — same hook
  codes, so what we write for SAMDOS will work with MasterDOS at
  runtime if a user supplies their own MasterDOS-loaded disk.
- MasterDOS adds capabilities (open multiple files; HOPEN/HCLOS
  populated; directory tree) but the core hook codes 147 / 148 /
  152 / 158 / 159 are identical.
- Task 7's stub uses *only* the SAMDOS-2 subset (single open input
  + single open output, sequentially). That's the lowest common
  denominator; it works on either DOS.

## Does SAMDOS need to be on the test disk image?

**Short answer: not for M0 / SimCoupé. Yes for real hardware.**

### On SimCoupé (the M0 / CI target): no.

SimCoupé has an option `-dosboot <bool>` (default = yes) that, when
the user's disk is unbootable (no valid SAMDOS file present), swaps
in an internal `samdos2.sbt` boot disk, lets SAMDOS install itself
into RAM, then puts the user's disk back. The mechanism lives in
`Base/SAMIO.cpp::Rst8Hook` (line 980): when the SAM ROM emits
`RST 8 / DEFB &35` ("No DOS") or `&13` ("Loading error"), SimCoupé
intercepts that, replaces the boot drive contents with `samdos2.sbt`,
and jumps the CPU back to the BOOTEX entrypoint. SAMDOS boots,
hooks itself in, and then the user's disk is restored — at which
point BASIC's `LOAD "auto"` runs the auto file from the user's disk.

So our Task-5 disk image, which contains only `auto`, `stub`, `IN`,
gets a working SAMDOS at runtime "for free", and Task 7's `RST 8`
hook calls will work without `tools/build-disk.sh` changes.

The SimCoupé manual confirms this in plain English (Manual.md line
52–54):

> The default SimCoupe settings avoid this error by substituting an
> internal DOS image, so you're more likely to see the following
> error instead.

The manual also lists the relevant CLI option (line 721–722):

```
-dosboot <bool>         Automagically boot DOS (default=yes)
-dosdisk <path>         Custom DOS boot disk (blank for SamDos 2.2)
```

Concretely: on a freshly booted SimCoupé the SAM ROM writes
`RST 8 / DEFB &35` because our disk has no boot sector recognised by
the ROM; SimCoupé sees the No-DOS code, swaps `samdos2.sbt` in,
re-runs BOOTEX, and now SAMDOS is resident.

We have already verified this end-to-end in Task 5: that disk has
no SAMDOS file, and yet the BASIC `LOAD "stub" CODE : CALL 32768`
auto-runs successfully. If `-dosboot` were off, we'd see "No DOS"
on the stripey screen and the BASIC `LOAD` would never execute.

### On real SAM Coupé hardware: yes.

A real SAM has only what's on the disk and in the ROM. The ROM does
**not** know about SAMDOS hook codes — those are SAMDOS's own
extension to `RST 8`, installed when SAMDOS loads. So a real SAM
without SAMDOS on disk produces the No-DOS error and never gets to
our stub.

For **future** real-hardware support, `tools/build-disk.sh` will need
to inject SAMDOS itself into the boot sector (or include a SAMDOS
file in the directory; the SAM ROM's BOOT routine looks for the
sector signature, then loads the DOS file). The mechanics of
"sams the .sbt onto the disk" are documented at
`~/git/simcoupe/Manual.md` lines 95–98 (SBT format) — essentially
the .sbt data is written verbatim onto sectors starting at track 0
sector 1, replacing the SAMDOS-less directory's start.

This is **not in M0 scope** — the SimCoupé harness is what we ship
in M0 and CI. Real-hardware testing is M-something-later, and at
that point a `--with-samdos` flag on `build-disk.sh` (or always-on)
plus a vendored copy of `samdos2.sbt` in the repo gets us there.

## How COMET uses RST 8 (cross-check)

The COMET disassembly (`reference/comet-decoded/comet.asm`) is a
useful sanity check on the protocol. It contains exactly four
`RST 8` calls. Two are in `ROMex.asm` (lines 59 and 99) and these
are *bare-metal pre-DOS error reports* (codes &37 = "Missing disk",
&13 = "Loading error", &35 = "No DOS") — not file I/O.

The two file-I/O `RST 8`s are in `comet.asm`:

```asm
filehead:                              ; comet.asm line 194
        LD   DE,&4B00                  ;start of uifa
        PUSH DE
        POP  IX
        LD   HL,filename               ;move file name
        LD   BC,15                     ; 15 = 1 (type) + 10 (name) + 4 (ext)
        LDIR
        RST  8                         ; get header from disk
        DEFB 129                       ;hookcode HGTHD
        RET
filename:
        DEFB 19                        ;type code (= code file)
        DEFM "No File       "          ; 10-char name + 4-char ext
```

```asm
loaddata:                              ; comet.asm line 1273
        IN   A,(251)
        PUSH AF
        LD   A,B
        OUT  (251),A
        RST  8
        DEFB 130                       ; hookcode HLOAD
        EX   AF,AF'
        POP  AF
        OUT  (251),A
        ...
```

```asm
fileheader:                            ; comet.asm line 1286
        ...
        LD   DE,&4B00
        PUSH DE
        POP  IX
        LD   A,19
        LD   (DE),A
        ...
        LDIR                           ; copy 10-char name into UIFA
        ...
        RST  8
        DEFB 129                       ;hookcode HGTHD
```

So COMET:

- Uses UIFA at `&4B00` (matches what we plan to do).
- Writes 15 bytes (1 type + 10 name + 4 ext), space-padded (matches).
- Uses **hooks 129 (HGTHD) and 130 (HLOAD)** — i.e. the high-level
  "give me the directory entry" + "load the whole file into memory"
  entry points. Not the streaming byte API.

Why the difference? COMET edits a small source file that fits
entirely in memory; loading and saving as monolithic blocks is the
natural fit. Our M0 stub is byte-streamed because (a) it parses one
mnemonic at a time without buffering the whole input, and (b) we
want to validate that the byte-level API actually works for the
streaming use cases of M1+ (label tables, multi-pass, etc.).

The protocol bits are identical:

- IX = `&4B00`.
- UIFA pre-filled with type byte + 10-char space-padded name +
  4-char space-padded ext.
- `RST 8` + `DEFB <hook>`.

So the cross-check is "yes, COMET confirms the calling convention,
even though it picks different hook codes."

## Pseudocode in stub.asm

For Task 7. The bodies in `src/sam_io.inc` will resolve to the
straightforward `ld ix, UIFA / rst 8 / defb HOOK_xxx` plus a
filename setup block:

```asm
; --- Read IN, write OUT, single byte at a time. ---
                org     &8000
                include "sam_io.inc"

start:          di                    ; we're a one-shot batch program

; -- delete OUT if it exists, to make HOFLE deterministic --
                ld      hl, name_OUT
                call    fill_uifa     ; type=&13, name+ext copied in
                ld      ix, UIFA
                rst     8
                defb    166           ; HERAZ; ignore "not found" error.

; -- open IN for reading --
                ld      hl, name_IN
                call    fill_uifa
                call    open_input    ; rst 8 / defb HOOK_HGFLE

; -- create OUT --
                ld      hl, name_OUT
                call    fill_uifa
                call    create_output ; rst 8 / defb HOOK_HOFLE

; -- copy 4 bytes IN → discard, then emit fixed [d5 03 20 1f] to OUT --
                ld      b, 4          ; ignore N input bytes
discard:        call    read_byte
                djnz    discard

                ld      hl, payload
                ld      b, 4
emit:           ld      a, (hl)
                call    write_byte
                inc     hl
                djnz    emit

                call    close_output  ; rst 8 / defb HOOK_CFSM

                halt                  ; SimCoupé sees DI;HALT, exits 0.

payload:        defb    &d5, &03, &20, &1f
name_IN:        defb    19            ; type: code (matches build-disk.sh)
                defm    "IN        "  ; 10 chars
                defm    "    "        ; 4-char ext
name_OUT:       defb    19
                defm    "OUT       "
                defm    "    "

; fill_uifa: copy 15 bytes from (HL) into UIFA, pad rest with &FF.
fill_uifa:      ld      de, UIFA
                ld      bc, 15
                ldir
                ld      a, &FF
                ld      b, 48 - 15
fu1:            ld      (de), a
                inc     de
                djnz    fu1
                ret
```

(The above is sketch-only; Task 7 writes the actual bytes and the
`call open_input` / `call create_output` / `call close_output` / etc.
bodies in `sam_io.inc`. The pre-`HERAZ` is the deterministic-rerun
trick. The 4-byte payload `d5 03 20 1f` is the M0-task-spec output
documented in the plan, and matches the bytes that `aarch64-none-elf-as`
emits for a single `nop` — i.e. a stand-in for "real assembler
output" until M1.)

## Open questions / future work

1. **MasterDOS HOPEN/HCLOS semantics.** If/when we add MasterDOS
   support, the no-op stubs at codes 134/135 become the canonical
   open/close. The current `sam_io.inc` keeps a `close_input` wrapper
   around what is currently a SAMDOS no-op so MasterDOS can be slotted
   in by changing one line.
2. **Custom `(hksp)` error handler.** Mid-stream EOF / disk-full
   errors longjmp out of our stub today. A custom handler at the
   SAMDOS-documented `(hksp)` slot (b.s line 160) would let us
   convert errors into a clean exit code. Useful for fuzz / negative
   tests in M2+; not needed for M0.
3. **Real-hardware path: bake SAMDOS onto the disk.** Out of M0
   scope, but the docs above point at the pieces:
   - SAMDOS is distributed as `samdos2.sbt` (a ~7 KB SBT bootable
     image).
   - Writing it onto an MGT image: copy the `.sbt` body into sectors
     starting at the disk's boot region (track 0 sector 1 onward),
     and update the directory accordingly. SimCoupé's
     `samdos2.sbt` resource is a good reference.
   - Cleanest harness change: a `tools/build-disk.sh --with-samdos`
     flag (default off in M0; on in real-hardware mode).

## References

- `~/git/samdos/src/samdos.s` and `a.s`–`h.s`. In particular:
  - `samhk` table — `b.s` line 497.
  - `hook` dispatcher entry — `b.s` line 439.
  - `hopen` / `hclos` stubs — `h.s` lines 162, 164.
  - `hofle` (open-write) — `h.s` line 242.
  - `hgfle` (open-read) — `h.s` line 252.
  - `sbyt` (write byte) — `c.s` line 533.
  - `lbyt` (read byte) — `c.s` line 557.
  - `cfsm` (close write) — `c.s` line 1306.
  - `evfile` UIFA parser — `h.s` line 377.
- `reference/comet-decoded/comet.asm` lines 194–205, 1273–1284,
  1286–1340 — RST 8 usage.
- `~/git/simcoupe/Manual.md` lines 52–54, 685–688, 721–722 —
  `dosboot` / `dosdisk` substitution behaviour.
- `~/git/simcoupe/Base/SAMIO.cpp::Rst8Hook` line 980 — concrete
  source for the substitution.
- `docs/comet/comet_v1-3_manual.pdf` — "COMET works with SAMDOS or
  MASTERDOS" confirms the hook-code API is shared.
