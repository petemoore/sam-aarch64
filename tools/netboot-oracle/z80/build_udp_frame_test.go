// Package z80_test runs the SAM-side netboot Z80 routines under the flat-memory
// harness and asserts their output matches the netboot-oracle golden vectors
// byte-for-byte — the host-verifiable half of the Z80 port (plan §6.1).
//
// Each test mirrors an assertion the Go authority gets in
// tools/netboot-oracle/oracle_test.go, but exercises the *Z80* implementation:
// it is the proof the asm port reproduces the Go authority's bytes.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Built artefacts live in the repo-root build/ dir (the Makefile target
// netboot-build-udp-frame). Paths are relative to this package dir
// (tools/netboot-oracle/z80).
const (
	binPath = "../../../build/netboot_build_udp_frame.bin"
	mapPath = "../../../build/netboot_build_udp_frame.map"
)

// loadMachine loads the assembled routine, skipping the test if the artefact is
// absent (a principled skip: the build step has not run). CI builds it first,
// so in CI this never skips.
func loadMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("netboot routine binary not built (%s); run `make netboot-build-udp-frame`", binPath)
	}
	mac, err := z80h.Load(binPath, mapPath)
	if err != nil {
		t.Fatalf("load routine: %v", err)
	}
	return mac
}

// runBuildUDPFrame fills the parameter block from a frame.UDP and runs
// build_udp_frame, returning the emitted frame (the harness packet buffer
// trimmed to the returned length).
func runBuildUDPFrame(t *testing.T, mac *z80h.Machine, u frame.UDP) []byte {
	t.Helper()

	// Stage the payload in scratch RAM the routine can read.
	const payloadStage = 0x6000
	mac.Write(payloadStage, u.Payload)

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return a
	}

	mac.Write(sym("PARAM_DST_MAC"), u.DstMAC[:])
	mac.Write(sym("PARAM_SRC_MAC"), u.SrcMAC[:])
	mac.Write(sym("PARAM_SRC_IP"), u.SrcIP[:])
	mac.Write(sym("PARAM_DST_IP"), u.DstIP[:])
	// Ports are big-endian on the wire; the routine copies the two bytes
	// straight through, so stage them big-endian.
	mac.Write(sym("PARAM_SRC_PORT"), []byte{byte(u.SrcPort >> 8), byte(u.SrcPort)})
	mac.Write(sym("PARAM_DST_PORT"), []byte{byte(u.DstPort >> 8), byte(u.DstPort)})
	mac.WriteU16LE(sym("PARAM_PAYLOAD_PTR"), payloadStage)
	mac.WriteU16LE(sym("PARAM_PAYLOAD_LEN"), uint16(len(u.Payload)))

	res, err := mac.Call("build_udp_frame")
	if err != nil {
		t.Fatalf("call build_udp_frame: %v", err)
	}
	want := uint16(frame.HeaderLen + len(u.Payload))
	if res.BC != want {
		t.Errorf("returned length BC = %d, want %d", res.BC, want)
	}
	return mac.Read(sym("PACKET"), int(res.BC))
}

// TestZ80BuildUDPFrameMatchesGoAuthority asserts the Z80 build_udp_frame emits
// exactly the bytes the Go authority frame.BuildUDPFrame emits for the same
// inputs — the byte-for-byte port-fidelity check (memory feedback_go_is_
// encoding_authority: port the Go function, verify the bytes equal).
func TestZ80BuildUDPFrameMatchesGoAuthority(t *testing.T) {
	mac := loadMachine(t)

	// Drive both implementations with the captured RRQ frame's fields — the
	// same fixture TestBuildUDPFrameRoundTrips uses on the Go side.
	orig := golden.TFTPRrqRoot1024
	u, ok := frame.ParseUDP(orig)
	if !ok {
		t.Fatal("ParseUDP rejected the captured RRQ")
	}
	in := frame.UDP{
		DstMAC: u.DstMAC, SrcMAC: u.SrcMAC,
		SrcIP: u.SrcIP, DstIP: u.DstIP,
		SrcPort: u.SrcPort, DstPort: u.DstPort,
		Payload: u.Payload,
	}

	goFrame := frame.BuildUDPFrame(in)
	z80Frame := runBuildUDPFrame(t, mac, in)

	if !bytes.Equal(z80Frame, goFrame) {
		t.Errorf("Z80 frame != Go authority frame\n z80 %x\n  go %x", z80Frame, goFrame)
	}
}

// TestZ80BuildUDPFrameMatchesCapture asserts the Z80-built frame reproduces the
// captured RRQ frame on every field build_udp_frame owns: the Ethernet header,
// the IP addresses+proto, the UDP ports+length, the payload, and a self-
// consistent IP header checksum. (The capture's TTL/identification/flags are
// the Pi's; the builder fixes its own canonical values — exactly as the Go
// TestBuildUDPFrameRoundTrips compares.)
func TestZ80BuildUDPFrameMatchesCapture(t *testing.T) {
	mac := loadMachine(t)

	orig := golden.TFTPRrqRoot1024
	u, _ := frame.ParseUDP(orig)
	built := runBuildUDPFrame(t, mac, frame.UDP{
		DstMAC: u.DstMAC, SrcMAC: u.SrcMAC,
		SrcIP: u.SrcIP, DstIP: u.DstIP,
		SrcPort: u.SrcPort, DstPort: u.DstPort,
		Payload: u.Payload,
	})

	if !bytes.Equal(built[0:14], orig[0:14]) {
		t.Errorf("ethernet header differs\n built %x\n  orig %x", built[0:14], orig[0:14])
	}
	bu, ok := frame.ParseUDP(built)
	if !ok {
		t.Fatal("Z80-built frame does not parse as IPv4/UDP")
	}
	if bu.SrcIP != u.SrcIP || bu.DstIP != u.DstIP {
		t.Errorf("built IPs %v->%v != orig %v->%v", bu.SrcIP, bu.DstIP, u.SrcIP, u.DstIP)
	}
	if bu.SrcPort != u.SrcPort || bu.DstPort != u.DstPort {
		t.Errorf("built ports %d->%d != orig %d->%d", bu.SrcPort, bu.DstPort, u.SrcPort, u.DstPort)
	}
	if !bytes.Equal(bu.Payload, u.Payload) {
		t.Errorf("built payload differs from captured")
	}
	if !ipChecksumValid(built[14:34]) {
		t.Errorf("Z80-built IP header checksum is not self-consistent")
	}
	if built[frame.OffIPProto] != frame.ProtoUDP {
		t.Errorf("built IP proto = %#x, want UDP", built[frame.OffIPProto])
	}
}

// ipChecksumValid reports whether a 20-byte IP header's checksum verifies (the
// one's-complement word sum is 0xffff). Mirrors the Go oracle_test helper.
func ipChecksumValid(hdr []byte) bool {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return sum == 0xffff
}
