// keyboard_test.go — i138: host-verification of the keyboard sysvar stub
// (harness.go InjectKeys / PendingKeys) and the inlined-KYIP2 poll routines
// (src/netboot/key_read_test.asm: key_read_loop + key_poll_once).
//
// The harness has no interrupt model, so no key-available events arrive via the
// 50 Hz interrupt.  i138 stubs the two sysvars the SAM ROM interrupt populates:
//   LASTK (0x5C08) — last key pressed
//   FLAGS (0x5C3B) — status flags; bit 5 (0x20) = key-available
//
// When InjectKeys is called, reads of FLAGS|0x20 and LASTK from the queue are
// intercepted in harness.go mem.Get/Set (the c0f62fa BASIC-emulation-spike
// mechanism, ~lines 147-176 of tools/basic-emulator-spike/main.go).
//
// Three tests:
//
//   TestKeyReadLoop — positive: inject "15\r", run key_read_loop, assert KR_BUF
//     == "15", KR_COUNT == 2, PendingKeys() == 0 (queue drained).
//
//   TestKeyReadLoopPartialConsume — inject more keys than read before the \r
//     (e.g. "AB\rXY"), run key_read_loop, assert KR_BUF == "AB", KR_COUNT == 2,
//     PendingKeys() == 2 (the "XY" tail is still in the queue after the
//     RES-5 loop stopped at \r which is not stored but IS consumed).
//
//   TestKeyPollOnceNoKey — negative control: with NO injected keys, call
//     key_poll_once, assert it returns NZ (A != 0 → no key), KR_COUNT == 0,
//     KR_BUF[0] == 0. Proves FLAGS bit 5 reads clear when the queue is empty
//     and the routine does not fabricate a key.
//
// All tests skip if the client boot binary is not built (run `make netboot-client-boot`).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestKeyReadLoop verifies that key_read_loop reads injected keys via the
// LASTK/FLAGS sysvar stub, stores them in KR_BUF, and returns after consuming
// the terminating \r, leaving the queue empty.
func TestKeyReadLoop(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}

	// Inject "15\r": two printable keys followed by a CR terminator.
	mac.InjectKeys([]byte("15\r"))

	if _, err := mac.CallEntry("key_read_loop", z80h.Entry{}); err != nil {
		t.Fatalf("key_read_loop: %v", err)
	}

	// KR_BUF must contain "15" (2 bytes).
	buf := mac.Read(symAddr(t, mac, "KR_BUF"), 2)
	if string(buf) != "15" {
		t.Errorf("KR_BUF = %q, want \"15\"", buf)
	}

	// KR_COUNT (LE word at KR_COUNT) must be 2.
	cnt := mac.Read(symAddr(t, mac, "KR_COUNT"), 2)
	if cnt[0] != 2 || cnt[1] != 0 {
		t.Errorf("KR_COUNT = [%d %d] (LE), want [2 0]", cnt[0], cnt[1])
	}

	// Queue must be fully drained: the \r RES-5 consumed the last entry.
	if n := mac.PendingKeys(); n != 0 {
		t.Errorf("PendingKeys() = %d after key_read_loop, want 0 (queue drained)", n)
	}
}

// TestKeyReadLoopPartialConsume verifies the consume path: if the injected
// queue has keys after the \r, they remain (the loop stops at \r, not at queue
// end).  Inject "AB\rXY"; the loop stores "AB", consumes \r, then stops.
// The "XY" tail must still be in the queue.
func TestKeyReadLoopPartialConsume(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}

	// Inject "AB\rXY": the loop reads "AB", then \r (terminator, not stored).
	// "XY" should remain in the queue.
	mac.InjectKeys([]byte("AB\rXY"))

	if _, err := mac.CallEntry("key_read_loop", z80h.Entry{}); err != nil {
		t.Fatalf("key_read_loop: %v", err)
	}

	// KR_BUF must contain "AB".
	buf := mac.Read(symAddr(t, mac, "KR_BUF"), 2)
	if string(buf) != "AB" {
		t.Errorf("KR_BUF = %q, want \"AB\"", buf)
	}

	// KR_COUNT must be 2.
	cnt := mac.Read(symAddr(t, mac, "KR_COUNT"), 2)
	if cnt[0] != 2 || cnt[1] != 0 {
		t.Errorf("KR_COUNT = [%d %d] (LE), want [2 0]", cnt[0], cnt[1])
	}

	// "XY" must still be pending.
	if n := mac.PendingKeys(); n != 2 {
		t.Errorf("PendingKeys() = %d, want 2 (\"XY\" tail still queued)", n)
	}
}

// TestKeyPollOnceNoKey is the negative control: with NO injected keys, FLAGS
// bit 5 must read clear (the queue is empty, so harness.Get falls through to the
// raw RAM value which is 0 → bit 5 clear), and key_poll_once must return NZ
// (no key consumed).  This proves the stub does not fabricate a key when the
// queue is empty.
func TestKeyPollOnceNoKey(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}

	// No keys injected.
	res, err := mac.CallEntry("key_poll_once", z80h.Entry{})
	if err != nil {
		t.Fatalf("key_poll_once: %v", err)
	}

	// The routine sets NZ (OR 1) when no key: A must be non-zero.
	if res.A == 0 {
		t.Errorf("key_poll_once with empty queue: A = 0 (Z flag set), want non-zero (NZ = no key)")
	}

	// KR_COUNT must remain 0 (nothing stored).
	cnt := mac.Read(symAddr(t, mac, "KR_COUNT"), 2)
	if cnt[0] != 0 || cnt[1] != 0 {
		t.Errorf("KR_COUNT = [%d %d] after no-key poll, want [0 0]", cnt[0], cnt[1])
	}

	// KR_BUF[0] must be 0 (nothing written).
	buf := mac.Read(symAddr(t, mac, "KR_BUF"), 1)
	if buf[0] != 0 {
		t.Errorf("KR_BUF[0] = %d after no-key poll, want 0", buf[0])
	}

	// Queue must still be empty.
	if n := mac.PendingKeys(); n != 0 {
		t.Errorf("PendingKeys() = %d, want 0 (nothing was injected)", n)
	}
}
