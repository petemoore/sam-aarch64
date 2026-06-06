package aarch64dec

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteAsm writes a labeled assembly listing of data to w.  The output
// is valid input for text2bin: one instruction per line, tab-indented,
// with synthetic labels L0/L1/… placed at every direct branch target
// within the binary.  Branch operands that resolve to a label are
// replaced with the label name so the source is safe to edit — inserting
// an instruction updates all dependent branches automatically.
//
// PC-relative non-branch operands (adrp, adr) are left as absolute hex;
// pair detection for adrp/add(:lo12:) is deferred.
//
// Words that cannot be decoded render as `.inst 0xNNNNNNNN`, which is a
// valid text2bin directive.
func WriteAsm(w io.Writer, base uint64, data []byte) error {
	// Pass 1: collect direct branch targets that land within the binary.
	type void struct{}
	targetSet := map[uint64]void{}
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])
		if tgt, ok := BranchTarget(pc, word); ok {
			if tgt >= base && tgt < base+uint64(len(data)) {
				targetSet[tgt] = void{}
			}
		}
	}

	// Build a sorted label map: ascending address order → L0, L1, …
	addrs := make([]uint64, 0, len(targetSet))
	for a := range targetSet {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })
	labelOf := make(map[uint64]string, len(addrs))
	for i, a := range addrs {
		labelOf[a] = fmt.Sprintf("L%d", i)
	}

	// Emit section header.
	if _, err := fmt.Fprintln(w, "\t.text"); err != nil {
		return err
	}

	// Pass 2: emit instructions, inserting label definitions and
	// replacing branch-target hex addresses with label names.
	for i := 0; i < len(data); i += 4 {
		pc := base + uint64(i)
		word := binary.LittleEndian.Uint32(data[i : i+4])

		if label, ok := labelOf[pc]; ok {
			if _, err := fmt.Fprintf(w, "%s:\n", label); err != nil {
				return err
			}
		}

		mnem, ops, ok := DecodeAt(pc, word)
		var line string
		if ok {
			if tgt, hasTgt := BranchTarget(pc, word); hasTgt {
				if label, hasLabel := labelOf[tgt]; hasLabel {
					ops = strings.ReplaceAll(ops, fmt.Sprintf("%#x", tgt), label)
				}
			}
			line = Format(mnem, ops)
		} else {
			line = fmt.Sprintf(".inst\t%#08x", word)
		}
		if _, err := fmt.Fprintf(w, "\t%s\n", line); err != nil {
			return err
		}
	}
	return nil
}
