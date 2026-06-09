package frontend

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func parseHelper(t *testing.T, src string) *format.File {
	t.Helper()
	return translateFile(t, src)
}

func TestParseLabelDef(t *testing.T) {
	f := parseHelper(t, "loop:\n")
	if len(f.Names) != 1 || f.Names[0] != "loop" {
		t.Errorf("names = %v", f.Names)
	}
	r := newRecCursor(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindLabelDef || rec.SymbolID != 0 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseLocalLabelDef(t *testing.T) {
	f := parseHelper(t, "3:\n")
	r := newRecCursor(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindLocalDef || rec.Digit != 3 {
		t.Errorf("rec = %+v", rec)
	}
}

func TestParseStandaloneComment(t *testing.T) {
	f := parseHelper(t, "// banner\n/* block */\n")
	r := newRecCursor(f.Records)
	rec, _ := r.Next()
	if rec.Kind != format.KindComment || rec.Placement != 0 || string(rec.Body) != " banner" {
		t.Errorf("rec0 = %+v", rec)
	}
	rec, _ = r.Next()
	if rec.Kind != format.KindComment || rec.Placement != 0 || string(rec.Body) != " block " {
		t.Errorf("rec1 = %+v", rec)
	}
}

func TestParseBlankLinesSkipped(t *testing.T) {
	f := parseHelper(t, "\n\n\n")
	if len(f.Records) != 0 {
		t.Errorf("expected no records, got %+v", f.Records)
	}
}

func TestParseLabelOnSameLineAsAnotherToken(t *testing.T) {
	f := parseHelper(t, "exit: // bye\n")
	rr := newRecCursor(f.Records)
	rec, _ := rr.Next()
	if rec.Kind != format.KindLabelDef {
		t.Errorf("rec0 = %+v", rec)
	}
	rec, _ = rr.Next()
	if rec.Kind != format.KindComment || rec.Placement != 1 || string(rec.Body) != " bye" {
		t.Errorf("rec1 = %+v", rec)
	}
}

func TestParseUnknownTokenFailsWithLocation(t *testing.T) {
	_, err := Translate([]byte("?\n"), "f.s")
	if err == nil {
		t.Fatal("expected error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("f.s:1:1")) {
		t.Errorf("error lacks position: %v", err)
	}
}
