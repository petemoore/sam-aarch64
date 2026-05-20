package builder

import (
	"fmt"
	"testing"
)

// fakeLine emits a minimal valid SAM BASIC program-area line:
// 4-byte header (line number BE + length LE) followed by a single
// 0x0D terminator (so lineLen = 1).
func fakeLine(lineNo uint16) []byte {
	return []byte{
		byte(lineNo >> 8), byte(lineNo & 0xFF),
		0x01, 0x00,
		0x0D,
	}
}

func progArea(lineNumbers ...uint16) []byte {
	var out []byte
	for _, n := range lineNumbers {
		out = append(out, fakeLine(n)...)
	}
	return out
}

func TestFindFreeLine(t *testing.T) {
	cases := []struct {
		name  string
		lines []uint16
		want  uint16
	}{
		{"empty program", nil, 1},
		{"single high line", []uint16{10}, 1},
		{"consecutive from 1", []uint16{1, 2, 3}, 4},
		{"gap at 3", []uint16{1, 2, 4, 5}, 3},
		{"starts at 2", []uint16{2, 3, 4}, 1},
		{"random spread", []uint16{10, 20, 30}, 1},
		{"1 used, gap at 2", []uint16{1, 3, 5}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findFreeLine(progArea(tc.lines...))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("findFreeLine = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFindInsertionOffset(t *testing.T) {
	cases := []struct {
		name   string
		lines  []uint16
		lineNo uint16
		want   int
	}{
		{"empty program", nil, 1, 0},
		{"insert at start (before line 10)", []uint16{10, 20}, 1, 0},
		{"insert between 10 and 20", []uint16{10, 20}, 15, 5},
		{"insert at end (above all)", []uint16{10, 20}, 30, 10},
		{"insert before single line", []uint16{100}, 50, 0},
		{"insert at gap between 2 and 4", []uint16{1, 2, 4, 5}, 3, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findInsertionOffset(progArea(tc.lines...), tc.lineNo)
			if got != tc.want {
				t.Errorf("findInsertionOffset = %d, want %d", got, tc.want)
			}
		})
	}
}

// LLIST format per logical line: 4 leading spaces + line number
// right-aligned to fill chars [0..4], cursor char at char 5 (' ' or
// '>'), then content. Continuations start with 6 spaces. These helpers
// build realistic LLIST output for the test cases.
func llistLogical(n uint16, cursor byte, body string) string {
	// Format the line number right-aligned to 5 chars.
	num := fmt.Sprintf("%5d", n)
	return num + string(cursor) + body
}

func llistCont(body string) string {
	return "      " + body
}

func TestStripInjectedLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    uint16
		want string
	}{
		{
			name: "single line - injected only",
			in:   llistLogical(1, ' ', "POKE 1,2") + "\r\n",
			n:    1,
			want: "",
		},
		{
			name: "injected first, user lines follow",
			in: llistLogical(1, ' ', "POKE 1,2") + "\r\n" +
				llistLogical(10, ' ', "PRINT") + "\r\n" +
				llistLogical(20, ' ', "GOTO 10") + "\r\n",
			n: 1,
			want: llistLogical(10, ' ', "PRINT") + "\r\n" +
				llistLogical(20, ' ', "GOTO 10") + "\r\n",
		},
		{
			name: "injected middle",
			in: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(7, ' ', "POKE 1,2") + "\r\n" +
				llistLogical(10, ' ', "GOTO 5") + "\r\n",
			n: 7,
			want: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(10, ' ', "GOTO 5") + "\r\n",
		},
		{
			name: "injected last",
			in: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(10, ' ', "PRINT") + "\r\n" +
				llistLogical(99, ' ', "CALL 16384") + "\r\n",
			n: 99,
			want: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(10, ' ', "PRINT") + "\r\n",
		},
		{
			name: "injected with wrap continuation",
			in: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(99, ' ', "POKE 23203,0: POKE 23204,0: POKE 16384,243: POKE 16385,118: LLIST : CAL") + "\r\n" +
				llistCont("L 16384") + "\r\n" +
				llistLogical(100, ' ', "GOTO 5") + "\r\n",
			n: 99,
			want: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(100, ' ', "GOTO 5") + "\r\n",
		},
		{
			name: "cursor `>` on injected line",
			in: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(7, '>', "POKE 1,2") + "\r\n" +
				llistLogical(10, ' ', "GOTO 5") + "\r\n",
			n: 7,
			want: llistLogical(5, ' ', "PRINT") + "\r\n" +
				llistLogical(10, ' ', "GOTO 5") + "\r\n",
		},
		{
			name: "N=1 must not match line 10",
			in: llistLogical(1, ' ', "POKE") + "\r\n" +
				llistLogical(10, ' ', "PRINT") + "\r\n",
			n:    1,
			want: llistLogical(10, ' ', "PRINT") + "\r\n",
		},
		{
			name: "N=15 must not match line 150",
			in: llistLogical(15, ' ', "INJ") + "\r\n" +
				llistLogical(150, ' ', "USER") + "\r\n",
			n:    15,
			want: llistLogical(150, ' ', "USER") + "\r\n",
		},
		{
			name: "N=2 must not match line 20",
			in: llistLogical(2, ' ', "INJ") + "\r\n" +
				llistLogical(20, ' ', "USER") + "\r\n",
			n:    2,
			want: llistLogical(20, ' ', "USER") + "\r\n",
		},
		{
			name: "wrap continuation on a NON-injected line is kept",
			in: llistLogical(5, ' ', "PRINT \"this is a really long line that wraps\"") + "\r\n" +
				llistCont("more wrapped content") + "\r\n" +
				llistLogical(7, ' ', "POKE 1,2") + "\r\n",
			n: 7,
			want: llistLogical(5, ' ', "PRINT \"this is a really long line that wraps\"") + "\r\n" +
				llistCont("more wrapped content") + "\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(StripInjectedLine([]byte(tc.in), tc.n))
			if got != tc.want {
				t.Errorf("StripInjectedLine\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
