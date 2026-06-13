package tls

// The TLS 1.3 1-RTT client handshake state machine — the host authority for the
// Z80 brick-6 capstone (src/netboot/tls_client_*.asm). It composes the verified
// leaves (key schedule, record, ClientHello, server-flight parser, transcript)
// and adds only the sequencing, exactly as http.Provisioner does for the
// firmware fetch. It is rx-driven so the Z80 port is a single record loop:
//
//	tx := c.First()                 // ClientHello record
//	for {
//	    drv_write(tx)
//	    rx := drv_read_record()
//	    tx, st, err := c.OnRecord(rx)
//	    if err != nil { fail }
//	    if tx != nil { drv_write(tx) }
//	    if st == StatusDone { break } // tx was the client Finished
//	}
//
// Scope (i88): exactly TLS_CHACHA20_POLY1305_SHA256 + X25519, 1-RTT, no
// certificate-chain validation (firmware is independently SHA-256-pinned). The
// server Finished IS verified — it proves the peer derived the same handshake
// secret. The HTTP GET over application keys is a later composition.

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
)

// Phase is the client's position in the 1-RTT flow.
type Phase int

const (
	PhaseInit   Phase = iota // before First()
	PhaseSentCH              // ClientHello sent, awaiting ServerHello
	PhaseGotSH               // ServerHello parsed + handshake keys derived
	PhaseDone                // client Finished produced; handshake complete
	PhaseError               // a fatal handshake error was reported
)

// Status is what OnRecord reports after processing one record.
type Status int

const (
	// StatusContinue: still handshaking; tx (if non-nil) is a record to send,
	// then keep reading inbound records.
	StatusContinue Status = iota
	// StatusDone: handshake complete; tx is the client Finished record to send.
	StatusDone
)

// Client drives the client side of the handshake. Build it with NewClient, emit
// First(), then feed every inbound TLS record to OnRecord.
type Client struct {
	priv   *ecdh.PrivateKey
	random [32]byte
	sid    [32]byte
	host   string

	phase      Phase
	transcript hash.Hash // running SHA-256 over the handshake messages
	ks         KeySchedule
	hashHS     []byte

	serverSeq uint64 // server handshake-record sequence number
	flight    []byte // accumulated decrypted server-flight handshake bytes

	// Captured for cross-checks / diagnostics.
	ServerPub [32]byte
	ECDHE     []byte
}

// NewClient builds a handshake client for host (the SNI), generating a fresh
// X25519 key pair, a random 32-byte ClientHello.random, and a random
// legacy_session_id (so crypto/tls runs in middlebox-compat mode, like the wild).
func NewClient(host string) (*Client, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	c := &Client{priv: priv, host: host, transcript: sha256.New()}
	if _, err := rand.Read(c.random[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(c.sid[:]); err != nil {
		return nil, err
	}
	return c, nil
}

// ClientRandom is the ClientHello.random — the key for matching this handshake's
// secrets in a server SSLKEYLOGFILE.
func (c *Client) ClientRandom() [32]byte { return c.random }

// Schedule returns the derived key schedule (handshake secrets after ServerHello,
// application secrets after the server Finished).
func (c *Client) Schedule() KeySchedule { return c.ks }

// Phase returns the current handshake phase.
func (c *Client) Phase() Phase { return c.phase }

// First emits the ClientHello as a plaintext handshake record and advances to
// PhaseSentCH. The legacy_record_version is 0x0301 for the first flight, as
// crypto/tls writes and accepts.
func (c *Client) First() []byte {
	pub := c.priv.PublicKey().Bytes()
	ch := BuildClientHello(c.random[:], c.sid[:], pub, c.host)
	c.transcript.Write(ch)
	c.phase = PhaseSentCH
	return plaintextRecord(0x16, 0x0301, ch)
}

// OnRecord feeds one inbound TLS record (5-byte header + payload) to the state
// machine. It returns a record to send (or nil), the loop status, and a fatal
// error if the handshake cannot proceed.
func (c *Client) OnRecord(rx []byte) (tx []byte, st Status, err error) {
	if len(rx) < 5 {
		return c.fail(fmt.Errorf("short record (%d bytes)", len(rx)))
	}
	switch rx[0] {
	case 0x14: // ChangeCipherSpec — TLS 1.3 middlebox compat; ignored.
		return nil, StatusContinue, nil
	case 0x15: // Alert
		return c.fail(fmt.Errorf("server alert: % x", rx[5:]))
	case 0x16: // plaintext handshake — the ServerHello
		return c.onServerHello(rx[5:])
	case 0x17: // application_data — an encrypted handshake-flight record
		return c.onEncrypted(rx)
	default:
		return c.fail(fmt.Errorf("unexpected record type 0x%02x", rx[0]))
	}
}

func (c *Client) fail(err error) ([]byte, Status, error) {
	c.phase = PhaseError
	return nil, StatusContinue, err
}

// onServerHello parses the ServerHello, folds it into the transcript, computes
// the ECDHE shared secret, and derives the handshake traffic keys.
func (c *Client) onServerHello(sh []byte) ([]byte, Status, error) {
	if c.phase != PhaseSentCH {
		return c.fail(fmt.Errorf("ServerHello in phase %d", c.phase))
	}
	pub, ok := ParseServerHello(sh)
	if !ok {
		return c.fail(errors.New("ServerHello rejected (suite/version/key_share/HRR)"))
	}
	c.ServerPub = pub
	c.transcript.Write(sh)
	c.hashHS = c.transcript.Sum(nil)

	serverPubKey, err := ecdh.X25519().NewPublicKey(pub[:])
	if err != nil {
		return c.fail(fmt.Errorf("server key_share: %w", err))
	}
	c.ECDHE, err = c.priv.ECDH(serverPubKey)
	if err != nil {
		return c.fail(fmt.Errorf("ECDH: %w", err))
	}
	c.ks.DeriveHandshake(c.ECDHE, c.hashHS)
	c.phase = PhaseGotSH
	return nil, StatusContinue, nil
}

// onEncrypted decrypts one server handshake-flight record, accumulates the
// handshake bytes, and — once the flight is complete — verifies the server
// Finished, derives the application secrets, and produces the client Finished.
func (c *Client) onEncrypted(record []byte) ([]byte, Status, error) {
	if c.phase != PhaseGotSH {
		return c.fail(fmt.Errorf("encrypted record in phase %d", c.phase))
	}
	ok, innerType, content := Open(c.ks.SHSKey, c.ks.SHSIV, c.serverSeq, record)
	if !ok {
		return c.fail(fmt.Errorf("failed to decrypt server record seq %d", c.serverSeq))
	}
	c.serverSeq++
	if innerType != 0x16 { // handshake
		return c.fail(fmt.Errorf("inner content type 0x%02x, want handshake", innerType))
	}
	c.flight = append(c.flight, content...)

	sf, complete, err := WalkServerFlight(c.flight)
	if err != nil {
		return c.fail(fmt.Errorf("server flight: %w", err))
	}
	if !complete {
		return nil, StatusContinue, nil // need more records
	}

	// Fold EE||Cert||CertVerify, snapshot, verify the server Finished.
	c.transcript.Write(sf.BeforeFin)
	hashBeforeFin := c.transcript.Sum(nil)
	if !hmac.Equal(hmacSHA256(c.ks.SHSFin, hashBeforeFin), sf.VerifyData) {
		return c.fail(errors.New("server Finished verify_data mismatch"))
	}
	c.transcript.Write(sf.Finished)

	// Derive application secrets, then build + seal the client Finished. Its
	// transcript context (ClientHello..server Finished) is the same hashAP.
	hashAP := c.transcript.Sum(nil)
	c.ks.DeriveApplication(hashAP)
	clientVerify := hmacSHA256(c.ks.CHSFin, hashAP)
	finMsg := append([]byte{20, 0x00, 0x00, byte(len(clientVerify))}, clientVerify...)
	c.transcript.Write(finMsg)
	finRecord := Seal(c.ks.CHSKey, c.ks.CHSIV, 0, 0x16, finMsg)
	c.phase = PhaseDone
	return finRecord, StatusDone, nil
}

// plaintextRecord wraps payload in a TLS record header.
func plaintextRecord(typ byte, version uint16, payload []byte) []byte {
	rec := []byte{typ, byte(version >> 8), byte(version), byte(len(payload) >> 8), byte(len(payload))}
	return append(rec, payload...)
}

// hmacSHA256 is HMAC-SHA256(key, data) — the Finished MAC and an HKDF building
// block.
func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}
