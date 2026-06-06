# Z80↔Go parity robustness seeds (i9) — fix + sweep + findings

**Date:** 2026-06-08 · **Item:** i9 ("parity robustness seeds")

Follow-up to the #87 parity audit (`docs/notes/2026-05-29-z80-go-parity-audit.md`)
and the i10 capability report
(`docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md`). Two of the three
"parity robustness seeds" strands are addressed here; the third (fixed Z80 table
sizes vs unbounded Go) was already largely covered by the IN/OUT-ceiling and
bump-arena work and is untouched.

## 1. sysname fail-HARD → fixed by extending the named subset (faithful to Go)

### Root cause

`src/sysname.asm` hard-aborts (`jp fail`, which halts the assembler) when a
PSTATE / DC / TLBI operand name is not found in the on-SAM lookup table:

- `sysname_lookup_pstate_miss` (`:421`) — PSTATE field miss.
- `sysname_lookup_dc_miss` (`:452`) — DC op miss.
- `sysname_lookup_tlbi_miss` (`:~488`) — TLBI op miss.

The sysreg path (`sysname_lookup_sysreg_miss`, `:389`) does NOT hard-fail: it
falls through to `sysname_parse_generic`, the `Sn_op1_Cm_Cn_op2` parser, which
mirrors Go's `format.ParseSysReg` generic fallback (`sysregs.go:102-108,195`).
PSTATE / DC / TLBI have **no such fallback** — neither in Go nor on the Z80.

The on-SAM tables (`src/sysreg_tables.inc`) carried only the entries the M5/M6
fixtures exercise:

| family | Z80 had | Go has |
|---|---|---|
| pstate | daifset, daifclr (2) | + spsel, uao, pan, dit, tco, ssbs (8) |
| dc     | civac, cvac, ivac (3) | + isw, csw, cisw, zva, cvau, cvap (9) |
| tlbi   | vmalle1, vae1is (2) | full EL1/EL2/EL3 set (32) |

So the hard-fail fired in two cases:

1. **Name valid in Go but absent from the SAM subset** (e.g. `msr pan,#1`,
   `dc zva,x0`, `tlbi alle1`): **Go encodes it correctly; the SAM assembler
   aborted.** A genuine divergence — the SAM tool failing on input the
   authority accepts.
2. **Name unknown to both** (e.g. `dc bogus,x0`): Go returns an error → assembly
   fails; the Z80 `jp fail` is the faithful mirror of that.

### Fix (faithful port, not a workaround)

The correct fix is the i10/audit-recommended **"extend the subset"**, not
"fail soft". Go's `ParsePState` / `ParseDC` / `ParseTLBI`
(`tools/sam-aarch64-format/sysregs.go`) have no generic fallback, so making the
Z80 fail-soft (e.g. invent a generic encoding) would emit bytes Go never emits —
the opposite of faithful. Instead `src/sysreg_tables.inc` now carries the
**full Go pstate / dc / tlbi tables**, byte-for-byte. After the fix:

- Case 1 disappears — every name Go encodes, the SAM assembler now encodes.
- Case 2 still `jp fail`s, which remains the faithful mirror of Go's
  error-on-unknown-name.

The `jp fail` paths are intentionally left in place: they are correct for a name
that is genuinely unknown to the authority.

This change also closes the symmetric **decoder** gap for free: the same shared
table feeds the page-15 disassembler (`src/disasm.asm` via `sysreg_names.inc`),
which previously rendered out-of-subset DC/TLBI/PSTATE tuples as `.inst`; they
now render their architectural names, matching Go's `decodeSys`.

### Durability guard

`tools/sam-aarch64-format/sysregs_z80sync_test.go` previously enforced only the
subset direction (every Z80 entry present in Go). It now ALSO enforces the
**reverse** for pstate/dc/tlbi (`checkComplete`): every Go entry must be in the
Z80 table. A future Go-side addition to those three families fails the guard
until the Z80 table is updated, so the fail-hard gap cannot silently reopen.
(sysreg_table stays subset-only — the generic form covers it.)

### Verification

- `sysregs_z80sync_test.go`: all four tables PASS (subset + complete).
- `TestSyntheticParity_ExtendedSysName`
  (`tools/z80-test-harness-go/synthetic_parity_test.go`): 19 previously-failing
  pstate/dc/tlbi names assembled through the **real prod SAM assembler**
  (`build/assembler-prod.bin`) byte-match GNU `as` (76 bytes).
- `TestDisasmOracle`: still 100% (5438/5438) — the extension adds no entry any
  release.img word exercises, so the oracle is unperturbed.
- `TestBootSelfTestsPass`: full paged boot path still clean with the larger
  page-13 / page-15 payloads.

## 2. Untested-form-combination sweep — `synthetic_parity_test.go`

A synthetic-fixture sweep that drives the i10 high-value families (which
`disasm.asm` handles but the release oracle never compares) through GNU `as`
(ground-truth bytes) → both the Z80 disassembler and Go `DecodeAt`, asserting
byte-for-byte agreement (Go is the authority; `.inst` fallback included).

**Families moved structural → empirically certified:**

- `TestSyntheticParity_CondSel`: base `csel`/`csinc`; aliases
  `cset`/`csetm`/`cinc`/`cinv`/`cneg` (32- and 64-bit, several conditions, both
  alias-firing and base operand shapes).
- `TestSyntheticParity_ExtendedReg`: extended-register `add`/`sub`/`adds`/`subs`
  with every `{u,s}xt{b,h,w}` / `uxtx` / `sxtx` extend and shift amounts 0-4
  (the i10 HIGH-priority address-arithmetic path).
- `TestSyntheticParity_SignedMul`: `smull`/`smaddl`/`smsubl`/`smnegl`/`smulh`.
- `TestSyntheticParity_ExtendedSysName`: the encoder fix above.

`sdiv` is excluded (known-missing across encoder + decoder; item i35).

Like the oracle, this is a dev-tool test (not a CI gate; SimCoupé remains the
sole gate). It skips cleanly if GNU `as` or `build/disasm.bin` is absent.

## 3. FINDINGS — genuine Z80↔Go DECODE disagreements (reported, not worked around)

Building the sweep surfaced two real disagreement classes. Per the prime
directive they are reported here for triage rather than papered over; both are
**decoder-only** and invisible to the oracle because no release.img word
exercises them.

### Finding A — `ccmp` / `ccmn` (conditional compare): Z80 decoder MISSING

- `0xfa410002` (`ccmp x0,x1,#2,eq`): Go decodes `ccmp` (binutils agrees);
  `src/disasm.asm` has **no ccmp/ccmn handler at all** → emits `.inst`.
- `ccmn` is unimplemented on BOTH sides (no encoder form, no decoder), so it
  agrees (both `.inst`) — but only by omission.

This is a clean **Z80 decoder port gap**: Go and binutils agree, the Z80 lacks
the family. The i10 report listed `ccmp` as "disasm.asm handler exists
(condsel/form region)" — that is **inaccurate**; no handler exists. The
**encoder** side is fine: the SAM assembler byte-matches GNU on `ccmp` (both
register and immediate forms — verified). Fixing this is a mechanical port of
the conditional-compare decoder to `disasm.asm` (Go authority:
`aarch64dec` form walk for `ccmp`).

### Finding B — non-alias base `csinv` / `csneg`: Z80 decoder TOO PERMISSIVE

- `0xda87b0c5` (`csinv x5,x6,x7,lt`): `src/disasm.asm` decodes `csinv`; Go's
  `DecodeAt` declines → `.inst`.
- `0xda8ac528` (`csneg x8,x9,x10,gt`): same, `csneg`.

Go has form-table entries only for `csel` and `csinc`
(`aarch64enc/manual_forms.go`); `csinv`/`csneg` are reachable ONLY via
`decodeCondSelAlias` (`aliases.go:502`), which fires only for the `Rn==Rm`
alias shapes (→ `csetm`/`cinv`/`cneg`). For `Rn != Rm`, Go has no `csinv`/`csneg`
form and the alias declines, so Go emits `.inst`. The Z80 `disasm_cs_base`
(`disasm.asm:~3231`) decodes all four base forms, so it emits `csinv`/`csneg`
where Go emits `.inst`.

Here the Z80 is **more capable than the Go authority** — and the Go authority is
itself **less capable than binutils** (binutils renders these as `csinv`/`csneg`).
The alias shapes (`cset`/`csetm`/`cinc`/`cinv`/`cneg`) and base `csel`/`csinc`
all agree; only non-alias base `csinv`/`csneg` diverge.

**Resolution is a Go-authority decision, not a mechanical port** (do we extend
Go's decoder to match binutils — after which the Z80 already agrees — or trim
the Z80 to match Go's current omission?). Per the "align with binutils" memory
the likely answer is "add `csinv`/`csneg` base forms to Go", but that is a
deliberate authority change, so it is reported here rather than made under i9.

Both findings are pinned by `TestSyntheticParity_KnownDisagreements` (skipped by
default; `PARITY_DISAGREEMENTS=1` asserts the current split state so a future fix
that closes a row trips the test and prompts promoting that family into a
certified sweep).
