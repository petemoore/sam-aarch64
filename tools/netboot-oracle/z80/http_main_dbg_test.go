// http_main_dbg_test.go — i70a D4: host-verification of the NETBOOT_DEBUG step-
// marker channel in src/netboot/http_main.asm. The debug boot binary built with
// -D NETBOOT_DEBUG=1 -D NETBOOT_HTTP_SMOKE=1 (netboot_http_boot_debug.bin)
// broadcasts a small "SDBG" UDP packet at each boot-path step, so an autonomous
// agent running tcpdump on the i70b hardware shot can localize a hang to the last
// marker seen — the i270 debug bottleneck removed, now extended to the HTTP fetch
// path (the server-side analogue was shipped in the serve debug binary).
//
// TestHTTPMainBootDebugMarkers loads the DEBUG binary (netboot_http_boot_debug.bin,
// SMOKE+DEBUG, 1-file manifest) and RunBoots http_main from &8000 with
// PHY link modelled UP. It asserts that four marker frames arrive BEFORE the ARP:
//   DBG_HTTP_ENTRY    (&60): ENC initialized + EEPROM read succeeded
//   DBG_HTTP_EEPROM_OK(&61): SD CSD read done + ENC RX re-armed
//   DBG_HTTP_LINK_UP  (&62): PHY link up (drv_wait_link returned BC!=0)
//   DBG_HTTP_FILE_START(&63): per-file fetch started (store_begin called by prov_start)
//
// The ARP follows because prov_first calls prov_start (which calls store_begin,
// emitting FILE_START) then http_fetch_first (which sends the ARP). With no RX
// queued, RunBoot returns spinning in the provisioning loop.
//
// THE HONESTY LINE (CLAUDE.md §5): proves markers are EMITTED in emulation.
// Using them to localize a real hang on hardware stays gated on the i70b smoke shot.
package z80_test

import (
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	httpBootDebugBin     = "../../../build/netboot_http_boot_debug.bin"
	httpBootDebugMap     = "../../../build/netboot_http_boot_debug.map"
	httpBootDebugStepCap = 2_000_000
)

// Marker codes for the http_main boot path (must match src/netboot/dbg_marker.asm).
const (
	dbgHTTPEntry     = 0x60 // ENC init OK + EEPROM chunk populated
	dbgHTTPEEPROMOK  = 0x61 // SD CSD read done + ENC RX re-armed
	dbgHTTPLinkUp    = 0x62 // PHY link up
	dbgHTTPFileStart = 0x63 // per-file fetch started (store_begin called)
	dbgHTTPFileSaved = 0x64 // per-file window persisted (HSAVE returned)
	dbgHTTPFileVerify = 0x65 // per-file verify done
	dbgHTTPAllDone   = 0x66 // all files fetched + persisted
	dbgHTTPFailCfg   = 0x70 // fail: EEPROM chunk absent or bad
	dbgHTTPFailInit  = 0x71 // fail: drv_init returned BC=0
	dbgHTTPFailLink  = 0x72 // fail: PHY link timeout
)

// isHTTPMarker reports whether a frame is a debug marker UDP packet (dest port 9001,
// "SDBG" magic at UDP payload offset 0). Using the same dbgMarkerCode helper from
// netboot_serve_dbg_test.go in the same package.
func httpMarkerCode(f []byte) (byte, bool) {
	return dbgMarkerCode(f) // defined in netboot_serve_dbg_test.go
}

// splitHTTPDebugFrames separates marker frames from non-marker frames in the TX
// output, returning them in order. A nil reply means no non-marker frame appeared.
func splitHTTPDebugFrames(frames [][]byte) (markers []byte, nonMarkers [][]byte) {
	for _, f := range frames {
		if code, ok := httpMarkerCode(f); ok {
			markers = append(markers, code)
		} else {
			nonMarkers = append(nonMarkers, f)
		}
	}
	return markers, nonMarkers
}

// TestHTTPMainBootDebugMarkers loads the http_main SMOKE+DEBUG binary and runs the
// boot wrapper (http_main) from &8000. It asserts:
//  1. Markers [ENTRY, EEPROM_OK, LINK_UP, FILE_START] arrive before the ARP request.
//  2. The ARP is present (exactly one non-marker frame: the prov_first ARP).
//  3. http_main is still running after the step cap (not halted on a fail border).
func TestHTTPMainBootDebugMarkers(t *testing.T) {
	mac, err := z80h.Load(httpBootDebugBin, httpBootDebugMap)
	if err != nil {
		t.Fatalf("http_main debug binary not built (%v); run `make netboot-http-boot-debug`", err)
	}
	enc := z80h.NewENC28J60()
	// Populate the EEPROM with a real Trinity identity so the EEPROM read succeeds
	// and DBG_HTTP_ENTRY fires (via drv_init + enc.ProgramTrinityNetwork).
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)
	// No SD CSD attached: the boot's csd_set_bd_records fails gracefully and
	// leaves BD_RECORDS = 0, so store_begin's free-record scan is skipped
	// (graceful-decline path, record 0 = floppy). The marker sequence under
	// test is unaffected; the scan itself is covered by TestProvStoreDemarcation
	// against the SPI card model (i70e).

	// No RX queued: after prov_first's ARP egresses, the provisioning loop spins
	// awaiting a reply that never comes. RunBoot returns with Halted=false.
	res, err := mac.RunBoot("http_main", z80h.Entry{StepCap: httpBootDebugStepCap})
	if err != nil {
		t.Fatalf("RunBoot http_main (debug): %v", err)
	}
	border, hadBorder := enc.LastBorder()
	t.Logf("http_main debug: halted=%v PC=&%04X steps=%d border=%d(written=%v) tx=%d",
		res.Halted, res.PC, res.Steps, border, hadBorder, len(enc.TXFrames()))

	// Separate marker frames from non-marker frames (the ARP).
	markers, nonMarkers := splitHTTPDebugFrames(enc.TXFrames())

	// The four pre-ARP boot-path markers must be present, in order.
	// ENTRY is the first transmittable marker (fires after drv_init, the earliest
	// point the ENC can TX); EEPROM_OK follows the CSD read + ENC RX re-arm;
	// LINK_UP follows drv_wait_link; FILE_START fires in store_begin, called by
	// prov_start inside prov_first — before http_fetch_first sends the ARP.
	want := []byte{dbgHTTPEntry, dbgHTTPEEPROMOK, dbgHTTPLinkUp, dbgHTTPFileStart}
	if len(markers) < len(want) {
		t.Fatalf("debug markers = %#x, want at least %#x; "+
			"fewer markers than expected means the boot path got stuck before FILE_START "+
			"(halted=%v border=%d — a fail border 2/1/6 points to cfg/init/link failure)",
			markers, want, res.Halted, border)
	}
	for i, w := range want {
		if markers[i] != w {
			t.Errorf("marker[%d] = &%02X, want &%02X (%#x full sequence)", i, markers[i], w, markers)
		}
	}
	t.Logf("boot-path markers = %#x (pre-ARP sequence correct)", markers[:len(want)])

	// Exactly one non-marker frame: prov_first's ARP request.
	if len(nonMarkers) != 1 {
		t.Fatalf("non-marker TX frames = %d, want exactly 1 (the prov_first ARP); "+
			"0 means prov_first never reached http_fetch_first; >1 unexpected",
			len(nonMarkers))
	}

	// http_main must still be spinning (awaiting the ARP reply), not halted.
	if res.Halted {
		t.Errorf("http_main debug halted (PC=&%04X border=%d); "+
			"expected spinning in the provisioning loop awaiting ARP reply", res.PC, border)
	}
}
