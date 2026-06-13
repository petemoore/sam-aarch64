// tls_client_test.go — the i88 host-verification of the TLS 1.3 client brick 6a
// (src/netboot/tls_client.asm), the composition + ClientHello sub-brick. It loads
// the composite (the five handshake bricks + x25519 + the client driver, built
// with -D NETBOOT_TLS_CLIENT) under the koron-go/z80 harness and verifies it
// against the Go authority (tools/netboot-oracle/tls) — the capture-then-replay
// oracle of the port plan, Parts 2/3.
//
// This increment covers tls_client_init + tls_client_first:
//   - inject the same raw scalar / random / session_id a deterministic Go client
//     uses, run tls_client_init, and assert CH_PUBKEY == the X25519 public key
//     crypto/ecdh derives from that scalar (proves the ECDHE pubkey step);
//   - run tls_client_first and assert TC_TX[:TC_TX_LEN] == the byte-for-byte
//     ClientHello record the Go client's First() emits.
// The record-driven state machine (tls_client_on_record) is verified in a later
// increment.
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
