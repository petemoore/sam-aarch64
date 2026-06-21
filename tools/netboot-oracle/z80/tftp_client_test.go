package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpClientBinPath = "../../../build/netboot_tftp_client.bin"
	tftpClientMapPath = "../../../build/netboot_tftp_client.map"
)

func loadClientMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpClientBinPath); err != nil {
		t.Fatalf("netboot TFTP client binary not built (%s); run `make netboot-tftp-client`", tftpClientBinPath)
	}
	mac, err := z80h.Load(tftpClientBinPath, tftpClientMapPath)
	if err != nil {
		t.Fatalf("load TFTP client: %v", err)
	}
	return mac
}

func csym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

// optBytes serialises option pairs to the wire form "name\0value\0..." that the
// Z80 build_rrq frames verbatim (the same pre-formatted blob build_oack takes).
func optBytes(opts []tftp.Option) []byte {
	var b []byte
	for _, o := range opts {
		b = append(b, []byte(o.Name)...)
		b = append(b, 0)
		b = append(b, []byte(o.Value)...)
		b = append(b, 0)
	}
	return b
}

// runBuildRRQ stages the filename, mode, and pre-formatted option bytes, runs
// build_rrq, and returns the emitted RRQ payload.
func runBuildRRQ(t *testing.T, mac *z80h.Machine, filename, mode string, opts []tftp.Option) []byte {
	t.Helper()
	const fnStage, modeStage = 0x5000, 0x5200
	mac.Write(fnStage, append([]byte(filename), 0))
	mac.Write(modeStage, append([]byte(mode), 0))
	mac.WriteU16LE(csym(t, mac, "RRQ_FILENAME_PTR"), fnStage)
	mac.WriteU16LE(csym(t, mac, "RRQ_MODE_PTR"), modeStage)

	ob := optBytes(opts)
	mac.Write(csym(t, mac, "RRQ_OPTS"), ob)
	mac.WriteU16LE(csym(t, mac, "RRQ_OPTS_LEN"), uint16(len(ob)))

	res, err := mac.Call("build_rrq")
	if err != nil {
		t.Fatalf("call build_rrq: %v", err)
	}
	return mac.Read(csym(t, mac, "CRBUF"), int(res.BC))
}

// TestZ80BuildRRQByteExact asserts the Z80 build_rrq reproduces the captured
// Pi RRQ byte-for-byte given the same filename/mode/options — mirroring the Go
// TestRRQBuilderByteExact (this pins the on-wire byte order).
func TestZ80BuildRRQByteExact(t *testing.T) {
	mac := loadClientMachine(t)

	for _, name := range []string{"TFTPRrqRoot1024", "TFTPRrqRoot1468"} {
		raw := map[string][]byte{
			"TFTPRrqRoot1024": golden.TFTPRrqRoot1024,
			"TFTPRrqRoot1468": golden.TFTPRrqRoot1468,
		}[name]
		t.Run(name, func(t *testing.T) {
			u, _ := frame.ParseUDP(raw)
			captured := u.Payload
			req, err := tftp.ParseRequest(captured)
			if err != nil {
				t.Fatalf("parse captured RRQ: %v", err)
			}
			got := runBuildRRQ(t, mac, req.Filename, req.Mode, req.Options)

			// Both the captured bytes and the Go authority's rebuild.
			want := tftp.BuildRRQ(req.Filename, req.Mode, req.Options)
			if !bytes.Equal(got, want) {
				t.Errorf("Z80 RRQ != Go authority\n got %x\nwant %x", got, want)
			}
			if !bytes.Equal(got, captured) {
				t.Errorf("Z80 RRQ != captured payload\n got %x\nwant %x", got, captured)
			}
		})
	}
}

// TestZ80BuildRRQClientOptionSet asserts the client builds the settled option
// set (blksize=1428, tsize=0, timeout=2, windowsize=4) byte-for-byte as the Go
// BuildRRQ does for spectrum4.img — the option string the SAM emits on hardware
// (mirrors the Go TestRRQBuilderMatchesClientOptionSet).
func TestZ80BuildRRQClientOptionSet(t *testing.T) {
	mac := loadClientMachine(t)

	got := runBuildRRQ(t, mac, "spectrum4.img", "octet", tftp.ClientOptionSet)
	want := tftp.BuildRRQ("spectrum4.img", "octet", tftp.ClientOptionSet)
	if !bytes.Equal(got, want) {
		t.Errorf("Z80 client RRQ != Go authority\n got %x\nwant %x", got, want)
	}
}

// readCStrC reads a NUL-terminated string from the machine's memory at addr.
func readCStrC(mac *z80h.Machine, addr uint16) string {
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

// findOptionValue runs find_option for one option name against the parsed OACK
// (parse_oack must have run first). Returns the value string and whether found.
func findOptionValue(t *testing.T, mac *z80h.Machine, name string) (string, bool) {
	t.Helper()
	const nameStage = 0x5400
	mac.Write(nameStage, append([]byte(name), 0))
	mac.WriteU16LE(csym(t, mac, "FIND_NAME_PTR"), nameStage)
	if _, err := mac.Call("find_option"); err != nil {
		t.Fatalf("call find_option(%q): %v", name, err)
	}
	if mac.Read(csym(t, mac, "FIND_OK"), 1)[0] != 1 {
		return "", false
	}
	vp := binary.LittleEndian.Uint16(mac.Read(csym(t, mac, "FIND_VALUE_PTR"), 2))
	return readCStrC(mac, vp), true
}

// TestZ80ParseOACKMatchesGo asserts the Z80 parse_oack decodes the captured
// server OACK into the same negotiated blksize/tsize the Go ParseOACK does
// (oracle §2: tsize = the real size, echoed blksize) — mirroring TestOACKParse.
func TestZ80ParseOACKMatchesGo(t *testing.T) {
	mac := loadClientMachine(t)

	u, _ := frame.ParseUDP(golden.TFTPOack)
	captured := u.Payload

	goOpts, err := tftp.ParseOACK(captured)
	if err != nil {
		t.Fatalf("Go ParseOACK: %v", err)
	}

	oackIn := csym(t, mac, "OACK_IN")
	mac.Write(oackIn, captured)
	mac.WriteU16LE(csym(t, mac, "OACK_IN_LEN"), uint16(len(captured)))
	if _, err := mac.Call("parse_oack"); err != nil {
		t.Fatalf("call parse_oack: %v", err)
	}
	if mac.Read(csym(t, mac, "OACK_OK"), 1)[0] != 1 {
		t.Fatalf("Z80 OACK_OK = 0, want a valid OACK")
	}
	gotCount := int(binary.LittleEndian.Uint16(mac.Read(csym(t, mac, "OACK_OPT_COUNT"), 2)))
	if gotCount != len(goOpts) {
		t.Errorf("OACK option count = %d, want %d", gotCount, len(goOpts))
	}

	// Each option's value (read back via find_option) must match the Go parse.
	for _, o := range goOpts {
		got, ok := findOptionValue(t, mac, o.Name)
		if !ok {
			t.Errorf("Z80 find_option(%q) not found", o.Name)
			continue
		}
		if got != o.Value {
			t.Errorf("OACK %q = %q, want %q", o.Name, got, o.Value)
		}
	}

	// The two negotiated values the client acts on: blksize=1024, tsize != 0.
	bs, ok := findOptionValue(t, mac, "blksize")
	if !ok || bs != "1024" {
		t.Errorf("OACK blksize = %q (ok=%v), want 1024", bs, ok)
	}
	ts, ok := findOptionValue(t, mac, "tsize")
	if !ok {
		t.Fatal("OACK missing tsize")
	}
	if n, _ := strconv.Atoi(ts); n == 0 {
		t.Errorf("OACK tsize = %q, want a non-zero real size", ts)
	}

	// A missing option is reported as not-found.
	if _, ok := findOptionValue(t, mac, "no-such-option"); ok {
		t.Error("find_option(no-such-option) reported found")
	}
}

// TestZ80ParseOACKRejectsBad asserts parse_oack sets OACK_OK=0 for a non-OACK
// opcode and a too-short payload (matching the Go ParseOACK error returns).
func TestZ80ParseOACKRejectsBad(t *testing.T) {
	mac := loadClientMachine(t)

	check := func(payload []byte, wantOK bool) {
		t.Helper()
		mac.Write(csym(t, mac, "OACK_IN"), payload)
		mac.WriteU16LE(csym(t, mac, "OACK_IN_LEN"), uint16(len(payload)))
		if _, err := mac.Call("parse_oack"); err != nil {
			t.Fatalf("call parse_oack: %v", err)
		}
		got := mac.Read(csym(t, mac, "OACK_OK"), 1)[0] == 1
		if got != wantOK {
			t.Errorf("OACK_OK = %v for %x, want %v", got, payload, wantOK)
		}
	}

	check([]byte{0, 3, 'a', 0, 'b', 0}, false) // DATA opcode, not OACK
	check([]byte{0}, false)                    // too short
	check([]byte{0, 6}, true)                  // valid empty OACK (opcode only)
	check([]byte{0, 6, 'a', 0, 'b', 0}, true)  // one valid option pair
	check([]byte{0, 6, 'a', 0, 'b'}, false)    // value missing its NUL -> error
}
