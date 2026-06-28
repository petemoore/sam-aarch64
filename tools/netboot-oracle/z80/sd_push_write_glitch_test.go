// sd_push_write_glitch_test.go — the i339 emulation gate: a transient CMD24
// failure mid-stream must be retried IN-CORE (deselect / re-select / re-issue),
// never by clearing sdc_card_ready into a full-init-ladder-per-block cascade.
//
// THE HARDWARE INCIDENT THIS PINS (i323 campaign, 2026-07-02, real SAM + 64 GB
// SDHC): one CMD24 data-response reject at ~sector 759 of a 1600-sector push
// degraded the stream from ~74 ms/sector to ~15 s/sector for the rest of the run
// — the failure cleared sdc_card_ready, so EVERY later write ran the full init
// ladder whose &38 wake leaves the shared PIC settling for seconds (the i242
// finding) and near-deafened ENC RX. The in-binary meters proved the card itself
// was never busy (mtr_busy=0): the cascade was pure state-machine amplification.
// B-DOS 1.5t never re-inits mid-stream (init only at HDINIT) — the retry-without-
// ladder shape is the authority-faithful one (CLAUDE.md rule 8).
//
// The second half of the fix is HONEST COUNTING: a sector whose write failed
// after every retry must not be counted, so finalize answers 'E' (push failed),
// never a false 'D' over a hole in the record.
package z80_test

import (
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// glitchSectors builds the 3 distinct body sectors the glitch tests stream.
func glitchSectors() [][]byte {
	sectors := make([][]byte, 3)
	for s := range sectors {
		sec := make([]byte, 512)
		for i := range sec {
			sec[i] = byte((s*37 + i*7 + 0x11) & 0xFF)
		}
		sectors[s] = sec
	}
	return sectors
}

// driveGlitchPush injects discovery + the 3 body blocks + a finalize and boots
// sd_push to completion (the premature 3-sector finalize makes it RET cleanly).
func driveGlitchPush(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for s, sec := range glitchSectors() {
		enc.InjectRX(sdPushBlock(uint16(s), sec))
	}
	enc.InjectRX(sdPushFrame([]byte{'F'}))
	if _, err := mac.RunBoot("sd_push_main", z80h.Entry{StepCap: 60_000_000}); err != nil {
		t.Fatalf("RunBoot sd_push_main faulted: %v", err)
	}
}

// TestSDPushWriteGlitchRetriesWithoutReinit arms ONE transient data-response
// failure on the second body write (write order: 1 = the catalogue claim,
// 2 = body linear 0, 3 = body linear 1) and asserts the i339 fix shape:
//   - the glitched sector is retried and LANDS (all three body sectors present,
//     each committed exactly once — the rejected attempt is not a commit);
//   - NO mid-stream re-init happens (exactly one init-bearing write: the claim —
//     a cascade regression would make this 2);
//   - the finalize count is intact (all 3 sectors written -> the usual premature
//     'E' + record 1, exactly as the glitch-free TestSDPushLogic sees);
//   - data safety holds (nothing outside record 1's band but the claim).
func TestSDPushWriteGlitchRetriesWithoutReinit(t *testing.T) {
	mac, enc, sd, _ := setupSDPushMain(t, z80h.CSDForV2(0x001D59))
	sd.FailWriteResp(2, 1) // claim + body0 clean, then fail body1's first attempt

	driveGlitchPush(t, mac, enc)

	base := spCSDBase
	recLBA := func(linear int) uint32 { return uint32(base) + uint32(linear) } // record 1: 1600*(1-1)=0
	wantWrites := []uint32{1, recLBA(0), recLBA(1), recLBA(2)}
	if writes := sd.WrittenSectors(); !equalU32(writes, wantWrites) {
		t.Fatalf("CMD24 writes landed at %v, want %v — the glitched sector must be retried in-core and land exactly once", writes, wantWrites)
	}
	if n := sd.CMD24WritesAfterInit(); n != 1 {
		t.Fatalf("%d init-bearing writes, want exactly 1 (the claim) — a transient CMD24 failure must NOT trigger a mid-stream re-init (the i339 cascade)", n)
	}
	if got := countPayload(enc.TXFrames(), []byte{'E', 1, 0}); got != 1 {
		t.Errorf("finalize did not reply 'E'+record 1 — all 3 sectors (incl. the retried one) should be counted; payloads=%v", txPayloads(enc.TXFrames()))
	}
	if outside := sd.WrittenSectorsOutsideRecord(uint32(base), 1); len(outside) != 1 || outside[0] != 1 {
		t.Fatalf("writes outside record 1's band = %v, want exactly [1] (the claim)", outside)
	}
}

// TestSDPushPersistentWriteFailureNeverFalselyCounts arms failures for every
// write from body linear 1 onward (the retries exhaust) and asserts:
//   - the wire loop stays alive (every block still acked — the host must not
//     stall behind a dead card);
//   - only the sector that actually landed is counted ("ERR: 1 SECS"), so the
//     finalize is an honest 'E' — a failed write must never inflate the count
//     toward a false 'D';
//   - still no re-init storm (exactly one init-bearing write).
func TestSDPushPersistentWriteFailureNeverFalselyCounts(t *testing.T) {
	mac, enc, sd, rec := setupSDPushMain(t, z80h.CSDForV2(0x001D59))
	sd.FailWriteResp(2, 999) // claim + body0 clean, then every attempt fails

	driveGlitchPush(t, mac, enc)

	base := spCSDBase
	wantWrites := []uint32{1, uint32(base)} // the claim + body linear 0 only
	if writes := sd.WrittenSectors(); !equalU32(writes, wantWrites) {
		t.Fatalf("CMD24 writes landed at %v, want %v (body 1/2 fail every retry and must not land)", writes, wantWrites)
	}
	if got := countPayloadPrefix(enc.TXFrames(), []byte{'.'}); got != 3 {
		t.Errorf("%d block acks, want 3 — the serve loop must stay alive through write failures", got)
	}
	if got := countPayload(enc.TXFrames(), []byte{'E', 1, 0}); got != 1 {
		t.Errorf("finalize did not reply 'E'+record 1; payloads=%v", txPayloads(enc.TXFrames()))
	}
	if got := countPayloadPrefix(enc.TXFrames(), []byte{'D'}); got != 0 {
		t.Errorf("a push with lost sectors replied 'D' — the count must only reflect sectors actually written")
	}
	printed := string(rec.Chars())
	if want := "ERR: 1 SECS"; !strings.Contains(printed, want) {
		t.Errorf("screen output missing %q (only body 0 landed); printed=%q", want, printed)
	}
	if n := sd.CMD24WritesAfterInit(); n != 1 {
		t.Fatalf("%d init-bearing writes, want exactly 1 — persistent failures must fail fast, not re-init per block", n)
	}
}

