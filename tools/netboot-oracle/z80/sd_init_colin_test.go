// sd_init_colin_test.go — POC-2 for i145f: run Colin Piggot's REAL, hardware-
// proven B-DOS 1.5t (beta 6) SD init ladder (&A623) end-to-end against the
// modeled SD-SPI card (sdcard.go, i145c), and assert the CSD it reads via CMD9
// over the MODELED SPI decodes to the same block/record count as the POC-1 Go
// reference. This is the emulation-first validation (CLAUDE.md rule 7): Colin's
// driver is proven on real Trinity hardware, so a faithful SPI model reproduces
// its success — proving the model is the contract for all downstream SD work.
//
// Where POC-1 (csd_decode_colin_test.go) HAND-FILLED the CSD buffer and entered
// only the decode/math steps, POC-2 runs the FULL ladder: the &38 microcontroller
// wake, CMD0/CMD8/CMD55+ACMD41/CMD58/CMD59/CMD9/CMD16, and the CSD decode — every
// byte the driver reads comes from the modeled card's command/response state
// machine. The CSD arriving at &780F via a real CMD9 (not a Go Write) is the
// proof the SPI model is faithful.
//
// Reuses POC-1's load/paging recipe, CSD builders (csdV2/csdV1) and Go reference
// formulae (refBlocksV2/refBlocksV1/refRecords). The B-DOS 1.5t binary is Colin's
// PROPRIETARY code, referenced by path, never copied into the repo; the test skips
// when it is absent ($BDOS15T_BIN or the default ~/sam-archive path).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Section-B-window addresses for the full init ladder (real - &4000).
const (
	sdInitEntryB = 0xA623 - secBBias // &6623  SD init ladder entry (HDINIT body)
)

// loadBdosWithSDCard loads Colin's binary into section B (POC-1 recipe) and
// attaches an ENC28J60 with the SD card configured with `csd`. The ENC's &DC
// status read returns not-busy (so wait loops exit) and its &DF data port drives
// the SD command/response model; the SD selects (&31/&30/&38/&3F/&04) route
// through the ENC's ctlSelect into the SD model.
func loadBdosWithSDCard(t *testing.T, csd [16]byte) *z80h.Machine {
	t.Helper()
	mac := loadBdosInSectionB(t) // skips if the binary is absent; loads at &4009
	enc := z80h.NewENC28J60()
	enc.AttachSD(csd)
	mac.AttachIO(enc) // replaces the sdPortStub with the full ENC+SD model
	return mac
}

// runColinInitLadder runs the FULL &A623 init ladder to its RET, returning the
// 32-bit block count it leaves in DE:HL (the value the HDINIT caller stores). The
// ladder ends by jumping to the deselect tail (&A664) which RETs, so RunFrom stops
// at the halt sentinel the harness pushes as the return address.
func runColinInitLadder(t *testing.T, mac *z80h.Machine) (blocks uint32, res z80h.CallResult) {
	t.Helper()
	var err error
	res, err = mac.RunFrom(sdInitEntryB, z80h.Entry{StepCap: 5_000_000})
	if err != nil {
		t.Fatalf("run Colin SD init ladder: %v", err)
	}
	blocks = uint32(res.DE)<<16 | uint32(res.HL)
	return blocks, res
}

// TestSDInitColinV2 runs Colin's REAL full init ladder against the modeled SD-SPI
// card configured with a v2.0/SDHC CSD, and asserts (1) the CSD arrived at &780F
// via the modeled CMD9 (byte-for-byte the configured register), and (2) the
// decoded block count + records match the Go reference — the same values POC-1
// gets from a hand-filled buffer, now obtained by running the real SPI ladder.
func TestSDInitColinV2(t *testing.T) {
	cases := []struct {
		name  string
		cSize uint32
	}{
		{"~3.7GB", 0x001D59},    // 7,694,336 blocks
		{"~30GB", 0x00F3FF},     // 63,963,136 blocks
		{"64GB-Pete", 0x01E8FF}, // ~61 GiB: 128,188,416 blocks (records overflow 16-bit)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV2(tc.cSize)
			mac := loadBdosWithSDCard(t, csd)

			blocks, res := runColinInitLadder(t, mac)
			if !res.Halted {
				t.Fatalf("init ladder did not RET cleanly (PC=&%04X)", res.PC)
			}

			// (1) The CSD must have been streamed into &780F by the real CMD9.
			got := mac.Read(csdBuf, 16)
			for i := 0; i < 16; i++ {
				if got[i] != csd[i] {
					t.Fatalf("CSD byte %d mismatch: modeled-SPI read 0x%02X, configured 0x%02X (full read=% 02x)",
						i, got[i], csd[i], got)
				}
			}

			// (2) The block count the ladder decoded must match the Go reference.
			want := refBlocksV2(tc.cSize)
			if blocks != want {
				t.Fatalf("C_SIZE=0x%X: ladder blocks=%d, Go reference=%d", tc.cSize, blocks, want)
			}

			// (3) Records math must match POC-1's value for the same block count.
			wantBase, wantRecords := refRecords(blocks)
			_, fullRecords := refRecordsFull(blocks)
			gotBase, gotRecords, rres := runColinRecords(t, mac, blocks)
			if !rres.ReachedStop {
				t.Fatalf("records math did not reach the store (PC=&%04X)", rres.PC)
			}
			if gotBase != wantBase || gotRecords != wantRecords {
				t.Fatalf("C_SIZE=0x%X blocks=%d: ladder base=%d records=%d, Go reference base=%d records=%d",
					tc.cSize, blocks, gotBase, gotRecords, wantBase, wantRecords)
			}
			note := "via modeled CMD9; ladder==Go"
			if fullRecords > 0xFFFF {
				note = "via modeled CMD9; ladder==Go(16-bit); TRUE records overflow the 16-bit slot"
			}
			t.Logf("v2 C_SIZE=0x%X  CSD[7..9]=%02x %02x %02x  blocks=%d (=%d GB)  base=%d  records=%d (%s)",
				tc.cSize, csd[7], csd[8], csd[9], blocks, blocks/2/1024/1024, gotBase, gotRecords, note)
		})
	}
}

// TestSDInitColinV1 runs Colin's REAL full init ladder against a v1.0/SDSC card.
func TestSDInitColinV1(t *testing.T) {
	cases := []struct {
		name                        string
		readBlLen, cSize, cSizeMult uint32
	}{
		{"2GB-SDSC", 10, 4095, 7}, // 4,194,304 blocks
		{"1GB-SDSC", 10, 3823, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := csdV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			mac := loadBdosWithSDCard(t, csd)

			blocks, res := runColinInitLadder(t, mac)
			if !res.Halted {
				t.Fatalf("init ladder did not RET cleanly (PC=&%04X)", res.PC)
			}

			got := mac.Read(csdBuf, 16)
			for i := 0; i < 16; i++ {
				if got[i] != csd[i] {
					t.Fatalf("CSD byte %d mismatch: modeled-SPI read 0x%02X, configured 0x%02X (full read=% 02x)",
						i, got[i], csd[i], got)
				}
			}

			want := refBlocksV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			if blocks != want {
				t.Fatalf("v1 READ_BL_LEN=%d C_SIZE=%d MULT=%d: ladder blocks=%d, Go reference=%d",
					tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, want)
			}

			wantBase, wantRecords := refRecords(blocks)
			gotBase, gotRecords, rres := runColinRecords(t, mac, blocks)
			if !rres.ReachedStop {
				t.Fatalf("records math did not reach the store (PC=&%04X)", rres.PC)
			}
			if gotBase != wantBase || gotRecords != wantRecords {
				t.Fatalf("v1 C_SIZE=%d blocks=%d: ladder base=%d records=%d, Go reference base=%d records=%d",
					tc.cSize, blocks, gotBase, gotRecords, wantBase, wantRecords)
			}
			t.Logf("v1 BL=%d C_SIZE=%d MULT=%d  blocks=%d  base=%d  records=%d (via modeled CMD9; ladder==Go)",
				tc.readBlLen, tc.cSize, tc.cSizeMult, blocks, gotBase, gotRecords)
		})
	}
}
