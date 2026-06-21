package frontend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// LoadLayoutFile reads a section-layout table from a JSON file — the backing
// format of the assembler's `-layout` flag, which lets a non-spectrum4 target
// drive the host flatten pass without recompiling (i56). The file is a JSON
// array of objects {name, start_align, trailing_align}, in section order; roles
// are assigned by position (see SectionLayout). A nil/absent flag leaves
// FlattenOptions.Layout nil, so Flatten falls back to SpectrumFourLayout.
//
// testdata/spectrum4.layout.json is the SpectrumFourLayout table in this format —
// a ready template to copy and edit for a new target.
func LoadLayoutFile(path string) ([]SectionLayout, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read layout %q: %w", path, err)
	}
	layout, err := ParseLayout(data)
	if err != nil {
		return nil, fmt.Errorf("layout %q: %w", path, err)
	}
	return layout, nil
}

// ParseLayout decodes a JSON section-layout table (see LoadLayoutFile). It
// rejects unknown fields (so a mistyped key is an error, not a silent zero) and
// an empty or unnamed-section table.
func ParseLayout(data []byte) ([]SectionLayout, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var layout []SectionLayout
	if err := dec.Decode(&layout); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(layout) == 0 {
		return nil, fmt.Errorf("no sections")
	}
	for i, s := range layout {
		if s.Name == "" {
			return nil, fmt.Errorf("section %d has an empty name", i)
		}
	}
	return layout, nil
}
