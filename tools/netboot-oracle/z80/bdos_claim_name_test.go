// bdos_claim_name_test.go — i195: adversarial host-verification of the record-name
// derivation + sanitisation that names a pushed Trinity SD record from its WRQ
// filename (src/netboot/bdos_seam.asm bdos_build_claim_entry + bdos_claim_record).
//
// THE THREAT MODEL: the WRQ filename is ATTACKER-CONTROLLABLE (it arrives over the
// network from a TFTP `put` client), and the derived name is written into a SHARED
// user resource — the Trinity SD card's central record list, alongside other
// people's disks. A sanitisation gap (an illegal/garbage byte stored) or an
// out-of-slot write (overrunning the 16-byte entry) is a real corruption hazard.
// These tests drive the REAL Z80 bdos_build_claim_entry / bdos_claim_record under
// the koron-go/z80 harness for a battery of adversarial filenames and assert:
//
//   - the built 16-byte entry is a SAFE, LEGIBLE name: every byte in the legal
//     printable record-name charset 0x20..0x7E, first byte legal-nonzero-bit7-clear
//     (so the entry never reads as free/illegal), never longer than 16 bytes;
//   - bdos_claim_record's read-modify-write touches ONLY the claimed record's own
//     16-byte slot — every OTHER entry in the list sector is preserved byte-for-byte
//     (the shared-resource safety invariant), proven against the FULL 512 bytes the
//     Z80 wrote back (ListWrite.RawSector), not the harness's single-entry reconcile;
//   - a 200-char / traversal / all-illegal / empty name cannot overrun or corrupt
//     the sector;
//   - the Z80 result matches the Go authority serve.ClaimRecordName byte-for-byte.
//
// THE HONESTY LINE (CLAUDE.md §5): bdos_read_list_sector / bdos_write_list_sector
// reach the card via the harness hooks BD_HOOK_LISTREAD/LISTWRITE (161/162). There
// is no Z80 SPI driver for CARD-ABSOLUTE list-sector access — that is the
// hardware-gated §8 open item (the i145h CMD17/CMD24 model serves Colin's
// RECORD-CLAMPED seek path, not card-absolute list reads). So the list
// read-modify-write is exercised at the HOOK level here, NOT through a raw CMD24
// SPI command frame. The hook faithfully models the on-card RMW result, and the
// safety invariant is asserted against the actual 512 bytes the Z80 code wrote —
// but it is emulation-verified, not hardware-verified, and not the raw-SPI path.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/serve"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// claimNameScratch is a free RAM address in the &C000..&FFFF window (the same
// window raw_record_sink_test stages slices into) where an adversarial WRQ
// filename string is written before bdos_build_claim_entry reads it.
const claimNameScratch = 0xC000

// buildClaimEntry writes name (NUL-terminated) into scratch RAM, points
// BD_CLAIM_NAME_PTR at it, runs the REAL bdos_build_claim_entry, and returns the
// 16 bytes it deposited into BD_CLAIM_ENTRY.
func buildClaimEntry(t *testing.T, mac *z80h.Machine, name string) []byte {
	t.Helper()
	buf := append([]byte(name), 0) // NUL-terminated
	mac.Write(claimNameScratch, buf)
	mac.WriteU16LE(symAddr(t, mac, "BD_CLAIM_NAME_PTR"), claimNameScratch)
	if _, err := mac.CallEntry("bdos_build_claim_entry", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_build_claim_entry(%q): %v", name, err)
	}
	return mac.Read(symAddr(t, mac, "BD_CLAIM_ENTRY"), 16)
}

// assertSafeLegibleName fails unless the 16-byte entry is a safe, legible record
// name: exactly 16 bytes, every byte in 0x20..0x7E, and the first byte a legal
// non-zero char with bit 7 clear (so it never reads as free or write-protected).
func assertSafeLegibleName(t *testing.T, name string, entry []byte) {
	t.Helper()
	if len(entry) != 16 {
		t.Fatalf("%q: entry is %d bytes, want exactly 16", name, len(entry))
	}
	for i, b := range entry {
		if b < 0x20 || b > 0x7E {
			t.Errorf("%q: entry[%d] = %#02x is outside the legal printable charset 0x20..0x7E", name, i, b)
		}
	}
	// First byte: legal, non-zero after masking the WP bit, and bit 7 clear.
	if entry[0]&0x80 != 0 {
		t.Errorf("%q: entry[0] = %#02x has bit 7 set (would read as write-protected)", name, entry[0])
	}
	if entry[0]&0x7F == 0 {
		t.Errorf("%q: entry[0] masks to 0 — the record would read as FREE/unnamed", name)
	}
}

// TestClaimEntrySanitisesAdversarialNames drives bdos_build_claim_entry for a
// battery of adversarial WRQ filenames and asserts the built 16-byte entry is
// always safe + legible, AND matches the Go authority serve.ClaimRecordName.
func TestClaimEntrySanitisesAdversarialNames(t *testing.T) {
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Skipf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}

	cases := []struct {
		desc string
		name string
	}{
		{"plain short name", "disk"},
		{"dotted suffix dropped", "recovery.mgt"},
		{"prefix stripped", "trinity-sam-disks/games.mgt"},
		{"prefix + dotted suffix", "trinity-sam-disks/Shadebobs.mgt"},
		{"exactly 16 chars (no cap loss)", "abcdefghijklmnop"},
		{"17 chars truncated to 16", "abcdefghijklmnopq"},
		{"name longer than 16 with suffix", "averylongdiskimagename.mgt"},
		{"200-char garbage (no overrun)", longName(200)},
		{"path traversal -> leaf", "../../../etc/passwd"},
		{"prefix + traversal -> leaf", "trinity-sam-disks/../../secret"},
		{"deep path -> final segment", "a/b/c/d/finaldisk.img"},
		{"trailing slash -> empty -> default", "somedir/"},
		{"control bytes replaced", "ab\x01\x02\x1fcd"},
		{"high-bit bytes replaced", "x\x80\xffy"},
		{"DEL byte replaced", "p\x7fq"},
		{"all-illegal -> all underscores", "\x01\x02\x03\x04"},
		{"empty -> default name", ""},
		{"only a dotted suffix -> default", ".mgt"},
		{"only prefix -> default", "trinity-sam-disks/"},
		{"only separators -> default", "///"},
		{"space-only stem stays (legal 0x20)", "   .mgt"},
		{"tilde boundary kept", "name~end"},
		{"mixed legal punctuation kept", "disk-01_v2"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			entry := buildClaimEntry(t, mac, tc.name)
			assertSafeLegibleName(t, tc.name, entry)

			// Parity with the Go authority: the trimmed, space-padded entry must equal
			// ClaimRecordName(name) space-padded to 16 bytes.
			want := padTo16(serve.ClaimRecordName(tc.name))
			if !bytes.Equal(entry, want) {
				t.Errorf("%q: Z80 entry %q != Go authority %q", tc.name, entry, want)
			}
		})
	}
}

// TestClaimRecordPreservesNeighbours is the SAFETY-INVARIANT test: claiming a free
// record with an adversarial name must write ONLY that record's own 16-byte slot —
// every other entry in the containing list sector preserved byte-for-byte — and
// never overrun. It drives the REAL bdos_claim_record read-modify-write and asserts
// against the FULL 512 bytes the Z80 wrote back (ListWrite.RawSector), so the
// proof is unmediated by the harness's single-entry reconciliation.
func TestClaimRecordPreservesNeighbours(t *testing.T) {
	// Adversarial names spanning the full threat surface; the claimed record gets
	// each in turn, with named neighbours all around it in the same list sector.
	advNames := []string{
		"normaldisk.mgt",
		longName(200),
		"../../../etc/passwd",
		"\x01\x02\x03ctrl\x7f\x80stuff",
		"trinity-sam-disks/../../escape.mgt",
		"", // empty -> default
		"averyveryverylongnamewaytoolong.img",
	}

	// The free record to claim. Choosing 10 puts it mid-sector (list sector 1, entry
	// index 9) so it has named neighbours on BOTH sides — the strongest preservation
	// check. Records 1..32 all live in list sector 1.
	const claimRecord = 10
	const totalRecords = 32

	for _, name := range advNames {
		t.Run(nameForSub(name), func(t *testing.T) {
			mac, err := z80h.Load(serveBootBin, serveBootMap)
			if err != nil {
				t.Skipf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
			}
			store := z80h.NewBDOSStore()
			card := z80h.NewCardModel()

			// Name every record EXCEPT claimRecord with a distinctive, recognisable
			// 16-byte entry, so any stray byte written outside the claimed slot is
			// caught. claimRecord is left all-zero (free).
			for n := 1; n <= totalRecords; n++ {
				if n == claimRecord {
					continue
				}
				card.SetRecordEntry(n, neighbourEntry(n))
			}
			store.AttachCard(card)
			mac.AttachBDOS(store)

			// Capture the list sector exactly as it is BEFORE the claim — the byte-for-byte
			// baseline every non-claimed entry must still equal afterwards.
			priorSector := card.ListSector(1)

			// Drive the real claim: set the record to claim + the adversarial name ptr.
			mac.WriteU16LE(symAddr(t, mac, "BD_FREE_RECORD"), uint16(claimRecord))
			buf := append([]byte(name), 0)
			mac.Write(claimNameScratch, buf)
			mac.WriteU16LE(symAddr(t, mac, "BD_CLAIM_NAME_PTR"), claimNameScratch)
			if _, err := mac.Call("bdos_claim_record"); err != nil {
				t.Fatalf("bdos_claim_record(%q): %v", name, err)
			}

			// Exactly one list-sector write, to list sector 1.
			lws := store.ListWrites()
			if len(lws) != 1 {
				t.Fatalf("ListWrites() = %d, want 1 (one claim)", len(lws))
			}
			lw := lws[0]
			if lw.ListSector != 1 {
				t.Fatalf("claim wrote list sector %d, want 1", lw.ListSector)
			}
			if lw.ChangedRecord != claimRecord {
				t.Fatalf("claim changed record %d, want %d", lw.ChangedRecord, claimRecord)
			}

			// THE SAFETY INVARIANT, proven against the full 512 bytes the Z80 wrote:
			// only the claimed record's own 16-byte slot may differ from the prior
			// sector; every other byte is preserved verbatim.
			const entrySize = 16
			slotStart := (claimRecord - 1) % 32 * entrySize // entry index 9 -> offset 144
			got := lw.RawSector
			for i := 0; i < len(got); i++ {
				inClaimedSlot := i >= slotStart && i < slotStart+entrySize
				if inClaimedSlot {
					continue // the claimed slot is allowed (and expected) to change
				}
				if got[i] != priorSector[i] {
					t.Fatalf("OUT-OF-SLOT WRITE: byte %d changed (%#02x -> %#02x); the adversarial name %q wrote outside record %d's 16-byte slot [%d..%d) — a shared-resource corruption",
						i, priorSector[i], got[i], name, claimRecord, slotStart, slotStart+entrySize)
				}
			}

			// The claimed slot itself is a safe, legible name.
			slot := got[slotStart : slotStart+entrySize]
			assertSafeLegibleName(t, name, slot)

			// And the CardModel now reads the claimed record as NAMED (not free).
			if e := card.RecordEntry(claimRecord); e[0]&0x7F == 0 {
				t.Errorf("%q: record %d still reads FREE after the claim (entry[0]=%#02x)", name, claimRecord, e[0])
			}

			// Parity: the written slot equals the Go authority's name, space-padded.
			want := padTo16(serve.ClaimRecordName(name))
			if !bytes.Equal(slot, want) {
				t.Errorf("%q: claimed slot %q != Go authority %q", name, slot, want)
			}
		})
	}
}

// neighbourEntry builds a recognisable, fully-legal 16-byte entry for record n: a
// distinct printable pattern keyed by n, so any stray byte from an out-of-slot
// write is detected. Every byte is in 0x20..0x7E and the first is non-zero.
func neighbourEntry(n int) [16]byte {
	var e [16]byte
	for i := range e {
		// 0x21..0x7E range, deterministic per (n, i): a busy, non-space pattern.
		e[i] = byte(0x21 + (n*7+i*3)%(0x7E-0x21))
	}
	return e
}

// padTo16 right-pads s with spaces to 16 bytes (the on-card entry layout). s is
// already <= 16 (ClaimRecordName truncates), so no truncation is needed here.
func padTo16(s string) []byte {
	out := make([]byte, 16)
	for i := range out {
		if i < len(s) {
			out[i] = s[i]
		} else {
			out[i] = ' '
		}
	}
	return out
}

// longName returns a string of n 'A' characters — an over-length adversarial name.
func longName(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'A'
	}
	return string(b)
}

// nameForSub renders an adversarial name as a safe, short subtest label.
func nameForSub(name string) string {
	if name == "" {
		return "empty"
	}
	const max = 24
	out := make([]byte, 0, max)
	for i := 0; i < len(name) && len(out) < max; i++ {
		c := name[i]
		if c < 0x20 || c > 0x7E || c == '/' || c == ' ' {
			out = append(out, '.')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}
