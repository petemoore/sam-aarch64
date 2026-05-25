package format

// MnemonicTable is the append-only ID ↔ name map for aarch64
// mnemonics that text2bin recognises. New mnemonics are appended;
// existing IDs never shift (§3, §9.1).
//
// Index in the slice is the on-disk mnemonic_id.
var MnemonicTable = []string{
	"nop", "add", "sub", "mov", "mvn",
	"ldr", "str", "ldp", "stp",
	"b", "bl", "br", "ret",
	"adrp",
	"and", "orr", "eor",
	"lsl", "lsr",
	"cmp",
	"cbz", "cbnz", "tbz", "tbnz",
	"csel", "csinc",
	"b.eq", "b.ne", "b.cs", "b.cc",
	"b.mi", "b.pl", "b.vs", "b.vc",
	"b.hi", "b.ls", "b.ge", "b.lt",
	"b.gt", "b.le", "b.al", "b.nv",
	// Aliases for b.cond (same encoding, different mnemonic names).
	"b.hs", // alias for b.cs (cond=2, CS=HS)
	"b.lo", // alias for b.cc (cond=3, CC=LO)
	// blr was already vendored; add to table.
	"blr",
	// subs: subtract setting flags.
	"subs",
	// tst: test (logical AND setting flags, result discarded).
	"tst",
	// bic: bit clear (AND NOT).
	"bic",
	// adr: PC-relative address (small range, not page-aligned).
	"adr",
	// bitfield operations.
	"bfi", "bfxil", "ubfx",
	// conditional set mask (alias of csinv with xzr/wzr).
	"csetm",
	// movk: move keeping other bits.
	"movk",
	// byte/halfword loads and stores.
	"ldrb", "strb", "ldrh", "strh",
	// 4-operand multiply-add/subtract.
	"madd", "msub",
	// unsigned multiply variants.
	"umull", "umulh", "umaddl", "umsubl",
	// movl: spectrum4 pseudo-instruction: "move 32-bit literal" into Wd/Wx.
	// Expands to MOVZ Rd, #lo16 + MOVK Rd, #hi16, lsl #16 (or just MOVZ if hi16=0).
	"movl",
}

var mnemonicIndex = func() map[string]uint16 {
	m := make(map[string]uint16, len(MnemonicTable))
	for i, n := range MnemonicTable {
		m[n] = uint16(i)
	}
	return m
}()

// MnemonicID returns the on-disk ID for a mnemonic name. ok=false if
// the name is not in the table.
func MnemonicID(name string) (uint16, bool) {
	id, ok := mnemonicIndex[name]
	return id, ok
}

// MnemonicName returns the name for an on-disk mnemonic ID, or "" if
// the ID is out of range.
func MnemonicName(id uint16) string {
	if int(id) >= len(MnemonicTable) {
		return ""
	}
	return MnemonicTable[id]
}
