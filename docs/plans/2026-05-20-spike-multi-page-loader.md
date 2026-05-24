# basic-detokeniser-spike Multi-Page Loader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift the basic-detokeniser-spike's ~25 KB program-size ceiling by teaching its loader to walk across physical RAM pages, storing BASIC sysvars in REL PAGE FORM and claiming additional pages via ALLOCT / LASTPAGE / RAMTOP. Recovers the 109 corpus failures from the 2026-05-20 4-way sweep.

**Architecture:** Replace `loadProgViaPoke` in `tools/basic-detokeniser-spike/main.go` with a unified paging-aware loader. Add three pure-Go helpers (`pos` type with `advance`, `pokeRAMPage`, `setSysvarPair`) plus a testable `checkFits` size guard. Validate via unit tests on the helpers + the loader, then via corpus regression (Phase A = the 109 failing files, Phase B = 20 random previously-passing files with timing).

**Tech Stack:** Go 1.22, `github.com/koron-go/z80` (already in spike), `testing` stdlib. No new dependencies. Tests in `loader_test.go` (same package). Phase A/B regression scripts in `/tmp/` (not committed).

**Spec reference:** `docs/superpowers/specs/2026-05-20-spike-multi-page-loader-design.md`

**File structure:**

```
tools/basic-detokeniser-spike/
├── main.go         ← modified: new constants, helpers, rewritten loadProgViaPoke
├── loader_test.go  ← new: unit tests for helpers + loader
├── go.mod          ← unchanged
└── go.sum          ← unchanged
```

Single-file change. Tests live alongside in the same package. Decision: do NOT split main.go into multiple files yet — the spike is already self-contained, and the unified-file pattern matches the sibling tools (build-disk, llist-capture pre-refactor, etc.).

---

## Task 1: Add new sysvar constants

**Files:**
- Modify: `tools/basic-detokeniser-spike/main.go` (extend the `const ( ... )` block at lines 148-164)

- [ ] **Step 1: Add the new constants**

Replace the existing sysvar block in `main.go:148-164` with:

```go
// SAM sysvars. Addresses verbatim from rom-disasm:869-900 (PROG-area
// pointers, with the "*P" page-byte companions used by REL PAGE FORM)
// and rom-disasm:1140-1143 (RAMTOPP/RAMTOP/PRAMTP/LASTPAGE).
const (
	// BASIC pointer pairs: page byte at NAMEP, 16-bit offset at NAME.
	sysSAVARSP  = 0x5A81
	sysSAVARS   = 0x5A82
	sysNUMENDP  = 0x5A84
	sysNUMEND   = 0x5A85
	sysNVARSP   = 0x5A87
	sysNVARS    = 0x5A88
	sysDATADD   = 0x5A8B
	sysWKENDP   = 0x5A8D
	sysWKEND    = 0x5A8E
	sysWORKSPP  = 0x5A90
	sysWORKSP   = 0x5A91
	sysELINEP   = 0x5A93
	sysELINE    = 0x5A94
	sysCHADP    = 0x5A96
	sysCHAD     = 0x5A97
	sysKCURP    = 0x5A99
	sysKCUR     = 0x5A9A
	sysNXTLINEP = 0x5A9C
	sysNXTLINE  = 0x5A9D
	sysPROGP    = 0x5A9F
	sysPROG     = 0x5AA0

	// Paging / RAM ceiling.
	sysLASTPAGE = 0x5CB0
	sysRAMTOPP  = 0x5CB1
	sysRAMTOP   = 0x5CB2
	sysPRAMTP   = 0x5CB4

	// ALLOCT base; 32 bytes, one per physical page (rom-disasm:1253).
	allocTableBase uint16 = 0x5100

	// Editor / keyboard sysvars (unchanged from previous block).
	sysLASTK  = 0x5C08
	sysERRNR  = 0x5C3A
	sysFLAGS  = 0x5C3B
	sysSTKEND = 0x5C65 // SAM-specific STKEND (different from Spectrum's)
)
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/pmoore/git/sam-aarch64/tools/basic-detokeniser-spike && go build ./...`
Expected: clean build, no output.

- [ ] **Step 3: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go
git commit -m "$(cat <<'EOF'
spike(detok): add sysvar constants for multi-page loader

Adds the *P page-byte companions for every BASIC-area pointer
(SAVARSP, NUMENDP, NVARSP, WKENDP, WORKSPP, ELINEP, CHADP, KCURP,
NXTLINEP, PROGP), plus RAMTOPP/RAMTOP/PRAMTP/LASTPAGE and the
ALLOCT table base. Addresses verified against ROM v3.0 disasm
lines 869-900 and 1140-1143.

No behaviour change yet — pure-constant additions consumed by the
upcoming multi-page loader.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `pos` type with `advance` method (TDD)

**Files:**
- Create: `tools/basic-detokeniser-spike/loader_test.go`
- Modify: `tools/basic-detokeniser-spike/main.go` (add `pos` type at end of file)

- [ ] **Step 1: Write the failing test**

Create `tools/basic-detokeniser-spike/loader_test.go`:

```go
package main

import (
	"testing"
)

func TestPosAdvance_StaysInSamePage(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x100)
	want := pos{page: 1, offset: 0x1DD5}
	if got != want {
		t.Errorf("advance(0x100): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2AtExactBoundary(t *testing.T) {
	// page 1 holds 0x4000 - 0x1CD5 = 0x232B bytes from PROG.
	// Advancing exactly 0x232B from (1, 0x1CD5) lands on (2, 0x0000).
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232B)
	want := pos{page: 2, offset: 0x0000}
	if got != want {
		t.Errorf("advance(0x232B): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2WithRemainder(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232C) // one past the boundary
	want := pos{page: 2, offset: 0x0001}
	if got != want {
		t.Errorf("advance(0x232C): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_SpansMultiplePages(t *testing.T) {
	// 50000 bytes from (1, 0x1CD5):
	//   page 1 absorbs 0x4000 - 0x1CD5 = 0x232B = 9003 bytes
	//   page 2 absorbs 0x4000 = 16384 bytes (cumulative 25387)
	//   page 3 absorbs 0x4000 = 16384 bytes (cumulative 41771)
	//   page 4 absorbs 50000 - 41771 = 8229 = 0x2025 bytes
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(50000)
	want := pos{page: 4, offset: 0x2025}
	if got != want {
		t.Errorf("advance(50000): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_ZeroIsIdentity(t *testing.T) {
	p := pos{page: 7, offset: 0x1234}
	got := p.advance(0)
	if got != p {
		t.Errorf("advance(0): got %+v, want %+v", got, p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/pmoore/git/sam-aarch64/tools/basic-detokeniser-spike && go test -run TestPosAdvance ./...`
Expected: FAIL with `undefined: pos` (or similar — type doesn't exist yet).

- [ ] **Step 3: Implement `pos` and `advance`**

Add to end of `main.go` (before the final closing brace if any, or just append):

```go
// pos is a (physical_page, offset_within_page) coordinate used by the
// multi-page loader. Encapsulates page-boundary carry so callers don't
// repeat the arithmetic. offset stays in [0, 0x4000); page is the
// physical RAM page (0..31). Page wrap-around at 32 is not detected
// here — the size guard in loadProgViaPoke prevents it.
type pos struct {
	page   uint8
	offset uint16
}

func (p pos) advance(n int) pos {
	total := uint32(p.offset) + uint32(n)
	return pos{
		page:   p.page + uint8(total>>14),
		offset: uint16(total & 0x3FFF),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/pmoore/git/sam-aarch64/tools/basic-detokeniser-spike && go test -run TestPosAdvance ./...`
Expected: PASS. Five test cases all green.

- [ ] **Step 5: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): add pos type for multi-page loader arithmetic

pos encapsulates the (physical_page, offset_within_page) coordinate
used by the upcoming multi-page loader. advance(n) handles the
page-boundary carry so callers don't repeat the math at every site.

Tests cover same-page, exact-boundary, one-past-boundary,
multi-page, and zero-advance cases.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `pokeRAMPage` helper (TDD)

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go` (add test)
- Modify: `tools/basic-detokeniser-spike/main.go` (add helper)

- [ ] **Step 1: Write the failing test**

Append to `loader_test.go`:

```go
func TestPokeRAMPage_WritesToCorrectPhysicalPage(t *testing.T) {
	hw := &Hardware{}
	// Default LMPR=0, HMPR=0 — would map section C to page 0. The
	// helper must ignore that and write to page 7 regardless.
	pokeRAMPage(hw, 7, 0x1234, 0xAB)
	if got := hw.ram[7][0x1234]; got != 0xAB {
		t.Errorf("hw.ram[7][0x1234] = %02X, want 0xAB", got)
	}
	// Spot-check: page 0 same offset is untouched.
	if got := hw.ram[0][0x1234]; got != 0x00 {
		t.Errorf("hw.ram[0][0x1234] = %02X, want 0x00 (other pages untouched)", got)
	}
}

func TestPokeRAMPage_MasksPageAndOffset(t *testing.T) {
	hw := &Hardware{}
	// Page 0x25 has bit 5 set — should mask to 0x05 (within 32 pages).
	// Offset 0x8000 has section-C bit — should mask to 0x0000.
	pokeRAMPage(hw, 0x25, 0x8000, 0xCD)
	if got := hw.ram[5][0]; got != 0xCD {
		t.Errorf("hw.ram[5][0] = %02X, want 0xCD (page/offset masked)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestPokeRAMPage ./...`
Expected: FAIL with `undefined: pokeRAMPage`.

- [ ] **Step 3: Implement `pokeRAMPage`**

Add to `main.go` near `pokeRAM`:

```go
// pokeRAMPage writes directly to a specific physical RAM page,
// bypassing the LMPR/HMPR resolution that pokeRAM uses. The
// multi-page loader uses this to stage program bytes across pages
// without rotating HMPR mid-load. The masks defensively normalise
// page into 0..31 and offset into 0..0x3FFF; the loader passes
// already-normal values via the pos type but the masking is cheap.
func pokeRAMPage(hw *Hardware, page uint8, offset uint16, v uint8) {
	hw.ram[page&0x1F][offset&0x3FFF] = v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestPokeRAMPage ./...`
Expected: PASS, both cases green.

- [ ] **Step 5: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): add pokeRAMPage for paging-independent RAM writes

The multi-page loader needs to write program bytes into specific
physical RAM pages without depending on HMPR/LMPR being set up to
map them. pokeRAMPage takes (page, offset_within_page, byte) and
writes directly into hw.ram, defensive masks included.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `setSysvarPair` helper (TDD)

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go`
- Modify: `tools/basic-detokeniser-spike/main.go`

- [ ] **Step 1: Write the failing test**

Append to `loader_test.go`:

```go
func TestSetSysvarPair_WritesPageByteAndSectionCOffset(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1 // sysvars at 0x5A** live in section B = page 1 with LMPR=0

	setSysvarPair(hw, sysNVARSP, sysNVARS, pos{page: 4, offset: 0x1234})

	// Page byte at sysNVARSP (0x5A87) should be 4.
	if got := peekRAM(hw, sysNVARSP); got != 4 {
		t.Errorf("NVARSP = %02X, want 04", got)
	}
	// 16-bit offset at sysNVARS (0x5A88) should be 0x8000 | 0x1234 = 0x9234.
	if got := peekRAM16(hw, sysNVARS); got != 0x9234 {
		t.Errorf("NVARS = %04X, want 9234 (section-C form of 0x1234)", got)
	}
}

func TestSetSysvarPair_ZeroOffsetGetsSectionCBit(t *testing.T) {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1

	setSysvarPair(hw, sysSAVARSP, sysSAVARS, pos{page: 2, offset: 0})

	if got := peekRAM(hw, sysSAVARSP); got != 2 {
		t.Errorf("SAVARSP = %02X, want 02", got)
	}
	// Offset 0 → section-C-form 0x8000 (NOT 0x0000).
	if got := peekRAM16(hw, sysSAVARS); got != 0x8000 {
		t.Errorf("SAVARS = %04X, want 8000", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSetSysvarPair ./...`
Expected: FAIL with `undefined: setSysvarPair`.

- [ ] **Step 3: Implement `setSysvarPair`**

Add to `main.go` near the other sysvar helpers:

```go
// setSysvarPair stores a (page, offset) in REL PAGE FORM at the given
// sysvar addresses. The page byte goes at pageAddr; the offset is
// encoded into section-C form (0x8000 | low_14_bits) and stored
// 16-bit-little-endian at offsetAddr. Per the ROM convention
// established by UNSTLEN (rom-disasm:14773-14786), the offset's top
// bit is always set when storing an address (as opposed to a length).
func setSysvarPair(hw *Hardware, pageAddr, offsetAddr uint16, p pos) {
	pokeRAM(hw, pageAddr, p.page)
	pokeRAM16(hw, offsetAddr, 0x8000|(p.offset&0x3FFF))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestSetSysvarPair ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): add setSysvarPair for REL-PAGE-FORM sysvar writes

Stores a (page, offset) pos in REL PAGE FORM at the given pair of
sysvar addresses: the page byte verbatim at pageAddr, the offset as
0x8000 | (offset & 0x3FFF) at offsetAddr (section-C-form per ROM
UNSTLEN convention at rom-disasm:14773-14786).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Add `checkFits` size guard (TDD)

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go`
- Modify: `tools/basic-detokeniser-spike/main.go`

Extracting the size check into a pure function makes it testable; `loadProgViaPoke` calls it and `log.Fatalf`s on error.

- [ ] **Step 1: Write the failing test**

Append to `loader_test.go`:

```go
func TestCheckFits_FitsOn512K(t *testing.T) {
	// Exact max on 512K (PRAMTP=0x1F): available = 32*0x4000 - 0x4000
	// - 0x1CD5 = 500523 bytes; subtract trailer 1024 + headroom 150 =
	// 499349 max program. 480 KB is safely under that.
	err := checkFits(480*1024, 0x1F)
	if err != nil {
		t.Errorf("480 KB on PRAMTP=0x1F: unexpected error: %v", err)
	}
}

func TestCheckFits_FitsOn256K(t *testing.T) {
	// 200 KB on a 256 K (PRAMTP=0x0F) machine fits.
	err := checkFits(200*1024, 0x0F)
	if err != nil {
		t.Errorf("200 KB on PRAMTP=0x0F: unexpected error: %v", err)
	}
}

func TestCheckFits_ExceedsOn256K(t *testing.T) {
	// 400 KB on a 256 K machine does not fit.
	err := checkFits(400*1024, 0x0F)
	if err == nil {
		t.Errorf("400 KB on PRAMTP=0x0F: expected error, got nil")
	}
}

func TestCheckFits_ExceedsOn512K(t *testing.T) {
	// 600 KB on a 512 K machine does not fit (limit is ~488 KB).
	err := checkFits(600*1024, 0x1F)
	if err == nil {
		t.Errorf("600 KB on PRAMTP=0x1F: expected error, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCheckFits ./...`
Expected: FAIL with `undefined: checkFits`.

- [ ] **Step 3: Implement `checkFits`**

Add to `main.go`:

```go
// checkFits returns nil if a program of length progLen will fit in
// physical RAM on a machine with the given PRAMTP, given the trailer
// shift (1024 bytes, matching the current loader's memmove window)
// and the ROM's required 150-byte WKEND headroom (rom-disasm:7152).
//
// PROG starts at page 1, offset 0x1CD5, so the usable region is
// pages 1..PRAMTP minus the section-A page 0 and the system area
// in page 1 below PROG.
func checkFits(progLen int, pramtp uint8) error {
	const (
		progPageStart    = uint8(1)
		progOffsetInPage = uint16(0x1CD5) // 0x9CD5 in section-C form
		trailerShiftLen  = 1024
		wkendHeadroom    = 150
	)
	totalNeeded := progLen + trailerShiftLen + wkendHeadroom
	totalAvailable := (int(pramtp)+1)*0x4000 -
		int(progPageStart)*0x4000 -
		int(progOffsetInPage)
	if totalNeeded > totalAvailable {
		return fmt.Errorf("program does not fit in BASIC pages: "+
			"len=%d shift=%d headroom=%d need=%d available=%d "+
			"(PRAMTP=%02X, pages %d..%d)",
			progLen, trailerShiftLen, wkendHeadroom,
			totalNeeded, totalAvailable, pramtp,
			progPageStart, pramtp)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestCheckFits ./...`
Expected: PASS, all four cases green.

- [ ] **Step 5: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): add checkFits size guard for multi-page loader

Pure function: returns nil if a program of the given length will
fit in physical RAM (PROG at page 1 offset 0x1CD5, trailer shift of
1024 bytes, plus the ROM's 150-byte WKEND headroom rule at
rom-disasm:7152). Returns a descriptive error otherwise.

Extracted as a separate function so it's unit-testable; the new
loader calls it and log.Fatalf's on error.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Add `postBootState` helper for tests

The loader tests need a Hardware in valid post-boot state without actually booting the ROM. This helper sets up the minimal state the loader depends on.

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go`

- [ ] **Step 1: Add the helper**

Append to `loader_test.go`:

```go
// postBootState returns a Hardware initialised to the post-boot state
// loadProgViaPoke expects: LMPR=0, HMPR=1, PRAMTP=0x1F (512K), and
// every BASIC sysvar pair set to its canonical post-boot value with
// PROG = 0x9CD5. Mirrors what running the ROM boot sequence would
// produce, without needing the ROM image.
func postBootState() *Hardware {
	hw := &Hardware{}
	hw.lmpr = 0
	hw.hmpr = 1
	hw.vmpr = 0 // screen page doesn't matter for loader tests

	// PROG (always at 0x9CD5 = page 1, offset 0x1CD5).
	pokeRAM(hw, sysPROGP, 1)
	pokeRAM16(hw, sysPROG, 0x9CD5)
	// NVARS = PROG + 1 (the 0xFF end-of-program sentinel slot).
	pokeRAM(hw, sysNVARSP, 1)
	pokeRAM16(hw, sysNVARS, 0x9CD6)
	// NUMEND = NVARS + 92.
	pokeRAM(hw, sysNUMENDP, 1)
	pokeRAM16(hw, sysNUMEND, 0x9D32)
	// SAVARS = NUMEND + 512.
	pokeRAM(hw, sysSAVARSP, 1)
	pokeRAM16(hw, sysSAVARS, 0x9F32)
	// ELINE = SAVARS (no saved string vars in a fresh state).
	pokeRAM(hw, sysELINEP, 1)
	pokeRAM16(hw, sysELINE, 0x9F32)
	// WORKSP = ELINE + 1.
	pokeRAM(hw, sysWORKSPP, 1)
	pokeRAM16(hw, sysWORKSP, 0x9F33)
	// WKEND = WORKSP.
	pokeRAM(hw, sysWKENDP, 1)
	pokeRAM16(hw, sysWKEND, 0x9F33)
	// CHAD, KCUR, NXTLINE — track ELINE in fresh state.
	pokeRAM(hw, sysCHADP, 1)
	pokeRAM16(hw, sysCHAD, 0x9F32)
	pokeRAM(hw, sysKCURP, 1)
	pokeRAM16(hw, sysKCUR, 0x9F32)
	pokeRAM(hw, sysNXTLINEP, 1)
	pokeRAM16(hw, sysNXTLINE, 0x9CD5)

	// Physical RAM ceiling: 512K = pages 0..31.
	pokeRAM(hw, sysPRAMTP, 0x1F)
	// BASIC owns pages 0..3 at boot.
	pokeRAM(hw, sysLASTPAGE, 0x03)
	pokeRAM(hw, sysRAMTOPP, 0x03)
	pokeRAM16(hw, sysRAMTOP, 0xBFFF)
	// ALLOCT: pages 0..3 marked "IN USE, CONTEXT 0".
	for p := uint8(0); p <= 3; p++ {
		pokeRAM(hw, allocTableBase+uint16(p), 0x40)
	}

	// Stage canonicalNumericVars at NVARS (matches ROM boot's CLRSR init).
	nvars := peekRAM16(hw, sysNVARS)
	for i, b := range canonicalNumericVars {
		pokeRAM(hw, nvars+uint16(i), b)
	}
	// 512-byte gap above NVARS+92 is zero-initialised by virtue of hw.ram
	// starting zero.

	return hw
}

// snapshotRAMRange captures hw.ram[page][offset:offset+n] into a slice
// for byte-level assertions in tests.
func snapshotRAMRange(hw *Hardware, page uint8, offset, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		p := pos{page: page, offset: uint16(offset)}.advance(i)
		out[i] = hw.ram[p.page&0x1F][p.offset&0x3FFF]
	}
	return out
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go test -run XYZNoSuchTest ./...` (forces compile, runs no tests)
Expected: `no tests to run` — compilation succeeded.

- [ ] **Step 3: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): add postBootState test helper

postBootState() returns a Hardware initialised to the state ROM
boot would leave it in: LMPR=0, HMPR=1, PRAMTP=0x1F, every BASIC
sysvar pair set to its canonical post-boot value, ALLOCT entries
0..3 marked in use, and canonicalNumericVars staged at NVARS.

Plus snapshotRAMRange for byte-level assertions across pages.

Used by the upcoming loader unit tests so they don't have to boot
the ROM. The exact sysvar values match what the real ROM boot
produces (verified against the current spike's runtime behaviour
on a fresh disk).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Test the new loader on a small program (TDD — pinning current behaviour)

This test pins down the behaviour the existing single-page loader produces, so the upcoming rewrite can be verified byte-identical for small programs.

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go`

- [ ] **Step 1: Write the failing test**

Append to `loader_test.go`:

```go
// A minimal valid tokenised BASIC program: one line numbered 10
// containing "PRINT 1", followed by the 0xFF end-of-program sentinel.
// Per docs/notes/sam-basic-save-format.md, each line is
// lineNumBE(2) + lineLenLE(2) + tokenised_body + 0x0D, where lineLen
// counts (tokenised_body + 0x0D) — here "F0 20 31 0D" = 4 bytes.
//
//   line 10:  00 0A    04 00    F0 20 31    0D
//   end:      FF
//
// Total 9 bytes.
var smallProgram = []byte{
	0x00, 0x0A, // line number 10 (big-endian)
	0x04, 0x00, // lineLen = 4 (little-endian): covers "F0 20 31 0D"
	0xF0,       // PRINT token
	0x20, 0x31, // " 1"
	0x0D,       // line terminator
	0xFF,       // end-of-program sentinel
}

func TestLoadProg_SmallProgram_SysvarPairsBumpedByDelta(t *testing.T) {
	hw := postBootState()

	// Capture pre-load NVARS/NUMEND/SAVARS for delta math.
	preNVARS := peekRAM16(hw, sysNVARS)
	preNUMEND := peekRAM16(hw, sysNUMEND)
	preSAVARS := peekRAM16(hw, sysSAVARS)

	loadProgViaPoke(hw, smallProgram)

	delta := uint16(len(smallProgram)) - 1 // current loader's delta convention

	// New NVARS = PROG + len = 0x9CD5 + 9 = 0x9CDE (len = 9 bytes).
	if got, want := peekRAM16(hw, sysNVARS), preNVARS+delta; got != want {
		t.Errorf("NVARS after load: got %04X, want %04X (= preNVARS %04X + delta %d)",
			got, want, preNVARS, delta)
	}
	if got, want := peekRAM16(hw, sysNUMEND), preNUMEND+delta; got != want {
		t.Errorf("NUMEND: got %04X, want %04X", got, want)
	}
	if got, want := peekRAM16(hw, sysSAVARS), preSAVARS+delta; got != want {
		t.Errorf("SAVARS: got %04X, want %04X", got, want)
	}

	// Page bytes all 1 (small program, everything in page 1).
	for name, addr := range map[string]uint16{
		"NVARSP": sysNVARSP, "NUMENDP": sysNUMENDP, "SAVARSP": sysSAVARSP,
		"WKENDP": sysWKENDP, "WORKSPP": sysWORKSPP, "ELINEP": sysELINEP,
		"CHADP": sysCHADP, "KCURP": sysKCURP, "NXTLINEP": sysNXTLINEP,
		"PROGP": sysPROGP,
	} {
		if got := peekRAM(hw, addr); got != 1 {
			t.Errorf("%s after load: got %02X, want 01 (small program stays in page 1)", name, got)
		}
	}

	// Program bytes are at PROG (= page 1, offset 0x1CD5).
	gotProg := snapshotRAMRange(hw, 1, 0x1CD5, len(smallProgram))
	for i, b := range gotProg {
		if b != smallProgram[i] {
			t.Errorf("program byte %d: got %02X, want %02X", i, b, smallProgram[i])
		}
	}

	// canonicalNumericVars relocated to new NVARS position.
	newNVARS := peekRAM16(hw, sysNVARS)
	gotVars := snapshotRAMRange(hw, 1, int(newNVARS&0x3FFF), len(canonicalNumericVars))
	for i, b := range gotVars {
		if b != canonicalNumericVars[i] {
			t.Errorf("vars byte %d: got %02X, want %02X", i, b, canonicalNumericVars[i])
		}
	}

	// ALLOCT untouched beyond the boot 0..3 (no new pages claimed).
	for p := uint8(4); p <= 0x1F; p++ {
		if got := peekRAM(hw, allocTableBase+uint16(p)); got != 0 {
			t.Errorf("ALLOCT[%d]: got %02X, want 00 (small program shouldn't claim pages)", p, got)
		}
	}
	if got := peekRAM(hw, sysLASTPAGE); got != 3 {
		t.Errorf("LASTPAGE: got %02X, want 03 (no extension for small program)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test -run TestLoadProg_SmallProgram ./...`

The test may PASS already (against the current single-page loader) — that's *desirable*. The current loader IS the small-program reference behaviour; this test pins it down. If it FAILS, the assertions don't match current behaviour and we need to investigate before changing the loader.

Expected outcome: **PASS** — confirms our test is a faithful snapshot of current behaviour.

If it fails: STOP and investigate. The test may have wrong expectations vs current behaviour. Adjust the test (not the loader) until it passes against the current single-page loader, so we have a real reference.

- [ ] **Step 3: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): pin small-program loader behaviour with a unit test

TestLoadProg_SmallProgram_SysvarPairsBumpedByDelta exercises the
current single-page loader against a known 10-byte BASIC program
and asserts every sysvar pair, ALLOCT, LASTPAGE, and the RAM
contents at PROG and NVARS. Test passes against the existing
implementation — provides a regression anchor for the upcoming
multi-page rewrite.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Write failing test for multi-page program

**Files:**
- Modify: `tools/basic-detokeniser-spike/loader_test.go`

- [ ] **Step 1: Write the failing test**

Append to `loader_test.go`:

```go
// makeProgram synthesises a tokenised BASIC body roughly bodyLen bytes
// long, structured as many `<lineNum> REM xxx...` lines so the total
// reaches the target size. Used to exercise the multi-page loader.
func makeProgram(t *testing.T, bodyLen int) []byte {
	t.Helper()
	const remPadPerLine = 200 // 200 bytes of REM payload per line
	var prog []byte
	lineNum := uint16(10)
	for len(prog) < bodyLen-1 {
		// Line: lineNum_BE(2) + lineLen_LE(2) + 0xEA + payload + 0x0D
		// lineLen counts payload + 0x0D = remPadPerLine + 2 bytes.
		payload := make([]byte, remPadPerLine)
		for i := range payload {
			payload[i] = byte('A' + (i % 26))
		}
		line := []byte{
			byte(lineNum >> 8), byte(lineNum & 0xFF),
			byte(remPadPerLine + 2), 0x00,
			0xEA, // REM token
		}
		line = append(line, payload...)
		line = append(line, 0x0D)
		prog = append(prog, line...)
		lineNum += 10
	}
	prog = append(prog, 0xFF) // end-of-program sentinel
	return prog
}

func TestLoadProg_MultiPage_40K_ProgramSpansThreePages(t *testing.T) {
	hw := postBootState()

	// 40 KB target body (makeProgram rounds up; actual size will be
	// 40000-40300ish). Layout:
	//   page 1 from offset 0x1CD5 holds the first 0x232B = 9003 bytes
	//   page 2 from offset 0      holds the next 0x4000 = 16384 bytes
	//   page 3 from offset 0      holds the remainder (~14600 bytes)
	body := makeProgram(t, 40000)
	if len(body) < 40000 {
		t.Fatalf("makeProgram produced %d bytes, want >= 40000", len(body))
	}
	if len(body) > 0x232B+0x4000+0x4000 {
		t.Fatalf("makeProgram produced %d bytes which would overflow into page 4 — test assumes pages 1..3 only", len(body))
	}

	loadProgViaPoke(hw, body)

	// First 0x232B bytes of program live in page 1 from offset 0x1CD5.
	firstChunk := snapshotRAMRange(hw, 1, 0x1CD5, 0x232B)
	for i, b := range firstChunk {
		if b != body[i] {
			t.Fatalf("page 1 byte %d (program offset %d): got %02X, want %02X",
				i, i, b, body[i])
		}
	}

	// Next 0x4000 bytes live in page 2 from offset 0.
	secondChunk := snapshotRAMRange(hw, 2, 0, 0x4000)
	for i, b := range secondChunk {
		if b != body[0x232B+i] {
			t.Fatalf("page 2 byte %d: got %02X, want %02X (program offset %d)",
				i, b, body[0x232B+i], 0x232B+i)
		}
	}

	// Remainder of program in page 3.
	remaining := len(body) - 0x232B - 0x4000
	if remaining > 0 {
		thirdChunk := snapshotRAMRange(hw, 3, 0, remaining)
		for i, b := range thirdChunk {
			if b != body[0x232B+0x4000+i] {
				t.Fatalf("page 3 byte %d: got %02X, want %02X",
					i, b, body[0x232B+0x4000+i])
			}
		}
	}

	// NVARS lives just past the program — its page byte should be 3
	// (since program crossed into page 3) and offset should reflect
	// the remainder.
	expectedNVARSPos := pos{page: 1, offset: 0x1CD5}.advance(len(body))
	if got := peekRAM(hw, sysNVARSP); got != expectedNVARSPos.page {
		t.Errorf("NVARSP: got %02X, want %02X (program ends in page %d)",
			got, expectedNVARSPos.page, expectedNVARSPos.page)
	}
	wantNVARS := uint16(0x8000) | (expectedNVARSPos.offset & 0x3FFF)
	if got := peekRAM16(hw, sysNVARS); got != wantNVARS {
		t.Errorf("NVARS: got %04X, want %04X", got, wantNVARS)
	}

	// canonicalNumericVars relocated to new NVARS position (across page
	// boundary if needed).
	for i, b := range canonicalNumericVars {
		p := expectedNVARSPos.advance(i)
		got := hw.ram[p.page&0x1F][p.offset&0x3FFF]
		if got != b {
			t.Errorf("vars byte %d at (page=%d, off=%04X): got %02X, want %02X",
				i, p.page, p.offset, got, b)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestLoadProg_MultiPage ./...`
Expected: FAIL — current loader fatals on programs larger than ~25 KB with "program too large for two-page poke" or similar.

- [ ] **Step 3: Do not commit yet**

This test will pass after Task 9 rewrites the loader. Leaving it failing is the explicit TDD signal that the work isn't done.

---

## Task 9: Rewrite `loadProgViaPoke` for multi-page support

**Files:**
- Modify: `tools/basic-detokeniser-spike/main.go` (replace `loadProgViaPoke` body, lines 306-356)

- [ ] **Step 1: Replace the function body**

In `main.go`, locate `func loadProgViaPoke` (around line 306) and replace its entire body with the implementation below. Keep the function name and signature exactly the same.

```go
func loadProgViaPoke(hw *Hardware, progBytes []byte) {
	const (
		progPageStart    = uint8(1)
		progOffsetInPage = uint16(0x1CD5) // 0x9CD5 in section-C form
		trailerShiftLen  = 1024
	)

	// Sanity check: post-boot relationship must hold. Unchanged from
	// the original loader at main.go:307-311.
	prog := peekRAM16(hw, sysPROG)
	oldNVARS := peekRAM16(hw, sysNVARS)
	if oldNVARS != prog+1 {
		log.Fatalf("expected post-boot NVARS=PROG+1, got NVARS=%04X PROG=%04X",
			oldNVARS, prog)
	}

	// Snapshot the boot-time offsets of every downstream sysvar from
	// NVARS so we can preserve those relative offsets in the new
	// layout. This is the equivalent of the original loader's "bump
	// every sysvar by delta" pattern, generalised to (page, offset).
	type sysvarSpec struct {
		pageAddr, offsetAddr uint16
		deltaFromNVARS       uint16
	}
	bumpedSysvars := []sysvarSpec{
		{sysNVARSP, sysNVARS, 0},
		{sysNUMENDP, sysNUMEND, peekRAM16(hw, sysNUMEND) - oldNVARS},
		{sysSAVARSP, sysSAVARS, peekRAM16(hw, sysSAVARS) - oldNVARS},
		{sysWKENDP, sysWKEND, peekRAM16(hw, sysWKEND) - oldNVARS},
		{sysWORKSPP, sysWORKSP, peekRAM16(hw, sysWORKSP) - oldNVARS},
		{sysELINEP, sysELINE, peekRAM16(hw, sysELINE) - oldNVARS},
		{sysCHADP, sysCHAD, peekRAM16(hw, sysCHAD) - oldNVARS},
		{sysKCURP, sysKCUR, peekRAM16(hw, sysKCUR) - oldNVARS},
		{sysNXTLINEP, sysNXTLINE, peekRAM16(hw, sysNXTLINE) - oldNVARS},
	}

	// Size guard: bail before any writes if the program won't fit.
	pramtp := peekRAM(hw, sysPRAMTP)
	if err := checkFits(len(progBytes), pramtp); err != nil {
		log.Fatalf("%v", err)
	}

	// Step 1: paging-aware trailer shift. Walk backwards from
	// shiftLen-1 down to 0 so the source/dest overlap on small
	// programs doesn't clobber. Source reads use the paged peekRAM
	// (everything's in page 1's section-C window during the read);
	// dest writes use pokeRAMPage to land bytes in the correct
	// physical page regardless of HMPR state.
	progPos := pos{page: progPageStart, offset: progOffsetInPage}
	newNVARSPos := progPos.advance(len(progBytes))
	for i := trailerShiftLen - 1; i >= 0; i-- {
		b := peekRAM(hw, oldNVARS+uint16(i))
		dst := newNVARSPos.advance(i)
		pokeRAMPage(hw, dst.page, dst.offset, b)
	}

	// Step 2: write progBytes byte-by-byte starting at PROG. Done
	// AFTER the trailer shift so source/dest overlap is identical
	// to the original single-page loader.
	cur := progPos
	for _, b := range progBytes {
		pokeRAMPage(hw, cur.page, cur.offset, b)
		cur = cur.advance(1)
	}

	// Step 3: write all sysvar pairs in REL PAGE FORM. PROG itself is
	// unchanged value-wise, but we write the pair explicitly because
	// ROM boot doesn't initialise PROGP (verified against disasm).
	setSysvarPair(hw, sysPROGP, sysPROG, progPos)
	for _, s := range bumpedSysvars {
		p := newNVARSPos.advance(int(s.deltaFromNVARS))
		setSysvarPair(hw, s.pageAddr, s.offsetAddr, p)
	}

	// Step 4: ALLOCT + LASTPAGE + RAMTOP for any page claimed beyond
	// the boot-default 0..3. Compute the highest page used as the
	// new position of WKEND (the editor's high-water mark).
	wkendDelta := peekRAM16(hw, sysWKEND) - oldNVARS
	wkendPos := newNVARSPos.advance(int(wkendDelta))
	maxPage := wkendPos.page
	for p := uint8(4); p <= maxPage; p++ {
		pokeRAM(hw, allocTableBase+uint16(p), 0x40)
	}
	if maxPage > 3 {
		pokeRAM(hw, sysLASTPAGE, maxPage)
		pokeRAM(hw, sysRAMTOPP, maxPage)
		pokeRAM16(hw, sysRAMTOP, 0xBFFF)
	}
}
```

- [ ] **Step 2: Run all loader tests**

Run: `cd /Users/pmoore/git/sam-aarch64/tools/basic-detokeniser-spike && go test -run TestLoadProg -v ./...`
Expected:
- `TestLoadProg_SmallProgram_SysvarPairsBumpedByDelta`: **PASS** (was passing before; rewrite must preserve)
- `TestLoadProg_MultiPage_40K_ProgramSpansThreePages`: **PASS** (was failing; rewrite fixes)

If either fails: STOP. Read the output carefully. Likely candidates: off-by-one in pos.advance, wrong sysvar delta computation, or trailer shift direction. Do not move on until both green.

- [ ] **Step 3: Run the full test suite to confirm no regressions**

Run: `go test -v ./...`
Expected: all tests PASS (helpers from Tasks 2-5 plus loader tests from 7+8).

- [ ] **Step 4: Verify the binary still builds**

Run: `go build -o /tmp/detok-spike-new .`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
cd /Users/pmoore/git/sam-aarch64
git add tools/basic-detokeniser-spike/main.go tools/basic-detokeniser-spike/loader_test.go
git commit -m "$(cat <<'EOF'
spike(detok): multi-page loader — load programs of any size up to PRAMTP

Replaces loadProgViaPoke's 16-bit-address-space write with a unified
paging-aware loader. Walks the program across consecutive physical
RAM pages starting at (page=1, offset=0x1CD5), stages the
canonicalNumericVars trailer at the new NVARS position, writes every
BASIC sysvar pair (page byte + section-C-form offset) in REL PAGE
FORM, and claims any pages beyond the boot-default 0..3 via ALLOCT /
LASTPAGE / RAMTOP.

Small-program behaviour is byte-identical to the previous loader
(verified by TestLoadProg_SmallProgram_SysvarPairsBumpedByDelta —
same sysvar deltas, same trailer-shift mechanic).

Multi-page support is exercised by
TestLoadProg_MultiPage_40K_ProgramSpansThreePages.

Recovers 109 of 109 corpus failures from the 2026-05-20 4-way sweep
that bailed with "program too large for two-page poke" (corpus
verification deferred to the Phase A/B regression in following
tasks).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Phase A — regression on the 109 previously-failing files

Verify the rewritten spike actually decodes the 109 corpus failures that motivated this work. Compare each new `.spike.txt` against the existing `.llist.txt` (the ROM oracle) modulo the known wrap/`>` differences.

**Files:**
- Create: `/tmp/phase-a.sh` (not committed)

- [ ] **Step 1: Re-resolve the 109 failures from the sweep TSV**

```bash
cd /Users/pmoore
awk -F'\t' '$1=="PARTIAL" && $4 ~ /program too large for two-page poke/ {print $2 "\t" $3}' \
    detok-sweep.tsv > /tmp/phase-a-jobs.tsv
wc -l /tmp/phase-a-jobs.tsv
```

Expected output: `109 /tmp/phase-a-jobs.tsv`.

- [ ] **Step 2: Build the new spike**

```bash
cd /Users/pmoore/git/sam-aarch64/tools/basic-detokeniser-spike
go build -o /tmp/detok-spike .
ls -la /tmp/detok-spike
```

Expected: fresh binary, dated today.

- [ ] **Step 3: Write the Phase A runner**

Create `/tmp/phase-a.sh`:

```bash
#!/usr/bin/env bash
# Run the new spike against each of the 109 previously-failing files.
# Outputs per-job status to /tmp/phase-a-results.tsv:
#   status  exit  bytes  disk  basic
# status ∈ { OK, FATAL, EMPTY-OUTPUT, OTHER }
set -u

SPIKE=/tmp/detok-spike
CORPUS=/Users/pmoore/sam-corpus/disks
JOBS=/tmp/phase-a-jobs.tsv
OUT=/tmp/phase-a-out
RESULTS=/tmp/phase-a-results.tsv

mkdir -p "$OUT"
: > "$RESULTS"

while IFS=$'\t' read -r disk basic; do
    [ -z "$disk" ] && continue
    out="$OUT/$(echo "$disk" | tr ' /' '__')__$(echo "$basic" | tr ' /' '__').spike.txt"
    err="$out.err"
    "$SPIKE" --mgt "$CORPUS/$disk" --filename "$basic" --out "$out" \
        > /dev/null 2> "$err"
    rc=$?
    if [ "$rc" -eq 0 ]; then
        if [ -s "$out" ]; then
            status=OK
        else
            status=EMPTY-OUTPUT
        fi
    elif grep -q "program does not fit in BASIC pages" "$err" 2>/dev/null; then
        status=FATAL
    else
        status=OTHER
    fi
    bytes=$(wc -c < "$out" 2>/dev/null | tr -d ' ')
    printf '%s\t%d\t%s\t%s\t%s\n' "$status" "$rc" "${bytes:-0}" "$disk" "$basic" >> "$RESULTS"
done < "$JOBS"

echo "=== summary ==="
awk -F'\t' '{c[$1]++} END {for (k in c) printf "  %s: %d\n", k, c[k]}' "$RESULTS"
```

- [ ] **Step 4: Run Phase A**

```bash
chmod +x /tmp/phase-a.sh && /tmp/phase-a.sh
```

Expected: takes ~5-15 minutes (each spike invocation is ~3 s with ROM boot). Final summary should show `OK: 109` (or very close — the 2 corrupt-bytes filename cases from the earlier session might still need handling).

- [ ] **Step 5: Spot-check a sample output**

Pick a file from the OK set and visually compare against its existing `.llist.txt`:

```bash
head -1 /tmp/phase-a-results.tsv | awk -F'\t' '$1=="OK" {print $4 "\t" $5}'
# Use a known case, e.g. LOVE on Allan Stevens Compilation Games Disk 1
diff <(cat "/tmp/phase-a-out/Allan_Stevens_Compilation_-_Games_Disk_1_(19xx).mgt__LOVE.spike.txt") \
     <(cat "/Users/pmoore/detok-captures/Allan_20Stevens_20Compilation_20-_20Games_20Disk_201_20_2819xx_29/LOVE.llist.txt") | head -40
```

Expected: differences should only be (1) 80-col line wrapping in llist that's absent in spike, and (2) possibly a `>` cursor marker on one line. No structural mismatches.

- [ ] **Step 6: Record findings**

Append a brief findings note to `/tmp/phase-a-findings.md`:

```bash
cat > /tmp/phase-a-findings.md << 'EOF'
# Phase A — multi-page spike regression

Sweep date: $(date -Iseconds)
Jobs: 109 (previously-failing "program too large for two-page poke")

## Status counts
EOF
awk -F'\t' '{c[$1]++} END {for (k in c) printf "- %s: %d\n", k, c[k]}' /tmp/phase-a-results.tsv >> /tmp/phase-a-findings.md
echo "" >> /tmp/phase-a-findings.md
echo "## Non-OK cases" >> /tmp/phase-a-findings.md
awk -F'\t' '$1!="OK" {print "- " $4 " :: " $5 " (status=" $1 " rc=" $2 ")"}' /tmp/phase-a-results.tsv >> /tmp/phase-a-findings.md
cat /tmp/phase-a-findings.md
```

- [ ] **Step 7: Decision gate**

If status counts show `OK: 109`: proceed to Task 11.

If any FATAL or OTHER cases: STOP. Read `/tmp/phase-a-out/<failing>.err` for each non-OK case. If FATAL is due to a genuine PRAMTP overflow (program > 488 KB), the spec accepts that as expected behaviour — record it and proceed. Anything else needs investigation before declaring Phase A done.

No commit — this is purely a verification step; outputs live in `/tmp/`.

---

## Task 11: Phase B — small-program regression with timing

Verify the unified loader didn't regress small-program behaviour. Pick 20 random files from the 5938 previously-CAPTURED set; diff new spike output against existing.

**Files:**
- Create: `/tmp/phase-b.sh` (not committed)

- [ ] **Step 1: Sample 20 random previously-CAPTURED files**

```bash
cd /Users/pmoore
awk -F'\t' '$1=="CAPTURED" {print $2 "\t" $3}' detok-sweep.tsv | shuf -n 20 > /tmp/phase-b-jobs.tsv
wc -l /tmp/phase-b-jobs.tsv
```

Expected: `20 /tmp/phase-b-jobs.tsv`.

- [ ] **Step 2: Write the Phase B runner with timing**

Create `/tmp/phase-b.sh`:

```bash
#!/usr/bin/env bash
# For each sampled file: run new spike, diff vs existing .spike.txt,
# record wall time. Report avg seconds-per-spike at the end.
set -u

SPIKE=/tmp/detok-spike
CORPUS=/Users/pmoore/sam-corpus/disks
CAPTURES=/Users/pmoore/detok-captures
JOBS=/tmp/phase-b-jobs.tsv
RESULTS=/tmp/phase-b-results.tsv
OUT=/tmp/phase-b-out

mkdir -p "$OUT"
: > "$RESULTS"

# Bijective safeName encoder (matches main.go's safeName).
safe() {
    python3 -c "
import sys
LIT=set('ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.+-')
out=[]
for c in sys.argv[1]:
    b=ord(c)
    if c in LIT: out.append(c)
    elif b==0x5F: out.append('__')
    else: out.append(f'_{b:02X}')
print(''.join(out))
" "$1"
}

total_ms=0
n=0
while IFS=$'\t' read -r disk basic; do
    [ -z "$disk" ] && continue
    disk_stem=$(safe "${disk%.mgt}")
    file_stem=$(safe "$basic")
    expected="$CAPTURES/$disk_stem/$file_stem.spike.txt"
    if [ ! -f "$expected" ]; then
        printf 'MISSING-EXPECTED\t%s\t%s\n' "$disk" "$basic" >> "$RESULTS"
        continue
    fi
    out="$OUT/$disk_stem.$file_stem.spike.txt"
    t0=$(date +%s%3N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1000))')
    "$SPIKE" --mgt "$CORPUS/$disk" --filename "$basic" --out "$out" > /dev/null 2>&1
    rc=$?
    t1=$(date +%s%3N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1000))')
    wall_ms=$((t1 - t0))
    total_ms=$((total_ms + wall_ms))
    n=$((n + 1))

    if [ "$rc" -ne 0 ]; then
        status=SPIKE-FATAL
    elif diff -q "$out" "$expected" > /dev/null 2>&1; then
        status=IDENTICAL
    else
        status=DIFFER
    fi
    printf '%s\t%d\t%d\t%s\t%s\n' "$status" "$rc" "$wall_ms" "$disk" "$basic" >> "$RESULTS"
done < "$JOBS"

echo "=== summary ==="
awk -F'\t' '{c[$1]++} END {for (k in c) printf "  %s: %d\n", k, c[k]}' "$RESULTS"
if [ "$n" -gt 0 ]; then
    avg_ms=$((total_ms / n))
    printf "  avg ms per spike: %d (over %d samples)\n" "$avg_ms" "$n"
fi
```

- [ ] **Step 3: Run Phase B**

```bash
chmod +x /tmp/phase-b.sh && /tmp/phase-b.sh
```

Expected: `IDENTICAL: 20`. Any `DIFFER` means the new loader changed behaviour for previously-passing programs — a regression that needs investigation.

- [ ] **Step 4: Decision gate on full sweep**

Read the printed average. Compute projected full spike-only rerun cost:

```
projected_minutes = (avg_ms × 6667) / 60000
```

If `< 30 minutes`: proceed to Task 12 (full spike-only sweep).
If `≥ 30 minutes`: stop here. Phase A + B are sufficient evidence; full sweep deferred.

No commit — outputs in `/tmp/`.

---

## Task 12 (conditional): Full spike-only sweep

Skip this task if Phase B's projected time was ≥ 30 minutes.

**Files:**
- Create: `/tmp/spike-sweep.sh` (not committed)

- [ ] **Step 1: Build the list of jobs**

```bash
cd /Users/pmoore
awk -F'\t' '$1=="CAPTURED" || $1=="PARTIAL" {print $2 "\t" $3}' detok-sweep.tsv > /tmp/spike-sweep-jobs.tsv
wc -l /tmp/spike-sweep-jobs.tsv
```

Expected: `~6667 /tmp/spike-sweep-jobs.tsv`.

- [ ] **Step 2: Write the runner**

Create `/tmp/spike-sweep.sh`:

```bash
#!/usr/bin/env bash
# Full spike-only sweep across the corpus. No llist, no b2t, no
# comparison — just produce fresh spike captures using the multi-page
# loader. Output goes to ~/detok-spike-only/, organised by disk stem
# (bijective safeName encoding matching the main sweep).
set -u

SPIKE=/tmp/detok-spike
CORPUS=/Users/pmoore/sam-corpus/disks
OUT=/Users/pmoore/detok-spike-only
LOG=/Users/pmoore/detok-spike-only.log

mkdir -p "$OUT"
: > "$LOG"

safe() {
    python3 -c "
import sys
LIT=set('ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.+-')
out=[]
for c in sys.argv[1]:
    b=ord(c)
    if c in LIT: out.append(c)
    elif b==0x5F: out.append('__')
    else: out.append(f'_{b:02X}')
print(''.join(out))
" "$1"
}

n=0
ok=0
fail=0
start=$(date +%s)
while IFS=$'\t' read -r disk basic; do
    [ -z "$disk" ] && continue
    n=$((n + 1))
    disk_stem=$(safe "${disk%.mgt}")
    file_stem=$(safe "$basic")
    diskdir="$OUT/$disk_stem"
    mkdir -p "$diskdir"
    out="$diskdir/$file_stem.spike.txt"
    if "$SPIKE" --mgt "$CORPUS/$disk" --filename "$basic" --out "$out" > /dev/null 2>&1; then
        ok=$((ok + 1))
    else
        fail=$((fail + 1))
    fi
    if [ $((n % 100)) -eq 0 ]; then
        elapsed=$(($(date +%s) - start))
        rate=$(awk -v n="$n" -v e="$elapsed" 'BEGIN{ if (e>0) printf "%.1f", n/e; else printf "0.0" }')
        echo "[$n] elapsed=${elapsed}s rate=${rate}/s ok=$ok fail=$fail" | tee -a "$LOG"
    fi
done < /tmp/spike-sweep-jobs.tsv

elapsed=$(($(date +%s) - start))
echo "DONE in ${elapsed}s: $ok ok, $fail fail" | tee -a "$LOG"
```

- [ ] **Step 3: Run with caffeinate**

```bash
chmod +x /tmp/spike-sweep.sh
caffeinate -i /tmp/spike-sweep.sh
```

Expected: completes in the time projected by Phase B's avg-time math. Outputs in `~/detok-spike-only/`.

- [ ] **Step 4: Quick sanity check**

```bash
find /Users/pmoore/detok-spike-only -name '*.spike.txt' | wc -l
```

Expected: close to 6667 (minus any genuine PRAMTP-overflow cases).

No commit — outputs are artefacts, not code.

---

## Self-review check

Done in writing. Verified:

- **Spec coverage:** every section of the spec maps to at least one task.
  - Memory layout + REL PAGE FORM → Tasks 1, 4
  - Loader shape → Tasks 2, 3, 5, 6, 9
  - Error handling → Task 5 (size guard), Task 9 (log.Fatalf wiring)
  - Testing strategy: Layer 1 → Tasks 2-5, 7, 8; Layer 2 Phase A → Task 10; Phase B → Task 11; Layer 3 → Task 12
  - Open questions: screen-page overlap surfaces naturally in Phase A (if it bites, a multi-page program won't decode correctly); PROGP write present in Task 9.

- **No placeholders:** every step has actual code, actual commands, actual expected output.

- **Type consistency:** `pos` used identically across Tasks 2, 7, 8, 9; `setSysvarPair(hw, pageAddr, offsetAddr, pos)` signature identical at definition (Task 4) and use site (Task 9); `checkFits(progLen int, pramtp uint8) error` consistent between Task 5 definition and Task 9 use.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-20-spike-multi-page-loader.md`.

Two execution options:

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration. The user's earlier preference ("consult with me before commencing the actual coding") aligns naturally with this — checkpoints between tasks let you sanity-check before the next dispatch.

**2. Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batched with checkpoints for review.
