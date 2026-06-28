// http_main_provstart_test.go — host-verification of prov_start in
// src/netboot/http_main.asm: the per-file connection re-init (the Z80 port of
// Provisioner.start(i)).
//
// TestProvStartIdentity calls prov_start for i=0, 1, 2 and asserts that each
// call (a) sets the per-file connection identity — client port and ISS — to the
// values the Go authority Provisioner would use (BasePort+i, BaseISS+i*ISSStride),
// (b) resets the stateful fields (CONN_STATE, FETCH_PHASE, CONN_SERVER_MAC,
// BODY_HDR_DONE), (c) points HTTP_PATH_PTR at FW_PATH and fills FW_PATH with the
// cdn.githubraw.com path the Go authority Plan(nil)[i].Path returns, and (d) copies
// the 32-byte pinned hash from the manifest record into CONN_PINNED_HASH, matching
// http.RPiFirmware.Files[i].SHA256.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// provTestConfig holds the fixed test provisioner config written into the binary
// before calling prov_start.  These mirror Go ProvisionConfig.BasePort /
// BaseISS — the Go authority values for the per-file arithmetic.
const (
	fClientPrt uint16 = 0xC000
	fISS       uint32 = 0x11223344
)

// writeProvConfig writes BASE_PORT (BE 16-bit) and BASE_ISS (BE 32-bit) into the
// loaded http_main binary so prov_start's arithmetic uses the known test values.
func writeProvConfig(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	put := func(name string, data []byte) {
		t.Helper()
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q: %v", name, err)
		}
		mac.Write(a, data)
	}
	// BASE_PORT big-endian: high byte first.
	p := fClientPrt
	put("BASE_PORT", []byte{byte(p >> 8), byte(p)})
	// BASE_ISS big-endian: MSB first.
	s := fISS
	put("BASE_ISS", []byte{
		byte(s >> 24), byte(s >> 16), byte(s >> 8), byte(s),
	})
}

// readU16BE reads a 2-byte big-endian value from the named symbol.
func readU16BE(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("symbol %q: %v", name, err)
	}
	raw := mac.Read(a, 2)
	return uint16(raw[0])<<8 | uint16(raw[1])
}

// readU32BE reads a 4-byte big-endian value from the named symbol.
func readU32BE(t *testing.T, mac *z80h.Machine, name string) uint32 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("symbol %q: %v", name, err)
	}
	raw := mac.Read(a, 4)
	return uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
}

// TestProvStartIdentity: prov_start(i) sets the per-file TCP identity + resets
// the connection state + wires the path and pinned hash, all byte-for-byte the
// Go Provisioner.start(i) authority (http.RPiFirmware + http.ISSStride).
func TestProvStartIdentity(t *testing.T) {
	plan, err := http.RPiFirmware.Plan(nil)
	if err != nil {
		t.Fatalf("http.RPiFirmware.Plan(nil): %v", err)
	}

	for _, i := range []int{0, 1, 2} {
		i := i
		t.Run(string(rune('0'+i)), func(t *testing.T) {
			// Load a fresh machine for each i so state from previous calls does
			// not bleed across iterations.
			mac := loadHTTPMain(t)
			writeProvConfig(t, mac)

			// Call prov_start with BC = i.
			if _, err := mac.CallEntry("prov_start", z80h.Entry{BC: uint16(i)}); err != nil {
				t.Fatalf("prov_start(%d): %v", i, err)
			}

			// --- CONN_CLIENT_PORT == BasePort + i (big-endian) ---
			wantPort := uint16(fClientPrt) + uint16(i)
			if got := readU16BE(t, mac, "CONN_CLIENT_PORT"); got != wantPort {
				t.Errorf("CONN_CLIENT_PORT = 0x%04X, want 0x%04X (BasePort+%d)", got, wantPort, i)
			}

			// --- CONN_ISS == BaseISS + i*ISSStride (big-endian 32-bit) ---
			wantISS := uint32(fISS) + uint32(i)*uint32(http.ISSStride)
			if got := readU32BE(t, mac, "CONN_ISS"); got != wantISS {
				t.Errorf("CONN_ISS = 0x%08X, want 0x%08X (BaseISS+%d*ISSStride)", got, wantISS, i)
			}

			// --- FETCH_PHASE == 0 (PH_ARP) ---
			phAddr, err := mac.Sym("FETCH_PHASE")
			if err != nil {
				t.Fatalf("FETCH_PHASE: %v", err)
			}
			if got := mac.Read(phAddr, 1)[0]; got != 0 {
				t.Errorf("FETCH_PHASE = %d, want 0 (PH_ARP)", got)
			}

			// --- CONN_STATE == 0 ---
			stAddr, err := mac.Sym("CONN_STATE")
			if err != nil {
				t.Fatalf("CONN_STATE: %v", err)
			}
			if got := mac.Read(stAddr, 1)[0]; got != 0 {
				t.Errorf("CONN_STATE = %d, want 0", got)
			}

			// --- CONN_SERVER_MAC all-zero (6 bytes) ---
			macAddr, err := mac.Sym("CONN_SERVER_MAC")
			if err != nil {
				t.Fatalf("CONN_SERVER_MAC: %v", err)
			}
			if got := mac.Read(macAddr, 6); !bytes.Equal(got, make([]byte, 6)) {
				t.Errorf("CONN_SERVER_MAC = %x, want all-zero", got)
			}

			// --- BODY_HDR_DONE == 0 ---
			hdrAddr, err := mac.Sym("BODY_HDR_DONE")
			if err != nil {
				t.Fatalf("BODY_HDR_DONE: %v", err)
			}
			if got := mac.Read(hdrAddr, 1)[0]; got != 0 {
				t.Errorf("BODY_HDR_DONE = %d, want 0", got)
			}

			// --- HTTP_PATH_PTR -> FW_PATH ---
			httpPtrAddr, err := mac.Sym("HTTP_PATH_PTR")
			if err != nil {
				t.Fatalf("HTTP_PATH_PTR: %v", err)
			}
			fwPathAddr, err := mac.Sym("FW_PATH")
			if err != nil {
				t.Fatalf("FW_PATH: %v", err)
			}
			gotPtr := binary.LittleEndian.Uint16(mac.Read(httpPtrAddr, 2))
			if gotPtr != fwPathAddr {
				t.Errorf("HTTP_PATH_PTR = 0x%04X, want FW_PATH=0x%04X", gotPtr, fwPathAddr)
			}

			// --- FW_PATH NUL-terminated string == Go authority Plan(nil)[i].Path ---
			raw := mac.Read(fwPathAddr, 256)
			n := bytes.IndexByte(raw, 0)
			if n < 0 {
				t.Fatalf("FW_PATH not NUL-terminated")
			}
			gotPath := string(raw[:n])
			wantPath := plan[i].Path
			if gotPath != wantPath {
				t.Errorf("FW_PATH = %q, want %q", gotPath, wantPath)
			}

			// --- CONN_PINNED_HASH == http.RPiFirmware.Files[i].SHA256 (32 bytes) ---
			hashAddr, err := mac.Sym("CONN_PINNED_HASH")
			if err != nil {
				t.Fatalf("CONN_PINNED_HASH: %v", err)
			}
			gotHash := mac.Read(hashAddr, 32)
			wantHash := http.RPiFirmware.Files[i].SHA256[:]
			if !bytes.Equal(gotHash, wantHash) {
				t.Errorf("CONN_PINNED_HASH[%d] =\n  %x\nwant\n  %x", i, gotHash, wantHash)
			}
		})
	}
}
