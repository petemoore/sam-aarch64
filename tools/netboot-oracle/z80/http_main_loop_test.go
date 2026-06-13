// http_main_loop_test.go — Brick 6 host-verification of the rx-driven download
// loop in src/netboot/http_main.asm (prov_first / prov_onframe / prov_next): the
// Z80 port of the Go authority tools/netboot-oracle/http/provision.go::Provisioner
// (.First / .OnFrame / .Next).
//
// Each test drives the Z80 loop and the Go Provisioner in LOCKSTEP over the same
// injected frames, asserting at every step that the Z80's transmitted frame and
// its PROV_STATUS equal the Go Provisioner's tx and Status byte-for-byte — the
// per-file ARP / SYN / ACK+GET / data-ACK / FIN-ACK cadence, the per-file
// ephemeral port + ISS (BasePort+i, BaseISS+i*ISSStride), and the FileDone vs
// AllDone status edge. After the run it cross-checks the per-file store double
// (PROV_STORE_OFFS slices of CONN_SINK_OUT, names, verdicts) against the Go
// MemStore + FileResult verdicts.
//
// The download plan is built from the Z80's OWN manifest read-back (fw_plan_path
// / fw_manifest_entry) so the GET paths + names are the Z80's, not a second source
// of truth — this test isolates the LOOP logic (the manifest content is gated by
// fw_source_test.go). Bodies are synthetic; because prov_start pins each file's
// REAL manifest hash, the test overrides CONN_PINNED_HASH to the served body's
// hash per file (the pin-copy itself is Brick 4-verified), so the verdict tracks
// the served bytes — true for clean files, false for a corrupted one.
package z80_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	nbhttp "github.com/petemoore/sam-aarch64/tools/netboot-oracle/http"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const provWindow = 64 // streaming flush window (> any HTTP/1.0 header)

// provFirst runs prov_first and returns the broadcast ARP frame it transmitted.
func provFirst(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	return provARPCall(t, mac, enc, "prov_first")
}

// provNext runs prov_next and returns the next file's broadcast ARP frame.
func provNext(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) []byte {
	t.Helper()
	return provARPCall(t, mac, enc, "prov_next")
}

// provARPCall runs a routine that transmits exactly one frame (the ARP) and
// returns it, checking BC == the wire frame length.
func provARPCall(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, entry string) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	res, err := mac.Call(entry)
	if err != nil {
		t.Fatalf("call %s: %v", entry, err)
	}
	tx := enc.TXFrames()
	if len(tx) != before+1 {
		t.Fatalf("%s transmitted %d frames, want 1", entry, len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("%s BC=%d but the wire frame is %d bytes", entry, res.BC, len(out))
	}
	return out
}

// provOnframe injects rx and runs one prov_onframe, returning the transmitted
// frame (or nil) and PROV_STATUS.
func provOnframe(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, rx []byte) (tx []byte, status byte) {
	t.Helper()
	before := len(enc.TXFrames())
	enc.InjectRX(rx)
	res, err := mac.Call("prov_onframe")
	if err != nil {
		t.Fatalf("call prov_onframe: %v", err)
	}
	status = mac.Read(symAddr(t, mac, "PROV_STATUS"), 1)[0]
	frames := enc.TXFrames()
	if res.BC == 0 {
		if len(frames) != before {
			t.Fatalf("prov_onframe BC=0 but transmitted a frame")
		}
		return nil, status
	}
	if len(frames) != before+1 {
		t.Fatalf("prov_onframe transmitted %d frames, want 1", len(frames)-before)
	}
	out := frames[len(frames)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("prov_onframe BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out, status
}

// provSrvSegZ80 builds a server→client TCP segment to the file's per-file
// ephemeral client port (the multi-file analogue of serverSeg, which is fixed to
// tcpConnCfg.clientPort).
func provSrvSegZ80(clientPort uint16, seq, ack uint32, flags uint8, payload []byte) []byte {
	c := tcpConnCfg
	return tcp.BuildSegment(tcp.Segment{
		DstMAC: c.clientMAC, SrcMAC: c.serverMAC, SrcIP: c.serverIP, DstIP: c.clientIP,
		SrcPort: c.serverPort, DstPort: clientPort, Seq: seq, Ack: ack,
		Flags: flags, Window: 5840, Payload: payload,
	})
}

// setupProvLoop loads http_main, fills the connection config + the per-file
// BASE_PORT/BASE_ISS policy, turns on streaming, sets the file count, and returns
// the machine, the emulated NIC, and the matching Go ProvisionConfig.
func setupProvLoop(t *testing.T, n int) (*z80h.Machine, *z80h.ENC28J60, nbhttp.ProvisionConfig) {
	t.Helper()
	mac := loadHTTPMain(t)
	fillTCPConnConfig(t, mac) // client/server MAC + IP + server port + (unused) ISS
	writeProvConfig(t, mac)   // BASE_PORT / BASE_ISS — the per-file policy
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	mac.WriteU16LE(symAddr(t, mac, "PROV_FILE_COUNT"), uint16(n))
	mac.Write(symAddr(t, mac, "CONN_SINK_ENABLED"), []byte{1})
	mac.WriteU16LE(symAddr(t, mac, "CONN_FLUSH_WINDOW"), provWindow)

	c := tcpConnCfg
	cfg := nbhttp.ProvisionConfig{
		ClientMAC: c.clientMAC, ClientIP: c.clientIP,
		ServerIP: c.serverIP, ServerPort: c.serverPort,
		BasePort: fClientPrt, BaseISS: fISS, Window: provWindow,
	}
	return mac, enc, cfg
}

// synthBody returns file i's synthetic body — a size mix: a multi-window file, a
// tiny single-flush file, and a header-sized one, so the streaming + header-skip
// is exercised across window boundaries.
func synthBody(i int) []byte {
	switch i {
	case 0:
		return []byte("the broadcom licence text, several lines worth!")
	case 1:
		return bytes.Repeat([]byte{0xAB, 0xCD}, 70) // 140 bytes — multi-window
	case 2:
		return []byte("fixup") // tiny — flushed only at the FIN
	default:
		return []byte(fmt.Sprintf("synthetic-firmware-body-for-file-%d", i))
	}
}

// buildZ80Plan reads file 0..n-1's request path + output name back out of the Z80
// manifest (fw_plan_path / fw_manifest_entry) and pairs each with a synthetic body
// pinned to that body's SHA-256 — the download plan the Go Provisioner is driven
// against, sourced from the Z80 itself.
func buildZ80Plan(t *testing.T, mac *z80h.Machine, n int) ([]nbhttp.FetchSpec, [][]byte) {
	t.Helper()
	fwHost := readCStr(t, mac, "FW_HOST")
	plan := make([]nbhttp.FetchSpec, n)
	bodies := make([][]byte, n)
	for i := 0; i < n; i++ {
		bodies[i] = synthBody(i)
		if _, err := mac.CallEntry("fw_plan_path", z80h.Entry{BC: uint16(i)}); err != nil {
			t.Fatalf("fw_plan_path(%d): %v", i, err)
		}
		path := readCStr(t, mac, "FW_PATH")
		res, err := mac.CallEntry("fw_manifest_entry", z80h.Entry{BC: uint16(i)})
		if err != nil {
			t.Fatalf("fw_manifest_entry(%d): %v", i, err)
		}
		namePtr := binary.LittleEndian.Uint16(mac.Read(res.BC, 2)) // record+0 = name ptr
		name := readCStrAt(mac, namePtr)
		plan[i] = nbhttp.FetchSpec{Name: name, Path: path, Host: fwHost, SHA256: sha256.Sum256(bodies[i])}
	}
	return plan, bodies
}

// provStep injects rx into both the Z80 loop and the Go Provisioner and asserts
// the transmitted frame + status match byte-for-byte. Returns the Z80 status.
func provStep(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, p *nbhttp.Provisioner, rx []byte, label string) byte {
	t.Helper()
	ztx, zst := provOnframe(t, mac, enc, rx)
	gtx, gst := p.OnFrame(rx)
	if !bytes.Equal(ztx, gtx) {
		t.Fatalf("%s: tx != Go Provisioner\n  z80 %x\n  go  %x", label, ztx, gtx)
	}
	if zst != byte(gst) {
		t.Fatalf("%s: PROV_STATUS = %d, want Go Status %d", label, zst, gst)
	}
	return zst
}

// driveProvZ80 plays the in-process server for the whole plan, driving the Z80
// loop and the Go Provisioner in lockstep (ARP → SYN/SYN-ACK → GET → response
// segments → FIN) and cross-checking every frame + status. corruptFile >= 0 flips
// the first byte of that file's served body so its verdict fails. segSize bounds
// the server's data-segment size.
func driveProvZ80(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, p *nbhttp.Provisioner,
	cfg nbhttp.ProvisionConfig, plan []nbhttp.FetchSpec, bodies [][]byte, segSize, corruptFile int) {
	t.Helper()
	c := tcpConnCfg

	if ztx, gtx := provFirst(t, mac, enc), p.First(); !bytes.Equal(ztx, gtx) {
		t.Fatalf("First: ARP != Go Provisioner\n  z80 %x\n  go  %x", ztx, gtx)
	}

	for fileIdx := 0; ; fileIdx++ {
		spec := plan[fileIdx]
		clientPort := cfg.BasePort + uint16(fileIdx)
		clientISS := cfg.BaseISS + uint32(fileIdx)*nbhttp.ISSStride
		srvISS := uint32(0x90000000) + uint32(fileIdx)*0x1000

		// prov_start pinned this file's REAL manifest hash; override it to the
		// served body's hash so the verdict tracks the bytes (Brick 5 already
		// proved the pin compare; the pin copy itself is Brick 4-verified).
		pinHash(t, mac, spec.SHA256)

		body := append([]byte{}, bodies[fileIdx]...)
		if fileIdx == corruptFile {
			body[0] ^= 0xFF
		}
		resp := append([]byte(fmt.Sprintf("HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n", len(body))), body...)

		// ARP reply -> SYN.
		provStep(t, mac, enc, p,
			frame.BuildARPReply(c.serverMAC, c.clientMAC, c.serverIP, c.clientIP),
			fmt.Sprintf("file%d arp->syn", fileIdx))

		// SYN-ACK -> the handshake-completing ACK+GET segment.
		provStep(t, mac, enc, p,
			provSrvSegZ80(clientPort, srvISS, clientISS+1, tcp.FlagSYN|tcp.FlagACK, nil),
			fmt.Sprintf("file%d syn-ack->get", fileIdx))

		// Response body in bounded segments, each ACKed.
		srvAck := clientISS + 1 + uint32(len(nbhttp.BuildRequest(spec.Path, spec.Host)))
		srvSeq := srvISS + 1
		for off := 0; off < len(resp); off += segSize {
			end := off + segSize
			if end > len(resp) {
				end = len(resp)
			}
			provStep(t, mac, enc, p,
				provSrvSegZ80(clientPort, srvSeq, srvAck, tcp.FlagPSH|tcp.FlagACK, resp[off:end]),
				fmt.Sprintf("file%d data@%d", fileIdx, off))
			srvSeq += uint32(end - off)
		}

		// Server FIN -> FIN-ACK + the file/all-done status.
		zst := provStep(t, mac, enc, p,
			provSrvSegZ80(clientPort, srvSeq, srvAck, tcp.FlagFIN|tcp.FlagACK, nil),
			fmt.Sprintf("file%d fin", fileIdx))

		if fileIdx == len(plan)-1 {
			if zst != byte(nbhttp.StatusAllDone) {
				t.Fatalf("last file %d: PROV_STATUS = %d, want AllDone", fileIdx, zst)
			}
			return
		}
		if zst != byte(nbhttp.StatusFileDone) {
			t.Fatalf("file %d: PROV_STATUS = %d, want FileDone", fileIdx, zst)
		}
		if ztx, gtx := provNext(t, mac, enc), p.Next(); !bytes.Equal(ztx, gtx) {
			t.Fatalf("file %d Next: ARP != Go Provisioner\n  z80 %x\n  go  %x", fileIdx, ztx, gtx)
		}
	}
}

// readZ80Store reads the per-file store double back: each file's CONN_SINK_OUT
// slice (demarcated by PROV_STORE_OFFS), its recorded name, and its verdict.
func readZ80Store(t *testing.T, mac *z80h.Machine, n int) (slices [][]byte, names []string, verdicts []byte) {
	t.Helper()
	if got := readWord(t, mac, "PROV_STORE_COUNT"); int(got) != n {
		t.Fatalf("PROV_STORE_COUNT = %d, want %d", got, n)
	}
	offsAddr := symAddr(t, mac, "PROV_STORE_OFFS")
	off := func(i int) uint16 { return binary.LittleEndian.Uint16(mac.Read(offsAddr+uint16(i*2), 2)) }
	sinkAddr := symAddr(t, mac, "CONN_SINK_OUT")
	namesAddr := symAddr(t, mac, "PROV_STORE_NAMES")
	slices = make([][]byte, n)
	names = make([]string, n)
	for i := 0; i < n; i++ {
		slices[i] = mac.Read(sinkAddr+off(i), int(off(i+1)-off(i)))
		ptr := binary.LittleEndian.Uint16(mac.Read(namesAddr+uint16(i*2), 2))
		names[i] = readCStrAt(mac, ptr)
	}
	verdicts = mac.Read(symAddr(t, mac, "PROV_FILE_VERDICTS"), n)
	return slices, names, verdicts
}

// verdictByte maps a Go FileResult.Verified bool to the Z80 verdict byte.
func verdictByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// TestProvisionMultiFileZ80: a three-file plan fetches each file end to end, every
// wire frame + status matching the Go Provisioner, each streamed body stored under
// its name in fetch order (header stripped), and each pinned hash verifying.
func TestProvisionMultiFileZ80(t *testing.T) {
	const n = 3
	mac, enc, cfg := setupProvLoop(t, n)
	plan, bodies := buildZ80Plan(t, mac, n)
	store := nbhttp.NewMemStore()
	p := nbhttp.NewProvisioner(cfg, plan, store)

	driveProvZ80(t, mac, enc, p, cfg, plan, bodies, 11, -1) // small segs straddle windows

	slices, names, verdicts := readZ80Store(t, mac, n)
	res := p.Results()
	for i := 0; i < n; i++ {
		if names[i] != store.Order[i] {
			t.Errorf("name[%d] = %q, want %q (Go store order)", i, names[i], store.Order[i])
		}
		if !bytes.Equal(slices[i], store.Files[names[i]]) {
			t.Errorf("file %d stored bytes != Go MemStore[%q]\n  z80 %x\n  go  %x", i, names[i], slices[i], store.Files[names[i]])
		}
		if !bytes.Equal(slices[i], bodies[i]) {
			t.Errorf("file %d stored bytes != served body", i)
		}
		if bytes.Contains(slices[i], []byte("HTTP/1.0")) {
			t.Errorf("file %d still contains the HTTP header", i)
		}
		if verdicts[i] != verdictByte(res[i].Verified) {
			t.Errorf("verdict[%d] = %d, want %d (Go Verified=%v)", i, verdicts[i], verdictByte(res[i].Verified), res[i].Verified)
		}
		if verdicts[i] != 1 {
			t.Errorf("file %d should verify (verdict %d)", i, verdicts[i])
		}
	}
}

// TestProvisionHashMismatchZ80: a corrupted body still streams and stores, but its
// verdict is 0 while its neighbours stay 1 — verdicts [1,0,1], matching the Go
// Provisioner. The integrity check that makes the untrusted-proxy fetch safe.
func TestProvisionHashMismatchZ80(t *testing.T) {
	const n = 3
	mac, enc, cfg := setupProvLoop(t, n)
	plan, bodies := buildZ80Plan(t, mac, n)
	store := nbhttp.NewMemStore()
	p := nbhttp.NewProvisioner(cfg, plan, store)

	driveProvZ80(t, mac, enc, p, cfg, plan, bodies, 7, 1) // corrupt file index 1

	slices, _, verdicts := readZ80Store(t, mac, n)
	res := p.Results()
	want := []byte{1, 0, 1}
	for i := 0; i < n; i++ {
		if verdicts[i] != want[i] {
			t.Errorf("verdict[%d] = %d, want %d", i, verdicts[i], want[i])
		}
		if verdicts[i] != verdictByte(res[i].Verified) {
			t.Errorf("verdict[%d] = %d != Go Verified=%v", i, verdicts[i], res[i].Verified)
		}
	}
	// The corrupted bytes still landed in the store (the verdict rejects them, not
	// the write — a caller acts on the verdict).
	corrupted := append([]byte{}, bodies[1]...)
	corrupted[0] ^= 0xFF
	if !bytes.Equal(slices[1], corrupted) {
		t.Errorf("file 1 stored bytes != the corrupted served body")
	}
}

// TestProvisionSingleFileZ80: the degenerate one-file plan reports AllDone directly
// (no intervening FileDone / Next), matching the Go Provisioner.
func TestProvisionSingleFileZ80(t *testing.T) {
	const n = 1
	mac, enc, cfg := setupProvLoop(t, n)
	plan, bodies := buildZ80Plan(t, mac, n)
	store := nbhttp.NewMemStore()
	p := nbhttp.NewProvisioner(cfg, plan, store)

	driveProvZ80(t, mac, enc, p, cfg, plan, bodies, 5, -1)

	slices, _, verdicts := readZ80Store(t, mac, n)
	if verdicts[0] != 1 {
		t.Errorf("single-file verdict = %d, want 1", verdicts[0])
	}
	if !bytes.Equal(slices[0], bodies[0]) {
		t.Errorf("single-file stored bytes != served body")
	}
	if len(p.Results()) != 1 || !p.Results()[0].Verified {
		t.Fatalf("Go single-file plan did not verify: %+v", p.Results())
	}
}
