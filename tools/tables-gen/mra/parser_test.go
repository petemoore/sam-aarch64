package mra

import (
	"os"
	"strings"
	"testing"
)

// minimalNopXML is a synthetic minimal NOP encoding that matches the
// structure of the plan's expected schema (individual <c> per bit, no
// settings/colspan/usename attributes). The parser must handle both this
// simplified form and the real MRA XML (see TestParseRealNopXML).
//
// NOP encoding: 0xD503201F
//   bits[31:22] = 1101010100
//   bit[21]     = 0  (L)
//   bits[20:19] = 00 (op0)
//   bits[18:16] = 011 (op1)
//   bits[15:12] = 0010 (CRn)
//   bits[11:8]  = 0000 (CRm)
//   bits[7:5]   = 000 (op2)
//   bits[4:0]   = 11111 (Rt)
const minimalNopXML = `<?xml version="1.0" ?>
<instructionsection type="instruction" id="NOP" title="NOP">
  <classes>
    <iclass name="System" oneof="1" id="iclass_system">
      <regdiagram form="32">
        <box hibit="31" width="10"><c>1</c><c>1</c><c>0</c><c>1</c><c>0</c><c>1</c><c>0</c><c>1</c><c>0</c><c>0</c></box>
        <box hibit="21" width="1"><c>0</c></box>
        <box hibit="20" width="2"><c>0</c><c>0</c></box>
        <box hibit="18" width="3"><c>0</c><c>1</c><c>1</c></box>
        <box hibit="15" width="4"><c>0</c><c>0</c><c>1</c><c>0</c></box>
        <box hibit="11" width="4"><c>0</c><c>0</c><c>0</c><c>0</c></box>
        <box hibit="7" width="3"><c>0</c><c>0</c><c>0</c></box>
        <box hibit="4" width="5"><c>1</c><c>1</c><c>1</c><c>1</c><c>1</c></box>
      </regdiagram>
      <encoding id="NOP_HI_hints" oneofinclass="1">
        <docvars>
          <docvar key="mnemonic" value="NOP"/>
        </docvars>
        <asmtemplate><text>NOP</text></asmtemplate>
      </encoding>
    </iclass>
  </classes>
</instructionsection>`

func TestParseMinimalNop(t *testing.T) {
	forms, err := ParseInstructionXML(strings.NewReader(minimalNopXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(forms) != 1 {
		t.Fatalf("expected 1 form, got %d", len(forms))
	}
	f := forms[0]
	if f.Mnemonic != "NOP" {
		t.Errorf("mnemonic = %q, want NOP", f.Mnemonic)
	}
	// All 32 bits fixed → pattern = 0xD503201F, mask = 0xFFFFFFFF.
	if f.Pattern != 0xD503201F {
		t.Errorf("pattern = 0x%08x, want 0xD503201F", f.Pattern)
	}
	if f.Mask != 0xFFFFFFFF {
		t.Errorf("mask = 0x%08x, want 0xFFFFFFFF", f.Mask)
	}
	if len(f.RawOperands) != 0 {
		t.Errorf("expected 0 operands, got %d: %+v", len(f.RawOperands), f.RawOperands)
	}
}

func TestParseRealNopXML(t *testing.T) {
	// Real vendored XML — exercises the schema as it actually is.
	f, err := openXML(t, "../../../reference/arm-mra/nop.xml")
	if err != nil {
		t.Fatalf("vendored reference/arm-mra/nop.xml missing or unreadable: %v", err)
	}
	defer f.Close()
	forms, err := ParseInstructionXML(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// We expect at least one form whose mnemonic is NOP (case-insensitive).
	var nopForm *ParsedForm
	for i := range forms {
		if strings.EqualFold(forms[i].Mnemonic, "NOP") {
			nopForm = &forms[i]
			break
		}
	}
	if nopForm == nil {
		t.Fatalf("no NOP form found among %d forms: %+v", len(forms), forms)
	}
	if nopForm.Pattern != 0xD503201F {
		t.Errorf("NOP pattern = 0x%08x, want 0xD503201F", nopForm.Pattern)
	}
	if nopForm.Mask != 0xFFFFFFFF {
		t.Errorf("NOP mask = 0x%08x, want 0xFFFFFFFF", nopForm.Mask)
	}
	if len(nopForm.RawOperands) != 0 {
		t.Errorf("NOP expected 0 operands, got %d: %+v", len(nopForm.RawOperands), nopForm.RawOperands)
	}
}

func openXML(t *testing.T, path string) (*os.File, error) {
	t.Helper()
	return os.Open(path)
}
