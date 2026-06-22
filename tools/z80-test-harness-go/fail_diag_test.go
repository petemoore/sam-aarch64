// fail_diag_test.go — turn a boot self-test FAIL banner into a human-readable
// diagnostic, so a failing assertion is identifiable without a disassembler
// (item i22).
//
// A failed boot self-test halts via `fail` (src/assembler.asm), emitting
//
//	FAIL<tag><pc>\n
//
// on the printer channel, where <tag> is two hex digits of LAST_FAIL_TAG and
// <pc> is four hex digits of LAST_FAIL_PC.  The four inline-literal assert
// helpers record their caller's site into LAST_FAIL_PC on the fail path (via
// fail_at_bc / fail_at_ret), so a non-zero <pc> names the failing call site;
// a zero <pc> means a direct fail / fail_with_tag site, identified by <tag>.
//
// describeFailBanner resolves a non-zero <pc> to the nearest preceding symbol
// in build/assembler.sym, so the failure reads e.g.
//
//	tag=00 pc=B0F1 (near assert_eq32_de_hl_imm+0x47)
//
// for a resident-suite assertion.  Off-axis suites (the page-12 cluster /
// page-13 test_mem) run under an LMPR swap, so their recorded PC is a
// section-A logical address that build/assembler.sym cannot name; describe
// then reports the raw PC, still uniquely identifying the site.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseFailBanner extracts the tag and call-site PC from a printer capture.
// ok is true iff the capture begins with the "FAIL" marker.  The new banner
// is FAIL<tag2><pc4>; a shorter body (e.g. an older FAIL<tag2> form) parses
// with pc=0.
func parseFailBanner(printer string) (tag string, pc uint16, ok bool) {
	if !strings.HasPrefix(printer, "FAIL") {
		return "", 0, false
	}
	body := strings.TrimSpace(strings.TrimPrefix(printer, "FAIL"))
	switch {
	case len(body) >= 6:
		tag = body[:2]
		if v, err := strconv.ParseUint(body[2:6], 16, 16); err == nil {
			pc = uint16(v)
		}
	case len(body) >= 2:
		tag = body[:2]
	default:
		tag = body
	}
	return tag, pc, true
}

// describeFailBanner renders a FAIL banner as a one-line diagnostic, resolving
// a non-zero call-site PC against the symbol table at symPath (best effort: a
// missing/unparseable table, or an off-axis PC, degrades to the raw PC).
// Returns "" when printer is not a FAIL banner.
func describeFailBanner(printer, symPath string) string {
	tag, pc, ok := parseFailBanner(printer)
	if !ok {
		return ""
	}
	base := fmt.Sprintf("tag=%s pc=%04X", tag, pc)
	if pc == 0 {
		return base // direct fail / fail_with_tag site — the tag is the id
	}
	if syms, err := loadSAMSymbols(symPath); err == nil {
		if name, off, ok := resolveNearestSymbol(syms, pc); ok {
			return fmt.Sprintf("%s (near %s+0x%X)", base, name, off)
		}
	}
	return base + " (off-axis or unresolved — see the suite's own .sym)"
}
