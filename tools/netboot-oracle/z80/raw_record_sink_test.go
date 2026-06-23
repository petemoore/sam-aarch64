// raw_record_sink_test.go — i122b: host-verification of the streaming raw-record
// write sink (src/netboot/raw_record_sink.asm), the DiskRecord storage-class
// persist path (docs/specs/netboot-storage-manifest-design.md §6.5).
//
// The sink re-blocks an arbitrary body byte-stream into 512-byte sectors and
// writes each full sector into the HRECORD-selected record via i114c's HWSAD seam
// (bdos_write_record). These tests drive the REAL Z80 sink under AttachBDOS — so
// the real RST 8 HWSAD dispatch runs and BDOSStore.SectorWrites captures it — and
// assert the emitted sectors (record, linear index, 512 bytes) match the Go
// authority bdos.RawSink fed the identical stream. A round-trip case reads the
// written sectors back via HRSAD and reconstructs the image.
//
// THE HONESTY LINE (CLAUDE.md §5): the HWSAD handler models the digital dispatch
// (which sector coordinates, which bytes, which record), not a real SD write.
// Emulation-verified is not hardware-verified.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// rrsScratch is a free flat-harness RAM window for staging chunk bytes before
// each raw_record_sink_leaf call: above the binary tail (&C2EB for the serve
// boot binary, which now carries sdc_init_ladder / bd_list_* at &BFFA–&C2EB
// after adding NETBOOT_REAL_LISTREAD) and clear of the harness stack (&6FFE)
// and HALT trap (&7000). Window is &D000..&EFFF (8 KB); chunks are staged in
// slices no larger than rrsSliceMax.
const (
	rrsScratch  = 0xD000
	rrsSliceMax = 0x2000 // 8 KB — within &D000..&EFFF
)

// rrsSeq returns n bytes with a position-dependent pattern (matching the Go
// raw_sink_test seq), so a misplaced/duplicated byte shows as a content mismatch.
func rrsSeq(start, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((start + i) * 7)
	}
	return b
}

func rrsOneByteEach(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i := range b {
		out[i] = []byte{b[i]}
	}
	return out
}

// feedZ80Sink streams chunks through the real Z80 raw_record_sink into a freshly
// loaded boot binary and returns the captured HWSAD sector writes. Each logical
// chunk is staged into rrsScratch in <=rrsSliceMax slices; because the sink
// re-blocks a byte stream independent of call boundaries, slicing for staging
// does not change the emitted sectors (the property the Go authority also holds).
func feedZ80Sink(t *testing.T, chunks [][]byte, finish bool, record int) (*z80h.Machine, []z80h.SectorWrite) {
	t.Helper()
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: byte(record)}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", record, err)
	}
	if _, err := mac.Call("raw_record_sink_reset"); err != nil {
		t.Fatalf("raw_record_sink_reset: %v", err)
	}
	for ci, c := range chunks {
		if len(c) == 0 {
			// Exercise the empty-chunk path (BC=0 → immediate return).
			if _, err := mac.CallEntry("raw_record_sink_leaf", z80h.Entry{HL: rrsScratch, BC: 0}); err != nil {
				t.Fatalf("raw_record_sink_leaf(empty chunk %d): %v", ci, err)
			}
			continue
		}
		for off := 0; off < len(c); {
			end := off + rrsSliceMax
			if end > len(c) {
				end = len(c)
			}
			slice := c[off:end]
			mac.Write(rrsScratch, slice)
			if _, err := mac.CallEntry("raw_record_sink_leaf", z80h.Entry{HL: rrsScratch, BC: uint16(len(slice))}); err != nil {
				t.Fatalf("raw_record_sink_leaf(chunk %d @%d): %v", ci, off, err)
			}
			off = end
		}
	}
	if finish {
		if _, err := mac.Call("raw_record_sink_finish"); err != nil {
			t.Fatalf("raw_record_sink_finish: %v", err)
		}
	}
	return mac, store.SectorWrites()
}

// rrsTotal reads the 32-bit LE RRS_TOTAL (the streamed image-size counter).
func rrsTotal(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	b := mac.Read(symAddr(t, mac, "RRS_TOTAL"), 4)
	return int(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
}

// goSink runs the Go authority over the same chunk stream.
func goSink(chunks [][]byte, finish bool) []bdos.RawSectorWrite {
	s := bdos.NewRawSink()
	for _, c := range chunks {
		s.Write(c)
	}
	if finish {
		s.Finish()
	}
	return s.Writes()
}

// TestRawRecordSinkMatchesGo asserts the Z80 sink emits exactly the sectors the
// Go authority does, across chunk shapes that stress the re-blocking: sub-sector
// fills, boundary-straddling chunks, multi-sector chunks, empty chunks, a
// non-aligned tail (flushed only on finish), and a full 1600-sector record.
func TestRawRecordSinkMatchesGo(t *testing.T) {
	const record = 7
	cases := []struct {
		name   string
		chunks [][]byte
		finish bool
	}{
		{"empty stream", nil, true},
		{"one exact sector", [][]byte{rrsSeq(0, 512)}, true},
		{"one byte at a time, exactly one sector", rrsOneByteEach(rrsSeq(0, 512)), true},
		{"sub-sector chunks completing a sector", [][]byte{rrsSeq(0, 200), rrsSeq(200, 200), rrsSeq(400, 112)}, true},
		{"a chunk straddling a boundary", [][]byte{rrsSeq(0, 500), rrsSeq(500, 100)}, true},
		{"one big chunk spanning many sectors", [][]byte{rrsSeq(0, 512 * 5)}, true},
		{"big chunk + non-aligned tail, finished", [][]byte{rrsSeq(0, 512*3+37)}, true},
		{"non-aligned tail, NOT finished (tail dropped)", [][]byte{rrsSeq(0, 512*2+9)}, false},
		{"empty chunks interspersed", [][]byte{rrsSeq(0, 100), {}, rrsSeq(100, 412), {}, rrsSeq(512, 10)}, true},
		{"irregular chunk sizes across several sectors", [][]byte{rrsSeq(0, 1), rrsSeq(1, 1023), rrsSeq(1024, 7), rrsSeq(1031, 600)}, true},
		{"full record (1600 sectors, sector-aligned)", [][]byte{rrsSeq(0, bdos.RecordSize)}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			mac, got := feedZ80Sink(t, c.chunks, c.finish, record)
			want := goSink(c.chunks, c.finish)
			if len(got) != len(want) {
				t.Fatalf("Z80 emitted %d sectors, Go authority %d", len(got), len(want))
			}

			// RRS_TOTAL (32-bit image-size counter) == the bytes streamed.
			wantTotal := 0
			for _, ch := range c.chunks {
				wantTotal += len(ch)
			}
			if gotTotal := rrsTotal(t, mac); gotTotal != wantTotal {
				t.Errorf("RRS_TOTAL = %d, want %d", gotTotal, wantTotal)
			}
			for i := range want {
				if got[i].Record != record {
					t.Errorf("sector[%d] record = %d, want %d", i, got[i].Record, record)
				}
				if got[i].LinearSec != want[i].LinearSec {
					t.Errorf("sector[%d] linear = %d, want %d", i, got[i].LinearSec, want[i].LinearSec)
				}
				if !bytes.Equal(got[i].Data[:], want[i].Data[:]) {
					t.Errorf("sector[%d] data mismatch", i)
				}
			}
		})
	}
}

// TestRawRecordSinkRoundTrip streams an image with a non-aligned tail, then reads
// every written sector back via HRSAD (bdos_read_sector) and reconstructs the
// image — proving the bytes landed where a later read (the boot/serve path) finds
// them. The final sector is zero-padded, so the reconstruction equals the source
// padded up to a sector boundary.
func TestRawRecordSinkRoundTrip(t *testing.T) {
	const record = 4
	src := rrsSeq(0, 512*2+200) // 2 full sectors + a 200-byte tail → 3 sectors after finish
	mac, writes := feedZ80Sink(t, [][]byte{src}, true, record)
	if len(writes) != 3 {
		t.Fatalf("emitted %d sectors, want 3", len(writes))
	}

	// Reconstruct by reading each linear sector back through the real HRSAD path.
	var got []byte
	for i := 0; i < len(writes); i++ {
		linear := writes[i].LinearSec
		track := linear / 10
		sector := linear%10 + 1
		mac.Write(symAddr(t, mac, "BD_READ_TRACK"), []byte{byte(track)})
		mac.Write(symAddr(t, mac, "BD_READ_SECTOR"), []byte{byte(sector)})
		if _, err := mac.Call("bdos_read_sector"); err != nil {
			t.Fatalf("bdos_read_sector(linear=%d): %v", linear, err)
		}
		got = append(got, mac.Read(symAddr(t, mac, "BD_READ_BUF"), 512)...)
	}

	want := make([]byte, len(writes)*512)
	copy(want, src) // remainder stays zero — the finish zero-pad
	if !bytes.Equal(got, want) {
		t.Errorf("round-trip mismatch: reconstructed %d bytes differ from the zero-padded source", len(got))
	}
}
