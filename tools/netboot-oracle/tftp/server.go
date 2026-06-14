package tftp

import "strings"

// Store is the flat name->object lookup the i83 server walks. On the SAM this
// is a directory of {name, record#, size} tuples over the B-DOS flat store
// (plan §3.3); here it is the Go reference behaviour. The server never encodes
// a file list — it serves whatever the store holds (oracle §3).
type Store interface {
	// Lookup returns the size of the named object and whether it exists.
	Lookup(name string) (size uint64, ok bool)
}

// MapStore is an in-memory Store for the oracle harness: name -> size.
type MapStore map[string]uint64

// Lookup implements Store.
func (m MapStore) Lookup(name string) (uint64, bool) {
	s, ok := m[name]
	return s, ok
}

// ResolveAction is the server's decision for one RRQ.
type ResolveAction int

const (
	// ActionOACK: the file is in the store; answer with an OACK then DATA.
	ActionOACK ResolveAction = iota
	// ActionError404: the file is not in the store (or is serial-subdir
	// prefixed); answer ERROR(1) and keep serving.
	ActionError404
)

// isSerialSubdir reports whether a requested name is under a serial-number
// subdirectory (e.g. "e0ff06da/start4.elf"). The boot ROM probes these first
// and falls back to root on not-found, so the server 404s them and serves only
// from the flat root (oracle §2 "serial-subdir, then root").
func isSerialSubdir(name string) bool {
	i := strings.IndexByte(name, '/')
	if i <= 0 {
		return false
	}
	prefix := name[:i]
	if len(prefix) < 4 {
		return false
	}
	for _, c := range prefix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Resolve decides how the i83 server answers one RRQ against the store. The
// returned size is meaningful only for ActionOACK. This is the Go reference for
// the Z80 server's filename-resolution step (plan §3 step 2; oracle §2-§3):
// 404 any serial-subdir prefix (the Pi retries at root), serve a flat-root hit
// via OACK, ERROR(1) every miss and keep serving.
func Resolve(s Store, name string) (ResolveAction, uint64) {
	if isSerialSubdir(name) {
		return ActionError404, 0
	}
	if size, ok := s.Lookup(name); ok {
		return ActionOACK, size
	}
	return ActionError404, 0
}

// AcceptedBlksize clamps a requested blksize to what the server supports. The
// capture shows 1024 and 1468; the server echoes the client's request when it
// is within bounds, falling back to the 512-byte RFC 1350 default otherwise.
func AcceptedBlksize(requested uint64) uint64 {
	const (
		min = 8
		max = 1468 // largest seen; one Ethernet frame
	)
	if requested < min || requested > max {
		return 512
	}
	return requested
}
