// Package builder constructs the test disk used by llist-capture and
// llist-sweep to capture the SAM ROM's LLIST output for a given BASIC
// file. The disk contains samdos2 (boot loader) plus a single BASIC
// file named "AUTO" — the target's body bytes with one extra control
// line spliced in. SAMDOS auto-loads "AUTO", the BASIC interpreter
// jumps to the control line (which we set as the file's auto-run),
// and that line POKEs a halt stub, runs bare LLIST, then CALLs the
// halt stub to quit SimCoupé.
//
// Consumers (llist-sweep, llist-capture CLI, basic-detokeniser-sweep)
// call BuildTestDisk to produce a .mgt and learn the injected line
// number, then post-process the captured LLIST output with
// StripInjectedLine to drop the line we added.
//
// Why bare LLIST: the SAM ROM has a slow path for LLIST that only
// fires when line 0 is in the listed range. Bare LLIST (no args)
// emits every line in the program but isn't equivalent to "LLIST 0 TO
// 65535" — it skips the slow path.
//
// Why lowest-unused line number: we never have to remove an existing
// user line to make room, so the captured LLIST output is faithful to
// the original program (modulo the line we add, which we strip out).
package builder

import (
	"bytes"
	"fmt"
	"os"

	"github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

const (
	// SamdosLoadAddress is the canonical samdos2 load address — see
	// ~/git/sam-aarch64/tools/build-disk/main.go for derivation.
	SamdosLoadAddress uint32 = 491529

	// HaltStubAddress is the load address of the 2-byte DI;HALT stub.
	// 0x4000 = 16384 = top-left of the SAM mode-1 screen file in
	// section B. Nothing we run from BASIC after the POKEs land will
	// touch the screen file, so the stub bytes survive until CALL
	// 16384 reaches them.
	HaltStubAddress uint16 = 16384

	// MaxLineNumber is the highest line number SAM accepts (grammar
	// §2.3 / ROM EVALLINO L4079). We search [1, MaxLineNumber] for a
	// free slot.
	MaxLineNumber uint16 = 65279
)

// haltStubBytes are the 2 bytes POKEd at HaltStubAddress: 0xF3 = DI,
// 0x76 = HALT. SimCoupé's -exitonhalt 1 flag exits when the Z80
// executes HALT with interrupts disabled.
var haltStubBytes = [2]byte{0xF3, 0x76}

// Result reports what BuildTestDisk produced.
type Result struct {
	InjectedLine uint16 // line number we spliced in (and that callers must strip from LLIST output)
	SamdosBytes  int    // size of samdos2 written to the disk
	AutoBytes    int    // size of the modified BASIC body written as "AUTO"
}

// BuildTestDisk reads the named BASIC file from sourceDisk, splices in
// a control line at the smallest unused line number ≥ 1, forces that
// line to be the file's auto-run, and writes the resulting test disk
// (samdos2 + AUTO BASIC) to outputPath. samdosPath is the path to
// samdos2.bin.
func BuildTestDisk(sourceDisk, basicName, outputPath, samdosPath string) (Result, error) {
	samdos2, err := os.ReadFile(samdosPath)
	if err != nil {
		return Result{}, fmt.Errorf("read samdos2 (%s): %w", samdosPath, err)
	}
	if len(samdos2) != 10000 {
		return Result{}, fmt.Errorf("samdos2: expected 10000 bytes, got %d", len(samdos2))
	}

	src, err := samfile.Load(sourceDisk)
	if err != nil {
		return Result{}, fmt.Errorf("load source disk %s: %w", sourceDisk, err)
	}
	target, entry, err := loadBasicFile(src, basicName)
	if err != nil {
		return Result{}, fmt.Errorf("read %s/%s: %w", sourceDisk, basicName, err)
	}

	injectedLine, modifiedBody, newNVARS, newNUMEND, newSAVARS, err := injectControlLine(target, entry)
	if err != nil {
		return Result{}, fmt.Errorf("inject control line: %w", err)
	}

	disk := samfile.NewDiskImage()

	if err := disk.AddCodeFile("samdos2", samdos2, SamdosLoadAddress, 0); err != nil {
		return Result{}, fmt.Errorf("AddCodeFile(samdos2): %w", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		return Result{}, fmt.Errorf("SetStartAddressPageUnusedBits(samdos2): %w", err)
	}
	if err := disk.AddBasicFileBody("AUTO", modifiedBody, newNVARS, newNUMEND, newSAVARS, injectedLine); err != nil {
		return Result{}, fmt.Errorf("AddBasicFileBody(AUTO): %w", err)
	}
	if err := disk.Save(outputPath); err != nil {
		return Result{}, fmt.Errorf("save %s: %w", outputPath, err)
	}

	return Result{
		InjectedLine: injectedLine,
		SamdosBytes:  len(samdos2),
		AutoBytes:    len(modifiedBody),
	}, nil
}

// StripInjectedLine removes the LLIST output for a single logical line
// numbered lineNo from data.
//
// LLIST output format per logical line:
//   bytes [0..4]  digits of the line number, right-aligned in a
//                 5-character field padded with leading spaces
//                 (e.g. "    1", "   10", "65279")
//   byte  [5]     cursor: '>' for the current line, ' ' otherwise
//   bytes [6..]   line content (tokens etc.)
//
// Long lines wrap at 80 columns; continuation segments start with
// exactly 6 leading spaces (matching the line-number column width)
// followed by the wrapped content. We drop the header segment whose
// embedded line number == lineNo, plus immediately-following
// continuation segments. The empty segment produced by splitting on a
// trailing CRLF is preserved so the rejoin reproduces the terminator.
func StripInjectedLine(data []byte, lineNo uint16) []byte {
	segments := bytes.Split(data, []byte("\r\n"))
	out := make([][]byte, 0, len(segments))
	skipping := false
	for _, seg := range segments {
		if n, ok := segmentLineNumber(seg); ok {
			if n == lineNo {
				skipping = true
				continue
			}
			skipping = false
			out = append(out, seg)
			continue
		}
		if skipping && segmentIsContinuation(seg) {
			continue
		}
		skipping = false
		out = append(out, seg)
	}
	return bytes.Join(out, []byte("\r\n"))
}

// segmentLineNumber decodes the line-number header at the start of a
// LLIST segment. Returns (n, true) if seg begins with a valid header:
// bytes [0..4] are leading spaces followed by 1-5 digits ending at
// byte 4, and byte 5 is the cursor char ' ' or '>'. Returns (0, false)
// otherwise.
func segmentLineNumber(seg []byte) (uint16, bool) {
	if len(seg) < 6 {
		return 0, false
	}
	if seg[5] != ' ' && seg[5] != '>' {
		return 0, false
	}
	i := 0
	for i < 5 && seg[i] == ' ' {
		i++
	}
	if i == 5 {
		// All five header bytes are spaces — no digits, this is a
		// continuation, not a logical-line header.
		return 0, false
	}
	var n uint32
	for i < 5 {
		c := seg[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint32(c-'0')
		i++
	}
	if n > 0xFFFF {
		return 0, false
	}
	return uint16(n), true
}

// segmentIsContinuation reports whether seg looks like the wrapped
// continuation of a logical line — i.e. begins with the 6 leading
// spaces that align with the line-number column.
func segmentIsContinuation(seg []byte) bool {
	if len(seg) < 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		if seg[i] != ' ' {
			return false
		}
	}
	return true
}

// loadBasicFile reads the named file from disk and confirms it's a
// SAM BASIC file. Returns the File plus its FileEntry (for the
// offsets in FileTypeInfo).
func loadBasicFile(disk *samfile.DiskImage, name string) (*samfile.File, *samfile.FileEntry, error) {
	var entry *samfile.FileEntry
	for _, fe := range disk.DiskJournal() {
		if fe == nil || !fe.Used() {
			continue
		}
		if fe.Name.String() == name {
			entry = fe
			break
		}
	}
	if entry == nil {
		return nil, nil, fmt.Errorf("file %q not found", name)
	}
	if entry.Type != samfile.FT_SAM_BASIC {
		return nil, nil, fmt.Errorf("file %q is type %v, not FT_SAM_BASIC", name, entry.Type)
	}
	f, err := disk.File(name)
	if err != nil {
		return nil, nil, fmt.Errorf("read %q: %w", name, err)
	}
	return f, entry, nil
}

// injectControlLine finds the smallest unused line number N ≥ 1 in
// target's program area, builds the control line at N, and splices it
// into the program-area bytes at the offset just before the first
// existing line whose number is > N. Returns N, the new body, and the
// updated section-boundary offsets.
//
// Body layout:
//   [lines]              0..NVARSOffset-1      (ends with 0xFF)
//   [numeric vars]       NVARSOffset..NUMENDOffset-1
//   [gap]                NUMENDOffset..SAVARSOffset-1
//   [string/array vars]  SAVARSOffset..end
func injectControlLine(target *samfile.File, entry *samfile.FileEntry) (uint16, []byte, uint32, uint32, uint32, error) {
	body := target.Body
	nvarsOff := pageFormLengthFromBytes(entry.FileTypeInfo[0:3])
	numendOff := pageFormLengthFromBytes(entry.FileTypeInfo[3:6])
	savarsOff := pageFormLengthFromBytes(entry.FileTypeInfo[6:9])

	if nvarsOff == 0 {
		return 0, nil, 0, 0, 0, fmt.Errorf("body has zero program length (NVARSOffset=0)")
	}
	if int(savarsOff) > len(body) {
		return 0, nil, 0, 0, 0, fmt.Errorf("SAVARSOffset %d exceeds body length %d", savarsOff, len(body))
	}
	if body[nvarsOff-1] != 0xFF {
		return 0, nil, 0, 0, 0, fmt.Errorf("expected 0xFF program-end at offset %d, got 0x%02X", nvarsOff-1, body[nvarsOff-1])
	}

	progBody := body[:nvarsOff-1]
	varsArea := body[nvarsOff-1:]

	lineNo, err := findFreeLine(progBody)
	if err != nil {
		return 0, nil, 0, 0, 0, err
	}
	insertAt := findInsertionOffset(progBody, lineNo)
	injected := buildInjectedLine(lineNo)
	injectedLen := uint32(len(injected))

	out := make([]byte, 0, len(progBody)+len(injected)+len(varsArea))
	out = append(out, progBody[:insertAt]...)
	out = append(out, injected...)
	out = append(out, progBody[insertAt:]...)
	out = append(out, varsArea...)

	return lineNo, out, nvarsOff + injectedLen, numendOff + injectedLen, savarsOff + injectedLen, nil
}

// findFreeLine walks the program area and returns the smallest line
// number in [1, MaxLineNumber] not already present. The program area
// is the lines section excluding the trailing 0xFF marker.
func findFreeLine(progBody []byte) (uint16, error) {
	used := make(map[uint16]bool)
	i := 0
	for i+3 < len(progBody) {
		num := uint16(progBody[i])<<8 | uint16(progBody[i+1])
		lineLen := uint16(progBody[i+2]) | uint16(progBody[i+3])<<8
		used[num] = true
		i += 4 + int(lineLen)
	}
	for n := uint16(1); n <= MaxLineNumber; n++ {
		if !used[n] {
			return n, nil
		}
	}
	return 0, fmt.Errorf("no free line number in [1, %d]", MaxLineNumber)
}

// findInsertionOffset returns the byte offset within progBody where a
// new line numbered lineNo should be spliced — just before the first
// existing line whose number is > lineNo, or at the end of progBody if
// no such line exists. SAM stores program lines in numerical order, so
// the splice must preserve that ordering.
func findInsertionOffset(progBody []byte, lineNo uint16) int {
	i := 0
	for i+3 < len(progBody) {
		num := uint16(progBody[i])<<8 | uint16(progBody[i+1])
		if num > lineNo {
			return i
		}
		lineLen := uint16(progBody[i+2]) | uint16(progBody[i+3])<<8
		i += 4 + int(lineLen)
	}
	return i
}

// pageFormLengthFromBytes decodes the 3-byte page-form length used in
// FileTypeInfo. Matches samfile.pageFormLength internally.
func pageFormLengthFromBytes(b []byte) uint32 {
	page := uint32(b[0])
	offset := uint16(b[1]) | uint16(b[2])<<8
	off := uint32(offset & 0x3FFF)
	return page*16384 + off
}

// buildInjectedLine constructs the on-disk bytes for the injected
// control line at lineNo:
//
//	<lineNo> POKE 23203,0: POKE 23204,0: POKE 16384,243: POKE 16385,118: LLIST: CALL 16384
//
// The first two POKEs zero XPTR (0x5AA3/0x5AA4 = 23203/23204) so the
// ROM's per-byte address compare during LLIST never emits a spurious
// flashing '?'. The next two POKEs place the DI;HALT stub at 0x4000.
// Bare LLIST emits every line (fast: the ROM slow path is only
// triggered when line 0 is in range). CALL 16384 jumps to the halt
// stub and never returns — SimCoupé -exitonhalt 1 then exits.
//
// On-disk layout: [MSB LSB LenLo LenHi] header, token bytes, 0x0D
// terminator.
func buildInjectedLine(lineNo uint16) []byte {
	line := sambasic.Line{
		Number: lineNo,
		Tokens: []sambasic.Token{
			sambasic.POKE, sambasic.Number(23203), sambasic.String(","), sambasic.Number(0),
			sambasic.String(":"),
			sambasic.POKE, sambasic.Number(23204), sambasic.String(","), sambasic.Number(0),
			sambasic.String(":"),
			sambasic.POKE, sambasic.Number(HaltStubAddress), sambasic.String(","), sambasic.Number(uint16(haltStubBytes[0])),
			sambasic.String(":"),
			sambasic.POKE, sambasic.Number(HaltStubAddress + 1), sambasic.String(","), sambasic.Number(uint16(haltStubBytes[1])),
			sambasic.String(":"),
			sambasic.LLIST,
			sambasic.String(":"),
			sambasic.CALL, sambasic.Number(HaltStubAddress),
		},
	}
	return line.Bytes()
}
