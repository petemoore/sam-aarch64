package z80_test

// tool_comeup_faithful_test.go — the i327 faithful come-up gate for the
// pushable tools: boot Colin's real ROM + B-DOS 1.5t to editor idle, load the
// tool into the exact trinload-pushed context (page 1, DOSCNT=0, LMPR=&1F/
// HMPR=1), run its real entry point, and assert it comes up — through the
// EEPROM config read, drv_init's chk_trinity identity probe (the i242/i327
// gate the flat harness cannot see: it stubs the probe timing), the CSD read,
// and the record-list scan — all the way to its serve loop, then answers a
// '?' discovery with its i329 tool tag on the virtual wire.
//
// This is the class of test the i319a hardware campaign lacked: the parked
// original wedged at sp_fail_init on the model's post-boot settle artifact
// (the i327 emulation gap, fixed in enc28j60.go SetTState/clockData) and
// masked the real scroll-wedge diagnosis. With the artifact fixed, a wedge
// here is a REAL tool regression.
//
// GATED on the proprietary captures; skips under SKIP_PRIVATE_TESTS (the one
// sanctioned skip, i253). Emulation-verified is not hardware-verified
// (CLAUDE.md §5).

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// comeUpFaithful boots the faithful machine, loads binPath+mapPath into the
// pushed-program context, runs entrySym to serveLoopSym, then injects a '?'
// discovery and returns the UDP payloads the tool transmitted in response.
func comeUpFaithful(t *testing.T, binPath, mapPath, buildHint, entrySym, serveLoopSym string) [][]byte {
	t.Helper()
	mac, _, _, enc := bootToEditorIdleSDENC(t)

	code := mustReadFile(t, binPath, buildHint)
	if err := mac.LoadSymbols(mapPath); err != nil {
		t.Fatalf("load %s: %v", mapPath, err)
	}
	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s", name, mapPath)
		}
		return a
	}

	const page = 1 // the deployment contract: trinload pushes tools to page 1
	sH := mac.Pager().HMPR
	mac.Pager().HMPR = page
	mac.Write(0x8000, code)
	mac.Pager().HMPR = sH
	armServeDispatch(mac, page)

	res, err := mac.ContinueFrom(sym(entrySym), z80h.Entry{
		StepCap: 40_000_000, FrameIntPeriod: 60000,
		StopPC: sym(serveLoopSym),
	})
	if err != nil {
		t.Fatalf("%s faulted: %v (PC=&%04X)", entrySym, err, res.PC)
	}
	if !res.ReachedStop {
		t.Fatalf("%s did not reach %s (&%04X): wedged at PC=&%04X after %d steps — the come-up path failed (drv_init/chk_trinity, CSD read, or the list scan)",
			entrySym, serveLoopSym, sym(serveLoopSym), res.PC, res.Steps)
	}

	// The tool adopted its identity from the real EEPROM chunk; read it back
	// so the discovery frame targets the tool exactly as a launcher would.
	// (sam_mac/sam_ip are equ aliases of chunk+0/+6, so only `chunk` is in
	// the symbol map.)
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Read(sym("chunk"), 6))
	copy(samIP[:], mac.Read(sym("chunk")+6, 4))

	txBefore := len(enc.TXFrames())
	enc.InjectRX(trinFrameTo(samMAC, samIP, []byte{'?'}))
	if _, err := mac.ContinueFrom(res.PC, z80h.Entry{StepCap: 4_000_000, FrameIntPeriod: 60000}); err != nil {
		t.Fatalf("discovery drive faulted: %v", err)
	}
	var payloads [][]byte
	for _, f := range enc.TXFrames()[txBefore:] {
		if u, ok := frame.ParseUDP(f); ok {
			payloads = append(payloads, u.Payload)
		}
	}
	return payloads
}

// assertToolTagReply asserts exactly one reply carrying the tool's i329
// discovery tag ('!' + two tag bytes) came back.
func assertToolTagReply(t *testing.T, payloads [][]byte, tag string) {
	t.Helper()
	n := 0
	for _, p := range payloads {
		if len(p) >= len(tag) && bytes.Equal(p[:len(tag)], []byte(tag)) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d %q discovery replies (payloads: %q), want exactly 1 — the tool is not live on the wire", n, tag, payloads)
	}
}

func TestSDPushComesUpFaithful(t *testing.T) {
	payloads := comeUpFaithful(t,
		sdPushFaithBin, sdPushFaithMap, "make netboot-sd-push",
		"sd_push_main", "sp_serve_loop")
	assertToolTagReply(t, payloads, "!SP")
}

func TestListRecordsComesUpFaithful(t *testing.T) {
	payloads := comeUpFaithful(t,
		listRecordsBin, listRecordsMap, "make netboot-list-records",
		"list_records_main", "lr_serve_loop")
	assertToolTagReply(t, payloads, "!LR")
}
