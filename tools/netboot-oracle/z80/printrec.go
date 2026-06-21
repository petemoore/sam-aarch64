package z80

import "github.com/koron-go/z80"

// PrintRecorder captures the characters a routine prints via the SAM ROM
// print-a-char restart (RST &10). It lets a test prove that screen-drawing code
// runs and emits the right text in the flat harness — which has neither a ROM nor
// a screen model — the same way AttachBDOS lets a test model the RST 8 hooks.
type PrintRecorder struct{ chars []byte }

// Chars returns the characters printed so far (in order).
func (p *PrintRecorder) Chars() []byte { return p.chars }

// AttachPrintRecorder intercepts RST &10 (the SAM ROM "print the character in A"
// restart, &0010): each call records A and resumes after the RST, so a print loop
// runs to completion without a ROM. Returns the recorder to read the text back.
func (mac *Machine) AttachPrintRecorder() *PrintRecorder {
	p := &PrintRecorder{}
	mac.setRSTHandler(0x0010, func(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16 {
		p.chars = append(p.chars, cpu.AF.Hi)
		return retAddr // RST &10 has no inline operand byte; resume after it
	})
	return p
}
