// Package zx0greedy implements a greedy ZX0-format compressor constrained to
// Z80-feasible data structures.
//
// # ZX0 bitstream format (forward, invert_mode=true — default zx0 tool output)
//
// Bits are packed MSB-first into output bytes.  The very first bit of the
// stream is written via the "backtrack" mechanism: the initial bit-holder byte
// is written only after its first bit is known.
//
// Token types:
//
//	Literals run:      0 | elias_gamma(count) | count literal bytes
//	Rep match:         0 | elias_gamma(length)
//	New-offset match:  1 | elias_gamma_inv((off-1)/128+1) | offset_lsb_byte
//	                     | [backtrack bit] | elias_gamma(length-1)
//	End marker:        1 | elias_gamma_inv(256)
//
// elias_gamma(v) emits 2*floor(log2(v)) interleaved 0/value-bits, then a 1
// (terminator).  elias_gamma_inv is the same with value-bits inverted.
// The new-offset LSB byte is (127 - (offset-1)%128) << 1; the low bit is
// the backtrack bit borrowed by the following length field.
//
// # Z80 port target
//
// Each function in this file maps to a named Z80 subroutine.  Comments note
// the Z80 scratch-register footprint and the flat-array structures that the
// Z80 port mirrors.
package zx0greedy

// Params controls the match-finder complexity.  All values must be set by
// the caller; use DefaultParams for a sensible baseline.
//
// Z80 scratch-RAM: hash table (H*2 bytes) + chain array (blockLen*2 bytes)
// + fixed state (~30 bytes) — see ScratchBytes.
type Params struct {
	// HashSize is the number of entries in the hash table.  Must be a power
	// of two in {256, 512, 1024, 2048}.
	// Z80: hash table lives in a flat uint16 array of H words (H*2 bytes).
	HashSize int

	// ChainDepth is the maximum number of chain steps per position.
	// In {4, 8, 16, 32}.
	// Z80: each chain step is ~20–40 T-states (hash probe + byte compare).
	ChainDepth int
}

// DefaultParams is the recommended operating point: H=512, D=16.
// At 4 KB blocks this trades ~3–4 % ratio vs H=2048/D=32 for half the
// scratch RAM and ~4× fewer chain steps.
var DefaultParams = Params{HashSize: 512, ChainDepth: 16}

// ScratchBytes returns the Z80 scratch-RAM required for a block of blockLen
// bytes with the given Params.
//
// Layout (in bytes):
//
//	hash table:   HashSize * 2
//	chain array:  blockLen * 2      (uint16 per input position)
//	fixed state:  30                (bit accumulator, output index, etc.)
func ScratchBytes(p Params, blockLen int) int {
	return p.HashSize*2 + blockLen*2 + 30
}

// Compress compresses src with greedy ZX0 parse using p.
//
// Returns a ZX0-format bitstream that the upstream dzx0_standard / dzx0_turbo
// Z80 decoders accept without modification.
//
// Z80 port: top-level entry point, calls matchFinder and bitWriter helpers.
// Scratch-RAM total: ScratchBytes(p, len(src)).
func Compress(src []byte, p Params) []byte {
	n := len(src)
	if n == 0 {
		// Empty input: emit end marker only.
		bw := newBitWriter()
		bw.writeBit(1)
		bw.writeEliasGammaInv(256)
		return bw.bytes()
	}

	mf := newMatchFinder(src, p)

	bw := newBitWriter()

	// pos is the current input position; litStart marks the start of the
	// pending literal run (may equal pos when no literals are pending).
	pos := 0
	litStart := 0
	lastOffset := 1 // ZX0 INITIAL_OFFSET

	// prevWasLit tracks whether the most recent emitted token was a literals
	// block.  A rep match can only be emitted when the previous token was a
	// literals block (the ZX0 state machine only has the transition
	// COPY_LITERALS→COPY_FROM_LAST_OFFSET, not COPY_FROM_*→COPY_FROM_LAST_OFFSET).
	// At stream start the implicit initial state is equivalent to "after a
	// match", so prevWasLit starts false.
	prevWasLit := false

	for pos < n {
		bestLen, bestOff := mf.findBest(pos)

		// Pending literal count: how many bytes have accumulated since the
		// last emitted token.
		litCount := pos - litStart

		// Rep match: only valid from COPY_LITERALS state (prevWasLit=true
		// or litCount>0 so we can flush literals first).
		// New-offset match: valid from any state (sep=1 path).
		// Minimum match lengths: rep≥2 (saves bits over two literals),
		// new≥3 (shorter matches don't save bits over raw literals).
		//
		// canRep: the decoder can be in COPY_LITERALS state before this rep
		// (prevWasLit, or we have pending literals to flush first).
		// canRep and isNew are mutually exclusive (bestOff can't both equal
		// and differ from lastOffset).
		canRep := (prevWasLit || litCount > 0) && bestOff == lastOffset && bestLen >= 2
		isNew := bestOff != lastOffset && bestLen >= 3

		if !canRep && !isNew {
			// No usable match at pos.  Accumulate a literal and advance.
			pos++
			continue
		}
		isRep := canRep

		// Flush pending literals before emitting a back-reference.
		if litCount > 0 {
			// Separator "go to literals" (from previous match/start state).
			bw.writeBit(0)
			bw.writeEliasGamma(litCount)
			for i := litStart; i < pos; i++ {
				bw.writeByte(src[i])
			}
			prevWasLit = true
		}

		if isRep {
			// Separator "go to rep" (from literals state) + rep length.
			bw.writeBit(0)
			bw.writeEliasGamma(bestLen)
			prevWasLit = false
		} else {
			// Separator "go to new offset" (from any state) + offset + length.
			bw.writeBit(1)
			msb := (bestOff-1)/128 + 1
			bw.writeEliasGammaInv(msb)
			lsbByte := byte((127 - (bestOff-1)%128) << 1)
			bw.writeByteWithBacktrack(lsbByte)
			bw.writeEliasGamma(bestLen - 1)
			lastOffset = bestOff
			prevWasLit = false
		}

		// Insert intermediate positions of the match into the hash chain so
		// future positions can reference them.  This improves match quality
		// without changing correctness.
		for k := 1; k < bestLen; k++ {
			mf.insert(pos + k)
		}

		pos += bestLen
		litStart = pos
	}

	// Flush any trailing literals.
	litCount := n - litStart
	if litCount > 0 {
		bw.writeBit(0)
		bw.writeEliasGamma(litCount)
		for i := litStart; i < n; i++ {
			bw.writeByte(src[i])
		}
	}

	// End marker: bit 1, elias_gamma_inv(256).
	bw.writeBit(1)
	bw.writeEliasGammaInv(256)

	return bw.bytes()
}

// ── Bit writer ───────────────────────────────────────────────────────────────

// bitWriter packs bits MSB-first into an output byte slice.
//
// Z80 equivalent: A register holds the current bit-group (shifted left),
// with a byte-pointer advancing through the output buffer.  Fixed state
// fits in ~10 bytes (output ptr 2B, bit-byte ptr 2B, bit-mask 1B,
// backtrack flag 1B, A register 1B, padding 3B).
type bitWriter struct {
	out       []byte
	bitIdx    int  // index of current bit-holder byte in out
	bitMask   byte // next bit position within out[bitIdx] (128 → 64 → ... → 1)
	backtrack bool // true when the next writeBit overwrites the LSB of the last byte
}

func newBitWriter() *bitWriter {
	return &bitWriter{bitMask: 0, backtrack: true}
}

// writeBit emits one bit.  If backtrack is set, the bit is ORed into the LSB
// of the last written byte instead of consuming a new bit slot.
//
// Z80: test backtrack flag → if set, OR A-reg bit into (out_ptr-1); else
// shift bit-mask right, write new byte when mask wraps.
func (bw *bitWriter) writeBit(v int) {
	if bw.backtrack {
		if v != 0 && len(bw.out) > 0 {
			bw.out[len(bw.out)-1] |= 1
		}
		bw.backtrack = false
		return
	}
	if bw.bitMask == 0 {
		bw.bitMask = 128
		bw.bitIdx = len(bw.out)
		bw.out = append(bw.out, 0)
	}
	if v != 0 {
		bw.out[bw.bitIdx] |= bw.bitMask
	}
	bw.bitMask >>= 1
}

// writeByte appends a literal byte to the output.
//
// Z80: LD (DE),A ; INC DE — 7 T-states.
func (bw *bitWriter) writeByte(b byte) {
	bw.out = append(bw.out, b)
}

// writeByteWithBacktrack appends a byte to the output and sets the backtrack
// flag so the next writeBit overwrites the byte's LSB.  Used for the
// new-offset LSB byte in the ZX0 format.
//
// Z80: write byte to (DE), INC DE, set backtrack flag (1-byte variable).
func (bw *bitWriter) writeByteWithBacktrack(b byte) {
	bw.out = append(bw.out, b)
	bw.backtrack = true
}

// writeEliasGamma emits the interlaced Elias-gamma code for v (v >= 1).
// The ZX0 variant interleaves a 0-bit (direction) with each value bit,
// followed by a 1-bit terminator.
//
// Encoding for v: find the highest power of two p <= v; for each bit
// position from p>>1 down to 1: emit bit 0, emit (v&bit != 0).
// Then emit bit 1.
//
// Z80: loop with SRL/RL instructions; ~20–30 T per bit pair.
func (bw *bitWriter) writeEliasGamma(v int) {
	// Find the highest power of two <= v.
	p := 1
	for p*2 <= v {
		p <<= 1
	}
	// Emit interleaved pairs for each bit below p.
	for p >>= 1; p > 0; p >>= 1 {
		bw.writeBit(0) // direction bit (not backwards)
		if v&p != 0 {
			bw.writeBit(1)
		} else {
			bw.writeBit(0)
		}
	}
	// Terminator.
	bw.writeBit(1)
}

// writeEliasGammaInv emits the inverted interlaced Elias-gamma code for v
// (v >= 1).  Same as writeEliasGamma but value bits are inverted.
//
// Used for new-offset MSB and the end-marker in the default (non-classic)
// ZX0 format (invert_mode=true).
//
// Z80: same loop, CPL on value bit before emit; ~22–32 T per bit pair.
func (bw *bitWriter) writeEliasGammaInv(v int) {
	p := 1
	for p*2 <= v {
		p <<= 1
	}
	for p >>= 1; p > 0; p >>= 1 {
		bw.writeBit(0)
		if v&p != 0 {
			bw.writeBit(0) // inverted
		} else {
			bw.writeBit(1) // inverted
		}
	}
	bw.writeBit(1)
}

func (bw *bitWriter) bytes() []byte {
	return bw.out
}

// ── Probe statistics ─────────────────────────────────────────────────────────

// ProbeStats records match-finder probe counts for a single block.
// Used to build the T-state model for the Z80 port estimate.
type ProbeStats struct {
	Positions  int // number of input positions processed
	ChainSteps int // total chain-link traversals
	ByteComps  int // total byte comparisons (match length counting)
}

// CollectProbeStats runs the match finder over src with p and returns the
// probe statistics without producing compressed output.  Used by the oracle
// test to build T-state model estimates.
func CollectProbeStats(src []byte, p Params) ProbeStats {
	n := len(src)
	mf := newMatchFinder(src, p)
	var stats ProbeStats
	for pos := 0; pos < n; pos++ {
		steps, cmps := mf.probeStats(pos)
		mf.insert(pos)
		stats.Positions++
		stats.ChainSteps += steps
		stats.ByteComps += cmps
	}
	return stats
}

// ── Match finder ─────────────────────────────────────────────────────────────

// matchFinder implements a hash-head + chain match finder over a fixed input
// block.
//
// Z80 port: hashHead is a uint16 array of p.HashSize entries (p.HashSize*2
// bytes); chain is a uint16 array of len(src) entries (len(src)*2 bytes).
// The Z80 uses 16-bit arithmetic throughout: all indices fit in uint16
// because block sizes are <= 8 KB (well within 65535).
//
// The sentinel value is 0xFFFF (no entry); position 0 is never a valid chain
// target because the minimum match offset is 1.
type matchFinder struct {
	src        []byte
	hashHead   []uint16 // hashHead[h] = most recent position with hash h, or 0xFFFF
	chain      []uint16 // chain[i] = previous position with same hash, or 0xFFFF
	p          Params
	maxOffset  int // maximum back-reference offset (ZX0 limit = 32640)
}

const (
	zx0MaxOffset = 32640
	noEntry      = uint16(0xFFFF)
)

func newMatchFinder(src []byte, p Params) *matchFinder {
	mf := &matchFinder{
		src:       src,
		hashHead:  make([]uint16, p.HashSize),
		chain:     make([]uint16, len(src)),
		p:         p,
		maxOffset: zx0MaxOffset,
	}
	for i := range mf.hashHead {
		mf.hashHead[i] = noEntry
	}
	for i := range mf.chain {
		mf.chain[i] = noEntry
	}
	return mf
}

// hash2 hashes two bytes at position i.
//
// Z80: LD A,(HL) ; LD H,A ; LD A,(HL+1) ; XOR H ; AND (hashMask>>8) style
// — two loads + one XOR + mask, ~20 T.  The exact polynomial doesn't matter
// for correctness; this one spreads 2-byte pairs evenly.
func (mf *matchFinder) hash2(i int) int {
	if i+1 >= len(mf.src) {
		return int(mf.src[i]) & (mf.p.HashSize - 1)
	}
	h := (int(mf.src[i])*31 ^ int(mf.src[i+1])) & (mf.p.HashSize - 1)
	return h
}

// insert adds position i to the hash chain.
//
// Z80: LD HL,(hashHead+h*2) ; LD (chain+i*2),HL ; LD (hashHead+h*2),i
// — three 16-bit loads/stores, ~36 T.
func (mf *matchFinder) insert(i int) {
	h := mf.hash2(i)
	mf.chain[i] = mf.hashHead[h]
	mf.hashHead[h] = uint16(i)
}

// findBest finds the best match (longest length, preferring rep-offset on
// ties) at position pos.  It also inserts pos into the hash chain.
//
// Returns (bestLen, bestOff).  bestLen=0 means no match found.
//
// Z80: called once per input byte; hot path.  Total T-states per call:
//   hash2 (~20 T) + insert (~36 T) + D chain steps × ~40 T each
//   = ~56 + D*40 T per input byte.
//   At D=16: ~696 T/byte; at D=8: ~376 T/byte.
//
// The function inserts pos AFTER probing so the chain never sees a self-match
// (identical to how the upstream C compressor builds the chain during
// optimize()).
func (mf *matchFinder) findBest(pos int) (bestLen, bestOff int) {
	n := len(mf.src)

	// Probe the chain.
	steps := 0
	candidate := int(mf.hashHead[mf.hash2(pos)])
	for candidate != int(noEntry) && steps < mf.p.ChainDepth {
		off := pos - candidate
		if off <= 0 || off > mf.maxOffset {
			candidate = int(mf.chain[candidate])
			steps++
			continue
		}

		// Count match length (16-bit arithmetic: blockLen <= 8192 < 65535).
		maxML := n - pos
		if maxML > 255 {
			maxML = 255 // ZX0 max match length via Elias-gamma fits 8 bits
		}
		ml := 0
		for ml < maxML && mf.src[candidate+ml] == mf.src[pos+ml] {
			ml++
		}

		if ml > bestLen || (ml == bestLen && off == bestOff) {
			bestLen = ml
			bestOff = off
		}
		candidate = int(mf.chain[candidate])
		steps++
	}

	// Insert pos into chain after probing.
	mf.insert(pos)

	return bestLen, bestOff
}

// probeStats counts chain steps and byte comparisons at pos WITHOUT inserting
// pos or updating bestLen/bestOff.  Used by CollectProbeStats to gather the
// T-state model data for the Z80 port estimate.
func (mf *matchFinder) probeStats(pos int) (chainSteps, byteComps int) {
	n := len(mf.src)
	steps := 0
	candidate := int(mf.hashHead[mf.hash2(pos)])
	for candidate != int(noEntry) && steps < mf.p.ChainDepth {
		off := pos - candidate
		if off <= 0 || off > mf.maxOffset {
			candidate = int(mf.chain[candidate])
			steps++
			continue
		}
		maxML := n - pos
		if maxML > 255 {
			maxML = 255
		}
		ml := 0
		for ml < maxML && mf.src[candidate+ml] == mf.src[pos+ml] {
			ml++
			byteComps++
		}
		byteComps++ // the final non-matching comparison that stopped the loop
		candidate = int(mf.chain[candidate])
		steps++
	}
	return steps, byteComps
}
