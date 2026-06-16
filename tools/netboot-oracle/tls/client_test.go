package tls

// The capstone: drive our Client through a complete 1-RTT handshake against a
// real crypto/tls.Server over net.Pipe, and cross-check every derived secret
// against the server's SSLKEYLOGFILE output. crypto/tls accepting our Finished
// (Handshake() == nil) proves the orchestration; the keylog match proves our
// independent key schedule + ECDHE agree with the stdlib's, byte for byte.

import (
	"bytes"
	gotls "crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// selfSignedCert is the test-side wrapper over buildServerCert (capture.go), the
// shared throwaway ECDSA-P256 certificate for the in-process server.
func selfSignedCert(t *testing.T) gotls.Certificate {
	t.Helper()
	cert, err := buildServerCert()
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func serverConfig(t *testing.T, keylog io.Writer) *gotls.Config {
	return &gotls.Config{
		Certificates:           []gotls.Certificate{selfSignedCert(t)},
		CipherSuites:           []uint16{gotls.TLS_CHACHA20_POLY1305_SHA256},
		CurvePreferences:       []gotls.CurveID{gotls.X25519},
		MinVersion:             gotls.VersionTLS13,
		MaxVersion:             gotls.VersionTLS13,
		KeyLogWriter:           keylog,
		SessionTicketsDisabled: true, // no post-handshake tickets -> no pipe deadlock
	}
}

// driveHandshake runs c through a complete 1-RTT handshake against a real
// crypto/tls.Server over net.Pipe and returns the server's SSLKEYLOGFILE output.
// It fails the test if the handshake does not complete.
func driveHandshake(t *testing.T, c *Client) string {
	t.Helper()
	cli, srv := net.Pipe()
	deadline := time.Now().Add(10 * time.Second)
	cli.SetDeadline(deadline)
	srv.SetDeadline(deadline)

	var keylog bytes.Buffer
	srvErr := make(chan error, 1)
	go func() {
		s := gotls.Server(srv, serverConfig(t, &keylog))
		err := s.Handshake()
		srv.Close()
		srvErr <- err
	}()

	tx := c.First()
	if _, err := cli.Write(tx); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}
	for {
		rx, err := readRecord(cli)
		if err != nil {
			t.Fatalf("read record (phase %d): %v", c.Phase(), err)
		}
		out, st, err := c.OnRecord(rx)
		if err != nil {
			t.Fatalf("OnRecord (phase %d): %v", c.Phase(), err)
		}
		if out != nil {
			if _, err := cli.Write(out); err != nil {
				t.Fatalf("write record: %v", err)
			}
		}
		if st == StatusDone {
			break
		}
	}
	cli.Close()

	if err := <-srvErr; err != nil {
		t.Fatalf("server Handshake() rejected our handshake: %v", err)
	}
	if c.Phase() != PhaseDone {
		t.Fatalf("client phase = %d, want PhaseDone", c.Phase())
	}
	return keylog.String()
}

func TestHandshakeAgainstCryptoTLS(t *testing.T) {
	c, err := NewClient("github.com")
	if err != nil {
		t.Fatal(err)
	}
	checkKeylog(t, driveHandshake(t, c), c)
}

// TestDeterministicClient: NewClientDeterministic is reproducible (same inputs ->
// identical ClientHello), the raw scalar round-trips, and a deterministic client
// still completes a real handshake. This is the foundation of the Z80 brick-6
// capture-then-replay oracle (the port plan, Part 2/3).
func TestDeterministicClient(t *testing.T) {
	var priv, random, sid [32]byte
	for i := range priv {
		priv[i] = byte(i + 1)
		random[i] = byte(i*3 + 7)
		sid[i] = byte(i*5 + 0x40)
	}
	mk := func() *Client {
		c, err := NewClientDeterministic("github.com", priv[:], random, sid)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// Same key/random/sid -> identical ClientHello record (so the Z80 port, fed
	// the same inputs, builds the same message).
	if !bytes.Equal(mk().First(), mk().First()) {
		t.Error("ClientHello not deterministic for fixed key/random/sid")
	}
	// The raw scalar round-trips (the value the Z80 loads into TC_CLIENT_PRIV).
	if got := mk().PrivateScalar(); !bytes.Equal(got, priv[:]) {
		t.Errorf("PrivateScalar = %x, want %x", got, priv[:])
	}
	// A deterministic client still completes a real handshake; secrets match keylog.
	c := mk()
	checkKeylog(t, driveHandshake(t, c), c)
}

// checkKeylog cross-checks the four TLS 1.3 traffic secrets logged by the server
// against our independently derived schedule, keyed by our ClientHello.random.
func checkKeylog(t *testing.T, keylog string, c *Client) {
	t.Helper()
	clientRandom := c.ClientRandom()
	cr := hex.EncodeToString(clientRandom[:])
	ks := c.Schedule()
	want := map[string][]byte{
		"CLIENT_HANDSHAKE_TRAFFIC_SECRET": ks.CHS,
		"SERVER_HANDSHAKE_TRAFFIC_SECRET": ks.SHS,
		"CLIENT_TRAFFIC_SECRET_0":         ks.CAP,
		"SERVER_TRAFFIC_SECRET_0":         ks.SAP,
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(keylog), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		label, lineRandom, secretHex := f[0], f[1], f[2]
		w, ok := want[label]
		if !ok {
			continue
		}
		if lineRandom != cr {
			continue // a different handshake's line
		}
		if secretHex != hex.EncodeToString(w) {
			t.Errorf("%s mismatch:\n server %s\n   ours %s", label, secretHex, hex.EncodeToString(w))
		}
		seen[label] = true
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("server keylog missing %s for our client_random %s", label, cr)
		}
	}
}

// TestClientHelloAcceptedByServer is a localized signal for staged debug: it
// confirms crypto/tls parses our ClientHello into the offer we built (so a
// full-handshake failure is downstream of the ClientHello).
func TestClientHelloAcceptedByServer(t *testing.T) {
	c, err := NewClient("github.com")
	if err != nil {
		t.Fatal(err)
	}
	ch := c.First()[5:] // strip the record header -> the ClientHello handshake message

	cli, srv := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	cli.SetDeadline(deadline)
	srv.SetDeadline(deadline)
	captured := make(chan *gotls.ClientHelloInfo, 1)

	go func() {
		cfg := &gotls.Config{
			GetConfigForClient: func(chi *gotls.ClientHelloInfo) (*gotls.Config, error) {
				ci := *chi
				captured <- &ci
				return nil, fmt.Errorf("captured; stop")
			},
		}
		_ = gotls.Server(srv, cfg).Handshake()
		srv.Close()
	}()
	go io.Copy(io.Discard, cli)
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(ch) >> 8), byte(len(ch))}, ch...)
	go cli.Write(rec)

	select {
	case chi := <-captured:
		if chi.ServerName != "github.com" {
			t.Errorf("ServerName = %q, want github.com", chi.ServerName)
		}
		var hasSuite bool
		for _, s := range chi.CipherSuites {
			if s == gotls.TLS_CHACHA20_POLY1305_SHA256 {
				hasSuite = true
			}
		}
		if !hasSuite {
			t.Errorf("offer missing TLS_CHACHA20_POLY1305_SHA256")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crypto/tls did not parse our ClientHello")
	}
	cli.Close()
}
