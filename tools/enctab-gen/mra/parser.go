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
			for _, dv := range enc.DocVars {
				if dv.Key == "mnemonic" {
					mnemonic = dv.Value
				}
			}
			forms = append(forms, ParsedForm{
				Mnemonic:    mnemonic,
				Pattern:     pattern,
				Mask:        mask,
				RawOperands: slots,
			})
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
