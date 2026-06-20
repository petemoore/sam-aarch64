// netboot_serve_terminate_test.go — i121e: host-verification of the serve unit's
// two graceful-termination exits, both returning control to trinload (the only way
// to run our code on the SAM until an auto-boot BIOS exists, so a server with no
// exit strands the machine — manifest design decision 6).
//
//   - The reserved-name (unattended) exit: a `tftp put tftp.done` is detected in
//     handle_wrq BEFORE any free-record find / claim / arm; the server sets
//     XFER_STOP_REQUESTED, sends a courtesy ACK-0, and stores nothing. sv_serve_loop
//     polls the flag after each frame and RETs.
//   - The keyboard Esc (attended) exit: a non-blocking poll of the SAM keyboard
//     matrix (port &f9 row &f7, bit 5 = Esc) at the top of sv_serve_loop RETs when
//     Esc is held — the same poll the ROM dumper (netboot_dumper.asm dm_serve_loop)
//     and trinload (trinload.asm read_loop) use.
//
// THE RET-REACHES-TRINLOAD MODEL: trinload bootstraps our program by pushing its
// `start` as the return address and `jp`-ing in, so a plain RET returns to it
// (proven by TestTrinloadPushRunReturn). serve_main is `jp`'d to and sv_serve_loop
// falls through with no extra stack frame, so the stack top at the loop is exactly
// that pushed return address. The harness models this the same way: CallEntry
// pushes the HALT trap as the return address, so a RET out of sv_serve_loop lands on
// the trap → res.Halted (the StopPC/ReachedStop idiom of trinload_test.go and the
// dumper Esc exit, expressed here as a clean RET-to-trap = Halted).
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5): the real trinload
// round-trip over the wire on real Trinity stays gated; what these prove is that the
// loop's exit logic RETs cleanly to the caller's pushed return address.
package z80_test

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// assertLoopRETs calls sv_serve_loop and asserts it RETs to the caller (lands on the
// HALT trap = trinload's pushed return address in the real boot path). A spin (the
// loop never exiting) shows as res.Halted == false at the step cap.
func assertLoopRETs(t *testing.T, mac *z80h.Machine, why string) {
	t.Helper()
	// A small step cap: a clean exit RETs in a handful of instructions; if the loop
	// fails to exit it spins on serve_serve_once (drv_read on an empty wire), so the
	// cap bounds the failure instead of running to the 5M default.
	res, err := mac.CallEntry("sv_serve_loop", z80h.Entry{StepCap: 200_000})
	if err != nil {
		t.Fatalf("call sv_serve_loop (%s): %v", why, err)
	}
	if !res.Halted {
		t.Fatalf("sv_serve_loop did not RET (%s): PC=&%04X steps=%d — the loop never returned to trinload",
			why, res.PC, res.Steps)
	}
}

// TestServeStopSentinelExit drives the reserved-name unattended exit through the
// REAL serve_serve_once dispatcher: a `tftp put tftp.done` WRQ must set
// XFER_STOP_REQUESTED, reply with a courtesy ACK-0, and NOT find/claim/arm a record
// (no sector writes, WRQ_RECV_ACTIVE == 0). Then sv_serve_loop, run on the same
// machine, RETs (the flag check after serve_serve_once unwinds to trinload).
func TestServeStopSentinelExit(t *testing.T) {
	const records, freeRecord = 8, 4 // a record IS free — proving the sentinel skips the claim anyway
	mac, enc, store, _ := loadServeRecordPush(t, records, freeRecord)

	// 1. The reserved-name WRQ → a courtesy ACK-0, the stop flag set, nothing armed.
	got := serveDemo(t, mac, enc, demoWRQ("tftp.done", nil))
	if got == nil {
		t.Fatal("tftp.done WRQ drew no reply — want a courtesy ACK-0")
	}
	pay := udpPayload(t, got)
	if tftp.Opcode(pay) != tftp.OpACK {
		t.Fatalf("tftp.done reply opcode = %d, want ACK(%d) — got %x", tftp.Opcode(pay), tftp.OpACK, pay)
	}
	if blk, err := tftp.ParseACK(pay); err != nil || blk != 0 {
		t.Fatalf("tftp.done reply = ACK block %d (err %v), want the courtesy ACK-0", blk, err)
	}

	// The stop flag is set, so sv_serve_loop will RET on its next poll.
	if v := mac.Read(symAddr(t, mac, "XFER_STOP_REQUESTED"), 1)[0]; v != 1 {
		t.Errorf("XFER_STOP_REQUESTED = %d after the tftp.done push, want 1", v)
	}

	// Reserved name ⇒ NEVER stored: no record was claimed or armed, even though a
	// free record exists. (The shared-resource invariant: the control name is not a
	// file, so no record-list entry, no sectors, no receiver.)
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 0 {
		t.Errorf("WRQ_RECV_ACTIVE = %d after tftp.done, want 0 (no receiver armed)", active)
	}
	if w := store.SectorWrites(); len(w) != 0 {
		t.Fatalf("SectorWrites() = %d after tftp.done, want 0 (the control name is never stored)", len(w))
	}
	if lw := store.ListWrites(); len(lw) != 0 {
		t.Fatalf("ListWrites() = %d after tftp.done, want 0 (no record claimed)", len(lw))
	}

	// 2. The serve loop RETs to trinload: the flag check after serve_serve_once
	//    (run here with an empty wire — a no-op) RETs to the caller's return address.
	assertLoopRETs(t, mac, "XFER_STOP_REQUESTED set")
}

// TestServeEscExit drives the attended keyboard exit: with Esc held at the SAM, the
// poll at the TOP of sv_serve_loop RETs immediately (before serve_serve_once), the
// same Esc-to-trinload poll the dumper (TestDumper... pattern) and trinload use.
func TestServeEscExit(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, _, _, _ := loadServeRecordPush(t, records, freeRecord)

	// No stop flag set (the sentinel path is untouched); the ONLY exit here is Esc.
	if v := mac.Read(symAddr(t, mac, "XFER_STOP_REQUESTED"), 1)[0]; v != 0 {
		t.Fatalf("XFER_STOP_REQUESTED = %d at start, want 0 (Esc is the only exit under test)", v)
	}

	// Operator holds Esc: the keyboard-matrix poll (`in a,(&f9); bit 5,a`) reads 0.
	mac.PressEsc(true)
	assertLoopRETs(t, mac, "Esc held")
}

// TestServeEscNotPressedSpins is the negative control for both exits: with NO key
// pressed (the harness default, matrix = 0xFF) and the stop flag clear,
// sv_serve_loop does NOT exit — it spins in serve_serve_once on the empty wire. This
// proves the exits are gated on Esc actually being held / the flag actually being set
// (neither is fabricated) and that the default host build keeps serving, the same
// guarantee TestServeBootFromEEPROM relies on. CallEntry reports a non-returning
// routine as a step-cap error (capIsError), so the spin is the error case here — the
// inverse of the clean RET the two exit tests assert.
func TestServeEscNotPressedSpins(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, _, _, _ := loadServeRecordPush(t, records, freeRecord)

	// No Esc, no stop flag: the loop must spin (never RET). CallEntry surfaces that as
	// a "did not return" error; a nil error (a clean RET) would mean an exit fired
	// without being requested — the gating bug this guards against.
	if _, err := mac.CallEntry("sv_serve_loop", z80h.Entry{StepCap: 200_000}); err == nil {
		t.Fatal("sv_serve_loop RETd with no Esc and no stop flag — an exit fired un-requested; the gating is broken")
	}
}

// TestServeNormalWRQStillArms is the regression guard: a normal (non-sentinel) WRQ
// still arms a disk-record push exactly as before i121e — the sentinel check is a
// no-op on any other name. The handshake byte-matches the Go authority and the
// receiver is armed (WRQ_RECV_ACTIVE == 1), with the stop flag left clear.
func TestServeNormalWRQStillArms(t *testing.T) {
	const records, freeRecord = 8, 4
	mac, enc, _, goRef := loadServeRecordPush(t, records, freeRecord)

	wrq := demoWRQ("upload.mgt", nil)
	eqFrame(t, "normal WRQ → ACK-0", serveDemo(t, mac, enc, wrq), goRef.OnFrame(wrq))

	// A normal push arms the receiver and does NOT request a stop.
	if active := mac.Read(symAddr(t, mac, "WRQ_RECV_ACTIVE"), 1)[0]; active != 1 {
		t.Errorf("WRQ_RECV_ACTIVE = %d after a normal WRQ, want 1 (the push must arm)", active)
	}
	if v := mac.Read(symAddr(t, mac, "XFER_STOP_REQUESTED"), 1)[0]; v != 0 {
		t.Errorf("XFER_STOP_REQUESTED = %d after a normal WRQ, want 0 (only tftp.done sets it)", v)
	}
}
