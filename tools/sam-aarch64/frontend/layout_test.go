package frontend

import (
	"reflect"
	"testing"
)

// layoutSrc has a code section and a BSS section, so a change to the BSS
// section's alignment moves a label's VMA — the lever the custom-layout test
// pulls.
const layoutSrc = `
.text
_start:
	ret
.section bss_kernel
kbuf:
	.space 256
`

// TestLayoutTemplateMatchesSpectrumFour guards testdata/spectrum4.layout.json
// (the user-facing `-layout` template) against drift from SpectrumFourLayout, and
// exercises LoadLayoutFile against a real file.
func TestLayoutTemplateMatchesSpectrumFour(t *testing.T) {
	got, err := LoadLayoutFile("testdata/spectrum4.layout.json")
	if err != nil {
		t.Fatalf("LoadLayoutFile: %v", err)
	}
	if !reflect.DeepEqual(got, SpectrumFourLayout) {
		t.Fatalf("testdata/spectrum4.layout.json has drifted from SpectrumFourLayout\n got %+v\nwant %+v", got, SpectrumFourLayout)
	}
}

func TestParseLayoutRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty array":   `[]`,
		"unknown field": `[{"name":".text","startalign":16}]`,
		"empty name":    `[{"name":"","start_align":16}]`,
		"not json":      `{`,
	}
	for name, in := range cases {
		if _, err := ParseLayout([]byte(in)); err == nil {
			t.Errorf("%s: ParseLayout(%q) = nil error, want an error", name, in)
		}
	}
}

func TestLoadLayoutFileMissing(t *testing.T) {
	if _, err := LoadLayoutFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadLayoutFile of a missing file = nil error, want an error")
	}
}

// TestFlattenDefaultLayoutMatchesExplicit proves a nil opts.Layout falls back to
// SpectrumFourLayout exactly — the property that keeps the spectrum4 release
// byte-match unchanged after the extraction.
func TestFlattenDefaultLayoutMatchesExplicit(t *testing.T) {
	f := translateFile(t, layoutSrc)
	def, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000})
	if err != nil {
		t.Fatalf("Flatten (default layout): %v", err)
	}
	explicit, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000, Layout: SpectrumFourLayout})
	if err != nil {
		t.Fatalf("Flatten (explicit layout): %v", err)
	}
	if !reflect.DeepEqual(def, explicit) {
		t.Fatal("nil-layout flatten differs from explicit SpectrumFourLayout flatten")
	}
}

// TestFlattenCustomLayoutChangesPlacement proves the -layout table actually
// drives placement: bumping the BSS section's start alignment moves its label's
// VMA, so the flattened record stream differs.
func TestFlattenCustomLayoutChangesPlacement(t *testing.T) {
	f := translateFile(t, layoutSrc)
	def, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000})
	if err != nil {
		t.Fatalf("Flatten (default): %v", err)
	}

	custom := append([]SectionLayout(nil), SpectrumFourLayout...)
	for i := range custom {
		if custom[i].Name == "bss_kernel" {
			custom[i].StartAlign = 0x100000 // far larger than the default 0x10
		}
	}
	got, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000, Layout: custom})
	if err != nil {
		t.Fatalf("Flatten (custom): %v", err)
	}
	if reflect.DeepEqual(def, got) {
		t.Fatal("custom layout produced identical output; the layout table is not driving placement")
	}
}

// TestFlattenLayoutMissingUsedSection confirms a layout that omits a section the
// source uses is an error, not a silent drop.
func TestFlattenLayoutMissingUsedSection(t *testing.T) {
	f := translateFile(t, layoutSrc)
	// A layout with only .text/.data — bss_kernel (used by the source) is absent.
	partial := []SectionLayout{
		{Name: ".text", StartAlign: 0x10},
		{Name: ".data", StartAlign: 0x10},
	}
	if _, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000, Layout: partial}); err == nil {
		t.Fatal("Flatten with a layout missing a used section = nil error, want an error")
	}
}

func TestFlattenEmptyLayout(t *testing.T) {
	f := translateFile(t, layoutSrc)
	if _, err := Flatten(f.Records, f.Names, FlattenOptions{OriginVMA: 0x1000, Layout: []SectionLayout{}}); err == nil {
		t.Fatal("Flatten with an empty layout = nil error, want an error")
	}
}
