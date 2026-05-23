package main

import "testing"

func TestLexBasic(t *testing.T) {
	toks, err := Lex([]byte("add x0, x1, #4\n"), "f.s")
	if err != nil {
		t.Fatal(err)
	}
	want := []TokKind{
		TokIdent, TokIdent, TokComma, TokIdent, TokComma,
		TokHash, TokInt, TokEOL, TokEOF,
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d toks, want %d: %+v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i].Kind != w {
			t.Errorf("tok[%d] = %v, want %v", i, toks[i].Kind, w)
		}
	}
}

func TestLexComments(t *testing.T) {
	toks, _ := Lex([]byte("// hi\nadd /* mid */ x0\n"), "f.s")
	want := []TokKind{TokLineComment, TokEOL, TokIdent, TokBlockComment, TokIdent, TokEOL, TokEOF}
	for i, w := range want {
		if toks[i].Kind != w {
			t.Errorf("tok[%d] = %v, want %v", i, toks[i].Kind, w)
		}
	}
}

func TestLexNumberBases(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"42", 42},
		{"0x2a", 42},
		{"0b101010", 42},
		{"'A'", 65},
	}
	for _, c := range cases {
		toks, err := Lex([]byte(c.in+"\n"), "f.s")
		if err != nil {
			t.Fatalf("lex %q: %v", c.in, err)
		}
		if toks[0].Kind != TokInt || toks[0].Int != c.want {
			t.Errorf("lex %q: got %+v, want int %d", c.in, toks[0], c.want)
		}
	}
}

func TestLexStringLit(t *testing.T) {
	toks, err := Lex([]byte(`.ascii "hi\n"`+"\n"), "f.s")
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Kind != TokIdent || toks[0].Text != ".ascii" {
		t.Errorf("tok[0] = %+v", toks[0])
	}
	if toks[1].Kind != TokString || string(toks[1].Bytes) != "hi\n" {
		t.Errorf("tok[1] = %+v", toks[1])
	}
}

func TestLexLocalLabelRef(t *testing.T) {
	toks, _ := Lex([]byte("b 1f\n"), "f.s")
	if toks[1].Kind != TokLocalRef || toks[1].Digit != 1 || toks[1].LocalDir != 'f' {
		t.Errorf("tok[1] = %+v, want LocalRef 1f", toks[1])
	}
}
