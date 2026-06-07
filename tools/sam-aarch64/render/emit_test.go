package render

import (
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEmitEmpty(t *testing.T) {
	out, err := EmitFile(fileFromRecords(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("emit of empty file = %q, want empty", string(out))
	}
}

func TestEmitLabelDef(t *testing.T) {
	st := format.NewSymbolTable()
	st.Intern("loop")
	out, err := EmitFile(fileFromRecords(st.Names(), []format.Record{labelRec(0)}))
	if err != nil {
		t.Fatal(err)
	}
	want := "loop:\n"
	if string(out) != want {
		t.Errorf("emit = %q, want %q", string(out), want)
	}
}

func TestEmitLocalDef(t *testing.T) {
	out, _ := EmitFile(fileFromRecords(nil, []format.Record{localRec(3)}))
	if string(out) != "3:\n" {
		t.Errorf("emit = %q, want %q", string(out), "3:\n")
	}
}

func TestEmitCommentPlacement(t *testing.T) {
	st := format.NewSymbolTable()
	st.Intern("x")
	out, _ := EmitFile(fileFromRecords(st.Names(), []format.Record{
		commentRec(0, []byte(" standalone")),
		labelRec(0),
		commentRec(1, []byte(" trailing")),
	}))
	want := "// standalone\nx: // trailing\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", string(out), want)
	}
}
