package tftp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
)

const sampleManifest = `# netboot serve manifest — design §2 boot-disk-local
!version 1
!strategy highest-free
!pool-includes-self no

# small files ride along on the boot disk
config.txt        local config.txt
overlays/x.dtbo   local OVLX

# firmware spans out to its own records
kernel8.img       remote size=12288 span=1 records=42:KERNEL8
start4.elf        remote size=2298756 span=3 records=900:S4A,901:S4B,902 sha256=` +
	"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n"

func parse(t *testing.T, text string) *Manifest {
	t.Helper()
	m, err := ParseManifest(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m
}

func TestParseHeaderAndEntries(t *testing.T) {
	m := parse(t, sampleManifest)

	if got := m.Header["strategy"]; got != "highest-free" {
		t.Errorf("strategy header = %q, want highest-free", got)
	}
	if got := m.Header["version"]; got != "1" {
		t.Errorf("version header = %q, want 1", got)
	}
	if len(m.Entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(m.Entries))
	}

	// local entry
	e, ok := m.Resolve("config.txt")
	if !ok || e.Kind != KindLocal || e.BDOSName != "config.txt" {
		t.Errorf("config.txt resolve = %+v, ok=%v", e, ok)
	}

	// remote single-record entry with a label
	e, ok = m.Resolve("kernel8.img")
	if !ok || e.Kind != KindRemote || e.Size != 12288 || e.Span != 1 {
		t.Fatalf("kernel8.img resolve = %+v, ok=%v", e, ok)
	}
	if len(e.Locators) != 1 || e.Locators[0].Record != 42 || e.Locators[0].Label != "KERNEL8" {
		t.Errorf("kernel8 locators = %+v", e.Locators)
	}

	// remote spanning entry with hash and a bare (label-less) third record
	e, ok = m.Resolve("start4.elf")
	if !ok || e.Span != 3 || len(e.Locators) != 3 {
		t.Fatalf("start4.elf resolve = %+v, ok=%v", e, ok)
	}
	if e.Locators[2].Record != 902 || e.Locators[2].Label != "" {
		t.Errorf("third locator = %+v, want {902 \"\"}", e.Locators[2])
	}
	if e.SHA256 == nil {
		t.Fatal("start4.elf SHA256 = nil, want set")
	}
}

// The manifest is a drop-in tftp.Store, so the existing Resolve answers an RRQ
// straight off it: hit → OACK with size, miss → ERROR404, serial-subdir → ERROR404.
func TestManifestBacksResolve(t *testing.T) {
	m := parse(t, sampleManifest)
	var _ Store = m // compile-time: *Manifest implements tftp.Store

	if act, size := Resolve(m, "kernel8.img"); act != ActionOACK || size != 12288 {
		t.Errorf("Resolve(kernel8.img) = %v,%d want OACK,12288", act, size)
	}
	if act, _ := Resolve(m, "nope.bin"); act != ActionError404 {
		t.Errorf("Resolve(miss) = %v want ERROR404", act)
	}
	// A serial-subdir prefix is 404'd before the store is even consulted.
	if act, _ := Resolve(m, "e0ff06da/start4.elf"); act != ActionError404 {
		t.Errorf("Resolve(serial-subdir) = %v want ERROR404", act)
	}
}

// ServePlan threads a remote blob through bdos.SpanPlan and zips the byte ranges
// with the manifest's record-number locators.
func TestServePlanRemoteSpanning(t *testing.T) {
	m := parse(t, sampleManifest)
	e, _ := m.Resolve("start4.elf")

	const cap = 819200 // a Trinity record's capacity; ceil(2298756/819200)==3 == span
	parts, err := e.ServePlan(cap)
	if err != nil {
		t.Fatalf("ServePlan: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}

	// Records come from the manifest; byte ranges come from bdos.SpanPlan and must
	// tile the blob contiguously and sum to its size.
	wantRecords := []int{900, 901, 902}
	total, off := 0, 0
	for i, p := range parts {
		if p.Record != wantRecords[i] {
			t.Errorf("part %d record = %d, want %d", i, p.Record, wantRecords[i])
		}
		if p.Offset != off {
			t.Errorf("part %d offset = %d, want %d (contiguous)", i, p.Offset, off)
		}
		if p.Length > cap {
			t.Errorf("part %d length %d exceeds cap %d", i, p.Length, cap)
		}
		off += p.Length
		total += p.Length
	}
	if total != int(e.Size) {
		t.Errorf("parts sum to %d, want size %d", total, e.Size)
	}

	// With a hash present the record names are content-addressed (design §6.3),
	// matching bdos.SpanRecordName so two projects sharing the file dedup for free.
	if parts[0].Name != bdos.SpanRecordName(*e.SHA256, 0) {
		t.Errorf("part0 name = %q, want content-addressed %q", parts[0].Name, bdos.SpanRecordName(*e.SHA256, 0))
	}
}

func TestServePlanLocal(t *testing.T) {
	m := parse(t, sampleManifest)
	e, _ := m.Resolve("config.txt")
	parts, err := e.ServePlan(819200)
	if err != nil {
		t.Fatalf("ServePlan: %v", err)
	}
	if len(parts) != 1 || parts[0].Record != -1 || parts[0].Name != "config.txt" || parts[0].Length != int(e.Size) {
		t.Errorf("local ServePlan = %+v", parts)
	}
}

// A recordCap that disagrees with the manifest's span is a stored-vs-served
// mismatch and must error, not silently truncate.
func TestServePlanCapMismatchErrors(t *testing.T) {
	m := parse(t, sampleManifest)
	e, _ := m.Resolve("start4.elf") // span=3
	if _, err := e.ServePlan(2298756); err == nil {
		t.Error("ServePlan with a cap implying 1 record but span=3 should error")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"unknown kind":    "foo bogus x",
		"local extra":     "foo local A B",
		"local long name": "foo local THISNAMEISTOOLONG",
		"remote no size":  "foo remote span=1 records=1",
		"remote no span":  "foo remote size=10 records=1",
		"remote no recs":  "foo remote size=10 span=1",
		"span mismatch":   "foo remote size=10 span=2 records=1",
		"bad sha":         "foo remote size=10 span=1 records=1 sha256=zz",
		"bad record num":  "foo remote size=10 span=1 records=x",
		"unknown field":   "foo remote size=10 span=1 records=1 weird=3",
		"not key=value":   "foo remote size=10 span=1 records=1 lonely",
		"too few tokens":  "justaname",
		"duplicate name":  "a local X\na local Y",
		"empty header":    "! ",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest(strings.NewReader(text)); err == nil {
				t.Errorf("ParseManifest(%q) succeeded, want error", text)
			}
		})
	}
}

// Comments and blank lines are ignored; '#' anywhere ends a line.
func TestParseIgnoresCommentsAndBlanks(t *testing.T) {
	m := parse(t, "\n\n# just a comment\n  \nkernel8.img remote size=10 span=1 records=5  # trailing\n")
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
	if e, _ := m.Resolve("kernel8.img"); e.Locators[0].Record != 5 || e.Size != 10 {
		t.Errorf("entry = %+v", e)
	}
}

// parse∘write is a faithful semantic round-trip: re-parsing the serialised form
// reproduces the same header and entries.
func TestRoundTrip(t *testing.T) {
	m := parse(t, sampleManifest)
	text := m.String()
	m2 := parse(t, text)

	if len(m2.Entries) != len(m.Entries) {
		t.Fatalf("round-trip entry count %d != %d\n%s", len(m2.Entries), len(m.Entries), text)
	}
	for i := range m.Entries {
		a, b := m.Entries[i], m2.Entries[i]
		if a.line() != b.line() {
			t.Errorf("entry %d round-trip mismatch:\n  %q\n  %q", i, a.line(), b.line())
		}
	}
	for _, k := range m.HeaderKeys {
		if m.Header[k] != m2.Header[k] {
			t.Errorf("header %q round-trip: %q != %q", k, m.Header[k], m2.Header[k])
		}
	}
	// A re-serialisation is byte-stable (canonical form is a fixed point).
	if text2 := m2.String(); text2 != text {
		t.Errorf("re-serialisation not byte-stable:\n%q\n%q", text, text2)
	}
}

// A hash-bearing entry round-trips its content hash, and the derived record name
// stays consistent with bdos.SpanRecordName across the round-trip.
func TestSHARoundTripAndContentAddressing(t *testing.T) {
	sum := sha256.Sum256([]byte("firmware bytes"))
	text := "k remote size=100 span=1 records=7 sha256=" + hex.EncodeToString(sum[:])
	m := parse(t, text)
	e, _ := m.Resolve("k")
	if e.SHA256 == nil || *e.SHA256 != sum {
		t.Fatalf("hash not preserved")
	}
	parts, err := e.ServePlan(100)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Name != bdos.SpanRecordName(sum, 0) {
		t.Errorf("name %q not content-addressed", parts[0].Name)
	}
}
