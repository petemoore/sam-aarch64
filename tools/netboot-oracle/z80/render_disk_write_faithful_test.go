package z80_test

// render_disk_write_faithful_test.go — the i365d-b1 emulation gate (Wall 1,
// docs/specs/i365-demo-architecture.md): stream a deterministic > free-RAM byte
// sequence into a Trinity SD record as a named MGT CODE file via raw CMD24
// (render_disk_sink.asm), then read it back through the REAL B-DOS 1.5t RST 8
// hooks (HGTHD size, HLOAD bytes) on Colin's real ROM + the SPI SD model — the
// faithful proof that the render write path produces a real, findable, loadable
// file (NOT the BDOSStore mock, whose HRECORD skips the "BDOS"+232 gate and whose
// HGTHD is a no-op — the i356 trap).
//
// Two tests:
//   - TestRenderDiskWriteRoundtripFaithful: RAM-loadable files across every
//     render_disk_sink finish path (single/multi sector, partial vs exact-510-
//     multiple bodies) — the full real-B-DOS HGTHD + HLOAD round-trip on the SAM,
//     verdict 'P', plus the physical bytes reconstructed from the SD model.
//   - TestRenderDiskWriteReleaseSizeFaithful: the exact release.src size (417374 B)
//     — HLOAD cannot hold it resident, so the SAM proves it via HGTHD (length) and
//     the Go side reconstructs the whole file from the record's contiguous sectors
//     and byte-compares to the generator (the > free-RAM proof).
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253).
// Emulation-verified is not hardware-verified (CLAUDE.md §5).

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	renderDiskProbeBin = "../../../build/render_disk_probe.bin"
	renderDiskProbeMap = "../../../build/render_disk_probe.map"

	rdpDataBase = 40  // first body sector = linearSec 40 (track 4 sector 1)
	rdpHdrLen   = 9   // 9-byte CODE header prefixing the body
	rdpPayload  = 510 // data bytes per sector (last 2 = forward link)
)

// rdpPattern is the probe's deterministic generator, byte-for-byte the asm
// `add a,&9D` loop: byte[i] = (i*0x9D) mod 256, seed 0.
func rdpPattern(n int) []byte {
	p := make([]byte, n)
	acc := byte(0)
	for i := range p {
		p[i] = acc
		acc += 0x9D
	}
	return p
}

// runRenderDiskProbe boots the faithful rig, seeds a scratch record, patches the
// probe's RDP_CONFIG (record, length, HLOAD flag), runs it to the serve loop, and
// returns the machine + SD model + the symbol resolver + the probe's outcome.
func runRenderDiskProbe(t *testing.T, rec, n int, hload bool) (mac *z80h.Machine, sd *z80h.SDCard, sym func(string) uint16, phase, verdict byte, detail uint16) {
	t.Helper()
	mac, _, sd, _ = bootToEditorIdleSDENC(t)

	// A realistic formatted Trinity record: the "BDOS"@232 validity stamp plus a
	// system CODE entry occupying slot 0 — the shape the append-writer assumes (a
	// real record, like the demo/b2a records, carries a "bdos" entry in slot 0). The
	// writer appends the streamed file into the first FREE slot AFTER slot 0, so the
	// "BDOS"@232 stamp (which lives inside slot 0's 256-byte range) is preserved. An
	// all-zeros (formatted-EMPTY) record leaves slot 0 free: the append lands there
	// and its 256-byte entry splices over offset 232, clobbering the stamp — which
	// real B-DOS HRECORD then rejects (wedging its record-select scan).
	seedFormattedScratchRecord(sd, rec, "rdscratch")
	seedRecordList(sd, map[int]string{1: "rec1", rec: "rdscratch"})

	code := append([]byte(nil), mustReadFile(t, renderDiskProbeBin, "make netboot-render-disk-probe")...)
	if err := mac.LoadSymbols(renderDiskProbeMap); err != nil {
		t.Fatalf("load %s: %v", renderDiskProbeMap, err)
	}
	sym = func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("symbol %q absent from %s", name, renderDiskProbeMap)
		}
		return a
	}

	cfgOff := int(sym("RDP_CONFIG")) - 0x8000
	if code[cfgOff] != 0x5A {
		t.Fatalf("config magic at file offset %#x is %#x, want 0x5A", cfgOff, code[cfgOff])
	}
	code[cfgOff+1], code[cfgOff+2] = byte(rec), byte(rec>>8) // RDP_CFG_RECORD LE16
	code[cfgOff+3] = byte(n)                                 // RDP_CFG_LEN LE32
	code[cfgOff+4] = byte(n >> 8)
	code[cfgOff+5] = byte(n >> 16)
	code[cfgOff+6] = byte(n >> 24)
	if hload {
		code[cfgOff+7] = 1 // RDP_CFG_HLOAD
	}

	const page = 1 // trinload deployment contract: tools land in page 1
	mac.Pager().HMPR = page
	mac.Write(0x8000, code)
	armServeDispatch(mac, page)

	res, err := mac.ContinueFrom(sym("render_disk_probe_main"), z80h.Entry{
		StepCap: 400_000_000, FrameIntPeriod: 60000,
		StopPC: sym("rdp_serve_loop"),
	})
	if err != nil {
		t.Fatalf("render_disk_probe_main faulted: %v (PC=&%04X)", err, res.PC)
	}
	if !res.ReachedStop {
		mac.Pager().HMPR = page
		ph := mac.Read(sym("rdp_phase"), 1)[0]
		t.Fatalf("probe did not reach rdp_serve_loop (&%04X): PC=&%04X after %d steps, last stage %q — a hook longjmped or wedged",
			sym("rdp_serve_loop"), res.PC, res.Steps, ph)
	}

	mac.Pager().HMPR = page
	phase = mac.Read(sym("rdp_phase"), 1)[0]
	verdict = mac.Read(sym("rdp_verdict"), 1)[0]
	detail = leU16(mac.Read(sym("rdp_detail"), 2))
	t.Logf("probe outcome (rec=%d n=%d hload=%v): phase=%q verdict=%q detail=%d steps=%d", rec, n, hload, phase, verdict, detail, res.Steps)
	return mac, sd, sym, phase, verdict, detail
}

// seedFormattedScratchRecord seeds a realistic formatted+populated Trinity record:
// seedRecordFromMGT stamps the "BDOS"@232 validity stamp in linearSec 0, and slot 0
// carries a system CODE entry so it is OCCUPIED — the exact shape the render sink's
// append-writer assumes (a real record, like the demo/b2a records, carries a "bdos"
// entry in slot 0). With slot 0 occupied the writer appends the streamed file into
// the first free slot after it, leaving the "BDOS"@232 stamp — which sits inside
// slot 0's 256-byte range — untouched. (A formatted-EMPTY record with slot 0 free
// makes the append land in slot 0 and splice over offset 232, invalidating it.)
func seedFormattedScratchRecord(sd *z80h.SDCard, rec int, label string) {
	img := make([]byte, 819200)
	img[0] = 0x13 // slot 0 type = CODE: the record's system entry (occupied)
	copy(img[1:11], "SYSTEM    ")
	img[0x0B], img[0x0C] = 0, 1 // one sector
	img[0x0D], img[0x0E] = 1, 1 // start (track 1, sector 1)
	seedRecordFromMGT(sd, rec, img, label)
}

// findRecordDirEntry scans a Trinity record's directory (linearSec 0..39, two
// 256-byte entries per sector) for the CODE (type 0x13) entry with the given
// space-padded name, returning its first (track,sector) and big-endian sector count.
// The append-writer places a new file in the first FREE slot after the record's
// existing entries, so the entry is located by name, not a fixed slot. Fatals if no
// such entry exists (proving the write produced a findable CODE entry).
func findRecordDirEntry(t *testing.T, sd *z80h.SDCard, rec int, name string) (track, sector byte, secCount int) {
	t.Helper()
	padded := name + "          "[:10-len(name)]
	for lin := 0; lin < 40; lin++ {
		sec, ok := sd.RecordDataSector(faithCSDBase, rec, lin)
		if !ok {
			continue // an unwritten directory sector — keep scanning
		}
		for _, base := range []int{0, 256} {
			e := sec[base : base+256]
			if e[0] != 0x13 || string(e[1:11]) != padded {
				continue
			}
			return e[0x0D], e[0x0E], int(e[0x0B])<<8 | int(e[0x0C])
		}
	}
	t.Fatalf("no CODE directory entry named %q found in record %d — the writer did not append a findable entry", name, rec)
	return 0, 0, 0
}

// mgtTSToRecordLinear mirrors the asm mgt_ts_to_record_linear
// (netboot_server.asm:1092): the side-major record-linear a Trinity record
// stores a given MGT (track_byte, sector) at.
func mgtTSToRecordLinear(track, sector byte) int {
	return int(track&0x80)>>7*800 + int(track&0x7F)*10 + int(sector-1)
}

// reconstructRecordFile FOLLOWS the file's on-disk MGT sector chain — exactly as
// real B-DOS HLOAD / the i365c serve do — from the RELEASESRC directory entry's
// first (track,sector), located BY NAME (the append-writer places it in the first
// free slot after the record's existing entries, not a fixed slot), hopping each
// sector's 2-byte forward link through the side-major mapping, until the terminal
// 0,0 link. This validates the forward-link encoding across every track wrap (sector
// 10 -> track++) and the side boundary (track 79 -> 128), not just that the data
// landed at the right linearSec. It asserts the chain is contiguous from rdpDataBase,
// then strips the 9-byte CODE header and returns the first n DATA bytes.
func reconstructRecordFile(t *testing.T, sd *z80h.SDCard, rec, n int) []byte {
	t.Helper()
	secCount := (n + rdpHdrLen + rdpPayload - 1) / rdpPayload // ceil((n+9)/510)
	track, sector, _ := findRecordDirEntry(t, sd, rec, "RELEASESRC")
	body := make([]byte, 0, secCount*rdpPayload)
	for hop := 0; ; hop++ {
		if hop >= secCount {
			t.Fatalf("chain did not terminate within %d sectors — a forward link is wrong or loops", secCount)
		}
		linear := mgtTSToRecordLinear(track, sector)
		if want := rdpDataBase + hop; linear != want {
			t.Fatalf("chain hop %d lands at linearSec %d (track %d sector %d), want contiguous %d — a forward link is wrong", hop, linear, track, sector, want)
		}
		sec, ok := sd.RecordDataSector(faithCSDBase, rec, linear)
		if !ok {
			t.Fatalf("record %d chain sector %d (linearSec %d) was never written", rec, hop, linear)
		}
		body = append(body, sec[:rdpPayload]...)
		nt, ns := sec[rdpPayload], sec[rdpPayload+1]
		if nt == 0 && ns == 0 { // terminal link
			if hop != secCount-1 {
				t.Fatalf("chain terminated early at hop %d, want %d sectors", hop, secCount)
			}
			break
		}
		track, sector = nt, ns
	}
	return body[rdpHdrLen : rdpHdrLen+n]
}

func assertRenderDiskMatch(t *testing.T, got, want []byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("reconstructed length %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte mismatch at offset %d: got %#02x want %#02x", i, got[i], want[i])
		}
	}
}

// assertDirEntryByName checks the directory entry the append-writer produced for
// `name`: it is a CODE entry (found by name in the first free slot after the
// record's existing entries — findRecordDirEntry fatals otherwise, so type+name are
// asserted), its big-endian sector count matches the body, AND the record's
// "BDOS"@232 validity stamp in linearSec 0 SURVIVED the append. The stamp check is
// the load-bearing proof that the writer preserved the record's validity (an append
// into slot 0 would splice over offset 232 — real B-DOS HRECORD would then reject it).
func assertDirEntryByName(t *testing.T, sd *z80h.SDCard, rec int, name string, n int) {
	t.Helper()
	_, _, gotSec := findRecordDirEntry(t, sd, rec, name)
	wantSec := (n + rdpHdrLen + rdpPayload - 1) / rdpPayload
	if gotSec != wantSec {
		t.Fatalf("dir entry %q sector count = %d, want %d", name, gotSec, wantSec)
	}
	dir, ok := sd.RecordDataSector(faithCSDBase, rec, 0)
	if !ok {
		t.Fatalf("record %d directory sector (linearSec 0) was never written", rec)
	}
	if !bytes.Equal(dir[232:236], []byte("BDOS")) {
		t.Fatalf(`dir sector +232 = % X, want "BDOS" — the append clobbered the record-validity stamp HRECORD checks`, dir[232:236])
	}
}

// TestRenderDiskWriteRoundtripFaithful runs a RAM-loadable file through the full
// real-B-DOS HGTHD + HLOAD round-trip for every distinct render_disk_sink finish
// path, so the boundary cases (a body that is an exact multiple of 510, where the
// final sector's forward link must be terminated 0,0) are proven, not assumed:
//
//	n=300  single sector, partial               (rds_fin_single)
//	n=501  single sector, EXACT 510-byte body   (rds_fin_seal0_term: sector-0 link fix)
//	n=2000 multi-sector, partial tail           (rds_fin_tail)
//	n=2031 multi-sector, EXACT 510-multiple body(boundary re-write of the last sector)
func TestRenderDiskWriteRoundtripFaithful(t *testing.T) {
	const rec = 2
	cases := []struct {
		name string
		n    int
	}{
		{"single-partial", 300},
		{"single-exact-boundary", 501}, // 9 + 501 = 510 = exactly one sector
		{"multi-partial-tail", 2000},   // > 1518, chained, partial last sector
		{"multi-exact-boundary", 2031}, // 9 + 2031 = 2040 = 4 sectors exactly
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mac, sd, _, phase, verdict, detail := runRenderDiskProbe(t, rec, c.n, true)
			_ = mac
			if phase != '5' {
				t.Fatalf("phase = %q, want '5' — the probe stopped short of the verdict", phase)
			}
			if verdict != 'P' {
				t.Fatalf("verdict = %q (detail=%d; &FEED=write fail, &FFFF=HGTHD size mismatch, else first HLOAD-compare mismatch offset), want 'P'", verdict, detail)
			}
			if detail != 0 {
				t.Fatalf("detail = %d, want 0 on a pass", detail)
			}
			// The physical bytes on the card, reconstructed from the record's sectors.
			assertRenderDiskMatch(t, reconstructRecordFile(t, sd, rec, c.n), rdpPattern(c.n))
			assertDirEntryByName(t, sd, rec, "RELEASESRC", c.n)
			// Data safety (i295): nothing written outside the target record's band.
			if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, rec); len(outside) != 0 {
				t.Fatalf("record write escaped record %d's band to LBAs %v — the i295 guard did not hold", rec, outside)
			}
		})
	}
}

func TestRenderDiskWriteReleaseSizeFaithful(t *testing.T) {
	const (
		rec = 2
		n   = 417374 // release.src's exact size; HLOAD cannot hold it (> free RAM)
	)
	// HLOAD off: the SAM proves the write via HGTHD (length); Go proves the bytes.
	mac, sd, _, phase, verdict, detail := runRenderDiskProbe(t, rec, n, false)
	_ = mac

	if phase != '5' {
		t.Fatalf("phase = %q, want '5'", phase)
	}
	if verdict != 'P' {
		t.Fatalf("verdict = %q (detail=%d; &FEED=write fail, &FFFF=HGTHD size mismatch), want 'P' — real B-DOS HGTHD did not report the 417374-byte file", verdict, detail)
	}

	assertRenderDiskMatch(t, reconstructRecordFile(t, sd, rec, n), rdpPattern(n))
	assertDirEntryByName(t, sd, rec, "RELEASESRC", n)

	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, rec); len(outside) != 0 {
		t.Fatalf("record write escaped record %d's band to LBAs %v — the i295 guard did not hold", rec, outside)
	}
}
