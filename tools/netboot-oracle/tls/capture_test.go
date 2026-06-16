package tls

// Verifies the brick-6 host oracle (capture.go): a CaptureHandshake transcript is
// a complete, replayable record. Replaying its Inbound through a fresh
// deterministic client (the same inputs) must reproduce the captured ClientHello
// record, the client Finished record, and all four traffic secrets — exactly the
// diff the Z80 tls_client.asm test performs, with the Z80 in place of this client.

import (
	"bytes"
	"testing"
)

func TestCaptureHandshakeReplay(t *testing.T) {
	var priv, random, sid [32]byte
	for i := range priv {
		priv[i] = byte(i + 1)
		random[i] = byte(i*3 + 7)
		sid[i] = byte(i*5 + 0x40)
	}

	cap, err := CaptureHandshake("github.com", priv, random, sid)
	if err != nil {
		t.Fatalf("CaptureHandshake: %v", err)
	}

	// Well-formedness of the recording.
	if cap.Priv != priv || cap.Random != random || cap.Sid != sid {
		t.Errorf("Capture did not echo the injected inputs")
	}
	if len(cap.CHRecord) < 5 || cap.CHRecord[0] != 0x16 {
		t.Errorf("CHRecord is not a plaintext handshake record: % x", firstBytes(cap.CHRecord, 5))
	}
	if len(cap.Inbound) == 0 {
		t.Fatalf("no inbound records captured")
	}
	if cap.Inbound[0][0] != 0x16 {
		t.Errorf("first inbound record (ServerHello) type = 0x%02x, want 0x16", cap.Inbound[0][0])
	}
	if len(cap.FinRecord) < 5 || cap.FinRecord[0] != 0x17 {
		t.Errorf("FinRecord is not an application_data (encrypted) record: % x", firstBytes(cap.FinRecord, 5))
	}
	for _, s := range [][]byte{cap.CHS, cap.SHS, cap.CAP, cap.SAP} {
		if len(s) != 32 {
			t.Errorf("traffic secret length = %d, want 32", len(s))
		}
	}

	// Replay: a fresh deterministic client fed the captured Inbound reproduces
	// every captured client output and secret — the oracle is sound.
	c, err := NewClientDeterministic("github.com", priv[:], random, sid)
	if err != nil {
		t.Fatalf("NewClientDeterministic: %v", err)
	}
	if got := c.First(); !bytes.Equal(got, cap.CHRecord) {
		t.Errorf("replay ClientHello mismatch:\n got %x\nwant %x", got, cap.CHRecord)
	}
	var gotFin []byte
	done := false
	for i, rx := range cap.Inbound {
		out, st, err := c.OnRecord(rx)
		if err != nil {
			t.Fatalf("replay OnRecord[%d] (phase %d): %v", i, c.Phase(), err)
		}
		if st == StatusDone {
			gotFin = out
			done = true
		}
	}
	if !done {
		t.Fatalf("replay never reached StatusDone")
	}
	if !bytes.Equal(gotFin, cap.FinRecord) {
		t.Errorf("replay client Finished mismatch:\n got %x\nwant %x", gotFin, cap.FinRecord)
	}
	ks := c.Schedule()
	for _, p := range []struct {
		name      string
		got, want []byte
	}{
		{"CHS", ks.CHS, cap.CHS}, {"SHS", ks.SHS, cap.SHS},
		{"CAP", ks.CAP, cap.CAP}, {"SAP", ks.SAP, cap.SAP},
	} {
		if !bytes.Equal(p.got, p.want) {
			t.Errorf("replay %s mismatch:\n got %x\nwant %x", p.name, p.got, p.want)
		}
	}
}

func firstBytes(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}
