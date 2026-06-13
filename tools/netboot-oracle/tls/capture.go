package tls

// The brick-6 host oracle (the port plan, Part 2). CaptureHandshake drives a
// deterministic Client through a complete 1-RTT TLS 1.3 handshake against an
// in-process crypto/tls.Server over net.Pipe and records every wire record plus
// the four traffic secrets. The Z80 tls_client.asm state machine is verified by
// replaying a Capture's Inbound records and asserting it reproduces the captured
// client outputs (CHRecord / FinRecord) and secrets, byte for byte (Part 3).
//
// A Capture is NOT reproducible run-to-run — the server uses fresh ECDHE
// randomness each handshake, so Inbound (and the secrets derived from it) differ.
// The determinism is conditional: given the SAME captured inputs AND the SAME
// captured Inbound, the client produces the SAME outputs. That is exactly the
// replay the Z80 test performs, so the server's randomness is absorbed into the
// recorded bytes and the Z80 diffs call-by-call against a fixed oracle.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

// Capture is a deterministic-on-replay recording of one TLS 1.3 handshake.
type Capture struct {
	Priv, Random, Sid  [32]byte // the injected client inputs (also in TC_CLIENT_PRIV/CH_RANDOM/CH_SESSION_ID)
	CHRecord           []byte   // the ClientHello record the client sent (TC_TX after tls_client_first)
	Inbound            [][]byte // each inbound record in order (ServerHello, CCS, the 0x17 flight records)
	FinRecord          []byte   // the client Finished record (TC_TX when the Z80 reaches DONE)
	CHS, SHS, CAP, SAP []byte   // the four traffic secrets (the client's own schedule)
}

// buildServerCert builds a throwaway ECDSA-P256 certificate for the in-process
// test server. We offer ecdsa_secp256r1_sha256 so the server can sign
// CertificateVerify with it; it is never validated (i88 scope), but TLS 1.3
// requires the server present one.
func buildServerCert() (gotls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return gotls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"github.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return gotls.Certificate{}, err
	}
	return gotls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

// readRecord reads one full TLS record (5-byte header + payload) from conn.
func readRecord(conn net.Conn) ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	n := int(hdr[3])<<8 | int(hdr[4])
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return append(hdr, body...), nil
}

// CaptureHandshake drives a deterministic client (the given raw X25519 scalar,
// ClientHello.random and legacy_session_id) through a full handshake against an
// in-process crypto/tls.Server and returns the recorded wire records + secrets.
// It errors if the handshake does not complete (the server must accept our
// Finished), so a returned Capture is always a complete, replayable transcript.
func CaptureHandshake(host string, priv, random, sid [32]byte) (*Capture, error) {
	c, err := NewClientDeterministic(host, priv[:], random, sid)
	if err != nil {
		return nil, err
	}
	cert, err := buildServerCert()
	if err != nil {
		return nil, err
	}

	cli, srv := net.Pipe()
	deadline := time.Now().Add(10 * time.Second)
	cli.SetDeadline(deadline)
	srv.SetDeadline(deadline)

	srvErr := make(chan error, 1)
	go func() {
		s := gotls.Server(srv, &gotls.Config{
			Certificates:           []gotls.Certificate{cert},
			CipherSuites:           []uint16{gotls.TLS_CHACHA20_POLY1305_SHA256},
			CurvePreferences:       []gotls.CurveID{gotls.X25519},
			MinVersion:             gotls.VersionTLS13,
			MaxVersion:             gotls.VersionTLS13,
			SessionTicketsDisabled: true, // no post-handshake tickets -> no pipe deadlock
		})
		err := s.Handshake()
		srv.Close()
		srvErr <- err
	}()

	cap := &Capture{Priv: priv, Random: random, Sid: sid}
	cap.CHRecord = c.First()
	if _, err := cli.Write(cap.CHRecord); err != nil {
		return nil, fmt.Errorf("write ClientHello: %w", err)
	}
	for {
		rx, err := readRecord(cli)
		if err != nil {
			return nil, fmt.Errorf("read record (phase %d): %w", c.Phase(), err)
		}
		cap.Inbound = append(cap.Inbound, rx)
		out, st, err := c.OnRecord(rx)
		if err != nil {
			return nil, fmt.Errorf("OnRecord (phase %d): %w", c.Phase(), err)
		}
		if out != nil {
			if _, err := cli.Write(out); err != nil {
				return nil, fmt.Errorf("write record: %w", err)
			}
			if st == StatusDone {
				cap.FinRecord = out
			}
		}
		if st == StatusDone {
			break
		}
	}
	cli.Close()

	if err := <-srvErr; err != nil {
		return nil, fmt.Errorf("server rejected our handshake: %w", err)
	}
	if c.Phase() != PhaseDone {
		return nil, fmt.Errorf("client phase = %d, want PhaseDone", c.Phase())
	}

	ks := c.Schedule()
	cap.CHS, cap.SHS, cap.CAP, cap.SAP = ks.CHS, ks.SHS, ks.CAP, ks.SAP
	return cap, nil
}
