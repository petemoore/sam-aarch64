package format

import "testing"

func TestParseSysRegNamed(t *testing.T) {
	cases := []struct {
		name string
		want SysReg
	}{
		{"nzcv", SysReg{Op0: 3, Op1: 3, CRn: 4, CRm: 2, Op2: 0}},
		{"currentel", SysReg{Op0: 3, Op1: 0, CRn: 4, CRm: 2, Op2: 2}},
		{"midr_el1", SysReg{Op0: 3, Op1: 0, CRn: 0, CRm: 0, Op2: 0}},
		{"mpidr_el1", SysReg{Op0: 3, Op1: 0, CRn: 0, CRm: 0, Op2: 5}},
		{"sctlr_el1", SysReg{Op0: 3, Op1: 0, CRn: 1, CRm: 0, Op2: 0}},
		{"esr_el1", SysReg{Op0: 3, Op1: 0, CRn: 5, CRm: 2, Op2: 0}},
		{"elr_el1", SysReg{Op0: 3, Op1: 0, CRn: 4, CRm: 0, Op2: 1}},
		{"far_el1", SysReg{Op0: 3, Op1: 0, CRn: 6, CRm: 0, Op2: 0}},
		{"cntpct_el0", SysReg{Op0: 3, Op1: 3, CRn: 14, CRm: 0, Op2: 1}},
		{"cntp_ctl_el0", SysReg{Op0: 3, Op1: 3, CRn: 14, CRm: 2, Op2: 1}},
		{"cntp_cval_el0", SysReg{Op0: 3, Op1: 3, CRn: 14, CRm: 2, Op2: 2}},
		{"elr_el3", SysReg{Op0: 3, Op1: 6, CRn: 4, CRm: 0, Op2: 1}},
		{"NZCV", SysReg{Op0: 3, Op1: 3, CRn: 4, CRm: 2, Op2: 0}}, // case-insensitive
	}
	for _, c := range cases {
		got, ok := ParseSysReg(c.name)
		if !ok {
			t.Errorf("ParseSysReg(%q) = !ok", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSysReg(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseSysRegGeneric(t *testing.T) {
	cases := []struct {
		name string
		want SysReg
	}{
		{"s3_1_c11_c0_2", SysReg{Op0: 3, Op1: 1, CRn: 11, CRm: 0, Op2: 2}},
		{"S3_1_C15_C2_1", SysReg{Op0: 3, Op1: 1, CRn: 15, CRm: 2, Op2: 1}},
		{"s0_0_c0_c0_0", SysReg{Op0: 0, Op1: 0, CRn: 0, CRm: 0, Op2: 0}},
	}
	for _, c := range cases {
		got, ok := ParseSysReg(c.name)
		if !ok {
			t.Errorf("ParseSysReg(%q) = !ok", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSysReg(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseSysRegRejects(t *testing.T) {
	bad := []string{
		"",
		"x0",
		"sctlr",            // not a known name, not a generic
		"s_1_c1_c1_0",      // missing op0
		"s4_0_c0_c0_0",     // op0 out of range
		"s3_8_c0_c0_0",     // op1 out of range
		"s3_0_d0_c0_0",     // CRn prefix wrong
		"s3_0_c16_c0_0",    // CRn out of range
		"s3_0_c0_c0_8",     // op2 out of range
	}
	for _, n := range bad {
		if _, ok := ParseSysReg(n); ok {
			t.Errorf("ParseSysReg(%q) = ok, want !ok", n)
		}
	}
}

func TestParsePState(t *testing.T) {
	cases := []struct {
		name string
		want PState
	}{
		{"daifset", PState{Op1: 3, Op2: 6}},
		{"daifclr", PState{Op1: 3, Op2: 7}},
		{"spsel", PState{Op1: 0, Op2: 5}},
		{"DAIFSET", PState{Op1: 3, Op2: 6}},
	}
	for _, c := range cases {
		got, ok := ParsePState(c.name)
		if !ok {
			t.Errorf("ParsePState(%q) = !ok", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePState(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
	if _, ok := ParsePState("nzcv"); ok {
		t.Errorf("ParsePState(nzcv) should fail (not a pstate field)")
	}
}

func TestParseDC(t *testing.T) {
	cases := []struct {
		name    string
		want    DCOp
		needsXt bool
	}{
		{"civac", DCOp{Op1: 3, CRn: 7, CRm: 14, Op2: 1, NeedsXt: true}, true},
		{"cvac", DCOp{Op1: 3, CRn: 7, CRm: 10, Op2: 1, NeedsXt: true}, true},
		{"ivac", DCOp{Op1: 0, CRn: 7, CRm: 6, Op2: 1, NeedsXt: true}, true},
		{"zva", DCOp{Op1: 3, CRn: 7, CRm: 4, Op2: 1, NeedsXt: true}, true},
	}
	for _, c := range cases {
		got, ok := ParseDC(c.name)
		if !ok {
			t.Errorf("ParseDC(%q) = !ok", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDC(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
	if _, ok := ParseDC("vmalle1"); ok {
		t.Errorf("ParseDC(vmalle1) should fail (it's TLBI)")
	}
}

func TestParseTLBI(t *testing.T) {
	cases := []struct {
		name string
		want TLBIOp
	}{
		{"vmalle1", TLBIOp{Op1: 0, CRn: 8, CRm: 7, Op2: 0}},
		{"vae1is", TLBIOp{Op1: 0, CRn: 8, CRm: 3, Op2: 1, NeedsXt: true}},
		{"alle1", TLBIOp{Op1: 4, CRn: 8, CRm: 7, Op2: 4}},
	}
	for _, c := range cases {
		got, ok := ParseTLBI(c.name)
		if !ok {
			t.Errorf("ParseTLBI(%q) = !ok", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("ParseTLBI(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}
