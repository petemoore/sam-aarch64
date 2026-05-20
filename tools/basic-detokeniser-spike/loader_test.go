package main

import (
	"testing"
)

func TestPosAdvance_StaysInSamePage(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x100)
	want := pos{page: 1, offset: 0x1DD5}
	if got != want {
		t.Errorf("advance(0x100): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2AtExactBoundary(t *testing.T) {
	// page 1 holds 0x4000 - 0x1CD5 = 0x232B bytes from PROG.
	// Advancing exactly 0x232B from (1, 0x1CD5) lands on (2, 0x0000).
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232B)
	want := pos{page: 2, offset: 0x0000}
	if got != want {
		t.Errorf("advance(0x232B): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_CrossesPage1To2WithRemainder(t *testing.T) {
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(0x232C) // one past the boundary
	want := pos{page: 2, offset: 0x0001}
	if got != want {
		t.Errorf("advance(0x232C): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_SpansMultiplePages(t *testing.T) {
	// 50000 bytes from (1, 0x1CD5):
	//   page 1 absorbs 0x4000 - 0x1CD5 = 0x232B = 9003 bytes
	//   page 2 absorbs 0x4000 = 16384 bytes (cumulative 25387)
	//   page 3 absorbs 0x4000 = 16384 bytes (cumulative 41771)
	//   page 4 absorbs 50000 - 41771 = 8229 = 0x2025 bytes
	p := pos{page: 1, offset: 0x1CD5}
	got := p.advance(50000)
	want := pos{page: 4, offset: 0x2025}
	if got != want {
		t.Errorf("advance(50000): got %+v, want %+v", got, want)
	}
}

func TestPosAdvance_ZeroIsIdentity(t *testing.T) {
	p := pos{page: 7, offset: 0x1234}
	got := p.advance(0)
	if got != p {
		t.Errorf("advance(0): got %+v, want %+v", got, p)
	}
}
