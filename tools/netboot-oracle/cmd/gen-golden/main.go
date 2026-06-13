// Command gen-golden extracts golden netboot vectors from Pete's off-repo
// captures (~/tftp-logs) and emits a masked, committable Go fixture file
// (golden/vectors_gen.go). It is run by hand when the vectors need refreshing;
// CI never runs it (the captures are not in the repo). See the netboot-oracle
// README for the regeneration command.
//
// The masking (internal/mask) rewrites the real LAN MACs/IPs to documentation
// placeholders while keeping every protocol field — crucially the option-43
// "Raspberry Pi Boot" blob — byte-identical, so the committed vectors are safe
// to publish yet remain an exact structural oracle for the Z80 port.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/pcap"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

func main() {
	logsDir := flag.String("logs", os.ExpandEnv("$HOME/tftp-logs"), "directory holding the off-repo captures")
	out := flag.String("out", "golden/vectors_gen.go", "output Go fixture file")
	flag.Parse()

	// Real endpoint addresses as decoded from rpi400-boot-spectrum4.pcapng
	// (see docs/notes/pi-netboot-capture-analysis.md §3). The two DHCP servers
	// (router .1, proxyDHCP/TFTP .52) and the client (.44) are all collapsed
	// onto the placeholder SAM/client pair, since the SAM plays every server
	// role in one responder.
	serverMACs := [][6]byte{
		{0xb8, 0x27, 0xeb, 0x86, 0xab, 0x24}, // proxyDHCP / TFTP server .52
		{0xb4, 0xb0, 0x24, 0x55, 0xfd, 0xb4}, // router / DHCP lease server .1
	}
	clientMAC := [6]byte{0xdc, 0xa6, 0x32, 0xec, 0xfc, 0x3c} // Pi 400
	serverIPs := [][4]byte{{192, 168, 2, 1}, {192, 168, 2, 52}, {192, 168, 2, 255}}
	clientIPs := [][4]byte{{192, 168, 2, 44}, {0, 0, 0, 0}}

	vectors := map[string][]byte{}

	pcapng := filepath.Join(*logsDir, "rpi400-boot-spectrum4.pcapng")
	frames, err := pcap.ReadFrames(pcapng)
	if err != nil {
		log.Fatalf("read %s: %v", pcapng, err)
	}
	log.Printf("%s: %d frames", filepath.Base(pcapng), len(frames))

	maskFrame := func(f []byte) []byte {
		cp := append([]byte(nil), f...)
		// Try both server MACs by registering both into one map.
		m := mask.NewAddrMap(serverMACs[0], clientMAC, serverIPs, clientIPs)
		m.Frame(cp)
		// Second pass to catch the other server MAC.
		m2 := mask.NewAddrMap(serverMACs[1], clientMAC, serverIPs, clientIPs)
		m2.Frame(cp)
		return cp
	}

	// Select the canonical golden frames by content.
	for _, f := range frames {
		if len(f) < 42 {
			continue
		}
		etype := binary.BigEndian.Uint16(f[12:])
		if etype != 0x0800 || f[23] != 0x11 { // IPv4 / UDP
			continue
		}
		sport := binary.BigEndian.Uint16(f[34:])
		dport := binary.BigEndian.Uint16(f[36:])
		payload := f[42:]

		// DHCP OFFER carrying the option-43 blob.
		if (sport == 67 || dport == 67 || sport == 68 || dport == 68) && len(payload) >= 240 {
			if msg, perr := dhcp.Parse(payload); perr == nil {
				switch msg.MsgType() {
				case dhcp.MsgDiscover:
					if _, ok := vectors["DHCPDiscover"]; !ok {
						vectors["DHCPDiscover"] = maskFrame(f)
					}
				case dhcp.MsgOffer:
					// Prefer the OFFER that carries option 43.
					if msg.Option(dhcp.OptVendorEncap) != nil {
						vectors["DHCPOfferPXE"] = maskFrame(f)
					} else if _, ok := vectors["DHCPOfferLease"]; !ok {
						vectors["DHCPOfferLease"] = maskFrame(f)
					}
				case dhcp.MsgRequest:
					if _, ok := vectors["DHCPRequest"]; !ok {
						vectors["DHCPRequest"] = maskFrame(f)
					}
				case dhcp.MsgACK:
					if _, ok := vectors["DHCPAck"]; !ok {
						vectors["DHCPAck"] = maskFrame(f)
					}
				}
			}
			continue
		}

		// TFTP.
		switch tftp.Opcode(payload) {
		case tftp.OpRRQ:
			req, rerr := tftp.ParseRequest(payload)
			if rerr != nil {
				continue
			}
			// A serial-subdir RRQ and a plain RRQ, plus one blksize=1468 RRQ.
			if _, ok := vectors["TFTPRrqSerial"]; !ok && bytes.ContainsRune([]byte(req.Filename), '/') {
				vectors["TFTPRrqSerial"] = maskFrame(f)
			}
			if v, _ := req.Option("blksize"); v == "1024" {
				if _, ok := vectors["TFTPRrqRoot1024"]; !ok && !bytes.ContainsRune([]byte(req.Filename), '/') {
					vectors["TFTPRrqRoot1024"] = maskFrame(f)
				}
			}
			if v, _ := req.Option("blksize"); v == "1468" {
				if _, ok := vectors["TFTPRrqRoot1468"]; !ok {
					vectors["TFTPRrqRoot1468"] = maskFrame(f)
				}
			}
		case tftp.OpOACK:
			if _, ok := vectors["TFTPOack"]; !ok {
				vectors["TFTPOack"] = maskFrame(f)
			}
		case tftp.OpDATA:
			if _, ok := vectors["TFTPData"]; !ok {
				if blk, _, derr := tftp.ParseDATA(payload); derr == nil && blk == 1 {
					vectors["TFTPData"] = maskFrame(f)
				}
			}
		case tftp.OpERROR:
			code, _, eerr := tftp.ParseError(payload)
			if eerr == nil && code == tftp.ErrFileNotFound {
				if _, ok := vectors["TFTPErrorNotFound"]; !ok {
					vectors["TFTPErrorNotFound"] = maskFrame(f)
				}
			}
		}
	}

	if err := emit(*out, vectors); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s with %d vectors", *out, len(vectors))
	for k, v := range vectors {
		log.Printf("  %-20s %d bytes", k, len(v))
	}
}

// emit writes the masked vectors as a gofmt'd Go source file.
func emit(path string, vectors map[string][]byte) error {
	names := []string{
		"DHCPDiscover", "DHCPOfferPXE", "DHCPOfferLease", "DHCPRequest", "DHCPAck",
		"TFTPRrqSerial", "TFTPRrqRoot1024", "TFTPRrqRoot1468",
		"TFTPOack", "TFTPData", "TFTPErrorNotFound",
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `// Code generated by cmd/gen-golden from ~/tftp-logs captures. DO NOT EDIT.
//
// These are masked golden vectors: the real LAN MACs/IPs are rewritten to
// documentation placeholders (server 192.0.2.1 / 02:00:00:00:00:01, client
// 192.0.2.44 / 02:00:00:00:00:44), while every protocol field — notably the
// option-43 "Raspberry Pi Boot" blob — is byte-identical to the capture. They
// are the byte-for-byte oracle for the SAM-side Z80 netboot port.
//
// Provenance: rpi400-boot-spectrum4.pcapng (a complete Pi 400 netboot),
// decoded in docs/notes/pi-netboot-capture-analysis.md. Regenerate with the
// command in the netboot-oracle README.
package golden

`)
	for _, name := range names {
		v, ok := vectors[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "// %s is a complete Ethernet frame (%d bytes).\nvar %s = []byte{\n", name, len(v), name)
		for i := 0; i < len(v); i += 12 {
			b.WriteString("\t")
			for j := i; j < i+12 && j < len(v); j++ {
				fmt.Fprintf(&b, "0x%02x, ", v[j])
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, src, 0o644)
}
