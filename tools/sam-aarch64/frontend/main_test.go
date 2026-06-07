package frontend

import (
	"testing"
)

func TestEmptyFileRoundtrip(t *testing.T) {
	f, err := Translate([]byte(""), "empty.s")
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 || len(f.Names) != 0 || len(f.Records) != 0 {
		t.Errorf("unexpected file shape: %+v", f)
	}
}
