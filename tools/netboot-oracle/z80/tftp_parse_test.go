package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpParseBinPath = "../../../build/netboot_tftp_parse.bin"
	tftpParseMapPath = "../../../build/netboot_tftp_parse.map"
)

func loadParseMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpParseBinPath); err != nil {
		t.Skipf("netboot TFTP parse binary not built (%s); run `make netboot-tftp-parse`", tftpParseBinPath)
	}
	mac, err := z80h.Load(tftpParseBinPath, tftpParseMapPath)
	if err != nil {
		t.Fatalf("load TFTP parse: %v", err)
	}
	return mac
}

func psym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

// readCStrAt reads a NUL-terminated string from the machine's memory at addr.
func readCStrAt(mac *z80h.Machine, addr uint16) string {
	var out []byte
	for i := 0; i < 600; i++ {
		b := mac.Read(addr+uint16(i), 1)[0]
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}

// runParse loads the RRQ payload into RRQ_IN, runs parse_request, and returns
// the decoded fields read back out of the routine's output block.
func runParse(t *testing.T, mac *z80h.Machine, payload []byte) (ok bool, opcode uint16, filename, mode string, optCount int) {
	t.Helper()
	rrqIn := psym(t, mac, "RRQ_IN")
	mac.Write(rrqIn, payload)
	mac.WriteU16LE(psym(t, mac, "RRQ_IN_LEN"), uint16(len(payload)))

	if _, err := mac.Call("parse_request"); err != nil {
		t.Fatalf("call parse_request: %v", err)
	}

	ok = mac.Read(psym(t, mac, "PARSE_OK"), 1)[0] == 1
	ob := mac.Read(psym(t, mac, "PARSE_OPCODE"), 2)
	opcode = binary.BigEndian.Uint16(ob) // stored big-endian as on the wire
	if ok {
		fnPtr := binary.LittleEndian.Uint16(mac.Read(psym(t, mac, "PARSE_FILENAME"), 2))
		filename = readCStrAt(mac, fnPtr)
		modePtr := binary.LittleEndian.Uint16(mac.Read(psym(t, mac, "PARSE_MODE"), 2))
		mode = readCStrAt(mac, modePtr)
		optCount = int(binary.LittleEndian.Uint16(mac.Read(psym(t, mac, "PARSE_OPT_COUNT"), 2)))
	}
	return
}

// TestZ80ParseRequestMatchesGo asserts the Z80 parse_request decodes the same
// opcode/filename/mode/option-count the Go authority ParseRequest does, for each
// captured RRQ (a root request and the serial-subdir probe). The wire bytes are
// already NUL-terminated, so the routine returns pointers into the buffer.
func TestZ80ParseRequestMatchesGo(t *testing.T) {
	mac := loadParseMachine(t)

	cases := []struct {
		name string
		raw  []byte
	}{
		{"TFTPRrqSerial", golden.TFTPRrqSerial},
		{"TFTPRrqRoot1024", golden.TFTPRrqRoot1024},
		{"TFTPRrqRoot1468", golden.TFTPRrqRoot1468},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, _ := frame.ParseUDP(c.raw)
			want, err := tftp.ParseRequest(u.Payload)
			if err != nil {
				t.Fatalf("Go ParseRequest: %v", err)
			}

			ok, opcode, filename, mode, optCount := runParse(t, mac, u.Payload)
			if !ok {
				t.Fatalf("Z80 PARSE_OK = 0, want a valid RRQ")
			}
			if opcode != want.Opcode {
				t.Errorf("opcode = %d, want %d", opcode, want.Opcode)
			}
			if filename != want.Filename {
				t.Errorf("filename = %q, want %q", filename, want.Filename)
			}
			if mode != want.Mode {
				t.Errorf("mode = %q, want %q", mode, want.Mode)
			}
			if optCount != len(want.Options) {
				t.Errorf("option count = %d, want %d", optCount, len(want.Options))
			}
		})
	}
}

// TestZ80ParseRequestRejectsBad asserts parse_request sets PARSE_OK=0 for a
// non-RRQ opcode and for a too-short payload (mirroring the Go error returns).
func TestZ80ParseRequestRejectsBad(t *testing.T) {
	mac := loadParseMachine(t)

	// Wrong opcode (DATA = 3): Go ParseRequest returns an error.
	ok, _, _, _, _ := runParse(t, mac, []byte{0, 3, 'x', 0, 'o', 0})
	if ok {
		t.Errorf("PARSE_OK = 1 for a DATA opcode, want 0")
	}

	// Too short (1 byte): Go Opcode returns 0, ParseRequest errors.
	ok, _, _, _, _ = runParse(t, mac, []byte{0})
	if ok {
		t.Errorf("PARSE_OK = 1 for a 1-byte payload, want 0")
	}

	// Missing mode NUL: filename present, mode runs to the end with no terminator.
	ok, _, _, _, _ = runParse(t, mac, []byte{0, 1, 'f', 0, 'o', 'c', 't', 'e', 't'})
	if ok {
		t.Errorf("PARSE_OK = 1 for an unterminated mode, want 0")
	}
}

// storeEntry encodes one flat-store entry: name, NUL, 4-byte little-endian size.
func storeEntry(name string, size uint32) []byte {
	b := append([]byte(name), 0)
	var s [4]byte
	binary.LittleEndian.PutUint32(s[:], size)
	return append(b, s[:]...)
}

// runResolve loads a NUL-terminated filename and the flat store, runs resolve,
// and returns the action byte and the 4-byte LE size.
func runResolve(t *testing.T, mac *z80h.Machine, filename string, store []byte) (action uint8, size uint32) {
	t.Helper()
	const nameStage = 0x5000
	mac.Write(nameStage, append([]byte(filename), 0))
	mac.WriteU16LE(psym(t, mac, "RESOLVE_NAME_PTR"), nameStage)
	mac.Write(psym(t, mac, "STORE"), store)

	if _, err := mac.Call("resolve"); err != nil {
		t.Fatalf("call resolve: %v", err)
	}
	action = mac.Read(psym(t, mac, "RESOLVE_ACTION"), 1)[0]
	size = binary.LittleEndian.Uint32(mac.Read(psym(t, mac, "RESOLVE_SIZE"), 4))
	return
}

// TestZ80ResolveMatchesGo pins the Z80 resolve against the Go authority's
// mandatory rules (oracle §2-§3): a flat-root hit -> OACK with the real size;
// a serial-subdir prefix -> ERROR404; every miss -> ERROR404 (and the routine
// is pure, so the server stays alive). The store and filenames mirror the Go
// TestServerResolveBehaviour.
func TestZ80ResolveMatchesGo(t *testing.T) {
	mac := loadParseMachine(t)

	store := bytes.Join([][]byte{
		storeEntry("config.txt", 1591),
		storeEntry("start4.elf", 2250656),
		storeEntry("kernel8.img", 1000),
		{0}, // empty-name terminator
	}, nil)

	goStore := tftp.MapStore{"config.txt": 1591, "start4.elf": 2250656, "kernel8.img": 1000}

	check := func(name string) {
		t.Helper()
		wantAct, wantSize := tftp.Resolve(goStore, name)
		gotAct, gotSize := runResolve(t, mac, name, store)
		if uint8(wantAct) != gotAct {
			t.Errorf("%q: action = %d, want %d", name, gotAct, uint8(wantAct))
		}
		if wantAct == tftp.ActionOACK && uint64(gotSize) != wantSize {
			t.Errorf("%q: size = %d, want %d", name, gotSize, wantSize)
		}
	}

	// A flat-root hit serves via OACK with the real size.
	check("config.txt")
	check("start4.elf")
	check("kernel8.img")

	// The serial-subdir probe from the capture must 404.
	su, _ := frame.ParseUDP(golden.TFTPRrqSerial)
	sreq, _ := tftp.ParseRequest(su.Payload)
	check(sreq.Filename)

	// Misses ERROR(1) — the boot ROM's optional-file probes.
	for _, miss := range []string{"recovery.elf", "pieeprom.sig", "dt-blob.bin", "armstub8-gic.bin"} {
		check(miss)
	}
}

// TestZ80SerialSubdirGateObservable makes the serial-subdir gate independently
// observable: each name below is present in the store *as a full key*, so the
// only way resolve can 404 it is if the serial-subdir gate fires *before* the
// store lookup. A name that is NOT a serial subdir falls through to the store
// and (being present) resolves to OACK. So OACK-vs-404 here is a direct probe
// of whether is_serial_subdir matched — and is pinned against the Go authority,
// which has the gate ahead of its own Lookup.
func TestZ80SerialSubdirGateObservable(t *testing.T) {
	mac := loadParseMachine(t)

	// Names whose full path is a store key, spanning the gate's boundaries.
	names := []string{
		"abcd/start4.elf",     // 4 hex  -> gate fires -> 404 despite the hit
		"00000000/start4.elf", // 8 hex (the capture) -> 404
		"abc/start4.elf",      // 3 hex (< 4) -> gate misses -> OACK (store hit)
		"abcg/start4.elf",     // non-hex char -> gate misses -> OACK (store hit)
		"start4.elf",          // no slash -> gate misses -> OACK (store hit)
	}
	var entries [][]byte
	goStore := tftp.MapStore{}
	for i, n := range names {
		entries = append(entries, storeEntry(n, uint32(100+i)))
		goStore[n] = uint64(100 + i)
	}
	entries = append(entries, []byte{0}) // empty-name terminator
	store := bytes.Join(entries, nil)

	for i, name := range names {
		wantAct, wantSize := tftp.Resolve(goStore, name)
		gotAct, gotSize := runResolve(t, mac, name, store)
		if uint8(wantAct) != gotAct {
			t.Errorf("%q: Z80 action = %d, Go action = %d", name, gotAct, uint8(wantAct))
		}
		if wantAct == tftp.ActionOACK && uint64(gotSize) != wantSize {
			t.Errorf("%q: size = %d, want %d (entry %d)", name, gotSize, wantSize, i)
		}
	}
}
