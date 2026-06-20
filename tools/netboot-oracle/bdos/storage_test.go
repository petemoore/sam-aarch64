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

// TestValidateDiskRecord pins the size+stamp validation contract.  Both
// conditions must hold independently, and each has its own distinct error
// message so the rejection reason is diagnosable.
func TestValidateDiskRecord(t *testing.T) {
	// A valid first sector: 512 bytes with the BDOS stamp at offset 232.
	validSector := make([]byte, 512)
	copy(validSector[BDOSStampOffset:], []byte("BDOS"))

	// Valid: exactly RecordSize and stamp present.
	if err := ValidateDiskRecord(RecordSize, validSector); err != nil {
		t.Errorf("valid record rejected: %v", err)
	}

	// Wrong size: one byte short.
	if err := ValidateDiskRecord(RecordSize-1, validSector); err == nil {
		t.Error("wrong-size record accepted, want rejection")
	}

	// Wrong size: one byte over.
	if err := ValidateDiskRecord(RecordSize+1, validSector); err == nil {
		t.Error("over-size record accepted, want rejection")
	}

	// Zero size.
	if err := ValidateDiskRecord(0, validSector); err == nil {
		t.Error("zero-size record accepted, want rejection")
	}

	// Correct size but stamp missing (all-zero first sector).
	noStamp := make([]byte, 512)
	if err := ValidateDiskRecord(RecordSize, noStamp); err == nil {
		t.Error("record with missing BDOS stamp accepted, want rejection")
	}

	// Correct size but stamp bytes wrong (4 bytes, not "BDOS").
	badStamp := make([]byte, 512)
	copy(badStamp[BDOSStampOffset:], []byte("XXXX"))
	if err := ValidateDiskRecord(RecordSize, badStamp); err == nil {
		t.Error("record with wrong stamp bytes accepted, want rejection")
	}

	// First sector too short to contain the stamp (235 bytes instead of >=236).
	shortSector := make([]byte, BDOSStampOffset+3)
	if err := ValidateDiskRecord(RecordSize, shortSector); err == nil {
		t.Error("record with too-short first sector accepted, want rejection")
	}

	// Stamp present but at wrong offset (231, one byte early) — stamp@232 absent.
	wrongOffset := make([]byte, 512)
	copy(wrongOffset[BDOSStampOffset-1:], []byte("BDOS"))
	if err := ValidateDiskRecord(RecordSize, wrongOffset); err == nil {
		t.Error("record with stamp at wrong offset accepted, want rejection")
	}
}
