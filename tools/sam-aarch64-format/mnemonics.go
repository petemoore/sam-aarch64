package format

// MnemonicTable is the ID ↔ name map for aarch64 mnemonics that
// text2bin recognises.  Index in the slice is the on-disk
// mnemonic_id.
//
// Format-stability policy (interim — see M1 spec §9, "Risks /
// Format ossification"):
//
//   - **For now** (no .tbn files exist outside `build/` and `/tmp/`;
//     none are committed to the repo, none shipped to end-users), the
//     table is mutable: removals + renumberings are allowed, with the
//     caveat that every consumer (manual_forms.go, data.go, the
//     hardcoded integer dispatches in refenc/pass2.go, and any test
//     that builds a record by literal ID) must be updated in lockstep.
//     PR #26 (drop unused `cls`) exercised this lockstep for the
//     first time.
//   - **Once `.tbn` files start being shipped or persisted** (the M5
//     compact `.tbn` format work, a packaged release, or anything
//     where pre-built `.tbn` artefacts exist in the wild), the table
//     reverts to strictly append-only.  Removals at that point need a
//     format-version bump.
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
	// sxtw: Sign-extend Word — alias of SBFM Xd,Xn,0,31 (C6.2.214).
	"ror", "mul", "udiv", "sxtw",
	// Unscaled load/store with signed 9-bit byte offset (ARM ARM C6.2.276 / C6.2.124).
	// These are distinct mnemonics from ldr/str; the offset is *not* scaled.
	"stur", "ldur",
	// System-register access (ARM ARM C6.2.137 / C6.2.140-C6.2.141).
	//   mrs Xt, <sysreg>           — Move System Register to general register.
	//   msr <sysreg>, Xt           — Move general register to System Register.
	//   msr <pstatefield>, #imm    — Set a PSTATE field (daifset etc.).
	// Both msr forms share a single mnemonic ID; the parser disambiguates
	// on the second operand kind.
	"mrs", "msr",
	// System cache and TLB maintenance (ARM ARM C6.2.66 / C6.2.281).
	//   dc <op>, Xt                — Data Cache maintenance op.
	//   tlbi <op>[, Xt]            — TLB Invalidate op (Xt optional).
	"dc", "tlbi",
	// ands: AND setting flags (ARM ARM C6.2.13 / C6.2.14). Immediate and
	// shifted-register forms. tst is the existing alias with Rd=xzr; ands
	// is the full 3-operand form.
	"ands",
	// movz: Move Wide with Zero (ARM ARM C6.2.131). Same surface syntax
	// as movk (with optional `lsl #N` suffix) but a different opcode.
	// movn: Move Wide with Not (ARM ARM C6.2.129). Same shape.
	"movz", "movn",
	// bfc: Bit Field Clear (ARM ARM C6.2.21). Alias of BFI Rd, XZR, lsb,
	// width. 3-operand form: `bfc Rd, #lsb, #width`. Same encoding as
	// BFI with Rn=XZR.
	// sbfx: Signed Bit Field Extract (ARM ARM C6.2.205). Alias of SBFM.
	//   `sbfx Rd, Rn, #lsb, #width`.
	"bfc", "sbfx",
	// Signed-extend loads (share the LDR/STR encoding template but with
	// a different opc field at bits 23:22). opc=10 selects "signed load
	// to 64-bit Xt", opc=11 selects "signed load to 32-bit Wt".
	// ldrsb: Load Signed Byte (ARM ARM C6.2.117). size=00.
	// ldrsh: Load Signed Halfword (ARM ARM C6.2.118). size=01.
	// ldrsw: Load Signed Word (ARM ARM C6.2.119). size=10, Xt only.
	"ldrsb", "ldrsh", "ldrsw",
	// ccmp: Conditional Compare (ARM ARM C6.2.43 / C6.2.44).
	//   ccmp Rn, #imm5, #nzcv, cond   — immediate form.
	//   ccmp Rn, Rm,    #nzcv, cond   — register form.
	// Both have 32-bit (Wn) and 64-bit (Xn) variants.  Used by
	// spectrum4/kernel/macros.s and friends to chain comparisons.
	"ccmp",
	// udf: Permanently Undefined instruction (ARM ARM C6.2.228).
	// Encoding: 0x0000NNNN where bits[31:16]=0, bits[15:0]=imm16.
	// Used by objdump/aarch64dec for zero-byte padding and undefined slots.
	"udf",
	// sxth: Sign-extend Halfword — alias of SBFM Rd, Wn, #0, #15 (ARM ARM C6.2.213).
	// sxtb: Sign-extend Byte    — alias of SBFM Rd, Wn, #0, #7  (ARM ARM C6.2.212).
	// uxtb: Unsigned-extend Byte     — alias of UBFM Wd, Wn, #0, #7  (ARM ARM C6.2.226).
	//       Only W-form (sf=0); 64-bit unsigned extend stays ubfx.
	// uxth: Unsigned-extend Halfword — alias of UBFM Wd, Wn, #0, #15 (ARM ARM C6.2.227).
	//       Only W-form (sf=0).
	"sxth", "sxtb", "uxtb", "uxth",
	// Unscaled halfword/byte load-store (ARM ARM C6.2.279/C6.2.276/C6.2.118/C6.2.115).
	// sturh/ldurh: halfword (size=01); sturb/ldurb: byte (size=00).
	// Register is always Wn; the size field is fixed by the mnemonic, not Rt width.
	// IDs 94-97, symmetric with stur=74/ldur=75.
	"sturh", "sturb", "ldurh", "ldurb",
	// adds: Add and Set Flags (ARM ARM C6.2.6). Immediate and shifted-register forms.
	// Symmetric with subs (ID 45): same encoding template, op=0 S=1 → base 0x31/0xb1.
	// ID 98.
	"adds",
	// sdiv: Signed Divide (ARM ARM C6.2.202). DP-2-source, mirrors udiv (ID 72)
	// exactly — same encoding template, opcode bit10 set: udiv base 0x1ac00800
	// (opcode 000010), sdiv base 0x1ac00c00 (opcode 000011). ID 99.
	"sdiv",
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
