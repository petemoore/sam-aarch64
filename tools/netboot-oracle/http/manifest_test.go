package http

import (
	"crypto/sha256"
	"os"
	"testing"
)

// TestRPiFirmwareManifest sanity-checks the reference manifest's shape: the pin,
// the six Tupfile files in order, and that PathFor composes the cdn path.
func TestRPiFirmwareManifest(t *testing.T) {
	m := RPiFirmware
	if m.Owner != "raspberrypi" || m.Repo != "firmware" {
		t.Errorf("owner/repo = %q/%q", m.Owner, m.Repo)
	}
	if m.SHA != "a43df3a002f60c4c2243a416d045eb5937585e8b" {
		t.Errorf("SHA = %q", m.SHA)
	}
	wantNames := []string{"LICENCE.broadcom", "bootcode.bin", "start.elf", "start4.elf", "fixup.dat", "fixup4.dat"}
	if len(m.Files) != len(wantNames) {
		t.Fatalf("len(Files) = %d, want %d", len(m.Files), len(wantNames))
	}
	for i, f := range m.Files {
		if f.Name != wantNames[i] {
			t.Errorf("file %d name = %q, want %q", i, f.Name, wantNames[i])
		}
		if f.Path != "boot/"+f.Name {
			t.Errorf("file %d path = %q, want boot/%s", i, f.Path, f.Name)
		}
		if want := "/raspberrypi/firmware/" + m.SHA + "/boot/" + f.Name; m.PathFor(f) != want {
			t.Errorf("file %d PathFor = %q, want %q", i, m.PathFor(f), want)
		}
		if f.SHA256 == ([32]byte{}) {
			t.Errorf("file %d has a zero SHA-256", i)
		}
	}
}

// TestManifestHashesMatchLocalFiles verifies the pinned hashes are the real
// content hashes — by re-hashing the actual firmware files when they are present
// (Pete's spectrum4 checkout). This is a local-only dev guard: the multi-MB
// blobs are NOT in this repo, so the test skips when they are absent (e.g. CI).
// The byte-for-byte Z80<->Go check (z80/fw_source_test.go) is the always-on gate.
func TestManifestHashesMatchLocalFiles(t *testing.T) {
	const dir = "/home/pmoore/git/spectrum4/src/spectrum4/firmware/"
	for _, f := range RPiFirmware.Files {
		data, err := os.ReadFile(dir + f.Name)
		if err != nil {
			t.Skipf("firmware files not present (%s); skipping local-file hash cross-check", f.Name)
		}
		if got := sha256.Sum256(data); got != f.SHA256 {
			t.Errorf("%s: pinned SHA-256 %x != actual %x", f.Name, f.SHA256, got)
		}
		if len(data) != f.Size {
			t.Errorf("%s: manifest size %d != actual %d", f.Name, f.Size, len(data))
		}
	}
}
