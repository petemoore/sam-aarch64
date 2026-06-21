package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	dhcpBinPath = "../../../build/netboot_dhcp_reply.bin"
	dhcpMapPath = "../../../build/netboot_dhcp_reply.map"
)

func loadDHCPMachine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(dhcpBinPath); err != nil {
		t.Fatalf("netboot DHCP routine binary not built (%s); run `make netboot-dhcp-reply`", dhcpBinPath)
	}
	mac, err := z80h.Load(dhcpBinPath, dhcpMapPath)
	if err != nil {
		t.Fatalf("load DHCP routine: %v", err)
	}
	return mac
}

// be32 stages a uint32 big-endian (the on-wire DHCP option order).
func be32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// TestZ80BuildDHCPReplyMatchesGoAuthority asserts the Z80 build_dhcp_reply
// emits exactly the bytes dhcp.BuildReply emits for the same ReplyParams —
// byte-for-byte port fidelity (the i86 OFFER/ACK template incl. the fixed
// option-43 "Raspberry Pi Boot" blob + the echoed client UUID).
func TestZ80BuildDHCPReplyMatchesGoAuthority(t *testing.T) {
	mac := loadDHCPMachine(t)

	// Echoed fields from the captured DISCOVER (the same fixture the Go
	// TestBuildReplyMatchesTemplate uses).
	du, _ := frame.ParseUDP(golden.DHCPDiscover)
	req, err := dhcp.Parse(du.Payload)
	if err != nil {
		t.Fatalf("parse DISCOVER: %v", err)
	}
	var uuid []byte
	if o := req.Option(dhcp.OptClientUUID); o != nil {
		uuid = o.Value
	}
	p := dhcp.ReplyParams{
		MsgType:    dhcp.MsgOffer,
		XID:        req.XID,
		YIAddr:     mask.ClientIP,
		ServerIP:   mask.ServerIP,
		SubnetMask: [4]byte{255, 255, 255, 0},
		Broadcast:  mask.Broadcast,
		LeaseSecs:  7200, T1Secs: 3600, T2Secs: 6300,
		CHAddr:     req.CHAddr,
		ClientUUID: uuid,
		Flags:      0x8000,
	}

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return a
	}
	mac.Write(sym("DP_MSGTYPE"), []byte{p.MsgType})
	mac.Write(sym("DP_XID"), p.XID[:])
	mac.Write(sym("DP_FLAGS"), []byte{byte(p.Flags >> 8), byte(p.Flags)})
	mac.Write(sym("DP_YIADDR"), p.YIAddr[:])
	mac.Write(sym("DP_SERVERIP"), p.ServerIP[:])
	mac.Write(sym("DP_SUBNET"), p.SubnetMask[:])
	mac.Write(sym("DP_BROADCAST"), p.Broadcast[:])
	mac.Write(sym("DP_LEASE"), be32(p.LeaseSecs))
	mac.Write(sym("DP_T1"), be32(p.T1Secs))
	mac.Write(sym("DP_T2"), be32(p.T2Secs))
	mac.Write(sym("DP_CHADDR"), p.CHAddr[:])
	mac.Write(sym("DP_UUID_LEN"), []byte{byte(len(uuid))})
	mac.Write(sym("DP_UUID"), uuid)

	res, err := mac.Call("build_dhcp_reply")
	if err != nil {
		t.Fatalf("call build_dhcp_reply: %v", err)
	}
	z80Body := mac.Read(sym("DBODY"), int(res.BC))
	goBody := dhcp.BuildReply(p)

	if !bytes.Equal(z80Body, goBody) {
		t.Errorf("Z80 DHCP body != Go authority\n z80 (%d) %x\n  go (%d) %x",
			len(z80Body), z80Body, len(goBody), goBody)
	}

	// The body must also parse back to the oracle §1 template fields.
	out, err := dhcp.Parse(z80Body)
	if err != nil {
		t.Fatalf("parse Z80 body: %v", err)
	}
	if out.MsgType() != dhcp.MsgOffer {
		t.Errorf("Z80 body msg type = %d, want OFFER", out.MsgType())
	}
	if o := out.Option(dhcp.OptVendorEncap); o == nil || !bytes.Equal(o.Value, dhcp.Option43RaspberryPiBoot) {
		t.Errorf("Z80 body option-43 blob not reproduced exactly")
	}
	if o := out.Option(dhcp.OptClientUUID); o == nil || !bytes.Equal(o.Value, uuid) {
		t.Errorf("Z80 body did not echo the client UUID verbatim")
	}
	if out.SIAddr != mask.ServerIP {
		t.Errorf("Z80 body siaddr = %v, want SAM %v", out.SIAddr, mask.ServerIP)
	}
}
