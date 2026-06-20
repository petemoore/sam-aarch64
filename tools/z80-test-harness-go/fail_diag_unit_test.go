// fail_diag_unit_test.go — unit coverage for the i22 FAIL-banner diagnostics
// (parseFailBanner / loadSAMSymbols / resolveNearestSymbol / describeFailBanner).
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFailBanner(t *testing.T) {
	cases := []struct {
		in      string
		wantTag string
		wantPC  uint16
		wantOK  bool
	}{
		{"FAILee0000", "ee", 0x0000, true}, // direct fail_with_tag site
		{"FAIL00B0F1", "00", 0xB0F1, true}, // helper-routed site (tag 0, PC set)
		{"FAIL00B0F1\n", "00", 0xB0F1, true},
		{"FAILee", "ee", 0x0000, true}, // shorter (older) body → pc 0
		{"OK", "", 0x0000, false},
		{"", "", 0x0000, false},
	}
	for _, c := range cases {
		tag, pc, ok := parseFailBanner(c.in)
		if tag != c.wantTag || pc != c.wantPC || ok != c.wantOK {
			t.Errorf("parseFailBanner(%q) = (%q, %#04x, %v); want (%q, %#04x, %v)",
				c.in, tag, pc, ok, c.wantTag, c.wantPC, c.wantOK)
		}
	}
}

func TestResolveNearestSymbol(t *testing.T) {
	syms := map[string]uint32{
		"BUILD_TESTS": 1,      // build flag — below code origin, never matched
		"fail":        0xB05F, // code symbols
		"fail_at_bc":  0xB0A3,
		"assert_eq32": 0xB0AA,
	}
	cases := []struct {
		pc       uint16
		wantName string
		wantOff  uint32
		wantOK   bool
	}{
		{0xB0AA, "assert_eq32", 0x00, true}, // exactly on a label
		{0xB0B0, "assert_eq32", 0x06, true}, // a few bytes in
		{0xB0A4, "fail_at_bc", 0x01, true},
		{0xB060, "fail", 0x01, true},
		{0x0005, "", 0x00, false}, // off-axis logical PC — below origin, unresolved
		{0x7FFF, "", 0x00, false}, // just below code origin
	}
	for _, c := range cases {
		name, off, ok := resolveNearestSymbol(syms, c.pc)
		if name != c.wantName || off != c.wantOff || ok != c.wantOK {
			t.Errorf("resolveNearestSymbol(pc=%04X) = (%q, %#x, %v); want (%q, %#x, %v)",
				c.pc, name, off, ok, c.wantName, c.wantOff, c.wantOK)
		}
	}
}

// TestDescribeFailBannerWithRealSymbols smoke-tests the full pipeline against
// the real build/assembler.sym: a synthetic helper-routed banner whose PC sits
// inside fail_at_bc must resolve to that symbol.  Skipped if the symbol table
// has not been built.
func TestDescribeFailBannerWithRealSymbols(t *testing.T) {
	root := repoRoot(t)
	symPath := filepath.Join(root, "build", "assembler.sym")
	if _, err := os.Stat(symPath); err != nil {
		t.Skipf("prerequisite missing: %s (run `make assembler`)", symPath)
	}

	syms, err := loadSAMSymbols(symPath)
	if err != nil {
		t.Fatalf("loadSAMSymbols: %v", err)
	}
	addr, okSym := syms["fail_at_bc"]
	if !okSym {
		t.Fatalf("fail_at_bc not in %s — the i22 diagnostic stub is unwired", symPath)
	}
	if addr < codeOrigin {
		t.Fatalf("fail_at_bc resolved to %#x, below code origin %#x", addr, codeOrigin)
	}

	// A banner whose PC lands two bytes into fail_at_bc.
	banner := "FAIL00" + uppercaseHex4(uint16(addr)+2)
	got := describeFailBanner(banner, symPath)
	want := "(near fail_at_bc+0x2)"
	if !containsSub(got, want) {
		t.Errorf("describeFailBanner(%q) = %q; want it to contain %q", banner, got, want)
	}

	// A direct-site banner (pc=0000) must NOT claim a symbol.
	if d := describeFailBanner("FAILee0000", symPath); containsSub(d, "near ") {
		t.Errorf("direct-site banner resolved a symbol: %q", d)
	}
}

func uppercaseHex4(v uint16) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[v>>12&0xf], hex[v>>8&0xf], hex[v>>4&0xf], hex[v&0xf]})
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
