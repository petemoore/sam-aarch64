// coverage_exclusions.go — the i111 drift-proofing allowlist.
//
// # Why an allowlist
//
// The dead-code report's "reclaimable bytes" estimate counts a symbol range as
// recoverable only if NO byte of it was read during the run.  Some ranges are
// legitimately never-read yet are NOT dead code we want to delete:
//
//   - Pure data / sentinel EQUATES that resolve to an address but emit no bytes
//     in the image (e.g. SYMTAB_END_OF_CHAIN = &FFFF).  buildSymRanges already
//     drops symbols past the image end, but in-image equates that alias real
//     code would otherwise mislabel a range.
//   - Error-only / diagnostic strings and halt paths that the happy-path boot
//     self-tests never reach but that real failures DO use.  Counting these as
//     "reclaimable" would wrongly flag live error-handling as dead.
//
// # The convention (documented, deliberately simple)
//
// An entry is a symbol-NAME PREFIX plus a one-line reason.  A range whose
// symbol name starts with any listed prefix is marked `excl` in the report and
// is NOT added to the reclaimable total — but it is still SHOWN (so a genuinely
// dead entry hiding behind an over-broad prefix stays visible).  To suppress a
// future false positive, add a prefix + reason here; that is the whole
// mechanism — no separate marker-symbol build step, no map-file annotation.
//
// Keep this list SHORT and justified.  When in doubt, leave a symbol OUT: the
// i205 optimization pass reviews each candidate by hand anyway, so a false
// "reclaimable" is a prompt to look, not a delete order.
package main

// defaultExclusions is the standing allowlist.  Each prefix names a class of
// symbols whose unread-ness during the happy-path boot is expected and must not
// inflate the reclaimable estimate.
func defaultExclusions() *exclusionList {
	return &exclusionList{prefixes: []exclEntry{
		// Error/diagnostic message strings: printed only on a self-test or
		// assemble FAILURE, which the passing boot never triggers.
		{prefix: "msg_fail", reason: "error-only message string"},
		// Symbol-table sentinel equates: address constants, not emitted code.
		{prefix: "SYMTAB_", reason: "sentinel equate, not code"},
	}}
}
