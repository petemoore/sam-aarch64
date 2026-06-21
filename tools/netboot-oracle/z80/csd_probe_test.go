// csd_probe_test.go — the i145a host-verification of the SD CSD-read probe
// (src/netboot/csd_probe.asm). It loads the probe host-test binary, attaches an
// ENC28J60 with the SD card configured (AttachSD) with a v2/64GB CSD (and a v1
// CSD in a second subtest), runs the probe's SD-read path (csd_read_into_stage)
// against the modeled SD-SPI card (sdcard.go), and asserts:
//
//  1. STAGE (&C000) holds the configured 16-byte CSD byte-for-byte after the read
//     — proving the NEW probe correctly drove the model's SD command ladder (&38
//     wake + CMD0/8/55+ACMD41/58/59 + CMD9 + the &FE-token CSD read) via the
//     emulated SPI; and
//  2. a full bare-RRQ TFTP transfer of csd.bin through serve_serve_once streams
//     those same 16 bytes.
//
// This is the emulation-first payoff (CLAUDE.md rule 7): the SD-SPI model was
// VALIDATED by running Colin Piggot's REAL, hardware-proven B-DOS 1.5t init
// ladder end-to-end against it (sd_init_colin_test.go / i145f). So a faithful port
// of the same protocol reads the configured CSD through the model; a port bug
// (wrong poll sense, byte-lag error, wrong command) the model EXPOSES. The probe
// is what later runs on real hardware (i145g), so the port must be genuinely
// faithful — this test catches an unfaithful one.
//
// NEEDS NO PROPRIETARY BINARY: it uses the project's own probe + a synthetic CSD,
// so it runs in CI once `make netboot-csd-probe` has built the bin (it skips
// cleanly if absent).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	csdProbeBinPath = "../../../build/csd_probe.bin"
	csdProbeMapPath = "../../../build/csd_probe.map"

	csdProbeStage = 0xC000 // STAGE (equ &C000) — the CSD read target (pyz80 omits equ syms)
	csdProbeBytes = 16     // the served csd.bin = 16 CSD register bytes
)

func loadCSDProbe(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(csdProbeBinPath); err != nil {
		t.Skipf("csd_probe binary not built (%s); run `make netboot-csd-probe`", csdProbeBinPath)
	}
	mac, err := z80h.Load(csdProbeBinPath, csdProbeMapPath)
	if err != nil {
		t.Fatalf("load csd_probe: %v", err)
	}
	return mac
}

// fillCSDProbeConfig writes CONFIG_* and copies the probe's own csd.bin STORE +
// SRC_TABLE templates (assembled into the host-test image) into the live tables
// resolve / resolve_src walk — what probe_provision does on hardware, driven here
// since probe_provision is excluded from the host-test build.
func fillCSDProbeConfig(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v & 0xff)} }
	mac.Write(symAddr(t, mac, "CONFIG_SERVERMAC"), csdProbeServerMAC[:])
	mac.Write(symAddr(t, mac, "CONFIG_SERVERIP"), csdProbeServerIP[:])
	mac.Write(symAddr(t, mac, "CONFIG_SERVERTID"), be16(csdProbeServerTID))

	// The store template ends exactly where the src template begins; pyz80's map
	// collapses probe_store_tmpl_end onto probe_src_tmpl (same address), so we use
	// the surviving name as the end marker.
	copyTemplate(t, mac, "probe_store_tmpl", "probe_src_tmpl", "STORE")
	copyTemplate(t, mac, "probe_src_tmpl", "probe_src_tmpl_end", "SRC_TABLE")
}

var (
	csdProbeServerMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	csdProbeServerIP  = [4]byte{192, 0, 2, 1}
	csdProbeClientMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	csdProbeClientIP  = [4]byte{192, 0, 2, 44}
)

const (
	csdProbeServerTID = 40136
	csdProbeClientTID = 30574
)

func csdProbeRRQ(name string) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: csdProbeServerMAC, SrcMAC: csdProbeClientMAC,
		SrcIP: csdProbeClientIP, DstIP: csdProbeServerIP,
		SrcPort: csdProbeClientTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", nil),
	})
}

func csdProbeAck(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: csdProbeServerMAC, SrcMAC: csdProbeClientMAC,
		SrcIP: csdProbeClientIP, DstIP: csdProbeServerIP,
		SrcPort: csdProbeClientTID, DstPort: csdProbeServerTID,
		Payload: tftp.BuildACK(block),
	})
}

// initCSDProbeDriver runs drv_init against the attached ENC so drv_read's RX path
// works (mirrors initDumperDriver). Must run after AttachIO.
func initCSDProbeDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	macAddr := symAddr(t, mac, "CONFIG_SERVERMAC")
	mac.Write(macAddr, csdProbeServerMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

// csdProbeServe injects req (if non-nil) and runs one serve_serve_once, returning
// the single frame the driver transmitted (or nil). Mirrors dumperServe.
func csdProbeServe(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, req []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	res, err := mac.Call("serve_serve_once")
	if err != nil {
		t.Fatalf("call serve_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("serve_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("serve_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	return tx[len(tx)-1]
}

// streamCSD drives a full bare-RRQ transfer of csd.bin and returns the
// concatenated DATA payloads (the reconstructed CSD bytes).
func streamCSD(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	var data []byte
	blocks := 0
	out := csdProbeServe(t, mac, enc, csdProbeRRQ("csd.bin"))
	for {
		if out == nil {
			t.Fatalf("csd.bin: transfer ended early after %d blocks", blocks)
		}
		_, payload, perr := tftp.ParseDATA(udpPayload(t, out))
		if perr != nil {
			t.Fatalf("csd.bin: block %d is not a DATA packet: %v", blocks+1, perr)
		}
		data = append(data, payload...)
		blocks++
		short := len(payload) < 512
		out = csdProbeServe(t, mac, enc, csdProbeAck(uint16(blocks)))
		if short {
			if out != nil {
				t.Fatalf("csd.bin: ACK of the short final block should end the transfer, got %x", out)
			}
			break
		}
	}
	return data
}

// runCSDProbeRead runs the probe's SD-read path (csd_read_into_stage) against the
// attached SD model, returning the 16 bytes left in STAGE.
func runCSDProbeRead(t *testing.T, mac *z80h.Machine) []byte {
	t.Helper()
	if _, err := mac.Call("csd_read_into_stage"); err != nil {
		t.Fatalf("call csd_read_into_stage: %v", err)
	}
	return mac.Read(csdProbeStage, csdProbeBytes)
}

// TestCSDProbeV2ReadsConfiguredCSD is the headline i145a check: against a v2/SDHC
// (64GB) card, the probe's SD-read path drives the model's full command ladder +
// CMD9 and leaves the configured CSD in STAGE byte-for-byte, and the serve path
// streams it.
func TestCSDProbeV2ReadsConfiguredCSD(t *testing.T) {
	cases := []struct {
		name  string
		cSize uint32
	}{
		{"~3.7GB", 0x001D59},
		{"~30GB", 0x00F3FF},
		{"64GB-Pete", 0x01E8FF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := z80h.CSDForV2(tc.cSize)
			mac := loadCSDProbe(t)
			fillCSDProbeConfig(t, mac)
			enc := z80h.NewENC28J60()
			enc.AttachSD(csd)
			mac.AttachIO(enc)
			initCSDProbeDriver(t, mac, enc)

			// (1) The SD-read path must leave the configured CSD in STAGE.
			got := runCSDProbeRead(t, mac)
			if !bytes.Equal(got, csd[:]) {
				for i := range csd {
					if got[i] != csd[i] {
						t.Fatalf("C_SIZE=0x%X CSD byte %d via modeled CMD9 = 0x%02X, configured 0x%02X (full=% 02x)",
							tc.cSize, i, got[i], csd[i], got)
					}
				}
			}

			// (2) The serve path streams the same 16 bytes over a bare-RRQ transfer.
			streamed := streamCSD(t, mac, enc)
			if len(streamed) != csdProbeBytes {
				t.Fatalf("streamed %d bytes, want %d", len(streamed), csdProbeBytes)
			}
			if !bytes.Equal(streamed, csd[:]) {
				t.Fatalf("served csd.bin = % 02x, configured = % 02x", streamed, csd[:])
			}

			t.Logf("v2 C_SIZE=0x%X  CSD[7..9]=%02x %02x %02x  blocks=%d (=%d GB)  served via modeled CMD9",
				tc.cSize, csd[7], csd[8], csd[9], (tc.cSize+1)<<10, (tc.cSize+1)<<10/2/1024/1024)
		})
	}
}

// TestCSDProbeV1ReadsConfiguredCSD repeats the check for a v1/SDSC card. A v1 card
// REJECTS CMD8 (R1 illegal-command) and reports a CCS-clear OCR; the probe runs
// the standard ladder regardless (CMD8 illegal is normal) and CMD9 still delivers
// the 16 CSD bytes — proving the v1 path stays in SPI-stream phase.
func TestCSDProbeV1ReadsConfiguredCSD(t *testing.T) {
	cases := []struct {
		name                        string
		readBlLen, cSize, cSizeMult uint32
	}{
		{"2GB-SDSC", 10, 4095, 7},
		{"1GB-SDSC", 10, 3823, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			csd := z80h.CSDForV1(tc.readBlLen, tc.cSize, tc.cSizeMult)
			mac := loadCSDProbe(t)
			fillCSDProbeConfig(t, mac)
			enc := z80h.NewENC28J60()
			enc.AttachSD(csd)
			mac.AttachIO(enc)
			initCSDProbeDriver(t, mac, enc)

			got := runCSDProbeRead(t, mac)
			if !bytes.Equal(got, csd[:]) {
				for i := range csd {
					if got[i] != csd[i] {
						t.Fatalf("v1 C_SIZE=%d CSD byte %d via modeled CMD9 = 0x%02X, configured 0x%02X (full=% 02x)",
							tc.cSize, i, got[i], csd[i], got)
					}
				}
			}

			streamed := streamCSD(t, mac, enc)
			if !bytes.Equal(streamed, csd[:]) {
				t.Fatalf("served csd.bin = % 02x, configured = % 02x", streamed, csd[:])
			}
			t.Logf("v1 BL=%d C_SIZE=%d MULT=%d  CSD[0]=%02x  served via modeled CMD9 (CMD8 rejected, ladder continued)",
				tc.readBlLen, tc.cSize, tc.cSizeMult, csd[0])
		})
	}
}

// TestCSDProbeRereadsOnRRQ confirms the refresh hook re-reads the card: after
// reconfiguring the model with a different CSD, a fresh csd.bin RRQ streams the
// NEW CSD (dumper_refresh_region calls csd_read_into_stage at rrq_hit), not a
// stale STAGE.
func TestCSDProbeRereadsOnRRQ(t *testing.T) {
	mac := loadCSDProbe(t)
	fillCSDProbeConfig(t, mac)
	enc := z80h.NewENC28J60()

	first := z80h.CSDForV2(0x001D59)
	enc.AttachSD(first)
	mac.AttachIO(enc)
	initCSDProbeDriver(t, mac, enc)

	got := streamCSD(t, mac, enc) // RRQ -> refresh hook reads CSD -> stream
	if !bytes.Equal(got, first[:]) {
		t.Fatalf("first csd.bin = % 02x, want % 02x", got, first[:])
	}

	// Reconfigure the card; a fresh RRQ must reflect the new CSD via the hook.
	second := z80h.CSDForV2(0x00F3FF)
	enc.AttachSD(second)
	got = streamCSD(t, mac, enc)
	if !bytes.Equal(got, second[:]) {
		t.Fatalf("re-read csd.bin = % 02x, want % 02x (refresh hook did not re-read?)", got, second[:])
	}
}
