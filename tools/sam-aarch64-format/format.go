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

// Flags is the header flags word written by this version (§2.1). It records
// editor-region capabilities the reader needs to know before parsing the
// sidecar. Files written here carry FlagTaggedSidecar.
const Flags uint16 = FlagTaggedSidecar

// FlagTaggedSidecar marks an editor region whose comment-sidecar rows carry a
// leading kind u8 discriminator (0 = comment, 1 = blank-run), per the i78
// source-structure design. A reader of a file with this bit parses tagged
// rows; a reader of a file without the bit parses the legacy untagged shape, where
// every row is definitionally a comment. The discriminator is editor-region
// only — the assembler-facing region and the assembled binary are unchanged,
// so the byte-match invariant is unaffected.
const FlagTaggedSidecar uint16 = 1 << 0
