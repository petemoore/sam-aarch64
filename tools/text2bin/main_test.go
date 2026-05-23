package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestEmptyFileRoundtrip(t *testing.T) {
	out, err := Translate([]byte(""), "empty.s")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Write(out)
	f, err := format.ReadFile(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if f.Version != 1 || len(f.Names) != 0 || len(f.Records) != 0 {
		t.Errorf("unexpected file shape: %+v", f)
	}
}
