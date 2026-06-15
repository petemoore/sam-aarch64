package http

import (
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tcp"
)

// Provisioner is the Go authority for the multi-file firmware download — the
// per-file fetch-orchestration loop the Z80 src/netboot/http_main.asm will port.
// It walks a download plan (Manifest.Plan) and fetches each selected file end to
// end, one TCP connection per file: ARP → handshake → GET → stream the body
// (HTTP header skipped) through a SHA-256 verify into the storage Store, then
// records whether the streamed bytes matched the file's pinned hash and advances
// to the next file.
//
// It composes the already-host-verified pieces and adds only the sequencing:
//   - http.Fetcher  — the single-file fetch (the one-tx-per-rx phase machine),
//   - tcp.HashingSink — the streamed-body SHA-256 verify (i100, q15 option c),
//   - the Store     — the per-file destination (a host stub here; on real Trinity
//     the B-DOS bounded-HSAVE spanning write, q16 / i93).
//
// It is rx-driven exactly like Fetcher, so the Z80 port is a single packet loop:
//
//	tx := p.First()                 // file 0's ARP — the first frame on the wire
//	for {
//	    drv_write(tx)
//	    rx := drv_read()
//	    tx, st := p.OnFrame(rx)
//	    switch st {
//	    case StatusContinue: // tx is this file's reply (or nil); keep going
//	    case StatusFileDone: drv_write(tx); tx = p.Next() // send FIN-ACK, start next file's ARP
//	    case StatusAllDone:  drv_write(tx); done           // send the last FIN-ACK
//	    }
//	}
//
// The one cross-file subtlety vs a single fetch: each file is a fresh TCP
// connection, so the client's ephemeral port and ISS must differ per file —
// BasePort+i and BaseISS+i*ISSStride — or successive connections to the same
// server would collide. This is the byte-for-byte porting spec for http_main.asm.
//
// Verification: host-verifiable end-to-end over the in-process server (the wire
// side — the per-file ARP/SYN/GET/ACK cadence and the per-file hash verdict —
// asserted against this authority). NOT host-verifiable: the real B-DOS RST-8
// HSAVE write-out (the Store's hardware implementation), the ENC28J60 silicon,
// and a fetch against live cdn.githubraw.com — gated on real Trinity (CLAUDE.md
// §5). The Store seam is exactly where that hardware boundary sits: the loop,
// the header skip, and the hash verify are all host-verified; only where the
// verified bytes finally land is stubbed.
type Provisioner struct {
	cfg   ProvisionConfig
	plan  []FetchSpec
	store Store

	idx  int              // index of the file currently being fetched
	cur  *Fetcher         // the current file's single-file fetch driver
	hash *tcp.HashingSink // the current file's streamed-body verify wrapper
	res  []FileResult     // one verdict per completed file, in fetch order
}

// ISSStride is how far each successive file's connection advances the initial
// send sequence number, keeping per-file connections distinct. The Z80 http_main
// mirrors this exactly (file i uses BaseISS + i*ISSStride).
const ISSStride uint32 = 0x10000

// ProvisionConfig is the client identity plus the per-file port/ISS policy shared
// across every fetch in the plan.
type ProvisionConfig struct {
	ClientMAC  frame.MAC
	ClientIP   frame.IPv4
	ServerIP   frame.IPv4
	ServerPort uint16 // default 80
	BasePort   uint16 // file i uses the ephemeral source port BasePort+i
	BaseISS    uint32 // file i uses the initial send sequence BaseISS + i*ISSStride
	Window     int    // streaming flush window (0 selects the connection default)
}

// FileResult is the verdict for one fetched file: whether the streamed body's
// SHA-256 matched the pinned hash, plus the computed digest for diagnostics.
type FileResult struct {
	Name     string
	Verified bool
	Got      [32]byte
}

// Status is what OnFrame reports about the loop after processing one frame.
type Status int

const (
	// StatusContinue: still fetching the current file; tx is its reply (or nil).
	StatusContinue Status = iota
	// StatusFileDone: the current file completed (verdict recorded); tx is its
	// FIN-ACK. The caller sends tx, then calls Next() to start the next file.
	StatusFileDone
	// StatusAllDone: the last file completed (verdict recorded); tx is the final
	// FIN-ACK. The plan is exhausted.
	StatusAllDone
)

// NewProvisioner builds a download-plan driver writing each file into store.
func NewProvisioner(cfg ProvisionConfig, plan []FetchSpec, store Store) *Provisioner {
	if cfg.ServerPort == 0 {
		cfg.ServerPort = 80
	}
	return &Provisioner{cfg: cfg, plan: plan, store: store}
}

// First starts the first file's fetch and returns its first wire frame (the
// broadcast ARP request). The plan must be non-empty.
func (p *Provisioner) First() []byte {
	p.idx = 0
	return p.start(0)
}

// start opens file i in the store, wires its body sink through a SHA-256 verify,
// builds the file's fetch driver, and returns the fetch's first frame.
func (p *Provisioner) start(i int) []byte {
	spec := p.plan[i]
	p.hash = tcp.NewHashingSink(p.store.Begin(spec.Name))
	p.cur = NewFetcher(FetchConfig{
		ClientMAC:  p.cfg.ClientMAC,
		ClientIP:   p.cfg.ClientIP,
		ClientPort: p.cfg.BasePort + uint16(i),
		ServerIP:   p.cfg.ServerIP,
		ServerPort: p.cfg.ServerPort,
		ISS:        p.cfg.BaseISS + uint32(i)*ISSStride,
		Path:       spec.Path,
		Host:       spec.Host,
	})
	p.cur.StreamTo(p.hash, p.cfg.Window)
	return p.cur.First()
}

// OnFrame drives the current file's fetch with one received frame. While the file
// is still arriving it returns the fetch's reply and StatusContinue. When the
// file completes it records the hash verdict, closes the file in the store, and
// returns the fetch's FIN-ACK with StatusFileDone (more files remain) or
// StatusAllDone (the plan is exhausted).
func (p *Provisioner) OnFrame(rx []byte) (tx []byte, st Status) {
	tx, fileDone := p.cur.OnFrame(rx)
	if !fileDone {
		return tx, StatusContinue
	}
	spec := p.plan[p.idx]
	got := p.hash.Sum()
	p.res = append(p.res, FileResult{Name: spec.Name, Verified: got == spec.SHA256, Got: got})
	p.store.End(spec.Name)
	if p.idx == len(p.plan)-1 {
		return tx, StatusAllDone
	}
	return tx, StatusFileDone
}

// Next starts the next file's fetch (after a StatusFileDone) and returns its
// first wire frame (the ARP). The caller must have already sent the StatusFileDone
// tx (the just-closed file's FIN-ACK).
func (p *Provisioner) Next() []byte {
	p.idx++
	return p.start(p.idx)
}

// Results returns the per-file verdicts recorded so far, in fetch order.
func (p *Provisioner) Results() []FileResult { return p.res }

// Index returns the index of the file currently being fetched.
func (p *Provisioner) Index() int { return p.idx }

// Store is the per-file destination the Provisioner streams each fetched firmware
// file into — the host-verifiable seam over the real B-DOS spanning write (q16 /
// i93). Begin opens a named file and returns the tcp.Sink its body bytes stream
// into (the Provisioner wraps that sink in a HashingSink to verify the streamed
// body against the pinned hash); End is called when the file's fetch completes.
//
// On the host, MemStore records each file's bytes for assertion. On real Trinity,
// Begin/End map to opening/closing a named B-DOS record set and the returned sink
// to the bounded HSAVE-per-window flush (the Z80 storage_sink_flush). The file's
// size — needed by the real write to size the records — is carried in the Z80
// FW_MANIFEST record, so the host seam does not need it.
type Store interface {
	// Begin opens a file by name and returns the sink its body streams into.
	Begin(name string) tcp.Sink
	// End closes the named file (the real Store flushes its final span here).
	End(name string)
}

// MemStore is a host-test Store that records each file's streamed body in memory,
// keyed by name, in fetch order — the recording double the real B-DOS spanning
// write stands in for.
type MemStore struct {
	Files map[string][]byte // name -> the full streamed body
	Order []string          // names in Begin order
}

// NewMemStore builds an empty in-memory Store.
func NewMemStore() *MemStore {
	return &MemStore{Files: map[string][]byte{}}
}

// Begin starts recording a new file and returns the sink appending to it.
func (s *MemStore) Begin(name string) tcp.Sink {
	s.Files[name] = nil
	s.Order = append(s.Order, name)
	return &memFileSink{store: s, name: name}
}

// End is a no-op for the in-memory double (nothing to flush).
func (s *MemStore) End(name string) {}

// memFileSink appends streamed body bytes to one file's recording in a MemStore.
type memFileSink struct {
	store *MemStore
	name  string
}

// Write appends a copy of chunk to the file's recorded body. The copy mirrors
// ChunkSink: the connection reuses its flush buffer across Writes.
func (w *memFileSink) Write(chunk []byte) {
	cp := make([]byte, len(chunk))
	copy(cp, chunk)
	w.store.Files[w.name] = append(w.store.Files[w.name], cp...)
}
