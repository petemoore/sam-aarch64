package http

import (
	"encoding/hex"
	"fmt"
)

// A firmware manifest pins an exact revision and the set of files available to
// download from it — the data the i100 downloader UX drives: the user picks a
// revision (a Manifest) and a subset of its Files, and each selected file is
// fetched from cdn.githubraw.com and verified against its pinned SHA-256.
//
// The pinned hash is load-bearing: cdn.githubraw.com is an untrusted proxy, so
// integrity rests on the hash, not the transport (q15 option c). The hashes here
// are the content of each file AT THE PINNED COMMIT (the same bytes the Tupfile
// downloads via github blob?raw=true, which raw.githubusercontent.com — and thus
// the cdn.githubraw.com proxy — serves); a fetch whose bytes hash to anything
// else is rejected. The Z80 port is the FW_MANIFEST table in
// src/netboot/fw_source.asm.

// FirmwareFile is one entry in a manifest.
type FirmwareFile struct {
	Name   string   // output name, e.g. "start4.elf"
	Path   string   // in-repo path under the revision, e.g. "boot/start4.elf"
	Size   int      // size in bytes (metadata for the picker UI)
	SHA256 [32]byte // pinned content hash — the trusted verify target
}

// Manifest pins a firmware revision (owner/repo/sha) and its downloadable files.
type Manifest struct {
	Owner string
	Repo  string
	SHA   string
	Files []FirmwareFile
}

// PathFor returns the cdn.githubraw.com request path for one file in the manifest.
func (m Manifest) PathFor(f FirmwareFile) string {
	return GithubRawPath(m.Owner, m.Repo, m.SHA, f.Path)
}

// FetchSpec is everything the per-file fetch loop needs for one file: where to
// GET it (the cdn.githubraw.com path + host) and the pinned SHA-256 to verify
// the streamed bytes against.
type FetchSpec struct {
	Name   string
	Path   string
	Host   string
	SHA256 [32]byte
}

// Plan builds the ordered fetch specs for the selected files of a manifest — the
// download plan the per-file fetch loop executes, and the backend of the
// revision + file-subset picker. selection is file indices, in the order to
// fetch; a nil/empty selection means all files in manifest order. It errors on
// an out-of-range index rather than panicking, since a selection may come from
// user input.
func (m Manifest) Plan(selection []int) ([]FetchSpec, error) {
	idx := selection
	if len(idx) == 0 {
		idx = make([]int, len(m.Files))
		for i := range m.Files {
			idx[i] = i
		}
	}
	specs := make([]FetchSpec, len(idx))
	for i, j := range idx {
		if j < 0 || j >= len(m.Files) {
			return nil, fmt.Errorf("file index %d out of range [0,%d)", j, len(m.Files))
		}
		f := m.Files[j]
		specs[i] = FetchSpec{Name: f.Name, Path: m.PathFor(f), Host: GithubRawHost, SHA256: f.SHA256}
	}
	return specs, nil
}

// SelectByName resolves output names (e.g. "start4.elf") to manifest indices,
// preserving the given order and erroring on an unknown name. The picker uses it
// to turn a user's file choices into a selection for Plan.
func (m Manifest) SelectByName(names []string) ([]int, error) {
	byName := make(map[string]int, len(m.Files))
	for i, f := range m.Files {
		byName[f.Name] = i
	}
	out := make([]int, len(names))
	for i, n := range names {
		j, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("manifest %s has no file %q", m.SHA, n)
		}
		out[i] = j
	}
	return out, nil
}

// hexHash decodes a 64-char hex SHA-256 literal into a [32]byte. It panics on a
// malformed literal — the manifest hashes are compile-time constants, so a bad
// one is a build-time bug, not a runtime condition.
func hexHash(s string) [32]byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		panic("http: bad manifest SHA-256 literal: " + s)
	}
	var h [32]byte
	copy(h[:], b)
	return h
}

// RPiFirmware is the reference manifest: the exact revision + six files Pete's
// spectrum4 build pins (src/spectrum4/firmware/Tupfile) — the last-working
// Raspberry Pi firmware before issue raspberrypi/firmware#1979 broke the next.
// The SHA-256s are the content hashes of those files at this commit. This is the
// authority the Z80 FW_MANIFEST table is byte-compared against.
var RPiFirmware = Manifest{
	Owner: "raspberrypi",
	Repo:  "firmware",
	SHA:   "a43df3a002f60c4c2243a416d045eb5937585e8b",
	Files: []FirmwareFile{
		{"LICENCE.broadcom", "boot/LICENCE.broadcom", 1594, hexHash("c7283ff51f863d93a275c66e3b4cb08021a5dd4d8c1e7acc47d872fbe52d3d6b")},
		{"bootcode.bin", "boot/bootcode.bin", 52476, hexHash("af603ebd97e7b692c30195563f7b25656eb05d57838cf1a715ebb470d1614ce4")},
		{"start.elf", "boot/start.elf", 2979296, hexHash("dd9b42041b566d8b94529a6eda68ded147fd18c6a4b5d6b9743226082114ec84")},
		{"start4.elf", "boot/start4.elf", 2255072, hexHash("e1ee9939c23d253ec278a11954d7a35762d6be0d9f1d2d35a656fe1ae3a0304e")},
		{"fixup.dat", "boot/fixup.dat", 7274, hexHash("d8b55a35202684527a973ee47140376f440fee00d9679dc4a852ed0f22eb1bbe")},
		{"fixup4.dat", "boot/fixup4.dat", 5413, hexHash("5f2d922a87bb3a75f9ae7b5078f52a83d6b83a2a866223a64d328341e5b4351e")},
	},
}
