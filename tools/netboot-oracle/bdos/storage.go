// storage.go — the Go authority for the i114c storage-class mechanism
// (docs/specs/netboot-storage-manifest-design.md §6.5, decision 5).
//
// A fetched/pushed object is stored as one of two classes chosen by an
// explicit namespace prefix, never inferred from the bytes:
//
//   - FlatFile (default) — any name with no special prefix; stored as a
//     flat-file archive, never bootable.
//   - DiskRecord — names with the "trinity-sam-disks/" prefix; stored as a
//     raw Trinity SD record.  Before committing, the server validates that the
//     image has size exactly RecordSize, and rejects on mismatch — corrupt
//     disk-records cannot accumulate.  The "trinity-sam-disks/" prefix is the
//     intent declaration; size is the structural check.  Which DOS (if any) is
//     formatted INSIDE the .mgt is one level deeper and irrelevant to Trinity.
//
// This is the host-verifiable authority for the Z80 bdos_validate_disk_record
// routine (src/netboot/bdos_seam.asm).
package bdos

import (
	"fmt"
)

// RecordSize is the exact byte length of one Trinity SD record.  A record is
// 80 tracks × 10 sectors × 512 bytes = 819,200 bytes — the complete `.mgt`
// image (docs/specs/trinity-record-detection-design.md §4.1).
const RecordSize = 819_200

// BDOSStampOffset is the byte offset of the 4-byte "BDOS" stamp within the
// first sector (sector 0) of a B-DOS-formatted record.  Identical to
// bdBDOSStampOffset in z80/bdos_store.go and to the offset bdos_inspect_record
// reads in bdos_seam.asm (ld hl, BD_READ_BUF+232).
const BDOSStampOffset = 232

// diskRecordPrefix is the TFTP-path prefix that declares "store as a raw SAM
// disk record."  The declaration is in the filename, not the content —
// explicit over implicit (design §6.5).
const diskRecordPrefix = "trinity-sam-disks/"

// StorageClass is the storage class chosen for a fetched/pushed object.
type StorageClass int

const (
	// FlatFile is the default class: stored as a flat-file archive, never bootable.
	FlatFile StorageClass = iota
	// DiskRecord is the SAM-disk-record class: validated and stored as a raw
	// Trinity SD record (size exactly RecordSize, BDOS stamp at byte 232).
	DiskRecord
)

// Classify parses a TFTP filename and returns its storage class and the
// internal storage name (the remainder after stripping any class prefix).
//
// "trinity-sam-disks/X" → DiskRecord, "X".
// Any other name → FlatFile, the name unchanged.
func Classify(name string) (class StorageClass, internalName string) {
	if len(name) > len(diskRecordPrefix) && name[:len(diskRecordPrefix)] == diskRecordPrefix {
		return DiskRecord, name[len(diskRecordPrefix):]
	}
	return FlatFile, name
}

// ValidateDiskRecord checks that the given image satisfies the DiskRecord
// structural contract:
//
//   - size == RecordSize (exactly 819,200 bytes)
//
// Size is the whole contract: a Trinity record is exactly 819,200 bytes, and any
// full SAM disk image (B-DOS, SAMDOS, a game, our build-disk output) is a valid
// bootable record — it does NOT need B-DOS installed on it. The "BDOS"@232 stamp
// is B-DOS's own catalog/format signature (InspectRecord / bdos_inspect_record
// still read it to IDENTIFY B-DOS disks for the overwrite-safety show-name gate),
// NOT a boot or selection requirement: booting a record executes the disk's own
// boot sector and never checks +232. Validating the stamp here wrongly rejected
// every bootable non-B-DOS-formatted disk (including all of ours). (Pete,
// 2026-06-21 + 2026-06-29.)
//
// Returns nil on success, or a descriptive error identifying the failure.
func ValidateDiskRecord(size int, firstSector []byte) error {
	if size != RecordSize {
		return fmt.Errorf("disk record: wrong size %d, want exactly %d (80 tracks × 10 sectors × 512 bytes)",
			size, RecordSize)
	}
	return nil
}
