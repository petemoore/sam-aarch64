package format

// DirectiveTable: append-only ID ↔ name map (§3).
var DirectiveTable = []string{
	".text", ".data",
	".byte", ".short", ".word", ".quad",
	".ascii", ".asciz",
	".equ", ".set",
	".global",
	".balign",
	".org",
	".skip", ".space",
	".inst",
	".align", // aarch64 GNU as: align to 2^N bytes
}

var directiveIndex = func() map[string]uint8 {
	m := make(map[string]uint8, len(DirectiveTable))
	for i, n := range DirectiveTable {
		m[n] = uint8(i)
	}
	return m
}()

func DirectiveID(name string) (uint8, bool) {
	id, ok := directiveIndex[name]
	return id, ok
}

func DirectiveName(id uint8) string {
	if int(id) >= len(DirectiveTable) {
		return ""
	}
	return DirectiveTable[id]
}
