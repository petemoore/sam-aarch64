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
