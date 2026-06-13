package mra

import (
	"testing"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

func TestMapField_RegisterNames(t *testing.T) {
	cases := []struct {
		name string
		ctx  FieldContext
		want enc.SlotKind
	}{
		{"Rd", FieldContext{Is64: true, AcceptsSP: false}, enc.Xreg},
		{"Rd", FieldContext{Is64: false, AcceptsSP: false}, enc.Wreg},
		{"Rd", FieldContext{Is64: true, AcceptsSP: true}, enc.XregOrSp},
		{"Rd", FieldContext{Is64: false, AcceptsSP: true}, enc.WregOrSp},
		{"Rn", FieldContext{Is64: true}, enc.Xreg},
		{"Rt", FieldContext{Is64: true}, enc.Xreg},
		{"Rm", FieldContext{Is64: true}, enc.Xreg},
	}
	for _, c := range cases {
		got, ok := MapField(c.name, c.ctx)
		if !ok || got != c.want {
			t.Errorf("MapField(%q, %+v) = (%v, %v), want %v", c.name, c.ctx, got, ok, c.want)
		}
	}
}

func TestMapField_Immediates(t *testing.T) {
	cases := []struct {
		name string
		want enc.SlotKind
	}{
		{"imm12", enc.Imm12Shifted},
		{"imm16", enc.Imm16Shifted},
		{"imm26", enc.BranchImm26},
		{"imm19", enc.BranchImm19},
		{"imm14", enc.BranchImm14},
		{"cond", enc.CondCode},
	}
	for _, c := range cases {
		got, ok := MapField(c.name, FieldContext{})
		if !ok || got != c.want {
			t.Errorf("MapField(%q) = (%v, %v), want %v", c.name, got, ok, c.want)
		}
	}
}

func TestMapField_UnknownReturnsFalse(t *testing.T) {
	if _, ok := MapField("xyzzy_not_a_real_field", FieldContext{}); ok {
		t.Errorf("MapField returned ok for nonsense name")
	}
}
