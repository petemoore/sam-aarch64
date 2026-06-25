// body_sink_test.go — the i100 host-verification of the HTTP-header-skip sink
// adapter (src/netboot/body_sink.asm, the bodySink Z80 port). It assembles the
// standalone module under the koron-go/z80 harness, feeds body_sink_write a
// sequence of chunks, and asserts the forwarded body bytes + the per-Write
// boundaries recorded by the body_dst_write test double match the Go authority
// http.NewBodySink (wrapping a tcp.ChunkSink) fed the identical chunks — the same
// "drive both with one input, compare byte-for-byte" pattern as the other leaves.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	bodySinkBinPath = "../../../build/netboot_body_sink.bin"
	bodySinkMapPath = "../../../build/netboot_body_sink.map"
)

func loadBodySink(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(bodySinkBinPath); err != nil {
		t.Fatalf("body_sink binary not built (%s); run `make netboot-body-sink`", bodySinkBinPath)
	}
	mac, err := z80h.Load(bodySinkBinPath, bodySinkMapPath)
	if err != nil {
		t.Fatalf("load body_sink: %v", err)
	}
	return mac
}

// runZ80BodySink feeds chunks through the Z80 body_sink_write on a fresh machine
// and returns the recorded forwarded body bytes + the per-Write chunk lengths.
func runZ80BodySink(t *testing.T, chunks [][]byte) (out []byte, chunkLens []int) {
	t.Helper()
	mac := loadBodySink(t)
	in, err := mac.Sym("BODY_IN")
	if err != nil {
		t.Fatalf("%v", err)
	}
	for i, ch := range chunks {
		if len(ch) > 4096 {
			t.Fatalf("chunk %d is %d bytes, exceeds BODY_IN (4096)", i, len(ch))
		}
		mac.Write(in, ch)
		if _, err := mac.CallEntry("body_sink_write", z80h.Entry{HL: in, BC: uint16(len(ch))}); err != nil {
			t.Fatalf("body_sink_write(chunk %d): %v", i, err)
		}
	}

	outLen := readU16(t, mac, "BODY_OUT_LEN")
	outAddr, err := mac.Sym("BODY_OUT")
	if err != nil {
		t.Fatalf("%v", err)
	}
	out = mac.Read(outAddr, int(outLen))

	count := readU16(t, mac, "BODY_CHUNK_COUNT")
	chunksAddr, err := mac.Sym("BODY_CHUNKS")
	if err != nil {
		t.Fatalf("%v", err)
	}
	raw := mac.Read(chunksAddr, int(count)*2)
	chunkLens = make([]int, count)
	for i := 0; i < int(count); i++ {
		chunkLens[i] = int(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return out, chunkLens
}

// readU16 reads a little-endian 16-bit value at a named symbol.
func readU16(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return binary.LittleEndian.Uint16(mac.Read(a, 2))
}

// goBodySink runs the Go authority over the same chunks and returns the forwarded
// body bytes + per-Write chunk lengths (the ChunkSink boundaries).
func goBodySink(chunks [][]byte) (out []byte, chunkLens []int) {
	sink := &tcp.ChunkSink{}
	bs := http.NewBodySink(sink)
	for _, ch := range chunks {
		bs.Write(ch)
	}
	out = sink.Bytes()
	chunkLens = make([]int, len(sink.Chunks))
	for i, c := range sink.Chunks {
		chunkLens[i] = len(c)
	}
	return out, chunkLens
}

// TestBodySink: the Z80 body_sink_write matches the Go bodySink byte-for-byte —
// the forwarded body and the per-Write boundaries are identical — across the
// header-in-one-chunk case, the header-then-body split, multi-window bodies, and
// the degenerate no-terminator / split-header cases (where both must drop and so
// agree on an empty/short body, the documented header-fits-in-first-window
// limit). The body never contains the HTTP header.
func TestBodySink(t *testing.T) {
	const head = "HTTP/1.0 200 OK\r\nContent-Length: 16\r\n\r\n"
	const shortHead = "HTTP/1.0 200 OK\r\n\r\n"

	cases := []struct {
		name   string
		chunks [][]byte
	}{
		{
			// Header + body in a single chunk: forward the body, header dropped.
			name:   "header_and_body_one_chunk",
			chunks: [][]byte{[]byte(head + "firmware-bytes!")},
		},
		{
			// Header alone (empty body in its chunk), then the body in the next:
			// the header chunk forwards nothing, the body chunk forwards whole.
			name:   "header_then_body",
			chunks: [][]byte{[]byte(shortHead), []byte("the-firmware-blob")},
		},
		{
			// Header + first body bytes share chunk 1; more body in 2 and 3 — a
			// multi-window body delivered over several flushes.
			name:   "header_with_body_then_more",
			chunks: [][]byte{[]byte(shortHead + "ABCD"), []byte("EFGHIJ"), []byte("KL")},
		},
		{
			// An empty chunk after the header is skipped on both sides (the
			// len(chunk) > 0 guard) and does not record a Write.
			name:   "empty_chunk_after_header",
			chunks: [][]byte{[]byte(shortHead + "X"), {}, []byte("Y")},
		},
		{
			// Degenerate: a chunk with a status line but no "\r\n\r\n" terminator
			// is dropped; both sides forward nothing.
			name:   "no_terminator_dropped",
			chunks: [][]byte{[]byte("HTTP/1.0 200 OK\r\nincomplete-header")},
		},
		{
			// Degenerate: the header straddles two chunks. bodySink parses each
			// chunk independently, so chunk 1 (no terminator) drops and chunk 2
			// (no status-line space) drops too — the body is lost. Both sides
			// agree (the documented header-fits-in-first-window limit).
			name:   "split_header_both_drop",
			chunks: [][]byte{[]byte("HTTP/1.0 200 OK\r\n"), []byte("\r\nlost-body")},
		},
		{
			// No data at all: nothing forwarded.
			name:   "empty_only",
			chunks: [][]byte{{}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOut, gotLens := runZ80BodySink(t, tc.chunks)
			wantOut, wantLens := goBodySink(tc.chunks)

			if !bytes.Equal(gotOut, wantOut) {
				t.Errorf("forwarded body = %q, want %q", gotOut, wantOut)
			}
			if fmt.Sprint(gotLens) != fmt.Sprint(wantLens) {
				t.Errorf("per-Write chunk lengths = %v, want %v", gotLens, wantLens)
			}
			if bytes.Contains(gotOut, []byte("HTTP/1.0")) {
				t.Errorf("forwarded body still contains the HTTP header: %q", gotOut)
			}
		})
	}
}
