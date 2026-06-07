// Package format implements the M1 binary tokenised aarch64 source
// format. Section numbers (§N) in comments refer to
// https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-23-m1-binary-tokenised-format-design.md.
package format

// Magic is the 4-byte file header tag (§2).
var Magic = [4]byte{'S', 'A', '6', '4'}

// Version is the on-disk format version (§2). v2 is the instruction-overlay
// format (M8 / i39a): the KindInsnRun record replaces the KindLitInsts run
// and folds symbol/PC-bearing instructions into the same run via a sparse
// patch overlay. It is a clean break — the reader rejects any other version.
const Version uint16 = 2

// Flags is reserved in v1 and must be zero.
const Flags uint16 = 0
