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

// TestPlanAll: Plan(nil) yields every file in manifest order, each spec carrying
// the cdn path, the cdn host, and the file's pinned hash.
func TestPlanAll(t *testing.T) {
	m := RPiFirmware
	specs, err := m.Plan(nil)
	if err != nil {
		t.Fatalf("Plan(nil): %v", err)
	}
	if len(specs) != len(m.Files) {
		t.Fatalf("Plan(nil) len = %d, want %d", len(specs), len(m.Files))
	}
	for i, s := range specs {
		f := m.Files[i]
		if s.Name != f.Name {
			t.Errorf("spec %d name = %q, want %q", i, s.Name, f.Name)
		}
		if s.Host != GithubRawHost {
			t.Errorf("spec %d host = %q, want %q", i, s.Host, GithubRawHost)
		}
		if want := m.PathFor(f); s.Path != want {
			t.Errorf("spec %d path = %q, want %q", i, s.Path, want)
		}
		if s.SHA256 != f.SHA256 {
			t.Errorf("spec %d sha256 = %x, want %x", i, s.SHA256, f.SHA256)
		}
	}
}

// TestPlanSubset: a selection picks a subset, in the given order.
func TestPlanSubset(t *testing.T) {
	m := RPiFirmware
	specs, err := m.Plan([]int{3, 2}) // start4.elf, then start.elf
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(specs) != 2 || specs[0].Name != "start4.elf" || specs[1].Name != "start.elf" {
		t.Fatalf("Plan([3,2]) = %v, want [start4.elf start.elf]", names(specs))
	}
	if want := "/raspberrypi/firmware/" + m.SHA + "/boot/start4.elf"; specs[0].Path != want {
		t.Errorf("spec 0 path = %q, want %q", specs[0].Path, want)
	}
}

// TestPlanOutOfRange: an out-of-range index errors (does not panic).
func TestPlanOutOfRange(t *testing.T) {
	if _, err := RPiFirmware.Plan([]int{99}); err == nil {
		t.Error("Plan([99]) = nil error, want out-of-range error")
	}
}

// TestSelectByName resolves names to indices in order, and errors on an unknown.
func TestSelectByName(t *testing.T) {
	m := RPiFirmware
	got, err := m.SelectByName([]string{"start4.elf", "fixup.dat"})
	if err != nil {
		t.Fatalf("SelectByName: %v", err)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("SelectByName = %v, want [3 4]", got)
	}
	if _, err := m.SelectByName([]string{"no-such-file"}); err == nil {
		t.Error("SelectByName(unknown) = nil error, want error")
	}
	// Compose: SelectByName -> Plan yields the named files in order.
	sel, _ := m.SelectByName([]string{"bootcode.bin", "start.elf"})
	specs, err := m.Plan(sel)
	if err != nil {
		t.Fatalf("Plan(sel): %v", err)
	}
	if names(specs)[0] != "bootcode.bin" || names(specs)[1] != "start.elf" {
		t.Errorf("composed plan = %v, want [bootcode.bin start.elf]", names(specs))
	}
}

func names(specs []FetchSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
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
			// The Pi firmware blobs are multi-MB external files (Pete's spectrum4
			// checkout), NOT committed to this repo — analogous to the proprietary
			// captures. Absence is the normal CI state, but it must be an EXPLICIT
			// skip gated on SKIP_PRIVATE_TESTS, never a silent one (i253). The
			// always-on byte-for-byte gate is z80/fw_source_test.go.
			if os.Getenv("SKIP_PRIVATE_TESTS") == "true" {
				t.Skipf("SKIP_PRIVATE_TESTS: firmware blobs absent — %s", f.Name)
			}
			t.Fatalf("firmware blobs not present (%s) — fetch them (Pete's spectrum4 checkout) or set SKIP_PRIVATE_TESTS=true", f.Name)
		}
		if got := sha256.Sum256(data); got != f.SHA256 {
			t.Errorf("%s: pinned SHA-256 %x != actual %x", f.Name, f.SHA256, got)
		}
		if len(data) != f.Size {
			t.Errorf("%s: manifest size %d != actual %d", f.Name, f.Size, len(data))
		}
	}
}
