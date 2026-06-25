package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Built artefact: the Makefile target netboot-build-tcp-segment.
const (
	tcpSegBinPath = "../../../build/netboot_build_tcp_segment.bin"
	tcpSegMapPath = "../../../build/netboot_build_tcp_segment.map"
)

func loadTCPSegMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tcpSegBinPath); err != nil {
		t.Fatalf("netboot routine binary not built (%s); run `make netboot-build-tcp-segment`", tcpSegBinPath)
	}
	mac, err := z80h.Load(tcpSegBinPath, tcpSegMapPath)
	if err != nil {
		t.Fatalf("load routine: %v", err)
	}
	return mac
}

// runBuildTCPSegment fills the parameter block from a tcp.Segment and runs
// build_tcp_segment, returning the emitted segment trimmed to the returned
// length.
func runBuildTCPSegment(t *testing.T, mac *z80h.Machine, s tcp.Segment) []byte {
	t.Helper()

	const payloadStage = 0x6000
	mac.Write(payloadStage, s.Payload)

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return a
	}
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	be32 := func(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

	mac.Write(sym("TPARAM_DST_MAC"), s.DstMAC[:])
	mac.Write(sym("TPARAM_SRC_MAC"), s.SrcMAC[:])
	mac.Write(sym("TPARAM_SRC_IP"), s.SrcIP[:])
	mac.Write(sym("TPARAM_DST_IP"), s.DstIP[:])
	mac.Write(sym("TPARAM_SRC_PORT"), be16(s.SrcPort))
	mac.Write(sym("TPARAM_DST_PORT"), be16(s.DstPort))
	mac.Write(sym("TPARAM_SEQ"), be32(s.Seq))
	mac.Write(sym("TPARAM_ACK"), be32(s.Ack))
	mac.Write(sym("TPARAM_FLAGS"), []byte{s.Flags})
	mac.Write(sym("TPARAM_WINDOW"), be16(s.Window))
	mac.WriteU16LE(sym("TPARAM_PAYLOAD_PTR"), payloadStage)
	mac.WriteU16LE(sym("TPARAM_PAYLOAD_LEN"), uint16(len(s.Payload)))

	res, err := mac.Call("build_tcp_segment")
	if err != nil {
		t.Fatalf("call build_tcp_segment: %v", err)
	}
	want := uint16(tcp.OffTCPPayload + len(s.Payload))
	if res.BC != want {
		t.Errorf("returned length BC = %d, want %d", res.BC, want)
	}
	return mac.Read(sym("TCP_PACKET"), int(res.BC))
}

// TestZ80BuildTCPSegmentMatchesGoAuthority asserts the Z80 build_tcp_segment
// emits exactly the bytes the Go authority tcp.BuildSegment emits, for a range
// of inputs covering empty / even / odd payloads and several flag combinations.
// The checksum is the load-bearing field: a byte-mismatch there fails this test.
func TestZ80BuildTCPSegmentMatchesGoAuthority(t *testing.T) {
	mac := loadTCPSegMachine(t)

	dstMAC := frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	srcMAC := frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	srcIP := frame.IPv4{192, 0, 2, 44}
	dstIP := frame.IPv4{192, 0, 2, 1}

	cases := []struct {
		name string
		seg  tcp.Segment
	}{
		{"syn-no-payload", tcp.Segment{
			SrcPort: 0xC000, DstPort: 80, Seq: 0x11223344, Ack: 0,
			Flags: tcp.FlagSYN, Window: 5760,
		}},
		{"ack-no-payload", tcp.Segment{
			SrcPort: 0xC000, DstPort: 80, Seq: 0x11223345, Ack: 0x99aabbcc,
			Flags: tcp.FlagACK, Window: 5840,
		}},
		{"psh-even-payload", tcp.Segment{
			SrcPort: 0xC000, DstPort: 80, Seq: 100, Ack: 200,
			Flags: tcp.FlagPSH | tcp.FlagACK, Window: 5840,
			Payload: []byte("GET / HTTP/1.0\r\n\r\n"), // 18 bytes (even)
		}},
		{"psh-odd-payload", tcp.Segment{
			SrcPort: 0xC000, DstPort: 80, Seq: 100, Ack: 200,
			Flags: tcp.FlagPSH | tcp.FlagACK, Window: 5840,
			Payload: []byte("GET /x HTTP/1.0\r\n\r\n"), // 19 bytes (odd)
		}},
		{"fin-ack", tcp.Segment{
			SrcPort: 0xC000, DstPort: 80, Seq: 0xfffffff0, Ack: 0x00000010,
			Flags: tcp.FlagFIN | tcp.FlagACK, Window: 0,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.seg
			in.DstMAC, in.SrcMAC, in.SrcIP, in.DstIP = dstMAC, srcMAC, srcIP, dstIP

			goSeg := tcp.BuildSegment(in)
			z80Seg := runBuildTCPSegment(t, mac, in)

			if !bytes.Equal(z80Seg, goSeg) {
				t.Errorf("Z80 segment != Go authority\n z80 %x\n  go %x", z80Seg, goSeg)
			}
		})
	}
}

// TestZ80BuildTCPSegmentChecksumValid asserts the Z80-built segment carries a
// self-consistent TCP pseudo-header checksum (and parses back) — an independent
// check that the Z80 checksum routine is right even if the Go authority were
// wrong (it isn't; cross-checked against Python in the PR).
func TestZ80BuildTCPSegmentChecksumValid(t *testing.T) {
	mac := loadTCPSegMachine(t)

	srcIP := frame.IPv4{192, 0, 2, 44}
	dstIP := frame.IPv4{192, 0, 2, 1}
	in := tcp.Segment{
		DstMAC:  frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		SrcMAC:  frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x44},
		SrcIP:   srcIP,
		DstIP:   dstIP,
		SrcPort: 0xC000, DstPort: 80, Seq: 0x01020304, Ack: 0x05060708,
		Flags: tcp.FlagPSH | tcp.FlagACK, Window: 5840,
		Payload: []byte("odd-length-payload!"), // 19 bytes (odd) — exercises the tail byte
	}
	seg := runBuildTCPSegment(t, mac, in)

	parsed, ok := tcp.ParseSegment(seg)
	if !ok {
		t.Fatal("Z80-built segment does not parse as IPv4/TCP")
	}
	if !bytes.Equal(parsed.Payload, in.Payload) {
		t.Errorf("parsed payload %q != input %q", parsed.Payload, in.Payload)
	}
	tcpLen := tcp.TCPHeaderLen + len(in.Payload)
	if !tcp.ChecksumValid(srcIP, dstIP, seg[tcp.OffL4:tcp.OffL4+tcpLen]) {
		t.Errorf("Z80-built TCP checksum is not self-consistent")
	}
}
