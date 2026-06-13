// fontproof prepares the inputs for, and builds the boot disks of, the
// i76 P1b font-proof: a Z80 probe that renders sample editor content on a
// real SAM MODE 3 screen with a 6-px font (85x32) and, for reference, the
// SAM ROM 8x8 charset (64x24).
//
// Subcommands:
//
//	fontproof font -header <five_pixel_font.h> -o <font6.bin> [-dump]
//	    Convert the vendored five-pixel-font C header
//	    (tools/editor-prototype/fonts/) to the SAM row-padded form the Z80
//	    renderer consumes: 96 glyphs (chars 32-127), 6 bytes per glyph, one
//	    byte per row, the 6 pixel columns in bits 7..2 (MSB = leftmost),
//	    bits 1..0 zero. Char 127 maps to the atlas's hollow-box glyph
//	    (index 95), mirroring fpf_get_glyph_position in the header.
//
//	fontproof text -src <release.s> -start-line <N> -rows <R> -cols <C> -o <out>
//	    Cut an R-line window from the source starting at 1-based line N,
//	    expand tabs (8-column stops), replace non-printable bytes with '?',
//	    truncate/pad each line to exactly C columns, and emit the flattened
//	    R*C-byte screen buffer the Z80 renderer walks.
//
//	fontproof disk -dos <samdos2.bin> -bin <fontproof.bin> -call <addr> -o <out.mgt>
//	    Build the bootable .mgt: DOS slot + auto-RUN BASIC (CLEAR 32767 /
//	    LOAD "fontproof" CODE 32768 / CALL <addr>) + the probe binary.
//	    Same boot recipe as tools/build-disk and build-i62-disk. CALL 32768
//	    renders the 6x6 screen; CALL 32771 the 8x8 reference screen.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	samfile "github.com/petemoore/samfile/v3"
	"github.com/petemoore/samfile/v3/sambasic"
)

const (
	texW       = 64 // five-pixel-font atlas dimensions (FPF_TEXTURE_WIDTH/HEIGHT)
	texH       = 64
	cellW      = 6 // FPF_GLYPH_WIDTH/HEIGHT: 5x5 ink in a 6x6 cell
	cellH      = 6
	glyphCount = 96 // chars 32..127; 127 -> hollow box (atlas index 95)

	loadAddress = 0x8000
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("fontproof: ")
	if len(os.Args) < 2 {
		log.Fatalf("usage: fontproof font|text|disk ...")
	}
	switch os.Args[1] {
	case "font":
		cmdFont(os.Args[2:])
	case "text":
		cmdText(os.Args[2:])
	case "disk":
		cmdDisk(os.Args[2:])
	default:
		log.Fatalf("unknown subcommand %q (want font, text or disk)", os.Args[1])
	}
}

// cmdFont decodes the fpf_compressed_font RLE stream from the vendored C
// header into the 64x64 1-bit atlas, then cuts the 6x6 glyph cells into the
// row-padded binary. RLE format (five_pixel_font.h, fpf_create_alpha_texture):
// a 0x00 byte is followed by a run-length byte meaning runlength*8 zero
// pixels; any other byte supplies 8 pixels MSB-first.
func cmdFont(args []string) {
	fs := flag.NewFlagSet("font", flag.ExitOnError)
	header := fs.String("header", "", "path to five_pixel_font.h")
	out := fs.String("o", "", "output font binary path")
	dump := fs.Bool("dump", false, "print the decoded glyphs as ASCII art for eyeballing")
	fs.Parse(args)
	if *header == "" || *out == "" {
		log.Fatalf("font: -header and -o are required")
	}

	src, err := os.ReadFile(*header)
	if err != nil {
		log.Fatalf("font: %v", err)
	}
	atlas := decodeAtlas(extractRLE(string(src)))

	bin := make([]byte, 0, glyphCount*cellH)
	for g := 0; g < glyphCount; g++ {
		gx := (g % (texW / cellW)) * cellW
		gy := (g / (texW / cellW)) * cellH
		for row := 0; row < cellH; row++ {
			var b byte
			for col := 0; col < cellW; col++ {
				if atlas[(gy+row)*texW+gx+col] {
					b |= 0x80 >> col
				}
			}
			bin = append(bin, b)
		}
	}

	if *dump {
		dumpGlyphs(bin)
	}
	if err := os.WriteFile(*out, bin, 0o644); err != nil {
		log.Fatalf("font: %v", err)
	}
	log.Printf("wrote %s (%d glyphs, %d bytes)", *out, glyphCount, len(bin))
}

// extractRLE pulls the fpf_compressed_font initialiser bytes out of the C
// header text.
func extractRLE(src string) []byte {
	m := regexp.MustCompile(`(?s)fpf_compressed_font\[\]\s*=\s*\{(.*?)\}`).FindStringSubmatch(src)
	if m == nil {
		log.Fatalf("font: fpf_compressed_font array not found in header")
	}
	var rle []byte
	for _, tok := range regexp.MustCompile(`\d+`).FindAllString(m[1], -1) {
		n, err := strconv.Atoi(tok)
		if err != nil || n > 255 {
			log.Fatalf("font: bad byte %q in fpf_compressed_font", tok)
		}
		rle = append(rle, byte(n))
	}
	return rle
}

// decodeAtlas expands the RLE stream to the texW*texH 1-bit atlas
// (row-major, top-to-bottom — the header's FPF_RASTER_Y_AXIS order).
func decodeAtlas(rle []byte) []bool {
	atlas := make([]bool, texW*texH)
	pos := 0
	emit := func(on bool) {
		if pos < len(atlas) {
			atlas[pos] = on
		}
		pos++
	}
	for i := 0; i < len(rle); i++ {
		if rle[i] == 0 {
			i++
			if i >= len(rle) {
				log.Fatalf("font: truncated RLE run")
			}
			for n := int(rle[i]) * 8; n > 0; n-- {
				emit(false)
			}
			continue
		}
		for bit := 0; bit < 8; bit++ {
			emit(rle[i]&(0x80>>bit) != 0)
		}
	}
	if pos > len(atlas) {
		log.Fatalf("font: RLE overruns the %dx%d atlas (%d pixels)", texW, texH, pos)
	}
	return atlas
}

func dumpGlyphs(bin []byte) {
	for g := 0; g < glyphCount; g++ {
		fmt.Printf("--- %d %q\n", g+32, rune(g+32))
		for row := 0; row < cellH; row++ {
			b := bin[g*cellH+row]
			var sb strings.Builder
			for col := 0; col < cellW; col++ {
				if b&(0x80>>col) != 0 {
					sb.WriteByte('#')
				} else {
					sb.WriteByte('.')
				}
			}
			fmt.Println(sb.String())
		}
	}
}

// cmdText flattens a window of the source file into the fixed rows*cols
// screen buffer.
func cmdText(args []string) {
	fs := flag.NewFlagSet("text", flag.ExitOnError)
	src := fs.String("src", "", "source text file (e.g. tests/release/release.s)")
	startLine := fs.Int("start-line", 1, "1-based first line of the window")
	rows := fs.Int("rows", 32, "screen rows")
	cols := fs.Int("cols", 85, "screen columns")
	out := fs.String("o", "", "output buffer path")
	fs.Parse(args)
	if *src == "" || *out == "" {
		log.Fatalf("text: -src and -o are required")
	}

	data, err := os.ReadFile(*src)
	if err != nil {
		log.Fatalf("text: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if *startLine < 1 || *startLine+*rows-1 > len(lines) {
		log.Fatalf("text: window %d..%d outside source (%d lines)", *startLine, *startLine+*rows-1, len(lines))
	}

	buf := make([]byte, 0, *rows**cols)
	for r := 0; r < *rows; r++ {
		line := expandTabs(lines[*startLine-1+r])
		row := make([]byte, *cols)
		for i := range row {
			row[i] = ' '
			if i < len(line) {
				c := line[i]
				if c < 32 || c > 126 {
					c = '?'
				}
				row[i] = c
			}
		}
		buf = append(buf, row...)
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		log.Fatalf("text: %v", err)
	}
	log.Printf("wrote %s (%dx%d window from %s:%d)", *out, *cols, *rows, *src, *startLine)
}

func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var sb strings.Builder
	col := 0
	for _, c := range s {
		if c == '\t' {
			n := 8 - col%8
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		sb.WriteRune(c)
		col++
	}
	return sb.String()
}

// cmdDisk builds the bootable .mgt — the build-i62-disk boot recipe with the
// probe's CALL address selectable (32768 = 6x6 screen, 32771 = 8x8 screen).
func cmdDisk(args []string) {
	fs := flag.NewFlagSet("disk", flag.ExitOnError)
	dosPath := fs.String("dos", "", "SAMDOS 2 boot file (reference/samdos/samdos2.bin)")
	binPath := fs.String("bin", "", "assembled fontproof.bin")
	call := fs.Uint("call", loadAddress, "entry address for the BASIC CALL")
	out := fs.String("o", "", "output .mgt path")
	fs.Parse(args)
	if *dosPath == "" || *binPath == "" || *out == "" {
		log.Fatalf("disk: -dos, -bin and -o are required")
	}

	dosBin, err := os.ReadFile(*dosPath)
	if err != nil {
		log.Fatalf("disk: %v", err)
	}
	probeBin, err := os.ReadFile(*binPath)
	if err != nil {
		log.Fatalf("disk: %v", err)
	}

	disk := samfile.NewDiskImage()

	// Slot 0: the DOS. ROM BOOT reads T4S1 raw; directory start address
	// replicated from the samdos2 source disk (page 29 + offset &8009 =
	// 491529, with the 0x60 unused-bits pattern), as in tools/build-disk.
	if err := disk.AddCodeFile("samdos2", dosBin, 491529, 0); err != nil {
		log.Fatalf("disk: AddCodeFile(samdos2): %v", err)
	}
	if err := disk.SetStartAddressPageUnusedBits("samdos2", 3); err != nil {
		log.Fatalf("disk: SetStartAddressPageUnusedBits: %v", err)
	}

	// Slot 1: AUTO BASIC (StartLine=10 marks auto-RUN).
	auto := &sambasic.File{
		StartLine: 10,
		Lines: []sambasic.Line{
			{Number: 10, Tokens: []sambasic.Token{
				sambasic.CLEAR,
				sambasic.Number(uint16(loadAddress - 1)),
			}},
			{Number: 20, Tokens: []sambasic.Token{
				sambasic.LOAD,
				sambasic.String(`"fontproof"`),
				sambasic.CODE,
				sambasic.Number(uint16(loadAddress)),
			}},
			{Number: 30, Tokens: []sambasic.Token{
				sambasic.CALL,
				sambasic.Number(uint16(*call)),
			}},
		},
	}
	if err := disk.AddBasicFile("auto", auto); err != nil {
		log.Fatalf("disk: AddBasicFile(auto): %v", err)
	}

	// Slot 2: the probe binary.
	if err := disk.AddCodeFile("fontproof", probeBin, loadAddress, 0); err != nil {
		log.Fatalf("disk: AddCodeFile(fontproof): %v", err)
	}

	if err := disk.Save(*out); err != nil {
		log.Fatalf("disk: write %s: %v", *out, err)
	}
	log.Printf("built %s (probe %d bytes, CALL %d)", *out, len(probeBin), *call)
}
