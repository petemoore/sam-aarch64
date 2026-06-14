// tls_client_test.go — the i88 host-verification of the TLS 1.3 client brick 6a
// (src/netboot/tls_client.asm), the composition + ClientHello sub-brick. It loads
// the composite (the five handshake bricks + x25519 + the client driver, built
// with -D NETBOOT_TLS_CLIENT) under the koron-go/z80 harness and verifies it
// against the Go authority (tools/netboot-oracle/tls) — the capture-then-replay
// oracle of the port plan, Parts 2/3.
//
// TestTLSClientInitFirst covers tls_client_init + tls_client_first:
//   - inject the same raw scalar / random / session_id a deterministic Go client
//     uses, run tls_client_init, and assert CH_PUBKEY == the X25519 public key
//     crypto/ecdh derives from that scalar (proves the ECDHE pubkey step);
//   - run tls_client_first and assert TC_TX[:TC_TX_LEN] == the byte-for-byte
//     ClientHello record the Go client's First() emits.
//
// TestTLSClientHandshakeReplay drives the full record-level state machine
// (tls_client_on_record -> on_server_hello / on_encrypted) by replaying a captured
// handshake (capture.go) and asserting the Z80 reproduces every client output +
// the intermediate traffic secrets, plus a negative control on the AEAD-auth path.
package z80_test

import (
	"bytes"
	"crypto/ecdh"
	"os"
	"testing"

	tls "github.com/petemoore/sam-aarch64/tools/netboot-oracle/tls"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	clientBinPath = "../../../build/netboot_tls_client.bin"
	clientMapPath = "../../../build/netboot_tls_client.map"
	// qsqTableComposite is qsq_table's relocated address under NETBOOT_TLS_CLIENT
	// (qsq.asm); the regenerable multiply table must sit above the emitted image.
	qsqTableComposite = 0xFB00
)

func loadTLSClient(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(clientBinPath); err != nil {
		t.Skipf("tls_client binary not built (%s); run `make netboot-tls-client`", clientBinPath)
	}
	mac, err := z80h.Load(clientBinPath, clientMapPath)
	if err != nil {
		t.Fatalf("load tls_client: %v", err)
	}
	return mac
}

// TestTLSClientInitFirst drives tls_client_init + tls_client_first and checks the
// X25519 public key and the ClientHello record against the deterministic Go client.
func TestTLSClientInitFirst(t *testing.T) {
	const host = "github.com"
	var priv, random, sid [32]byte
	for i := range priv {
		priv[i] = byte(i + 1)
		random[i] = byte(i*3 + 7)
		sid[i] = byte(i*5 + 0x40)
	}

	// The Go authority: the reference ClientHello record + the raw scalar.
	c, err := tls.NewClientDeterministic(host, priv[:], random, sid)
	if err != nil {
		t.Fatalf("NewClientDeterministic: %v", err)
	}
	wantRecord := c.First() // [0x16 0x03 0x01 len16 || ClientHello]
	scalar := c.PrivateScalar()

	// Independent expected public key (crypto/ecdh, not the authority's own copy).
	pk, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		t.Fatalf("ecdh NewPrivateKey: %v", err)
	}
	wantPub := pk.PublicKey().Bytes()

	mac := loadTLSClient(t)

	// The image must not collide with the relocated qsq_table (qsq.asm comment).
	if end := mustSym(t, mac, "tls_client_end"); int(end) >= qsqTableComposite {
		t.Fatalf("tls_client image top %#x reaches qsq_table %#x — relocate the table or shrink the image",
			end, qsqTableComposite)
	}

	// Inject the deterministic inputs.
	mac.Write(mustSym(t, mac, "TC_CLIENT_PRIV"), scalar)
	mac.Write(mustSym(t, mac, "CH_RANDOM"), random[:])
	mac.Write(mustSym(t, mac, "CH_SESSION_ID"), sid[:])
	mac.Write(mustSym(t, mac, "CH_HOSTNAME"), []byte(host))
	mac.WriteU16LE(mustSym(t, mac, "CH_HOSTNAME_LEN"), uint16(len(host)))

	// init computes CH_PUBKEY = X25519(scalar, basepoint) — one Montgomery ladder
	// (~182M harness steps), so it needs the raised step cap the field tests use.
	if _, err := mac.CallEntry("tls_client_init", z80h.Entry{StepCap: x25519StepCap}); err != nil {
		t.Fatalf("tls_client_init: %v", err)
	}
	if gotPub := mac.Read(mustSym(t, mac, "CH_PUBKEY"), 32); !bytes.Equal(gotPub, wantPub) {
		t.Fatalf("CH_PUBKEY mismatch:\n got %x\nwant %x", gotPub, wantPub)
	}

	// first builds the ClientHello handshake record into TC_TX (cheap byte work
	// plus a transcript SHA-256 over the message).
	if _, err := mac.CallEntry("tls_client_first", z80h.Entry{}); err != nil {
		t.Fatalf("tls_client_first: %v", err)
	}
	txLen := readU16LE(mac, mustSym(t, mac, "TC_TX_LEN"))
	if gotTx := mac.Read(mustSym(t, mac, "TC_TX"), txLen); !bytes.Equal(gotTx, wantRecord) {
		t.Fatalf("ClientHello record mismatch (got %d bytes, want %d):\n got %x\nwant %x",
			txLen, len(wantRecord), gotTx, wantRecord)
	}
}

// initFirstClient loads the composite, injects the capture's inputs, runs
// tls_client_init + tls_client_first, and asserts the ClientHello record matches —
// the shared setup for the replay test and its negative control. It returns the
// loaded machine, positioned at PhaseSentCH (ready for inbound records).
func initFirstClient(t *testing.T, cap *tls.Capture) *z80h.Machine {
	t.Helper()
	mac := loadTLSClient(t)

	if end := mustSym(t, mac, "tls_client_end"); int(end) >= qsqTableComposite {
		t.Fatalf("tls_client image top %#x reaches qsq_table %#x — relocate the table or shrink the image",
			end, qsqTableComposite)
	}

	mac.Write(mustSym(t, mac, "TC_CLIENT_PRIV"), cap.Priv[:])
	mac.Write(mustSym(t, mac, "CH_RANDOM"), cap.Random[:])
	mac.Write(mustSym(t, mac, "CH_SESSION_ID"), cap.Sid[:])
	mac.Write(mustSym(t, mac, "CH_HOSTNAME"), []byte("github.com"))
	mac.WriteU16LE(mustSym(t, mac, "CH_HOSTNAME_LEN"), uint16(len("github.com")))

	if _, err := mac.CallEntry("tls_client_init", z80h.Entry{StepCap: x25519StepCap}); err != nil {
		t.Fatalf("tls_client_init: %v", err)
	}
	if _, err := mac.CallEntry("tls_client_first", z80h.Entry{}); err != nil {
		t.Fatalf("tls_client_first: %v", err)
	}
	txLen := readU16LE(mac, mustSym(t, mac, "TC_TX_LEN"))
	if gotTx := mac.Read(mustSym(t, mac, "TC_TX"), txLen); !bytes.Equal(gotTx, cap.CHRecord) {
		t.Fatalf("ClientHello record mismatch (got %d bytes, want %d):\n got %x\nwant %x",
			txLen, len(cap.CHRecord), gotTx, cap.CHRecord)
	}
	return mac
}

// feedRecord writes one inbound TLS record into TC_RX/TC_RX_LEN and runs
// tls_client_on_record (with the raised step cap — the ServerHello record runs the
// second X25519 ladder).
func feedRecord(t *testing.T, mac *z80h.Machine, rec []byte) {
	t.Helper()
	mac.Write(mustSym(t, mac, "TC_RX"), rec)
	mac.WriteU16LE(mustSym(t, mac, "TC_RX_LEN"), uint16(len(rec)))
	if _, err := mac.CallEntry("tls_client_on_record", z80h.Entry{StepCap: x25519StepCap}); err != nil {
		t.Fatalf("tls_client_on_record: %v", err)
	}
}

const (
	tcPhaseGotSH = 2
	tcPhaseDone  = 3
	tcPhaseError = 4
	tcStatusDone = 1
)

// TestTLSClientHandshakeReplay captures a full TLS 1.3 handshake against an
// in-process crypto/tls.Server (capture.go) and replays the captured inbound
// records through the Z80 state machine, asserting it reproduces — byte for byte —
// the captured client ClientHello + Finished records and the four traffic secrets.
// The Z80 must derive the same ECDHE, key schedule, decrypt + verify the server
// Finished, and seal its own Finished, all from the captured inputs.
func TestTLSClientHandshakeReplay(t *testing.T) {
	var priv, random, sid [32]byte
	for i := range priv {
		priv[i] = byte(i + 1)
		random[i] = byte(i*3 + 7)
		sid[i] = byte(i*5 + 0x40)
	}
	cap, err := tls.CaptureHandshake("github.com", priv, random, sid)
	if err != nil {
		t.Fatalf("CaptureHandshake: %v", err)
	}

	t.Run("replay", func(t *testing.T) {
		mac := initFirstClient(t, cap)

		var sawSH, sawDone bool
		for i, rec := range cap.Inbound {
			feedRecord(t, mac, rec)

			phase := mac.Read(mustSym(t, mac, "TC_PHASE"), 1)[0]
			if phase == tcPhaseError {
				t.Fatalf("record %d (type 0x%02x): TC_PHASE=ERROR", i, rec[0])
			}

			// After the ServerHello record the handshake secrets are derived.
			if rec[0] == 0x16 {
				if phase != tcPhaseGotSH {
					t.Fatalf("after ServerHello: TC_PHASE=%d, want GOT_SH(%d)", phase, tcPhaseGotSH)
				}
				if got := mac.Read(mustSym(t, mac, "KS_CHS"), 32); !bytes.Equal(got, cap.CHS) {
					t.Fatalf("KS_CHS mismatch:\n got %x\nwant %x", got, cap.CHS)
				}
				if got := mac.Read(mustSym(t, mac, "KS_SHS"), 32); !bytes.Equal(got, cap.SHS) {
					t.Fatalf("KS_SHS mismatch:\n got %x\nwant %x", got, cap.SHS)
				}
				sawSH = true
			}

			// On DONE the client Finished record + application secrets are ready.
			if mac.Read(mustSym(t, mac, "TC_STATUS"), 1)[0] == tcStatusDone {
				if phase != tcPhaseDone {
					t.Fatalf("at StatusDone: TC_PHASE=%d, want DONE(%d)", phase, tcPhaseDone)
				}
				txLen := readU16LE(mac, mustSym(t, mac, "TC_TX_LEN"))
				if gotTx := mac.Read(mustSym(t, mac, "TC_TX"), txLen); !bytes.Equal(gotTx, cap.FinRecord) {
					t.Fatalf("client Finished record mismatch (got %d bytes, want %d):\n got %x\nwant %x",
						txLen, len(cap.FinRecord), gotTx, cap.FinRecord)
				}
				if got := mac.Read(mustSym(t, mac, "KS_CAP"), 32); !bytes.Equal(got, cap.CAP) {
					t.Fatalf("KS_CAP mismatch:\n got %x\nwant %x", got, cap.CAP)
				}
				if got := mac.Read(mustSym(t, mac, "KS_SAP"), 32); !bytes.Equal(got, cap.SAP) {
					t.Fatalf("KS_SAP mismatch:\n got %x\nwant %x", got, cap.SAP)
				}
				sawDone = true
			}
		}
		if !sawSH {
			t.Fatal("never processed a ServerHello record")
		}
		if !sawDone {
			t.Fatal("handshake never reached StatusDone")
		}
	})

	// Negative control: flip one byte of an encrypted flight (0x17) record before
	// feeding it. The AEAD tag authentication must reject it -> TC_PHASE=ERROR.
	t.Run("tampered_flight_rejected", func(t *testing.T) {
		// Find an inbound 0x17 record to corrupt.
		var idx int = -1
		for i, rec := range cap.Inbound {
			if rec[0] == 0x17 {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Skip("no encrypted flight record in the capture")
		}

		mac := initFirstClient(t, cap)
		var gotError bool
		for i, rec := range cap.Inbound {
			fed := rec
			if i == idx {
				// Flip a byte inside the ciphertext (past the 5-byte header).
				tampered := append([]byte(nil), rec...)
				tampered[6] ^= 0x01
				fed = tampered
			}
			feedRecord(t, mac, fed)
			if mac.Read(mustSym(t, mac, "TC_PHASE"), 1)[0] == tcPhaseError {
				gotError = true
				break
			}
		}
		if !gotError {
			t.Fatal("tampered flight record was accepted; expected TC_PHASE=ERROR")
		}
	})
}
