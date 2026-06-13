package http

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

// provSrvSeg builds a server-side TCP segment to the client's per-file ephemeral
// port (the multi-file analogue of fServerSeg, which is fixed to fClientPrt).
func provSrvSeg(clientPort uint16, seq, ack uint32, flags uint8, payload []byte) []byte {
	return tcp.BuildSegment(tcp.Segment{
		DstMAC: fClientMAC, SrcMAC: fServerMAC, SrcIP: fServerIP, DstIP: fClientIP,
		SrcPort: fServerPrt, DstPort: clientPort, Seq: seq, Ack: ack,
		Flags: flags, Window: 5840, Payload: payload,
	})
}

// provPlan builds a synthetic download plan: one FetchSpec per (name, body),
// each pinned to the SHA-256 of its body. Returns the plan and a name->body map.
func provPlan(files []struct{ name, body string }) ([]FetchSpec, map[string][]byte) {
	plan := make([]FetchSpec, len(files))
	bodies := make(map[string][]byte, len(files))
	for i, f := range files {
		body := []byte(f.body)
		plan[i] = FetchSpec{
			Name: f.name, Path: "/raspberrypi/firmware/abc/boot/" + f.name,
			Host: GithubRawHost, SHA256: sha256.Sum256(body),
		}
		bodies[f.name] = body
	}
	return plan, bodies
}

// driveProvision plays the in-process server for a whole plan, driving the
// Provisioner file by file (ARP → SYN/SYN-ACK → GET → response segments → FIN),
// returning the per-file body bytes actually streamed past the header skip. If
// corruptFile >= 0, that file's served body is mutated (its first byte flipped)
// so its hash will not match — the bytes still stream and store, only the verdict
// flips. segSize bounds the server's data segment size.
func driveProvision(t *testing.T, p *Provisioner, cfg ProvisionConfig, plan []FetchSpec,
	bodies map[string][]byte, segSize, corruptFile int) {
	t.Helper()

	tx := p.First()
	for fileIdx := 0; ; fileIdx++ {
		spec := plan[fileIdx]
		clientPort := cfg.BasePort + uint16(fileIdx)
		clientISS := cfg.BaseISS + uint32(fileIdx)*ISSStride
		srvISS := uint32(0x90000000) + uint32(fileIdx)*0x1000

		body := append([]byte{}, bodies[spec.Name]...)
		if fileIdx == corruptFile {
			body[0] ^= 0xFF
		}
		resp := append([]byte(fmt.Sprintf("HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n", len(body))), body...)

		// tx is this file's broadcast ARP request.
		if _, _, ok := frame.ParseARPReply(tx); ok {
			t.Fatalf("file %d: First/Next should be an ARP request, not a reply", fileIdx)
		}

		// ARP reply -> SYN.
		tx, st := p.OnFrame(frame.BuildARPReply(fServerMAC, fClientMAC, fServerIP, fClientIP))
		if st != StatusContinue {
			t.Fatalf("file %d: status %d after ARP reply, want Continue", fileIdx, st)
		}
		if s, ok := tcp.ParseSegment(tx); !ok || s.Flags != tcp.FlagSYN || s.Seq != clientISS {
			t.Fatalf("file %d: expected SYN seq=%#x, got flags=%#x seq=%#x ok=%v", fileIdx, clientISS, s.Flags, s.Seq, ok)
		}

		// SYN-ACK -> ACK+GET.
		tx, st = p.OnFrame(provSrvSeg(clientPort, srvISS, clientISS+1, tcp.FlagSYN|tcp.FlagACK, nil))
		if st != StatusContinue {
			t.Fatalf("file %d: status %d after SYN-ACK, want Continue", fileIdx, st)
		}
		gs, ok := tcp.ParseSegment(tx)
		if !ok || gs.Flags != tcp.FlagPSH|tcp.FlagACK {
			t.Fatalf("file %d: expected PSH|ACK GET, got flags=%#x ok=%v", fileIdx, gs.Flags, ok)
		}
		if !bytes.Equal(gs.Payload, BuildRequest(spec.Path, spec.Host)) {
			t.Fatalf("file %d: GET payload = %q, want request for %q", fileIdx, gs.Payload, spec.Path)
		}
		srvAck := clientISS + 1 + uint32(len(BuildRequest(spec.Path, spec.Host)))

		// Response body in bounded segments, each ACKed.
		srvSeq := srvISS + 1
		for off := 0; off < len(resp); off += segSize {
			end := off + segSize
			if end > len(resp) {
				end = len(resp)
			}
			seg := resp[off:end]
			tx, st = p.OnFrame(provSrvSeg(clientPort, srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, seg))
			if st != StatusContinue {
				t.Fatalf("file %d: status %d mid-body, want Continue", fileIdx, st)
			}
			if as, ok := tcp.ParseSegment(tx); !ok || as.Flags&tcp.FlagACK == 0 {
				t.Fatalf("file %d: expected an ACK for body segment, got flags=%#x ok=%v", fileIdx, as.Flags, ok)
			}
			srvSeq += uint32(len(seg))
		}

		// Server FIN -> FIN-ACK + the file/all-done status.
		tx, st = p.OnFrame(provSrvSeg(clientPort, srvSeq, srvAck, tcp.FlagFIN|tcp.FlagACK, nil))
		if fs, ok := tcp.ParseSegment(tx); !ok || fs.Flags&tcp.FlagFIN == 0 {
			t.Fatalf("file %d: expected FIN-ACK teardown, got flags=%#x ok=%v", fileIdx, fs.Flags, ok)
		}
		if fileIdx == len(plan)-1 {
			if st != StatusAllDone {
				t.Fatalf("last file %d: status %d, want AllDone", fileIdx, st)
			}
			return
		}
		if st != StatusFileDone {
			t.Fatalf("file %d: status %d, want FileDone", fileIdx, st)
		}
		tx = p.Next()
	}
}

func provTestConfig() ProvisionConfig {
	return ProvisionConfig{
		ClientMAC: fClientMAC, ClientIP: fClientIP,
		ServerIP: fServerIP, ServerPort: fServerPrt,
		BasePort: fClientPrt, BaseISS: fISS, Window: 32,
	}
}

// TestProvisionMultiFile: a three-file plan fetches each file end to end, each
// streamed body is stored under its name in fetch order, and each file's pinned
// hash verifies.
func TestProvisionMultiFile(t *testing.T) {
	files := []struct{ name, body string }{
		{"LICENCE.broadcom", "the broadcom licence text, several lines worth"},
		{"bootcode.bin", string(bytes.Repeat([]byte{0xAB, 0xCD}, 70))}, // 140 bytes, multi-window
		{"fixup.dat", "fixup"}, // tiny, single sub-window body
	}
	plan, bodies := provPlan(files)
	store := NewMemStore()
	cfg := provTestConfig()
	p := NewProvisioner(cfg, plan, store)

	// Small segments so windows straddle segment boundaries across files.
	driveProvision(t, p, cfg, plan, bodies, 11, -1)

	if got, want := store.Order, []string{"LICENCE.broadcom", "bootcode.bin", "fixup.dat"}; !equalStrs(got, want) {
		t.Errorf("store order = %v, want %v", got, want)
	}
	for name, want := range bodies {
		if !bytes.Equal(store.Files[name], want) {
			t.Errorf("stored %q = %d bytes, want the %d-byte body", name, len(store.Files[name]), len(want))
		}
		if bytes.Contains(store.Files[name], []byte("HTTP/1.0")) {
			t.Errorf("stored %q still contains the HTTP header", name)
		}
	}
	res := p.Results()
	if len(res) != len(plan) {
		t.Fatalf("got %d results, want %d", len(res), len(plan))
	}
	for i, r := range res {
		if r.Name != plan[i].Name {
			t.Errorf("result %d name = %q, want %q", i, r.Name, plan[i].Name)
		}
		if !r.Verified {
			t.Errorf("file %q did not verify (got hash %x, want %x)", r.Name, r.Got, plan[i].SHA256)
		}
		if r.Got != sha256.Sum256(bodies[r.Name]) {
			t.Errorf("file %q computed hash %x, want sha256(body)", r.Name, r.Got)
		}
	}
}

// TestProvisionHashMismatch: a corrupted body still streams and stores, but its
// verify verdict is false while its neighbours stay true — the integrity check
// that makes the untrusted-proxy fetch safe (q15 option c).
func TestProvisionHashMismatch(t *testing.T) {
	files := []struct{ name, body string }{
		{"a.bin", "alpha-body-bytes"},
		{"b.bin", "beta-body-bytes-here"}, // this one is served corrupted
		{"c.bin", "gamma-body-bytes"},
	}
	plan, bodies := provPlan(files)
	store := NewMemStore()
	cfg := provTestConfig()
	p := NewProvisioner(cfg, plan, store)

	driveProvision(t, p, cfg, plan, bodies, 7, 1) // corrupt file index 1

	res := p.Results()
	if res[0].Verified != true || res[2].Verified != true {
		t.Errorf("neighbour files should verify: res[0]=%v res[2]=%v", res[0].Verified, res[2].Verified)
	}
	if res[1].Verified {
		t.Errorf("corrupted file b.bin verified — the pinned-hash check failed to reject it")
	}
	// The corrupted bytes still landed in the store (the verdict, not the write,
	// is what rejects them; a caller acts on Results()).
	if bytes.Equal(store.Files["b.bin"], bodies["b.bin"]) {
		t.Errorf("served body for b.bin was not actually corrupted in the test")
	}
}

// TestProvisionSingleFile: the loop's degenerate one-file case reports AllDone
// directly with no intervening FileDone/Next.
func TestProvisionSingleFile(t *testing.T) {
	plan, bodies := provPlan([]struct{ name, body string }{{"start.elf", "kernelbytes"}})
	store := NewMemStore()
	cfg := provTestConfig()
	p := NewProvisioner(cfg, plan, store)

	driveProvision(t, p, cfg, plan, bodies, 5, -1)

	if len(p.Results()) != 1 || !p.Results()[0].Verified {
		t.Fatalf("single-file plan did not verify: %+v", p.Results())
	}
	if !bytes.Equal(store.Files["start.elf"], bodies["start.elf"]) {
		t.Errorf("stored single file mismatch")
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
