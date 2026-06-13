// fw_source_test.go — the i100 host-verification of the cdn.githubraw.com
// commit-pinned path builder (src/netboot/fw_source.asm). It assembles the
// standalone module under the koron-go/z80 harness, runs fw_build_path, reads
// the FW_* config strings back out of the binary, feeds them to the Go authority
// http.GithubRawPath, and asserts the built FW_PATH matches byte-for-byte — the
// same one-source-of-truth pattern as http_get's HTTP_PATH/HTTP_HOST.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	fwSourceBinPath = "../../../build/netboot_fw_source.bin"
	fwSourceMapPath = "../../../build/netboot_fw_source.map"
)

func loadFWSource(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(fwSourceBinPath); err != nil {
		t.Skipf("fw_source binary not built (%s); run `make netboot-fw-source`", fwSourceBinPath)
	}
	mac, err := z80h.Load(fwSourceBinPath, fwSourceMapPath)
	if err != nil {
		t.Fatalf("load fw_source: %v", err)
	}
	return mac
}

// TestFWBuildPath: fw_build_path writes /<owner>/<repo>/<sha>/<path> into FW_PATH
// byte-for-byte the Go authority http.GithubRawPath, built from the same FW_*
// config strings read back out of the binary.
func TestFWBuildPath(t *testing.T) {
	mac := loadFWSource(t)

	owner := readCStr(t, mac, "FW_OWNER")
	repo := readCStr(t, mac, "FW_REPO")
	sha := readCStr(t, mac, "FW_SHA")
	repoPath := readCStr(t, mac, "FW_REPOPATH")

	res, err := mac.Call("fw_build_path")
	if err != nil {
		t.Fatalf("call fw_build_path: %v", err)
	}

	want := http.GithubRawPath(owner, repo, sha, repoPath)

	addr, err := mac.Sym("FW_PATH")
	if err != nil {
		t.Fatalf("%v", err)
	}
	raw := mac.Read(addr, 256)
	n := bytes.IndexByte(raw, 0)
	if n < 0 {
		t.Fatalf("FW_PATH not NUL-terminated")
	}
	got := string(raw[:n])

	if got != want {
		t.Errorf("FW_PATH = %q, want %q", got, want)
	}
	if int(res.BC) != len(want) {
		t.Errorf("fw_build_path returned BC=%d, want path length %d", res.BC, len(want))
	}
}

// TestFWManifest: the Z80 FW_MANIFEST table matches the Go authority
// http.RPiFirmware — each record's output name, in-repo path, and pinned SHA-256,
// reached via fw_manifest_entry by index. Iterating all files (and so reaching
// the last record) implicitly covers the count. The manifest's revision must
// also be the FW_SHA the path builder uses.
func TestFWManifest(t *testing.T) {
	mac := loadFWSource(t)

	if got := readCStr(t, mac, "FW_SHA"); got != http.RPiFirmware.SHA {
		t.Errorf("FW_SHA = %q, want the manifest revision %q", got, http.RPiFirmware.SHA)
	}

	for i, want := range http.RPiFirmware.Files {
		res, err := mac.CallEntry("fw_manifest_entry", z80h.Entry{BC: uint16(i)})
		if err != nil {
			t.Fatalf("fw_manifest_entry(%d): %v", i, err)
		}
		rec := res.BC
		// record layout: name ptr(2), path ptr(2), pinned SHA-256(32).
		namePtr := binary.LittleEndian.Uint16(mac.Read(rec, 2))
		pathPtr := binary.LittleEndian.Uint16(mac.Read(rec+2, 2))
		hash := mac.Read(rec+4, 32)

		if got := readCStrAt(mac, namePtr); got != want.Name {
			t.Errorf("file %d name = %q, want %q", i, got, want.Name)
		}
		if got := readCStrAt(mac, pathPtr); got != want.Path {
			t.Errorf("file %d path = %q, want %q", i, got, want.Path)
		}
		if !bytes.Equal(hash, want.SHA256[:]) {
			t.Errorf("file %d sha256 = %x, want %x", i, hash, want.SHA256)
		}
	}
}
