// trinload_test.go — i132: run simonowen/trinload under the flat-memory koron-z80
// harness and validate the push→run→return (?/@/X) cycle in emulation.
//
// trinload (src/netboot/trinload.asm, vendored from simonowen/trinload @ a4b7af7) is
// the Trinity network code loader: it listens on UDP port 0xEDB0 for three messages —
// '?' (discovery, reply '!'), '@' (data block, LDIR into SAM RAM), and 'X' (execute
// at an address, RET-return to trinload) — and is the final-push vehicle for delivering
// and running payloads over the network (i129/i132). This test validates the full
// push→run→return cycle:
//
//  1. A '?' frame arrives → trinload replies '!' on the wire.
//  2. An '@' frame arrives with a small test program → trinload LDIRs it into RAM at
//     the specified offset (&8000), with the page byte written to HMPR.
//  3. An 'X' frame arrives → trinload executes the test program; the program writes a
//     sentinel byte and RETs; control returns to trinload's read_loop (StopPC).
//
// The test also exercises the flat-harness ROM stubs that replace the SAM ROM routines
// trinload calls (JCLSBL/JSETSTRM/rst 16/ROM_LDIR); without them the run spins off
// into zeroed memory and hangs. ROM_LDIR is the critical one: it is the LDIR that
// copies the '@' data block into RAM.
//
// Emulation-verified is NOT hardware-verified — the final integration gate stays real
// Trinity hardware (CLAUDE.md §5). What this test proves: trinload's protocol logic
// is correct in emulation. What it does not prove: real-silicon TX/RX timing, PHY
// link-up, or the SAM paging hardware (HMPR writes are recorded but not acted on in
// the flat all-RAM model).
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	trinloadBin     = "../../../build/trinload.bin"
	trinloadMap     = "../../../build/trinload.map"
	trinloadOrg     = 0x6000
	trinloadStepCap = 2_000_000
)

// trinloadFrame builds a UDP frame addressed to the SAM (the trinload server) with
// the given payload. The DstPort is 0xEDB0 (trinload's listen port).
func trinloadFrame(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  frame.MAC(mask.ServerMAC),
		SrcMAC:  frame.MAC(mask.ClientMAC),
		SrcIP:   frame.IPv4(mask.ClientIP),
		DstIP:   frame.IPv4(mask.ServerIP),
		SrcPort: 40000,
		DstPort: 0xEDB0,
		Payload: payload,
	})
}

// installTrinloadROMStubs installs minimal stubs for the SAM ROM routines trinload
// calls. The flat harness has no ROM, so each stub is a bare RET (or LDIR; RET for
// ROM_LDIR). Without these, the run wanders into zeroed memory and loops forever.
//
//   - rst 16 (&0010): print-char; stub discards the output.
//   - ROM_LDIR (&008F): LDIR in ROM (trinload uses it for the '@' data copy — this
//     is load-bearing; a bare RET here would skip the copy and break the test).
//   - JSETSTRM (&0112): set output stream; stub ignores it.
//   - JCLSBL (&014E): CLS; stub ignores it.
func installTrinloadROMStubs(mac *z80h.Machine) {
	mac.Write(0x0010, []byte{0xC9})             // rst 16 (print char) -> RET (output discarded)
	mac.Write(0x008F, []byte{0xED, 0xB0, 0xC9}) // ROM_LDIR -> LDIR; RET (load-bearing: the @ data copy)
	mac.Write(0x0112, []byte{0xC9})             // JSETSTRM -> RET
	mac.Write(0x014E, []byte{0xC9})             // JCLSBL  -> RET
}

// TestTrinloadPushRunReturn validates the full push→run→return cycle:
//   - '?' discovery → '!' reply on the wire
//   - '@' data block → program bytes at &8000, HMPR set to page 1
//   - 'X' execute → test program runs, sentinel written, RETs to trinload start
func TestTrinloadPushRunReturn(t *testing.T) {
	mac, err := z80h.LoadAt(trinloadBin, trinloadMap, trinloadOrg)
	if err != nil {
		t.Fatalf("trinload not built (%v); run `make netboot-trinload`", err)
	}
	installTrinloadROMStubs(mac)

	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// The pushed test program: ld a,&5A; ld (&9000),a; ret
	// Writes sentinel 0x5A to &9000 (clear of trinload's code &6000-&6D05 and its
	// packet buffer &6D06-&72FC), then RETs back to trinload's start.
	prog := []byte{0x3E, 0x5A, 0x32, 0x00, 0x90, 0xC9}

	// Inject the three frames in order; trinload's read_loop pulls one per drv_read.
	enc.InjectRX(trinloadFrame([]byte{'?'}))
	enc.InjectRX(trinloadFrame(append([]byte{'@', 0x01, 0x00, 0x00}, prog...)))
	enc.InjectRX(trinloadFrame([]byte{'X', 0x01, 0x00, 0x80}))

	startAddr, err := mac.Sym("start")
	if err != nil {
		t.Fatalf("sym start: %v", err)
	}
	res, err := mac.RunBoot("start", z80h.Entry{
		StepCap:    trinloadStepCap,
		StopPC:     startAddr,
		StopPCSkip: 1,
	})
	if err != nil {
		t.Fatalf("RunBoot trinload start: %v", err)
	}
	t.Logf("trinload: PC=&%04X steps=%d reachedStop=%v tx=%d",
		res.PC, res.Steps, res.ReachedStop, len(enc.TXFrames()))

	// Control must return to trinload's start after the pushed program RET'd.
	if !res.ReachedStop {
		t.Fatalf("control did not return to trinload's start after the pushed program RET'd (PC=&%04X, steps=%d)", res.PC, res.Steps)
	}
	if res.PC != startAddr {
		t.Errorf("PC=&%04X after StopPC trigger, want start=&%04X", res.PC, startAddr)
	}

	// The pushed program must have run (sentinel 0x5A at &9000).
	if got := mac.Read(0x9000, 1)[0]; got != 0x5A {
		t.Errorf("sentinel @ &9000 = &%02X, want &5A — the pushed program did not run", got)
	}

	// The '@' data must have landed at the correct offset (&8000 + 0 = &8000).
	// trinload copies len(payload) bytes (4-byte header + prog) from packet+46;
	// the first len(prog) bytes are the real data; the trailing 4 are the over-copy
	// (harmless — the pushed program RETs before reaching them, so we only assert
	// the first len(prog) bytes).
	if got := mac.Read(0x8000, len(prog)); !bytes.Equal(got, prog) {
		t.Errorf("program bytes @ &8000 = %x, want %x — the @ data-write (offset/ROM_LDIR) failed", got, prog)
	}

	// HMPR must have been set to page 1 (from both '@' and 'X' packets).
	if p, ok := enc.LastHMPR(); !ok || p != 1 {
		t.Errorf("trinload set HMPR=%d (written=%v), want page 1 from the @/X packets", p, ok)
	}

	// The '!' discovery reply must appear among the TX frames.
	foundReply := false
	for _, f := range enc.TXFrames() {
		u, ok := frame.ParseUDP(f)
		if ok && bytes.Equal(u.Payload, []byte{'!'}) {
			foundReply = true
			break
		}
	}
	if !foundReply {
		t.Errorf("no '!' discovery reply among %d TX frames — trinload did not answer the ? query", len(enc.TXFrames()))
	}
}

// TestTrinloadDataWriteToOffset validates the '@' data-write path independently of
// execution: only '?' and '@' to a non-zero offset are injected; no 'X'. The run
// spins at the step cap busy-looping in read_loop after the queue drains (expected).
// The assertion is that the data landed at &8000 + 0x0040 = &8040.
//
// Only the first len(data) bytes are asserted; the 4 trailing over-copy bytes that
// trinload copies beyond the real data (a quirk of its BC = UDP_length - 8 calculation
// including the 4-byte '@'/page/offset header) are not checked.
func TestTrinloadDataWriteToOffset(t *testing.T) {
	mac, err := z80h.LoadAt(trinloadBin, trinloadMap, trinloadOrg)
	if err != nil {
		t.Fatalf("trinload not built (%v); run `make netboot-trinload`", err)
	}
	installTrinloadROMStubs(mac)

	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	// Inject discovery (consumed first) then data at page 1 offset 0x0040.
	enc.InjectRX(trinloadFrame([]byte{'?'}))
	enc.InjectRX(trinloadFrame(append([]byte{'@', 0x01, 0x40, 0x00}, data...)))

	// No 'X': trinload spins in read_loop; RunBoot returns at the step cap.
	res, err := mac.RunBoot("start", z80h.Entry{StepCap: trinloadStepCap})
	if err != nil {
		t.Fatalf("RunBoot trinload start: %v", err)
	}
	t.Logf("trinload offset test: PC=&%04X steps=%d tx=%d", res.PC, res.Steps, len(enc.TXFrames()))

	// The '@' data must have landed at &8000 + 0x0040 = &8040
	// (trinload's `set 7,d` forces the high bit of the offset MSB, placing data in
	// the top 32K regardless of the raw offset value).
	if got := mac.Read(0x8040, len(data)); !bytes.Equal(got, data) {
		t.Errorf("data @ &8040 = %x, want %x — the @ offset arithmetic failed", got, data)
	}
}

// countDiscoveryReplies counts the '!' discovery replies among the TX frames.
// trinload answers each '?' query with a single-byte '!' UDP payload; the '@' and
// 'X' acks carry 4-byte payloads, so they do not collide with this count.
func countDiscoveryReplies(frames [][]byte) int {
	n := 0
	for _, f := range frames {
		if u, ok := frame.ParseUDP(f); ok && bytes.Equal(u.Payload, []byte{'!'}) {
			n++
		}
	}
	return n
}

// TestTrinloadWedgedByNonReturningProgram models the i278 wedge class: a pushed
// program that never RETs to trinload's `start` holds the CPU forever, so
// trinload's read_loop never runs again and a *later* discovery push goes
// unanswered — the SAM appears "not responding even though running", recoverable
// only by a reset / power-cycle (i266/TAPO). This is exactly the i270 WRQ-hang
// symptom observed on hardware 2026-06-26.
//
// The pushed program here is `jr $` (an infinite self-loop), the emulation
// stand-in for any program whose exit strands the CPU instead of returning to
// trinload (a bare `di; halt`, or a hung serve loop). The sequence is:
//  1. '?'  → trinload replies '!'           (reply #1, proves it was listening)
//  2. '@'  → loads `jr $` at &8000
//  3. 'X'  → executes it; the CPU wedges in the self-loop, never reaching start
//  4. '?'  → arrives, but read_loop never runs again, so NO reply egresses
//
// The wedge is proven by exactly ONE '!' reply: the pre-execute discovery was
// answered, the post-execute one was not.
func TestTrinloadWedgedByNonReturningProgram(t *testing.T) {
	mac, err := z80h.LoadAt(trinloadBin, trinloadMap, trinloadOrg)
	if err != nil {
		t.Fatalf("trinload not built (%v); run `make netboot-trinload`", err)
	}
	installTrinloadROMStubs(mac)

	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// The wedge program: jr $ (0x18 0xFE) — loops on itself forever, never RETs.
	wedge := []byte{0x18, 0xFE}

	enc.InjectRX(trinloadFrame([]byte{'?'}))                                     // discovery #1
	enc.InjectRX(trinloadFrame(append([]byte{'@', 0x01, 0x00, 0x00}, wedge...))) // load wedge @ &8000
	enc.InjectRX(trinloadFrame([]byte{'X', 0x01, 0x00, 0x80}))                   // execute @ &8000
	enc.InjectRX(trinloadFrame([]byte{'?'}))                                     // discovery #2 — must go unanswered

	startAddr, err := mac.Sym("start")
	if err != nil {
		t.Fatalf("sym start: %v", err)
	}
	// No StopPC: the wedged program never reaches start, so the run spins to the cap.
	res, err := mac.RunBoot("start", z80h.Entry{StepCap: trinloadStepCap})
	if err != nil {
		t.Fatalf("RunBoot trinload start: %v", err)
	}
	t.Logf("trinload wedge: PC=&%04X steps=%d replies=%d tx=%d",
		res.PC, res.Steps, countDiscoveryReplies(enc.TXFrames()), len(enc.TXFrames()))

	// Control must NOT have returned to trinload's start — the program wedged.
	if res.PC == startAddr {
		t.Errorf("PC returned to start=&%04X — the wedge program unexpectedly returned to trinload", startAddr)
	}

	// Exactly one '!' reply: discovery #1 was answered, discovery #2 was not, because
	// the CPU is stuck in the wedge program and read_loop never ran again.
	if got := countDiscoveryReplies(enc.TXFrames()); got != 1 {
		t.Errorf("got %d '!' discovery replies, want 1 — a wedged program must leave the post-execute discovery unanswered (else the wedge model is wrong)", got)
	}
}

// TestTrinloadRespondsToRediscoveryAfterCleanReturn is the positive control for
// TestTrinloadWedgedByNonReturningProgram: a pushed program that RETs cleanly hands
// control back to trinload's start, which re-runs drv_init and re-enters read_loop,
// so a *later* discovery push IS answered. This is the i278 outcome a clean-exit
// (#4 tr_terminate) program must satisfy — trinload stays re-pushable.
//
// The sequence is identical to the wedge test except the pushed program is `ret`:
//  1. '?'  → reply '!'           (reply #1)
//  2. '@'  → loads `ret` at &8000
//  3. 'X'  → executes; the program RETs → control returns to start → read_loop
//  4. '?'  → answered → reply '!' (reply #2)
//
// Two '!' replies prove trinload recovered to its listen loop after the program ran.
func TestTrinloadRespondsToRediscoveryAfterCleanReturn(t *testing.T) {
	mac, err := z80h.LoadAt(trinloadBin, trinloadMap, trinloadOrg)
	if err != nil {
		t.Fatalf("trinload not built (%v); run `make netboot-trinload`", err)
	}
	installTrinloadROMStubs(mac)

	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// The clean program: ret (0xC9) — returns immediately to trinload's start.
	clean := []byte{0xC9}

	enc.InjectRX(trinloadFrame([]byte{'?'}))                                     // discovery #1
	enc.InjectRX(trinloadFrame(append([]byte{'@', 0x01, 0x00, 0x00}, clean...))) // load ret @ &8000
	enc.InjectRX(trinloadFrame([]byte{'X', 0x01, 0x00, 0x80}))                   // execute @ &8000
	enc.InjectRX(trinloadFrame([]byte{'?'}))                                     // discovery #2 — must be answered

	// No StopPC: after the clean RET, start re-runs and read_loop answers discovery
	// #2; the run then spins to the cap once the inject queue drains.
	res, err := mac.RunBoot("start", z80h.Entry{StepCap: trinloadStepCap})
	if err != nil {
		t.Fatalf("RunBoot trinload start: %v", err)
	}
	t.Logf("trinload clean-return: PC=&%04X steps=%d replies=%d tx=%d",
		res.PC, res.Steps, countDiscoveryReplies(enc.TXFrames()), len(enc.TXFrames()))

	// Two '!' replies: trinload returned to read_loop after the program RET'd and
	// answered the second discovery — it is still re-pushable.
	if got := countDiscoveryReplies(enc.TXFrames()); got != 2 {
		t.Errorf("got %d '!' discovery replies, want 2 — after a clean RET trinload must re-enter read_loop and answer the re-discovery", got)
	}
}
