package assemble

import (
	"bytes"
	"io"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// EnableUsage turns on the peak-usage census (refenc --dump-usage).
// It must be called before Pass1.
func EnableUsage() { usage = newUsage() }

// DumpUsage writes the census to w, recording the total output size.
// No-op if EnableUsage was not called.
func DumpUsage(w io.Writer, totalOut int) {
	if usage == nil {
		return
	}
	usage.TotalOutBytes = totalOut
	usage.Dump(w)
}

// CompactTBNBytes compacts f's record stream (using p1's pass-1 results)
// and returns the serialized compact v2 .tbn bytes: the name table is
// rebuilt by interning f.Names in ID order (reproducing the same IDs the
// records reference), and label/local definitions move to the header
// tables. Byte-for-byte identical to the former refenc -emit-compact-tbn
// output.
func CompactTBNBytes(f *format.File, p1 *Pass1Result) ([]byte, error) {
	compacted, sidecar, globals, err := Compact(f, p1)
	if err != nil {
		return nil, err
	}
	st := format.NewSymbolTable()
	for _, n := range f.Names {
		st.Intern(n)
	}
	labels, locals := headerRows(f, p1)
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, labels, locals, compacted, globals, sidecar); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
