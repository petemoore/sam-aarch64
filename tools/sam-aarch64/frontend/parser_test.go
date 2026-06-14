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

func TestParseBlankLinesBecomeBlankRun(t *testing.T) {
	// i78: blank source lines are preserved as a single BLANK_RUN record
	// carrying the run length, so the renderer can reproduce them.
	f := parseHelper(t, "\n\n\n")
	if len(f.Records) != 1 {
		t.Fatalf("expected one blank-run record, got %+v", f.Records)
	}
	rec := f.Records[0]
	if rec.Kind != format.KindBlankRun || rec.RunLen != 3 {
		t.Errorf("rec = %+v (want KindBlankRun RunLen=3)", rec)
	}
}

func TestParseBlankRunBetweenStatements(t *testing.T) {
	// A blank run between two statements is one record between them; a textless
	// `//` is a separate KindComment (empty body), not a blank run.
	f := parseHelper(t, "  nop\n\n\n  nop\n")
	var kinds []format.RecordKind
	var runLen uint32
	for _, r := range f.Records {
		kinds = append(kinds, r.Kind)
		if r.Kind == format.KindBlankRun {
			runLen = r.RunLen
		}
	}
	// inst, blank-run(2), inst
	if len(kinds) != 3 || kinds[0] != format.KindInst || kinds[1] != format.KindBlankRun || kinds[2] != format.KindInst {
		t.Fatalf("kinds = %v", kinds)
	}
	if runLen != 2 {
		t.Errorf("blank run len = %d, want 2", runLen)
	}
}

func TestParseTextlessCommentIsNotBlankRun(t *testing.T) {
	// A `//` with no body is a comment row (empty body), distinct from a blank
	// source line (design §1 disambiguation).
	f := parseHelper(t, "//\n")
	if len(f.Records) != 1 {
		t.Fatalf("records = %+v", f.Records)
	}
	rec := f.Records[0]
	if rec.Kind != format.KindComment || len(rec.Body) != 0 {
		t.Errorf("rec = %+v (want empty-body KindComment)", rec)
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
