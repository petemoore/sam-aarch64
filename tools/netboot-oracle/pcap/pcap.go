// Package pcap is a dependency-free reader for libpcap and pcapng capture
// files — enough to extract Ethernet frames, no cgo/libpcap. It exists so the
// oracle harness can read Pete's off-repo captures (~/tftp-logs, kept out of
// the repo per the publication policy) and re-derive golden vectors, and so
// the committed golden-vector test fixtures can be regenerated.
//
// Scope: it returns the raw Ethernet frame bytes of each packet. It does not
// decode interface metadata, timestamps, or non-Ethernet link types.
package pcap

import (
	"encoding/binary"
	"fmt"
	"os"
)

// ReadFrames reads every Ethernet frame from a libpcap or pcapng file at path.
func ReadFrames(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFrames(data)
}

// ParseFrames extracts Ethernet frames from in-memory libpcap or pcapng bytes,
// dispatching on the file magic.
func ParseFrames(data []byte) ([][]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("pcap: file too short (%d bytes)", len(data))
	}
	switch {
	case data[0] == 0xd4 && data[1] == 0xc3 && data[2] == 0xb2 && data[3] == 0xa1:
		return parseLibpcap(data, binary.LittleEndian)
	case data[0] == 0xa1 && data[1] == 0xb2 && data[2] == 0xc3 && data[3] == 0xd4:
		return parseLibpcap(data, binary.BigEndian)
	case data[0] == 0x0a && data[1] == 0x0d && data[2] == 0x0d && data[3] == 0x0a:
		return parsePcapng(data)
	default:
		return nil, fmt.Errorf("pcap: unrecognised magic %x", data[:4])
	}
}

// parseLibpcap walks a classic libpcap file: 24-byte global header, then
// per-record 16-byte header (ts_sec, ts_usec, incl_len, orig_len) + frame.
func parseLibpcap(data []byte, bo binary.ByteOrder) ([][]byte, error) {
	const globalHdr = 24
	const recHdr = 16
	if len(data) < globalHdr {
		return nil, fmt.Errorf("pcap: truncated global header")
	}
	var frames [][]byte
	off := globalHdr
	for off+recHdr <= len(data) {
		inclLen := int(bo.Uint32(data[off+8:]))
		off += recHdr
		if inclLen < 0 || off+inclLen > len(data) {
			break // truncated final record
		}
		frame := make([]byte, inclLen)
		copy(frame, data[off:off+inclLen])
		frames = append(frames, frame)
		off += inclLen
	}
	return frames, nil
}

// pcapng block types we care about.
const (
	blkSHB = 0x0A0D0D0A // Section Header Block
	blkEPB = 0x00000006 // Enhanced Packet Block
	blkSPB = 0x00000003 // Simple Packet Block
)

// parsePcapng walks a pcapng file block by block. Block byte order is set by
// the SHB's byte-order magic (0x1A2B3C4D); all captures here are little-endian
// but we honour the magic anyway.
func parsePcapng(data []byte) ([][]byte, error) {
	var frames [][]byte
	bo := binary.ByteOrder(binary.LittleEndian)
	off := 0
	for off+12 <= len(data) {
		blkType := binary.LittleEndian.Uint32(data[off:])
		// The SHB's own length is byte-order-independent for our purposes; for
		// subsequent blocks the SHB has already fixed `bo`.
		blkLen := int(bo.Uint32(data[off+4:]))
		if blkType == blkSHB {
			// SHB body starts at off+8 with the byte-order magic.
			if off+12 > len(data) {
				break
			}
			if binary.LittleEndian.Uint32(data[off+8:]) == 0x1A2B3C4D {
				bo = binary.LittleEndian
			} else {
				bo = binary.BigEndian
			}
			blkLen = int(bo.Uint32(data[off+4:]))
		}
		if blkLen < 12 || off+blkLen > len(data) {
			break
		}
		body := data[off+8 : off+blkLen-4] // strip type+len header and trailing len
		switch blkType {
		case blkEPB:
			// body: iface_id(4) ts_hi(4) ts_lo(4) cap_len(4) orig_len(4) data...
			if len(body) >= 20 {
				capLen := int(bo.Uint32(body[12:]))
				if capLen >= 0 && 20+capLen <= len(body) {
					frame := make([]byte, capLen)
					copy(frame, body[20:20+capLen])
					frames = append(frames, frame)
				}
			}
		case blkSPB:
			// body: orig_len(4) data...
			if len(body) >= 4 {
				frame := make([]byte, len(body)-4)
				copy(frame, body[4:])
				frames = append(frames, frame)
			}
		}
		off += blkLen
	}
	return frames, nil
}
