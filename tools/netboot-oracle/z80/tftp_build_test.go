package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tftpBinPath = "../../../build/netboot_tftp_build.bin"
	tftpMapPath = "../../../build/netboot_tftp_build.map"
)

func loadTFTPMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tftpBinPath); err != nil {
		t.Fatalf("netboot TFTP builders binary not built (%s); run `make netboot-tftp-build`", tftpBinPath)
	}
	mac, err := z80h.Load(tftpBinPath, tftpMapPath)
	if err != nil {
		t.Fatalf("load TFTP builders: %v", err)
	}
	return mac
}

func tsym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

// TestZ80BuildOACKByteExact asserts the Z80 build_oack reproduces the captured
// server OACK byte-for-byte given the same negotiated option bytes — mirroring
// the Go TestServerOACKByteExact (oracle §2).
func TestZ80BuildOACKByteExact(t *testing.T) {
	mac := loadTFTPMachine(t)

	u, _ := frame.ParseUDP(golden.TFTPOack)
	captured := u.Payload // the full OACK payload, opcode 6 + options

	// The option bytes are everything after the 2-byte opcode.
	optBytes := captured[2:]
	mac.Write(tsym(t, mac, "OACK_OPTS"), optBytes)
	mac.WriteU16LE(tsym(t, mac, "OACK_OPTS_LEN"), uint16(len(optBytes)))

	res, err := mac.Call("build_oack")
	if err != nil {
		t.Fatalf("call build_oack: %v", err)
	}
	got := mac.Read(tsym(t, mac, "TBUF"), int(res.BC))
	if !bytes.Equal(got, captured) {
		t.Errorf("Z80 OACK != captured\n got %x\nwant %x", got, captured)
	}
}

// TestZ80BuildDATAByteExact asserts the Z80 build_data reproduces the captured
// DATA packet byte-for-byte (block 1, the negotiated blksize of data).
func TestZ80BuildDATAByteExact(t *testing.T) {
	mac := loadTFTPMachine(t)

	u, _ := frame.ParseUDP(golden.TFTPData)
	captured := u.Payload
	block, data, err := tftp.ParseDATA(captured)
	if err != nil {
		t.Fatalf("parse captured DATA: %v", err)
	}

	const dataStage = 0x5000
	mac.Write(dataStage, data)
	mac.Write(tsym(t, mac, "DATA_BLOCK"), []byte{byte(block >> 8), byte(block)})
	mac.WriteU16LE(tsym(t, mac, "DATA_PTR"), dataStage)
	mac.WriteU16LE(tsym(t, mac, "DATA_LEN"), uint16(len(data)))

	res, err := mac.Call("build_data")
	if err != nil {
		t.Fatalf("call build_data: %v", err)
	}
	got := mac.Read(tsym(t, mac, "TBUF"), int(res.BC))

	// Compare against the Go authority's BuildDATA (which is what the captured
	// DATA payload equals for this block).
	want := tftp.BuildDATA(block, data)
	if !bytes.Equal(got, want) {
		t.Errorf("Z80 DATA != Go authority\n got len %d\nwant len %d", len(got), len(want))
	}
	if !bytes.Equal(got, captured) {
		t.Errorf("Z80 DATA != captured DATA payload")
	}
}

// TestZ80BuildErrorByteExact asserts the Z80 build_error reproduces the
// captured "file not found" ERROR packet byte-for-byte (opcode 5, code 1,
// the NUL-terminated message) — the ERROR(1)-on-miss the server must emit
// and keep serving (oracle §2).
func TestZ80BuildErrorByteExact(t *testing.T) {
	mac := loadTFTPMachine(t)

	u, _ := frame.ParseUDP(golden.TFTPErrorNotFound)
	captured := u.Payload
	code, msg, err := tftp.ParseError(captured)
	if err != nil {
		t.Fatalf("parse captured ERROR: %v", err)
	}

	// Stage the message with its terminating NUL (the routine copies up to and
	// including the NUL).
	const msgStage = 0x5000
	mac.Write(msgStage, append([]byte(msg), 0))
	mac.Write(tsym(t, mac, "ERR_CODE"), []byte{byte(code >> 8), byte(code)})
	mac.WriteU16LE(tsym(t, mac, "ERR_MSG_PTR"), msgStage)

	res, err := mac.Call("build_error")
	if err != nil {
		t.Fatalf("call build_error: %v", err)
	}
	got := mac.Read(tsym(t, mac, "TBUF"), int(res.BC))

	want := tftp.BuildError(code, msg)
	if !bytes.Equal(got, want) {
		t.Errorf("Z80 ERROR != Go authority\n got %x\nwant %x", got, want)
	}
	if !bytes.Equal(got, captured) {
		t.Errorf("Z80 ERROR != captured ERROR payload\n got %x\nwant %x", got, captured)
	}
}
