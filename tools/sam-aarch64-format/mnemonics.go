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
	// System barriers and exception return.
	// eret: Exception Return (no operand).                     ARM ARM C6.2.84.
	// isb: Instruction Synchronization Barrier (optional arg). ARM ARM C6.2.99.
	// dsb: Data Synchronization Barrier (mandatory arg).       ARM ARM C6.2.74.
	// dmb: Data Memory Barrier (mandatory arg).                ARM ARM C6.2.73.
	// wfi: Wait For Interrupt (no operand).                    ARM ARM C6.2.305.
	"eret", "isb", "dsb", "dmb", "wfi",
	// Single-source ALU.
	// ror:  Rotate Right — immediate alias of EXTR Rd,Rn,Rn,#imm (C6.2.196)
	//       or register form RORV Rd,Rn,Rm (C6.2.197).
	// mul:  Multiply — alias of MADD Rd,Rn,Rm,XZR (C6.2.139).
	// udiv: Unsigned divide (C6.2.222).
	// cls:  Count Leading Sign-bits (C6.2.39).
	// sxtw: Sign-extend Word — alias of SBFM Xd,Xn,0,31 (C6.2.214).
	"ror", "mul", "udiv", "cls", "sxtw",
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
