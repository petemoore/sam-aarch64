package editmodel

import (
	"bytes"
	"math/rand"
	"testing"
)

// oracleLine mirrors one line as the property test's ground truth.
type oracleLine struct {
	id   RecordID
	text []byte
}

// propertySeeds is the fixed set of RNG seeds used for deterministic,
// reproducible property runs. Multiple seeds exercise different interleavings
// of insert and delete ops.
var propertySeeds = []int64{42, 137, 9999, 0xdeadbeef, 12345678}

func TestPropertyBlockListCorrectness(t *testing.T) {
	for _, seed := range propertySeeds {
		seed := seed // capture for t.Run closure
		t.Run(seedName(seed), func(t *testing.T) {
			runPropertySeed(t, seed)
		})
	}
}

func seedName(seed int64) string {
	// Build a short human-readable name without fmt to avoid import.
	return "seed" + itoa(seed)
}

// itoa converts an int64 to its decimal string representation.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// Reverse.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func runPropertySeed(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))

	d := New()
	var oracle []oracleLine
	var deletedIDs []RecordID

	// Build-up phase: insert ~3000 lines of random length 10–60 bytes.
	const buildLines = 3000
	for i := 0; i < buildLines; i++ {
		lc := d.LineCount()
		at := rng.Intn(lc + 1)
		text := randomLine(rng, 10, 60)
		id := d.InsertLine(at, text)
		oracle = append(oracle[:at], append([]oracleLine{{id: id, text: copyBytes(text)}}, oracle[at:]...)...)
	}

	// Interleaved phase: ~4000 random insert/delete ops.
	const interleaveOps = 4000
	for i := 0; i < interleaveOps; i++ {
		lc := d.LineCount()
		// Bias 60% insert, 40% delete (or insert if doc is empty).
		if lc == 0 || rng.Intn(10) < 6 {
			at := rng.Intn(lc + 1)
			text := randomLine(rng, 10, 60)
			id := d.InsertLine(at, text)
			oracle = append(oracle[:at], append([]oracleLine{{id: id, text: copyBytes(text)}}, oracle[at:]...)...)
		} else {
			at := rng.Intn(lc)
			id := d.DeleteLine(at)
			deletedIDs = append(deletedIDs, id)
			oracle = append(oracle[:at], oracle[at+1:]...)
		}

		// Periodic mid-sequence invariant check.
		if i%500 == 0 {
			if err := d.checkInvariants(); err != nil {
				t.Fatalf("seed %d op %d: invariant violation: %v", seed, i, err)
			}
		}
	}

	// ── Final assertions ──────────────────────────────────────────────────────

	// 1. Line count matches oracle.
	if d.LineCount() != len(oracle) {
		t.Fatalf("seed %d: LineCount %d != oracle %d", seed, d.LineCount(), len(oracle))
	}

	// 2. Every line's id and text match oracle.
	for i, o := range oracle {
		gotID, gotText := d.LineAt(i)
		if gotID != o.id {
			t.Fatalf("seed %d: line %d id mismatch: want %d got %d", seed, i, o.id, gotID)
		}
		if !bytes.Equal(gotText, o.text) {
			t.Fatalf("seed %d: line %d text mismatch: want %q got %q", seed, i, o.text, gotText)
		}
	}

	// 3. IndexOf for every live id returns its oracle index.
	for i, o := range oracle {
		idx, ok := d.IndexOf(o.id)
		if !ok {
			t.Fatalf("seed %d: IndexOf(%d) not found; oracle index %d", seed, o.id, i)
		}
		if idx != i {
			t.Fatalf("seed %d: IndexOf(%d) = %d; want oracle index %d", seed, o.id, idx, i)
		}
	}

	// 4. IndexOf for a sample of deleted ids returns ok==false.
	sample := deletedIDs
	if len(sample) > 200 {
		sample = sample[:200]
	}
	for _, id := range sample {
		if _, ok := d.IndexOf(id); ok {
			t.Fatalf("seed %d: deleted id %d still found by IndexOf", seed, id)
		}
	}

	// 5. checkInvariants.
	if err := d.checkInvariants(); err != nil {
		t.Fatalf("seed %d: final invariant violation: %v", seed, err)
	}

	// 6. Serialize → Load → re-Serialize: byte-stable; reloaded doc matches oracle.
	var buf1 bytes.Buffer
	if err := d.Serialize(&buf1); err != nil {
		t.Fatalf("seed %d: Serialize: %v", seed, err)
	}
	d2, err := Load(bytes.NewReader(buf1.Bytes()))
	if err != nil {
		t.Fatalf("seed %d: Load: %v", seed, err)
	}
	var buf2 bytes.Buffer
	if err := d2.Serialize(&buf2); err != nil {
		t.Fatalf("seed %d: re-Serialize: %v", seed, err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("seed %d: serialize→load→serialize not byte-stable", seed)
	}
	if d2.LineCount() != len(oracle) {
		t.Fatalf("seed %d: reloaded LineCount %d != oracle %d", seed, d2.LineCount(), len(oracle))
	}
	for i, o := range oracle {
		gotID, gotText := d2.LineAt(i)
		if gotID != o.id {
			t.Fatalf("seed %d: reloaded line %d id mismatch", seed, i)
		}
		if !bytes.Equal(gotText, o.text) {
			t.Fatalf("seed %d: reloaded line %d text mismatch", seed, i)
		}
		if _, ok := d2.IndexOf(o.id); !ok {
			t.Fatalf("seed %d: IndexOf(%d) not found on reloaded doc", seed, o.id)
		}
	}

	// Log block statistics so a reader can confirm splits and merges occurred.
	blockCount := len(d.blocks)
	minLines, maxLines := 0, 0
	if blockCount > 0 {
		minLines = len(d.blocks[0].lines)
		maxLines = len(d.blocks[0].lines)
		for _, b := range d.blocks[1:] {
			if len(b.lines) < minLines {
				minLines = len(b.lines)
			}
			if len(b.lines) > maxLines {
				maxLines = len(b.lines)
			}
		}
	}
	t.Logf("seed=%d blocks=%d minLinesPerBlock=%d maxLinesPerBlock=%d liveLines=%d deletedSampled=%d",
		seed, blockCount, minLines, maxLines, d.LineCount(), len(sample))

	// Assert that multiple blocks exist — the run genuinely exercised splits.
	if blockCount <= 1 {
		t.Fatalf("seed %d: expected >1 blocks (splits must have occurred), got %d", seed, blockCount)
	}
}

// randomLine returns a random byte slice of length in [minLen, maxLen].
func randomLine(rng *rand.Rand, minLen, maxLen int) []byte {
	n := minLen + rng.Intn(maxLen-minLen+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rng.Intn(26))
	}
	return b
}

func copyBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
