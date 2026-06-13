// Package mra parses ARM MRA (Machine Readable Architecture) XML
// instruction-class files into a structure the enctab-gen generator
// can map into aarch64enc Forms.
//
// Schema notes (this release of the ARM MRA):
//
//   - A <box> with a "settings" attribute has all its <c> children treated
//     as fixed bits (mask bits), even if the box also has a "name" attribute.
//     The settings value equals the number of fixed bits in the box.
//
//   - A <box> without a "settings" attribute (or settings="0") is a variable
//     field. If it also has usename="1", it is a named operand slot.
//
//   - Within a variable box, bits may be represented as:
//       * Individual <c></c> elements (empty body = operand bit).
//       * A single <c colspan="N"> element covering all N bits at once.
//       * Individual <c>0</c> or <c>1</c> — not expected in variable boxes
//         but handled defensively.
//
//   - The real XML has a DOCTYPE declaration. Go's encoding/xml decoder
//     handles this by skipping the directive token; no special handling needed.
package mra

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParsedForm is one encoding (instructionsection > iclass > encoding).
// Slot mapping to aarch64enc.OperandSlot happens in Task 14.
type ParsedForm struct {
	Mnemonic    string
	Pattern     uint32
	Mask        uint32
	RawOperands []RawOperandSlot
}

// RawOperandSlot is one named box in the regdiagram that is a variable
// (non-fixed) field; converted to an aarch64enc.OperandSlot by enctab-gen's
// field-mapping logic.
type RawOperandSlot struct {
	Name        string
	BitPosition uint8 // LSB of the box
	BitWidth    uint8
}

// XML types matching the real MRA schema.

type xmlC struct {
	XMLName xml.Name `xml:"c"`
	Colspan string   `xml:"colspan,attr"`
	Body    string   `xml:",chardata"`
}

type xmlBox struct {
	XMLName  xml.Name `xml:"box"`
	Hibit    int      `xml:"hibit,attr"`
	Width    int      `xml:"width,attr"`
	Name     string   `xml:"name,attr"`
	Settings string   `xml:"settings,attr"` // present → all <c> are fixed
	Usename  string   `xml:"usename,attr"`
	Cs       []xmlC   `xml:"c"`
}

type xmlRegDiagram struct {
	XMLName xml.Name `xml:"regdiagram"`
	Boxes   []xmlBox `xml:"box"`
}

type xmlDocVar struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

type xmlEncoding struct {
	XMLName xml.Name    `xml:"encoding"`
	DocVars []xmlDocVar `xml:"docvars>docvar"`
	// Encoding-level boxes override iclass-level variable boxes with fixed values
	// (bitdiffs). Each box here has a name matching an iclass-level variable box
	// and a fixed <c>0</c> or <c>1</c> body.
	Boxes []xmlBox `xml:"box"`
}

type xmlIclass struct {
	XMLName  xml.Name      `xml:"iclass"`
	Reg      xmlRegDiagram `xml:"regdiagram"`
	Encoding []xmlEncoding `xml:"encoding"`
}

type xmlInstruction struct {
	XMLName xml.Name    `xml:"instructionsection"`
	Classes []xmlIclass `xml:"classes>iclass"`
}

// ParseInstructionXML reads one MRA instruction XML and returns
// every encoding declared inside.
func ParseInstructionXML(r io.Reader) ([]ParsedForm, error) {
	var doc xmlInstruction
	dec := xml.NewDecoder(r)
	// The real MRA XMLs contain a DOCTYPE declaration. Go's xml.Decoder
	// handles unknown directives by returning a xml.Directive token which
	// we can safely skip. Set AutoClose to avoid issues with HTML-style tags.
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("mra: xml decode: %w", err)
	}

	var forms []ParsedForm
	for _, cls := range doc.Classes {
		pattern, mask, slots := convertRegDiagram(cls.Reg.Boxes)
		for _, enc := range cls.Encoding {
			mnemonic := ""
			aliasMnemonic := ""
			for _, dv := range enc.DocVars {
				switch dv.Key {
				case "mnemonic":
					mnemonic = dv.Value
				case "alias_mnemonic":
					aliasMnemonic = dv.Value
				}
			}
			// Apply encoding-level box overrides (bitdiffs): these fix iclass-level
			// variable fields to specific 0/1 values for this encoding variant.
			// We update pattern/mask and remove the overridden field from slots.
			encPattern, encMask, filteredSlots := applyEncodingOverrides(pattern, mask, slots, enc.Boxes)
			forms = append(forms, ParsedForm{
				Mnemonic:    mnemonic,
				Pattern:     encPattern,
				Mask:        encMask,
				RawOperands: filteredSlots,
			})
			// If the encoding also declares an alias_mnemonic (e.g. CMP is an alias
			// for SUBS, MOV is an alias for ADD), emit a second form for the alias so
			// the encoder table covers both the canonical and alias spellings.
			if aliasMnemonic != "" && aliasMnemonic != mnemonic {
				forms = append(forms, ParsedForm{
					Mnemonic:    aliasMnemonic,
					Pattern:     encPattern,
					Mask:        encMask,
					RawOperands: filteredSlots,
				})
			}
		}
	}
	return forms, nil
}

// convertRegDiagram converts the box-and-c structure into the
// pattern/mask/operand-slot triple.
//
// Each box covers bits [hibit, hibit-width+1]. The width defaults to 1 if
// the attribute is absent (zero value).
//
// Fixed-bit rule: a box with a non-empty "settings" attribute treats ALL its
// <c> children as fixed bits (0 or 1), regardless of the box name. This is
// how the MRA marks ISA-defined constants (e.g. NOP's CRm, op2, Rt fields).
//
// Variable-bit rule: a box without "settings" is a variable field. Its bits
// may be expressed as:
//   - Individual <c></c> (empty body) — one operand bit each.
//   - A single <c colspan="N"> — N operand bits.
//   - Individual <c>0</c> or <c>1</c> — fixed bits within the box (rare).
//
// Operand slot: a variable box with usename="1" and a non-empty name becomes
// a RawOperandSlot if it contains at least one operand bit.
func convertRegDiagram(boxes []xmlBox) (pattern, mask uint32, slots []RawOperandSlot) {
	for _, box := range boxes {
		width := box.Width
		if width == 0 {
			width = 1
		}
		if box.Hibit < 0 || box.Hibit > 31 {
			continue
		}
		lobit := box.Hibit - width + 1
		if lobit < 0 {
			continue
		}

		isFixed := strings.TrimSpace(box.Settings) != ""

		if isFixed {
			// All <c> children are fixed bits in left-to-right (MSB-first) order.
			for i, c := range box.Cs {
				bitPos := box.Hibit - i
				if bitPos < 0 || bitPos > 31 {
					continue
				}
				body := strings.TrimSpace(c.Body)
				mask |= uint32(1) << bitPos
				if body == "1" {
					pattern |= uint32(1) << bitPos
				}
				// "0" or anything else → mask set, pattern clear (already done).
			}
			continue
		}

		// Variable box: parse each <c> element.
		hasOperandBit := false
		if len(box.Cs) == 0 {
			// No <c> elements at all → treat all bits as operand bits.
			hasOperandBit = true
		}
		for i, c := range box.Cs {
			colspanStr := strings.TrimSpace(c.Colspan)
			if colspanStr != "" {
				// A single <c colspan="N"> covers all N bits. Empty body = operand.
				n, err := strconv.Atoi(colspanStr)
				if err != nil || n <= 0 {
					n = width
				}
				body := strings.TrimSpace(c.Body)
				if body == "0" || body == "1" {
					// Fixed bits within a colspan — set each.
					for j := 0; j < n; j++ {
						bitPos := box.Hibit - j
						if bitPos < 0 || bitPos > 31 {
							continue
						}
						mask |= uint32(1) << bitPos
						if body == "1" {
							pattern |= uint32(1) << bitPos
						}
					}
				} else {
					// Operand bits.
					hasOperandBit = true
				}
			} else {
				// Individual <c> — one bit.
				bitPos := box.Hibit - i
				if bitPos < 0 || bitPos > 31 {
					continue
				}
				body := strings.TrimSpace(c.Body)
				switch body {
				case "0":
					mask |= uint32(1) << bitPos
				case "1":
					mask |= uint32(1) << bitPos
					pattern |= uint32(1) << bitPos
				default:
					// Empty or any other value → operand bit.
					hasOperandBit = true
				}
			}
		}

		if box.Name != "" && box.Usename == "1" && hasOperandBit {
			slots = append(slots, RawOperandSlot{
				Name:        box.Name,
				BitPosition: uint8(lobit),
				BitWidth:    uint8(width),
			})
		}
	}
	return pattern, mask, slots
}

// applyEncodingOverrides applies encoding-level <box> elements (bitdiffs) to
// the iclass-level pattern/mask/slots. Each override box has a name matching
// an iclass-level variable slot and fixed <c>0</c> or <c>1</c> children.
//
// For each override box:
//  1. Its bits are applied to pattern/mask (fixing the bit values).
//  2. The corresponding slot is removed from the returned slots slice.
//
// Slots not overridden are passed through unchanged.
func applyEncodingOverrides(pattern, mask uint32, slots []RawOperandSlot, overrides []xmlBox) (uint32, uint32, []RawOperandSlot) {
	if len(overrides) == 0 {
		return pattern, mask, slots
	}

	// Build a set of overridden field names.
	overridden := make(map[string]bool)
	for _, box := range overrides {
		if box.Name == "" {
			continue
		}
		width := box.Width
		if width == 0 {
			width = 1
		}
		if box.Hibit < 0 || box.Hibit > 31 {
			continue
		}
		// Apply each <c> child as a fixed bit.
		// An empty body means this bit remains variable (not fixed by
		// this override); skip it so we don't incorrectly clear the
		// mask/pattern bits that the iclass already set.
		for i, c := range box.Cs {
			bitPos := box.Hibit - i
			if bitPos < 0 || bitPos > 31 {
				continue
			}
			body := strings.TrimSpace(c.Body)
			if body == "" {
				// Variable bit — not being overridden; leave as-is.
				continue
			}
			mask |= uint32(1) << bitPos
			if body == "1" {
				pattern |= uint32(1) << bitPos
			} else {
				pattern &^= uint32(1) << bitPos
			}
		}
		overridden[box.Name] = true
	}

	// Filter slots: remove any whose name was overridden.
	filtered := slots[:0:0]
	for _, s := range slots {
		if !overridden[s.Name] {
			filtered = append(filtered, s)
		}
	}
	return pattern, mask, filtered
}
