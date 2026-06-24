package tftp

// The serve manifest (i114a). A manifest decouples the TFTP served namespace
// from B-DOS's flat 10-char filename limits: the served name is the manifest key
// (full length, full charset, '/'-paths and all), and each entry points at the
// storage that holds the blob — either a local B-DOS file on the boot disk or a
// set of remote record locators elsewhere on the card. The server reads the
// manifest once from its boot record into RAM, then resolves each RRQ by an exact
// string match against the keys, yielding the record list + size with no
// directory scan and no size-summing (both are manifest fields).
//
// This file is the Go authority for decisions 1-3 of the design (encoding =
// human-editable line-based text; remote locator key = record number + optional
// name; local vs remote entry kinds). The storage-allocation HEADER policy
// (first-free / fixed-list / highest-free — design §4) is parsed generically here
// into Header and given meaning by i114b. Design:
// docs/specs/netboot-storage-manifest-design.md §1-3, §6.
//
// Format (line-based text; one entry per line):
//
//	# comment                      -- '#' to end of line; blank lines ignored
//	!key value...                  -- header directive (design §4 header section)
//	NAME local BDOSNAME            -- blob is a B-DOS file on the boot disk
//	NAME remote size=N span=M records=R[:L],...  [sha256=HEX]
//
// NAME is the first whitespace-delimited token — the served TFTP name, which may
// contain '/' (paths are just keys; B-DOS stays flat) but no whitespace, matching
// every real Pi-netboot filename. A remote entry's records field is a
// comma-separated list of locators, each a B-DOS record NUMBER with an optional
// ':LABEL' (the human-visible record name a 'samdisk dir' / 'DIR' shows); span
// must equal the locator count. sha256 is optional (64 hex chars).

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
)

// EntryKind distinguishes a blob stored as a local boot-disk file from one stored
// in remote record(s) elsewhere on the card (design §3).
type EntryKind int

const (
	// KindLocal: the blob is a B-DOS file on the boot disk itself; BDOSName names it.
	KindLocal EntryKind = iota
	// KindRemote: the blob spans record(s) elsewhere; Locators + Size + Span describe it.
	KindRemote
)

func (k EntryKind) String() string {
	switch k {
	case KindLocal:
		return "local"
	case KindRemote:
		return "remote"
	default:
		return "?"
	}
}

// Locator is one remote record holding (part of) a blob: the B-DOS record NUMBER
// (the primary key the provisioner chose and HRECORD selects) plus an optional
// human-visible LABEL for portability across card copies (design §6 decision 2).
type Locator struct {
	Record int    // B-DOS record number (Trinity slot)
	Label  string // optional record name/label; "" if none
}

// Entry is one resolved served name → storage mapping.
type Entry struct {
	Name     string    // the served TFTP name (the manifest key)
	Kind     EntryKind // local or remote
	BDOSName string    // KindLocal: the B-DOS file name on the boot disk
	Size     uint64    // total logical size in bytes (both kinds)
	Span     int       // KindRemote: number of records the blob occupies (== len(Locators))
	Locators []Locator // KindRemote: the ordered record locators
	SHA256   *[32]byte // optional content hash; nil if absent
}

// Manifest is a parsed serve manifest: the ordered entries plus the generic
// header directives (interpreted by i114b). Order is preserved so a parse∘write
// round-trip is faithful.
type Manifest struct {
	Header     map[string]string // header directive key → value (design §4 policy)
	HeaderKeys []string          // header keys in first-seen order (round-trip stability)
	Entries    []Entry           // entries in file order
	index      map[string]int    // served name → index into Entries
}

// ParseManifest reads a manifest from r. It is strict: a malformed line, an
// unknown entry kind, a duplicate served name, a span that disagrees with the
// locator count, or a bad field is an error rather than a silently skipped line —
// the manifest is the serve-time source of truth, so a typo must surface, never
// silently drop a file (design §6: hand-editable, debuggable).
func ParseManifest(r io.Reader) (*Manifest, error) {
	m := &Manifest{Header: map[string]string{}, index: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := stripComment(sc.Text())
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "!") {
			if err := m.parseHeader(line); err != nil {
				return nil, fmt.Errorf("manifest line %d: %w", lineNo, err)
			}
			continue
		}
		e, err := parseEntry(line)
		if err != nil {
			return nil, fmt.Errorf("manifest line %d: %w", lineNo, err)
		}
		if _, dup := m.index[e.Name]; dup {
			return nil, fmt.Errorf("manifest line %d: duplicate served name %q", lineNo, e.Name)
		}
		m.index[e.Name] = len(m.Entries)
		m.Entries = append(m.Entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// stripComment removes a '#' comment from a line. '#' is only a comment when it
// is not inside a field — served names never contain '#', so a bare cut is safe.
func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

func (m *Manifest) parseHeader(line string) error {
	body := strings.TrimSpace(strings.TrimPrefix(line, "!"))
	key, val, _ := strings.Cut(body, " ")
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if key == "" {
		return fmt.Errorf("empty header directive")
	}
	if _, seen := m.Header[key]; !seen {
		m.HeaderKeys = append(m.HeaderKeys, key)
	}
	m.Header[key] = val
	return nil
}

func parseEntry(line string) (Entry, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Entry{}, fmt.Errorf("entry needs at least NAME and KIND")
	}
	e := Entry{Name: fields[0]}
	switch fields[1] {
	case "local":
		if len(fields) != 3 {
			return Entry{}, fmt.Errorf("local entry %q wants exactly: NAME local BDOSNAME", e.Name)
		}
		if len(fields[2]) > 10 {
			return Entry{}, fmt.Errorf("local entry %q: B-DOS name %q exceeds 10 chars", e.Name, fields[2])
		}
		e.Kind = KindLocal
		e.BDOSName = fields[2]
		return e, nil
	case "remote":
		e.Kind = KindRemote
		return parseRemoteFields(e, fields[2:])
	default:
		return Entry{}, fmt.Errorf("entry %q: unknown kind %q (want local or remote)", e.Name, fields[1])
	}
}

func parseRemoteFields(e Entry, fields []string) (Entry, error) {
	var sawSize, sawSpan, sawRecords bool
	for _, f := range fields {
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			return Entry{}, fmt.Errorf("remote entry %q: field %q is not key=value", e.Name, f)
		}
		switch key {
		case "size":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return Entry{}, fmt.Errorf("remote entry %q: bad size %q: %w", e.Name, val, err)
			}
			e.Size, sawSize = n, true
		case "span":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return Entry{}, fmt.Errorf("remote entry %q: bad span %q (want a positive integer)", e.Name, val)
			}
			e.Span, sawSpan = n, true
		case "records":
			locs, err := parseLocators(val)
			if err != nil {
				return Entry{}, fmt.Errorf("remote entry %q: %w", e.Name, err)
			}
			e.Locators, sawRecords = locs, true
		case "sha256":
			b, err := hex.DecodeString(val)
			if err != nil || len(b) != 32 {
				return Entry{}, fmt.Errorf("remote entry %q: sha256 must be 64 hex chars", e.Name)
			}
			var h [32]byte
			copy(h[:], b)
			e.SHA256 = &h
		default:
			return Entry{}, fmt.Errorf("remote entry %q: unknown field %q", e.Name, key)
		}
	}
	switch {
	case !sawSize:
		return Entry{}, fmt.Errorf("remote entry %q: missing size", e.Name)
	case !sawSpan:
		return Entry{}, fmt.Errorf("remote entry %q: missing span", e.Name)
	case !sawRecords:
		return Entry{}, fmt.Errorf("remote entry %q: missing records", e.Name)
	case e.Span != len(e.Locators):
		return Entry{}, fmt.Errorf("remote entry %q: span=%d disagrees with %d record locators", e.Name, e.Span, len(e.Locators))
	}
	return e, nil
}

func parseLocators(val string) ([]Locator, error) {
	parts := strings.Split(val, ",")
	locs := make([]Locator, 0, len(parts))
	for _, p := range parts {
		num, label, _ := strings.Cut(p, ":")
		n, err := strconv.Atoi(num)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad record number %q (want a non-negative integer)", num)
		}
		if len(label) > 10 {
			return nil, fmt.Errorf("record label %q exceeds 10 chars", label)
		}
		locs = append(locs, Locator{Record: n, Label: label})
	}
	return locs, nil
}

// Lookup makes *Manifest a tftp.Store: it returns the total size of the named
// blob and whether it exists. This lets the existing Resolve(store, name) answer
// an RRQ straight off the manifest (404 a serial-subdir, OACK with the size on a
// hit, ERROR(1) on a miss) with no change to the server's resolve step.
func (m *Manifest) Lookup(name string) (uint64, bool) {
	if i, ok := m.index[name]; ok {
		return m.Entries[i].Size, true
	}
	return 0, false
}

// Resolve returns the full entry for a served name — the name → record resolve.
// Unlike Lookup (which yields only the size for the tftp.Store interface), this
// surfaces the record locator list the serve loop reads the blob from.
func (m *Manifest) Resolve(name string) (Entry, bool) {
	if i, ok := m.index[name]; ok {
		return m.Entries[i], true
	}
	return Entry{}, false
}

// ServePart is one record of a resolved serve plan: the storage that holds the
// [Offset, Offset+Length) byte range of the logical blob. Record is the B-DOS
// record number to HRECORD-select (-1 for a boot-disk-local file), Name the B-DOS
// file/record name HLOAD reads.
type ServePart struct {
	Record int    // B-DOS record number; -1 for a local boot-disk file
	Name   string // B-DOS file/record name to HLOAD
	Offset int    // byte offset of this part within the logical blob
	Length int    // bytes this part contributes
}

// ServePlan returns the ordered read plan for an entry: the records to HLOAD and
// concatenate into the TFTP stream, in order. For a remote entry it threads the
// blob size through bdos.SpanPlan to derive the per-record byte ranges (and the
// content-addressed record names when a hash is present, design §6 decision 3),
// then zips those ranges with the manifest's explicit record-number locators. The
// caller supplies recordCap (the per-record byte cap, a hardware detail pinned
// when the real persist is built); ServePlan errors if that cap implies a record
// count different from the manifest's span — a stored-vs-served disagreement that
// must surface, not be papered over. A local entry is a single part read by its
// B-DOS name.
func (e Entry) ServePlan(recordCap int) ([]ServePart, error) {
	if e.Kind == KindLocal {
		return []ServePart{{Record: -1, Name: e.BDOSName, Offset: 0, Length: int(e.Size)}}, nil
	}
	var hash [32]byte
	if e.SHA256 != nil {
		hash = *e.SHA256
	}
	span := bdos.SpanPlan(hash, int(e.Size), recordCap)
	if len(span) != len(e.Locators) {
		return nil, fmt.Errorf("entry %q: recordCap %d implies %d records but manifest span is %d", e.Name, recordCap, len(span), len(e.Locators))
	}
	parts := make([]ServePart, len(span))
	for i, sr := range span {
		name := sr.Name // content-addressed (design §6.3) when a hash is present
		if e.SHA256 == nil && e.Locators[i].Label != "" {
			name = e.Locators[i].Label
		}
		parts[i] = ServePart{Record: e.Locators[i].Record, Name: name, Offset: sr.Offset, Length: sr.Length}
	}
	return parts, nil
}

// WriteTo serialises the manifest back to its canonical text form: header
// directives first (in first-seen order), then entries in file order. parse∘write
// is a faithful semantic round-trip (comments aside), which both makes the format
// the authority and lets the i70/i100 provisioner emit a manifest it can re-read.
func (m *Manifest) WriteTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriter(w)
	for _, k := range m.HeaderKeys {
		if v := m.Header[k]; v != "" {
			fmt.Fprintf(bw, "!%s %s\n", k, v)
		} else {
			fmt.Fprintf(bw, "!%s\n", k)
		}
	}
	for _, e := range m.Entries {
		fmt.Fprintln(bw, e.line())
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	return 0, nil
}

// String renders the manifest in canonical text form.
func (m *Manifest) String() string {
	var sb strings.Builder
	m.WriteTo(&sb)
	return sb.String()
}

func (e Entry) line() string {
	if e.Kind == KindLocal {
		return fmt.Sprintf("%s local %s", e.Name, e.BDOSName)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s remote size=%d span=%d records=%s", e.Name, e.Size, e.Span, locatorsField(e.Locators))
	if e.SHA256 != nil {
		fmt.Fprintf(&sb, " sha256=%s", hex.EncodeToString(e.SHA256[:]))
	}
	return sb.String()
}

func locatorsField(locs []Locator) string {
	parts := make([]string, len(locs))
	for i, l := range locs {
		if l.Label != "" {
			parts[i] = fmt.Sprintf("%d:%s", l.Record, l.Label)
		} else {
			parts[i] = strconv.Itoa(l.Record)
		}
	}
	return strings.Join(parts, ",")
}

// Names returns the served names the manifest resolves, sorted — a stable view
// for callers that enumerate the manifest (e.g. a DIR-style listing).
func (m *Manifest) Names() []string {
	names := make([]string, 0, len(m.Entries))
	for _, e := range m.Entries {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return names
}
