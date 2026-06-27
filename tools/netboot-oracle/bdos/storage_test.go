package bdos

import (
	"testing"
)

// TestClassify pins the storage-class dispatch on the "trinity-sam-disks/"
// prefix (design §6.5, decision 5): the prefix is the ONLY class selector;
// content is never inspected at classify time.
func TestClassify(t *testing.T) {
	cases := []struct {
		name         string
		wantClass    StorageClass
		wantInternal string
	}{
		// Prefixed names → DiskRecord; internalName is the remainder.
		{"trinity-sam-disks/recovery", DiskRecord, "recovery"},
		{"trinity-sam-disks/myboot.mgt", DiskRecord, "myboot.mgt"},
		// Plain names → FlatFile; internalName is unchanged.
		{"kernel8.img", FlatFile, "kernel8.img"},
		{"config.txt", FlatFile, "config.txt"},
		// The prefix itself (no remainder) → FlatFile (remainder would be empty,
		// which is not a valid storage name; the prefix alone is treated as a plain
		// flat-file name).
		{"trinity-sam-disks/", FlatFile, "trinity-sam-disks/"},
		// A name that contains the prefix but not at position 0 → FlatFile.
		{"backup/trinity-sam-disks/x", FlatFile, "backup/trinity-sam-disks/x"},
	}
	for _, c := range cases {
		class, internal := Classify(c.name)
		if class != c.wantClass {
			t.Errorf("Classify(%q): class = %v, want %v", c.name, class, c.wantClass)
		}
		if internal != c.wantInternal {
			t.Errorf("Classify(%q): internalName = %q, want %q", c.name, internal, c.wantInternal)
		}
	}
}

// TestValidateDiskRecord pins the SIZE-ONLY validation contract: a Trinity
// record is exactly RecordSize bytes, and that is the whole structural check.
// A pushed .mgt does NOT need B-DOS installed on it — the DOS (if any) inside
// the .mgt is one level deeper and irrelevant to Trinity (Pete, 2026-06-21 +
// 2026-06-29). The "trinity-sam-disks/" prefix carries the intent; size
// confirms it. The old "BDOS"@232 gate wrongly rejected every bootable
// non-B-DOS-formatted disk (including all of ours); it has been removed.
func TestValidateDiskRecord(t *testing.T) {
	// Exactly RecordSize with NO stamp (the real-world case — e.g. a SAMDOS or
	// game .mgt like cj.mgt): ACCEPTED.
	noStamp := make([]byte, 512)
	if err := ValidateDiskRecord(RecordSize, noStamp); err != nil {
		t.Errorf("stampless full-size .mgt rejected (DOS-inside is irrelevant): %v", err)
	}

	// Exactly RecordSize with a non-"BDOS" stamp (some other DOS inside): ACCEPTED.
	otherDOS := make([]byte, 512)
	copy(otherDOS[BDOSStampOffset:], []byte("XXXX"))
	if err := ValidateDiskRecord(RecordSize, otherDOS); err != nil {
		t.Errorf("full-size .mgt with a non-BDOS DOS rejected: %v", err)
	}

	// A B-DOS-formatted disk (stamp present) is of course still valid.
	withStamp := make([]byte, 512)
	copy(withStamp[BDOSStampOffset:], []byte("BDOS"))
	if err := ValidateDiskRecord(RecordSize, withStamp); err != nil {
		t.Errorf("B-DOS-formatted record rejected: %v", err)
	}

	// firstSector is no longer inspected, so even a nil/short sector validates
	// when the size is right (size is the whole contract now).
	if err := ValidateDiskRecord(RecordSize, nil); err != nil {
		t.Errorf("nil first sector with correct size rejected: %v", err)
	}

	// Wrong sizes are still rejected (one short, one over, zero).
	for _, sz := range []int{RecordSize - 1, RecordSize + 1, 0} {
		if err := ValidateDiskRecord(sz, withStamp); err == nil {
			t.Errorf("wrong-size record (%d) accepted, want rejection", sz)
		}
	}
}
