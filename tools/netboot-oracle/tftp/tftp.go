// Package tftp builds and parses the TFTP packets the SAM netboot client (i82)
// and server (i83) exchange: RRQ, OACK, DATA, ACK, ERROR. The wire-level
// authority is the real Pi 400 capture (docs/notes/pi-netboot-capture-analysis.md
// §2) and the RFC corpus in docs/notes/tftp-protocol-research.md (RFC 1350 core,
// 2347 OACK, 2348 blksize, 2349 timeout/tsize, 7440 windowsize). The builders/
// parsers here are the Go reference for the Z80 state machines.
package tftp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
)

// TFTP opcodes (the 2-byte big-endian value at the start of the UDP payload).
const (
	OpRRQ   = 1 // read request
	OpWRQ   = 2 // write request
	OpDATA  = 3
	OpACK   = 4
	OpERROR = 5
	OpOACK  = 6
)

// Error codes (RFC 1350 §5). The Pi boot ROM's probe relies on FileNotFound on
// a miss; the server must answer it and keep serving (oracle §2).
const (
	ErrNotDefined       = 0
	ErrFileNotFound     = 1
	ErrAccessViolation  = 2
	ErrDiskFull         = 3
	ErrIllegalOp        = 4
	ErrUnknownTID       = 5
	ErrFileExists       = 6
	ErrNoSuchUser       = 7
	ErrOptionNegRefused = 8
)

// ClientOptionSet is the RRQ option set the i82 client requests. blksize=1428
// fits one Ethernet frame (no reassembly); tsize=0 asks the server to report the
// file's size in the OACK; timeout=2 is the retransmit hint.
//
// windowsize is deliberately NOT requested: per RFC 7440 a client that asks for
// windowsize must handle windowed delivery (ACK only the last block of each
// window, not every block), which this lock-step receiver does not — requesting
// it would break against any server that grants it (e.g. macOS tftpd OACKs
// windowsize). Windowed transfer is a future throughput optimization; until then
// the client stays a correct lock-step (one ACK per block) RFC 1350/2347 client.
var ClientOptionSet = []Option{
	{"blksize", "1428"},
	{"tsize", "0"},
	{"timeout", "2"},
}

// Option is one TFTP option (name=value, both NUL-terminated on the wire).
type Option struct {
	Name  string
	Value string
}

// readCStr reads a NUL-terminated string from b starting at *off, advancing
// *off past the NUL. Returns false if no NUL is found.
func readCStr(b []byte, off *int) (string, bool) {
	start := *off
	for i := start; i < len(b); i++ {
		if b[i] == 0 {
			*off = i + 1
			return string(b[start:i]), true
		}
	}
	return "", false
}

// Request is a decoded RRQ/WRQ.
type Request struct {
	Opcode   uint16
	Filename string
	Mode     string // "octet"
	Options  []Option
}

// Opcode returns the opcode of a TFTP payload, or 0 if too short.
func Opcode(payload []byte) uint16 {
	if len(payload) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(payload)
}

// BuildRRQ builds a read request: opcode 1, filename NUL, mode NUL, then each
// option name NUL value NUL. The Go authority for the i82 client's RRQ builder
// (plan §2 step 2).
func BuildRRQ(filename, mode string, opts []Option) []byte {
	var b bytes.Buffer
	hdr := []byte{0, OpRRQ}
	b.Write(hdr)
	b.WriteString(filename)
	b.WriteByte(0)
	b.WriteString(mode)
	b.WriteByte(0)
	for _, o := range opts {
		b.WriteString(o.Name)
		b.WriteByte(0)
		b.WriteString(o.Value)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// ParseRequest decodes an RRQ/WRQ payload (the i83 server's RRQ parser, plan §3
// step 1).
func ParseRequest(payload []byte) (*Request, error) {
	op := Opcode(payload)
	if op != OpRRQ && op != OpWRQ {
		return nil, fmt.Errorf("tftp: not an RRQ/WRQ (opcode %d)", op)
	}
	off := 2
	fn, ok := readCStr(payload, &off)
	if !ok {
		return nil, fmt.Errorf("tftp: RRQ missing filename")
	}
	mode, ok := readCStr(payload, &off)
	if !ok {
		return nil, fmt.Errorf("tftp: RRQ missing mode")
	}
	r := &Request{Opcode: op, Filename: fn, Mode: mode}
	for off < len(payload) {
		name, ok := readCStr(payload, &off)
		if !ok {
			break
		}
		val, ok := readCStr(payload, &off)
		if !ok {
			return nil, fmt.Errorf("tftp: option %q missing value", name)
		}
		r.Options = append(r.Options, Option{Name: name, Value: val})
	}
	return r, nil
}

// Option returns the value of a named option (case-sensitive on the wire here,
// though RFC 2347 makes option names case-insensitive — the Pi uses lowercase).
func (r *Request) Option(name string) (string, bool) {
	for _, o := range r.Options {
		if o.Name == name {
			return o.Value, true
		}
	}
	return "", false
}

// BuildOACK builds an option-acknowledgement: opcode 6, then each option name
// NUL value NUL. The server answers tsize=<actual size> and echoes the accepted
// blksize (oracle §2). The Go authority for the i83 server's OACK builder.
func BuildOACK(opts []Option) []byte {
	var b bytes.Buffer
	b.Write([]byte{0, OpOACK})
	for _, o := range opts {
		b.WriteString(o.Name)
		b.WriteByte(0)
		b.WriteString(o.Value)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// ParseOACK decodes an OACK into its option list (the i82 client's OACK parse,
// plan §2 step 3).
func ParseOACK(payload []byte) ([]Option, error) {
	if Opcode(payload) != OpOACK {
		return nil, fmt.Errorf("tftp: not an OACK (opcode %d)", Opcode(payload))
	}
	var opts []Option
	off := 2
	for off < len(payload) {
		name, ok := readCStr(payload, &off)
		if !ok {
			break
		}
		val, ok := readCStr(payload, &off)
		if !ok {
			return nil, fmt.Errorf("tftp: OACK option %q missing value", name)
		}
		opts = append(opts, Option{Name: name, Value: val})
	}
	return opts, nil
}

// OptionUint returns a named option's value parsed as an unsigned integer.
func OptionUint(opts []Option, name string) (uint64, bool) {
	for _, o := range opts {
		if o.Name == name {
			n, err := strconv.ParseUint(o.Value, 10, 64)
			if err != nil {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// BuildDATA builds a DATA packet: opcode 3, block number (big-endian), data.
func BuildDATA(block uint16, data []byte) []byte {
	b := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(b[0:], OpDATA)
	binary.BigEndian.PutUint16(b[2:], block)
	copy(b[4:], data)
	return b
}

// ParseDATA returns the block number and data slice of a DATA packet.
func ParseDATA(payload []byte) (block uint16, data []byte, err error) {
	if Opcode(payload) != OpDATA || len(payload) < 4 {
		return 0, nil, fmt.Errorf("tftp: not a DATA packet")
	}
	return binary.BigEndian.Uint16(payload[2:]), payload[4:], nil
}

// BuildACK builds an ACK packet: opcode 4, block number (big-endian).
func BuildACK(block uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], OpACK)
	binary.BigEndian.PutUint16(b[2:], block)
	return b
}

// ParseACK returns the block number of an ACK packet.
func ParseACK(payload []byte) (uint16, error) {
	if Opcode(payload) != OpACK || len(payload) < 4 {
		return 0, fmt.Errorf("tftp: not an ACK packet")
	}
	return binary.BigEndian.Uint16(payload[2:]), nil
}

// BuildError builds an ERROR packet: opcode 5, error code (big-endian), message
// NUL. The server sends ErrFileNotFound on every miss and keeps serving — the
// single most important robustness requirement (oracle §2).
func BuildError(code uint16, msg string) []byte {
	b := make([]byte, 4+len(msg)+1)
	binary.BigEndian.PutUint16(b[0:], OpERROR)
	binary.BigEndian.PutUint16(b[2:], code)
	copy(b[4:], msg)
	b[len(b)-1] = 0
	return b
}

// ParseError returns the error code and message of an ERROR packet.
func ParseError(payload []byte) (code uint16, msg string, err error) {
	if Opcode(payload) != OpERROR || len(payload) < 4 {
		return 0, "", fmt.Errorf("tftp: not an ERROR packet")
	}
	code = binary.BigEndian.Uint16(payload[2:])
	off := 4
	msg, _ = readCStr(payload, &off)
	return code, msg, nil
}
