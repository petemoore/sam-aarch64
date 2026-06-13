package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	arpBinPath = "../../../build/netboot_build_arp_request.bin"
	arpMapPath = "../../../build/netboot_build_arp_request.map"
)

func loadARPMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(arpBinPath); err != nil {
		t.Skipf("netboot ARP-request binary not built (%s); run `make netboot-build-arp-request`", arpBinPath)
	}
	mac, err := z80h.Load(arpBinPath, arpMapPath)
	if err != nil {
		t.Fatalf("load ARP request: %v", err)
	}
	return mac
}

func asym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}

// TestZ80BuildARPRequestByteExact asserts the Z80 build_arp_request reproduces
// the Go authority's BuildARPRequest output byte-for-byte, for the same src
// MAC/IP and target IP — the 42-byte RFC 826 Ethernet ARP request the i82
// client broadcasts to learn the server's MAC (plan §5.1).
func TestZ80BuildARPRequestByteExact(t *testing.T) {
	mac := loadARPMachine(t)

	srcMAC := frame.MAC{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
	srcIP := frame.IPv4{192, 168, 50, 1}
	targetIP := frame.IPv4{192, 168, 50, 10}

	mac.Write(asym(t, mac, "ARP_SRC_MAC"), srcMAC[:])
	mac.Write(asym(t, mac, "ARP_SRC_IP"), srcIP[:])
	mac.Write(asym(t, mac, "ARP_TARGET_IP"), targetIP[:])

	res, err := mac.Call("build_arp_request")
	if err != nil {
		t.Fatalf("call build_arp_request: %v", err)
	}
	if int(res.BC) != frame.ARPFrameLen {
		t.Errorf("BC = %d, want frame len %d", res.BC, frame.ARPFrameLen)
	}

	got := mac.Read(asym(t, mac, "ARP_PACKET"), int(res.BC))
	want := frame.BuildARPRequest(srcMAC, srcIP, targetIP)
	if !bytes.Equal(got, want) {
		t.Errorf("Z80 ARP request != Go authority\n got %x\nwant %x", got, want)
	}
}

// TestZ80BuildARPRequestZeroTargetMAC pins the one field that distinguishes a
// request from a reply: the target hardware address must be all-zero (the
// unknown we are resolving). A second run with the buffer pre-dirtied confirms
// the routine actively zeroes it rather than relying on a clean buffer.
func TestZ80BuildARPRequestZeroTargetMAC(t *testing.T) {
	mac := loadARPMachine(t)

	srcMAC := frame.MAC{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	srcIP := frame.IPv4{10, 0, 0, 2}
	targetIP := frame.IPv4{10, 0, 0, 1}

	// Pre-dirty the whole frame buffer so a missed write would be observable.
	pkt := asym(t, mac, "ARP_PACKET")
	dirty := bytes.Repeat([]byte{0x5a}, frame.ARPFrameLen)
	mac.Write(pkt, dirty)

	mac.Write(asym(t, mac, "ARP_SRC_MAC"), srcMAC[:])
	mac.Write(asym(t, mac, "ARP_SRC_IP"), srcIP[:])
	mac.Write(asym(t, mac, "ARP_TARGET_IP"), targetIP[:])

	if _, err := mac.Call("build_arp_request"); err != nil {
		t.Fatalf("call build_arp_request: %v", err)
	}

	got := mac.Read(pkt, frame.ARPFrameLen)
	want := frame.BuildARPRequest(srcMAC, srcIP, targetIP)
	if !bytes.Equal(got, want) {
		t.Errorf("Z80 ARP request != Go authority (dirty buffer)\n got %x\nwant %x", got, want)
	}
	// The target hardware address is the 6 bytes at payload offset 18.
	tha := got[frame.EthHeaderLen+18 : frame.EthHeaderLen+24]
	if !bytes.Equal(tha, []byte{0, 0, 0, 0, 0, 0}) {
		t.Errorf("target hardware address = %x, want all zero", tha)
	}
}
