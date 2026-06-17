// asmparse_test.go — host-verification of src/asmparse.asm (i48c: the aarch64
// assembler-source parser; Brick B2a — mnemonic name→id lookup).
//
// Drives mnemonic_lookup under the flat-memory koron-go/z80 harness and asserts
// the returned ID matches the Go authority format.MnemonicID for every name in
// MnemonicTable, plus a batch of non-mnemonics (asserting not-found).
//
// Unlike asmlex_test.go (which transcribes the heavyweight frontend lexer), the
// authority here is the pure-stdlib leaf package sam-aarch64-format, so this
// test imports it directly: there is no transcription, and the Z80 table is
// compared straight against format.MnemonicID / format.MnemonicTable. The
// committed src/mnemonic_names.inc is itself generated from that same authority
// (tables-gen) and guarded against drift by `make tables-sync-check`.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

const (
	apBinPath = "../../../build/asmparse.bin"
	apMapPath = "../../../build/asmparse.map"
)

func loadAsmparse(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(apBinPath); err != nil {
		t.Skipf("asmparse binary not built (%s); run `make asmparse-z80`", apBinPath)
	}
	mac, err := z80h.Load(apBinPath, apMapPath)
	if err != nil {
		t.Fatalf("load asmparse: %v", err)
	}
	return mac
}

// lookupZ80 writes name into AP_NAMEBUF and runs mnemonic_lookup, returning the
// found flag (A==1) and the returned ID (HL, valid only when found).
func lookupZ80(t *testing.T, mac *z80h.Machine, name string) (found bool, id uint16) {
	t.Helper()
	buf, _ := mac.Sym("AP_NAMEBUF")
	mac.Write(buf, []byte(name))
	res, err := mac.CallEntry("mnemonic_lookup", z80h.Entry{HL: buf, BC: uint16(len(name))})
	if err != nil {
		t.Fatalf("mnemonic_lookup(%q): %v", name, err)
	}
	return res.A == 1, res.HL
}

// TestMnemonicLookupAll drives mnemonic_lookup over every name in the Go
// authority's MnemonicTable and asserts the Z80 returns its on-disk ID (the
// table index), cross-checked against format.MnemonicID.
func TestMnemonicLookupAll(t *testing.T) {
	mac := loadAsmparse(t)
	for wantID, name := range format.MnemonicTable {
		// Sanity: the authority agrees the index is the ID.
		if gotID, ok := format.MnemonicID(name); !ok || int(gotID) != wantID {
			t.Fatalf("authority inconsistency: MnemonicID(%q) = %d,%v want %d", name, gotID, ok, wantID)
		}
		found, id := lookupZ80(t, mac, name)
		if !found {
			t.Errorf("mnemonic_lookup(%q): not found, want id %d", name, wantID)
			continue
		}
		if int(id) != wantID {
			t.Errorf("mnemonic_lookup(%q): id = %d, want %d", name, id, wantID)
		}
	}
	t.Logf("verified %d mnemonics resolve to their MnemonicTable index", len(format.MnemonicTable))
}

// TestMnemonicLookupNonMnemonics asserts that strings which are NOT mnemonics
// resolve to not-found — including registers, near-misses (prefixes/suffixes of
// real mnemonics), the empty string, and identifiers the lexer would produce
// but that are not in the table. Each is cross-checked against the authority.
func TestMnemonicLookupNonMnemonics(t *testing.T) {
	mac := loadAsmparse(t)
	nonMnemonics := []string{
		"", "x0", "w5", "sp", "lr", "loop", "_start", "foo", ".text",
		"ad", "ad2", "addd", "adda", "nopp", "no", "sub2", "movx",
		"b.zz", "b.e", "b.eqq", "ccm", "csne", "csnegg", "ABCD", "Add",
	}
	for _, name := range nonMnemonics {
		// The authority must agree these are not mnemonics; if a future table
		// adds one, this test (not just the Z80) should be updated.
		if _, ok := format.MnemonicID(name); ok {
			t.Fatalf("test bug: %q is actually a mnemonic in the authority", name)
		}
		found, id := lookupZ80(t, mac, name)
		if found {
			t.Errorf("mnemonic_lookup(%q): found id %d, want not-found", name, id)
		}
	}
}
