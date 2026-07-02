package z80_test

// hook_roundtrip_faithful_test.go — the i93b emulation gate: the whole B-DOS
// whole-file hook round-trip (HRECORD select -> HSAVE -> HGTHD -> HLOAD ->
// byte-compare) dispatched via the REAL RST 8 against Colin's real ROM + B-DOS
// 1.5t + the SD model, from the exact trinload-pushed context (page 1,
// DOSCNT=0, LMPR=&1F/HMPR=1). This is the faithful rehearsal of the i93b
// hardware shot: sd-push a scratch record, run build/hook_roundtrip.bin
// against it, expect verdict 'P'.
//
// Two tests:
//   - TestHookRoundtripComesUpFaithful registers the tool with the i327
//     come-up gate (entry -> serve loop -> exactly one "!HR" discovery reply).
//     The unpatched binary probes record 1, whose seeded first directory
//     sector carries the "BDOS" +232 stamp, so the whole probe runs en route
//     to the serve loop.
//   - TestHookRoundtripFaithful seeds a scratch record (an all-zeros .mgt,
//     stamped by seedRecordFromMGT exactly as sd_push stamps a pushed record),
//     patches the tool's HKRT_CONFIG to that record, runs entry -> serve loop,
//     and asserts: phase '6' + verdict 'P' + detail 0 in the tool's RAM, the
//     same verdict over the wire via the 'R' verb (what the launcher decodes),
//     and the HSAVEd pattern physically present in the scratch record's LBA
//     band on the SD model.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS
// (i253). Emulation-verified is not hardware-verified (CLAUDE.md §5): the
// real-Trinity run is the i93b gate itself.

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	hookRoundtripBin = "../../../build/hook_roundtrip.bin"
	hookRoundtripMap = "../../../build/hook_roundtrip.map"

	// hkrtPatLen mirrors PAT_LEN in src/netboot/hook_roundtrip.asm: 3 full
	// sectors + 17 bytes, crossing sector boundaries with an odd tail.
	hkrtPatLen = 1553
)

// hkrtPattern computes the tool's deterministic pattern for the SRC_BUF at
// srcAddr — i62test's generator f(addr) = 2*low XOR high, byte-for-byte the
// hkrt_fill loop (`ld a,l ; add a,a ; xor h`).
func hkrtPattern(srcAddr uint16) []byte {
	pat := make([]byte, hkrtPatLen)
	for i := range pat {
		addr := srcAddr + uint16(i)
		pat[i] = byte(addr&0xFF)*2 ^ byte(addr>>8)
	}
	return pat
}

func TestHookRoundtripComesUpFaithful(t *testing.T) {
	payloads := comeUpFaithful(t,
		hookRoundtripBin, hookRoundtripMap, "make netboot-hook-roundtrip",
		"hook_roundtrip_main", "hkrt_serve_loop")
	assertToolTagReply(t, payloads, "!HR")
}

func TestHookRoundtripFaithful(t *testing.T) {
	mac, _, sd, enc := bootToEditorIdleSDENC(t)

	// Seed the scratch record: an all-zeros .mgt is what the hardware ladder
	// sd-pushes ("hkscratch"); seedRecordFromMGT stamps "BDOS" at body +232 and
	// the label at +210 exactly as sd_push mutates sector 0 on write, so the
	// HRECORD select's stamp validation passes.
	const rec = 2
	seedRecordFromMGT(sd, rec, make([]byte, 819200), "hkscratch")
	seedRecordList(sd, map[int]string{1: "rec1", rec: "hkscratch"})

	// Load the tool into the deployment-contract page and patch HKRT_CONFIG to
	// the scratch record (what hook-roundtrip.py does by file offset).
	code := append([]byte(nil), mustReadFile(t, hookRoundtripBin, "make netboot-hook-roundtrip")...)
	if err := mac.LoadSymbols(hookRoundtripMap); err != nil {
		t.Fatalf("load %s: %v", hookRoundtripMap, err)
	}
	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s", name, hookRoundtripMap)
		}
		return a
	}
	cfgOff := int(sym("HKRT_CONFIG")) - 0x8000
	if code[cfgOff] != 0x5A {
		t.Fatalf("config magic at file offset %#x is %#x, want 0x5A", cfgOff, code[cfgOff])
	}
	code[cfgOff+1], code[cfgOff+2] = byte(rec), byte(rec>>8) // HKRT_CFG_RECORD (LE16)

	const page = 1 // the deployment contract: trinload pushes tools to page 1
	mac.Pager().HMPR = page
	mac.Write(0x8000, code)
	armServeDispatch(mac, page)

	// Run the whole probe: entry -> (HRECORD, HSAVE, HGTHD, HLOAD, compare) ->
	// serve loop.
	res, err := mac.ContinueFrom(sym("hook_roundtrip_main"), z80h.Entry{
		StepCap: 60_000_000, FrameIntPeriod: 60000,
		StopPC: sym("hkrt_serve_loop"),
	})
	if err != nil {
		t.Fatalf("hook_roundtrip_main faulted: %v (PC=&%04X)", err, res.PC)
	}
	if !res.ReachedStop {
		mac.Pager().HMPR = page
		phase := mac.Read(sym("hkrt_phase"), 1)[0]
		t.Fatalf("probe did not reach hkrt_serve_loop (&%04X): PC=&%04X after %d steps, last completed stage %q — a hook longjmped or wedged (the on-hardware signature would be the same stage digit)",
			sym("hkrt_serve_loop"), res.PC, res.Steps, phase)
	}

	// The probe's own verdict, read straight from the tool's RAM.
	mac.Pager().HMPR = page
	phase := mac.Read(sym("hkrt_phase"), 1)[0]
	verdict := mac.Read(sym("hkrt_verdict"), 1)[0]
	detail := leU16(mac.Read(sym("hkrt_detail"), 2))
	t.Logf("probe outcome: phase=%q verdict=%q detail=%d (steps=%d)", phase, verdict, detail, res.Steps)
	if phase != '6' {
		t.Fatalf("phase = %q, want '6' — the probe stopped short of the verdict", phase)
	}
	if verdict != 'P' {
		t.Fatalf("verdict = %q (detail=%d, &FFFF = recorded-size mismatch, else first mismatch offset), want 'P' — the hook round-trip did not carry the bytes", verdict, detail)
	}
	if detail != 0 {
		t.Fatalf("detail = %d, want 0 on a pass", detail)
	}

	// The same verdict over the wire via the 'R' verb — what the launcher
	// decodes. The tool adopted its identity from the real EEPROM chunk.
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Read(sym("chunk"), 6))
	copy(samIP[:], mac.Read(sym("chunk")+6, 4))
	txBefore := len(enc.TXFrames())
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'R'}))
	if _, err := mac.ContinueFrom(res.PC, z80h.Entry{StepCap: 4_000_000, FrameIntPeriod: 60000}); err != nil {
		t.Fatalf("'R' report drive faulted: %v", err)
	}
	wantReply := []byte{'P', '6', 0x00, 0x00}
	replies := 0
	for _, f := range enc.TXFrames()[txBefore:] {
		if u, ok := frame.ParseUDP(f); ok && bytes.Equal(u.Payload, wantReply) {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("got %d 'R' replies matching % X, want exactly 1 — the report verb does not carry the verdict", replies, wantReply)
	}

	// The HSAVE physically landed: the pattern is present in a CMD24-written
	// block INSIDE the scratch record's LBA band. The needle starts past the
	// 9-byte file header so it sits contiguously within the first body sector.
	pat := hkrtPattern(sym("SRC_BUF"))
	needle := pat[16:80]
	blk, off := findPatternBlock(sd, needle)
	if blk == 0 {
		t.Fatalf("the HSAVEd pattern is not in any SD store block — the save did not carry the staged bytes to the card")
	}
	lo := uint32(faithCSDBase + recSectors*(rec-1))
	hi := lo + recSectors
	if blk < lo || blk >= hi {
		t.Fatalf("the pattern landed at LBA %d (offset %d), outside record %d's band [%d,%d) — the write was not record-directed", blk, off, rec, lo, hi)
	}
	t.Logf("HSAVE landed the pattern in record %d's band: LBA %d offset %d", rec, blk, off)
}
