// sd_sector_probe_test.go — the i299 host-verification of the READ-ONLY raw-sector
// dump extension (csd_probe.asm built with -D SD_SECTOR_PROBE, target
// build/sd_sector_probe.bin). It attaches the SD-SPI model (sdcard.go) with known
// sector content seeded at the exact card-absolute LBAs the probe reads, drives a
// full bare-RRQ TFTP transfer of each sector-dump file (list.bin / r13.bin / r3.bin)
// through serve_serve_once, and asserts the streamed bytes equal the seeded sectors
// in order — proving the probe's CMD17 reads hit the right LBAs (LBA 1; the
// SP_LBA_REC13 / SP_LBA_REC3 windows) and stream them faithfully.
//
// This is the emulation-first gate (CLAUDE.md rule 7) before the probe reads Pete's
// real shared SD card: the SD-SPI model is the one Colin's REAL B-DOS 1.5t init
// ladder was validated against (i145f), so a wrong LBA, a byte-lag bug, or a
// misordered multi-sector loop is caught here, not on hardware. The probe is
// read-only (CMD17 only — no CMD24 in the binary), so it cannot harm the card; this
// test additionally proves it reads the sectors it claims to.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sectorProbeBinPath = "../../../build/sd_sector_probe.bin"
	sectorProbeMapPath = "../../../build/sd_sector_probe.map"

	// The probe's absolute LBAs (csd_probe.asm SP_LBA_REC2/REC13/REC13ALT + list).
	spLBAList     = 1
	spLBARec2     = 2438 + 1600*1 - 4  // 4034  (record 2 = Comet v18)
	spLBARec13    = 2438 + 1600*12 - 4 // 21634 (record 13 = our write)
	spLBARec13Alt = 2438 + 1600*13 - 4 // 23234 (record 13 = alt n*1600+base formula)
)

func loadSectorProbe(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sectorProbeBinPath); err != nil {
		t.Fatalf("sd_sector_probe binary not built (%s); run `make netboot-sd-sector-probe`", sectorProbeBinPath)
	}
	mac, err := z80h.Load(sectorProbeBinPath, sectorProbeMapPath)
	if err != nil {
		t.Fatalf("load sd_sector_probe: %v", err)
	}
	return mac
}

// seededSector returns a deterministic 512-byte sector whose every byte encodes its
// LBA, so a wrong-LBA or misordered read is detectable: byte i = (lba + i) & 0xFF.
func seededSector(lba uint32) []byte {
	sec := make([]byte, 512)
	for i := range sec {
		sec[i] = byte(lba + uint32(i))
	}
	return sec
}

// streamFile drives a full bare-RRQ transfer of name and returns the concatenated
// DATA payloads. Generalises streamCSD (csd_probe_test.go) to any served file.
func streamFile(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, name string) []byte {
	t.Helper()
	var data []byte
	blocks := 0
	out := csdProbeServe(t, mac, enc, csdProbeRRQ(name))
	for {
		if out == nil {
			t.Fatalf("%s: transfer ended early after %d blocks", name, blocks)
		}
		_, payload, perr := tftp.ParseDATA(udpPayload(t, out))
		if perr != nil {
			t.Fatalf("%s: block %d is not a DATA packet: %v", name, blocks+1, perr)
		}
		data = append(data, payload...)
		blocks++
		short := len(payload) < 512
		out = csdProbeServe(t, mac, enc, csdProbeAck(uint16(blocks)))
		if short {
			if out != nil {
				t.Fatalf("%s: ACK of the short final block should end the transfer, got %x", name, out)
			}
			break
		}
	}
	return data
}

// TestSectorProbeReadsSeededLBAs proves each sector-dump file reads the exact
// card-absolute LBAs the probe targets and streams them in order.
func TestSectorProbeReadsSeededLBAs(t *testing.T) {
	mac := loadSectorProbe(t)
	fillCSDProbeConfig(t, mac)
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(z80h.CSDForV2(0x01E8FF)) // a v2/SDHC card (block addressing, like Pete's)
	mac.AttachIO(enc)
	initCSDProbeDriver(t, mac, enc)

	cases := []struct {
		name  string
		base  uint32
		count int
	}{
		{"list.bin", spLBAList, 1},
		{"r2.bin", spLBARec2, 8},
		{"r13.bin", spLBARec13, 8},
		{"rax.bin", spLBARec13Alt, 8},
	}

	// Seed every targeted sector with its LBA-encoding pattern.
	for _, c := range cases {
		for s := 0; s < c.count; s++ {
			sd.SeedSector(c.base+uint32(s), seededSector(c.base+uint32(s)))
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want []byte
			for s := 0; s < c.count; s++ {
				want = append(want, seededSector(c.base+uint32(s))...)
			}
			got := streamFile(t, mac, enc, c.name)
			if len(got) != c.count*512 {
				t.Fatalf("%s: streamed %d bytes, want %d (%d sectors)", c.name, len(got), c.count*512, c.count)
			}
			if !bytes.Equal(got, want) {
				// Find the first divergent sector for a useful message.
				for s := 0; s < c.count; s++ {
					gs := got[s*512 : s*512+512]
					ws := want[s*512 : s*512+512]
					if !bytes.Equal(gs, ws) {
						t.Fatalf("%s sector %d (LBA %d): read does not match seed (first bytes got %02x %02x %02x, want %02x %02x %02x) — wrong LBA or misordered read",
							c.name, s, c.base+uint32(s), gs[0], gs[1], gs[2], ws[0], ws[1], ws[2])
					}
				}
			}
			t.Logf("%s: %d sectors from LBA %d streamed byte-exact via the raw CMD17 path", c.name, c.count, c.base)
		})
	}
}
