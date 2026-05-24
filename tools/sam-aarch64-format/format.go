// Package format implements the M1 binary tokenised aarch64 source
// format. Section numbers (§N) in comments refer to
// docs/specs/2026-05-23-m1-binary-tokenised-format-design.md.
package format

// Magic is the 4-byte file header tag (§2).
var Magic = [4]byte{'S', 'A', '6', '4'}

// Version is the on-disk format version (§2). v1 is the only release.
const Version uint16 = 1

// Flags is reserved in v1 and must be zero.
const Flags uint16 = 0
