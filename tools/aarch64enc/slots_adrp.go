package aarch64enc

import "fmt"

// encodeAdrpImm packs a page-relative byte offset into the (immlo:2,
// immhi:19) pair used by adrp. immlo always lives at bits 29..30,
// immhi always at bits 5..23. The slot's BitPosition/BitWidth are
// not consulted (the layout is fixed by the adrp encoding).
//
// Offset must be a multiple of 4096 (page-aligned) and fit in ±4GB.
func encodeAdrpImm(slot OperandSlot, byteOffset int64) (uint32, error) {
	if byteOffset%4096 != 0 {
		return 0, fmt.Errorf("AdrpImm: offset %d not page-aligned", byteOffset)
	}
	pageOffset := byteOffset / 4096
	half := int64(1) << 20
	if pageOffset >= half || pageOffset < -half {
		return 0, fmt.Errorf("AdrpImm: page offset %d out of ±4GB range", pageOffset)
	}
	imm21 := uint32(pageOffset) & ((1 << 21) - 1)
	immlo := imm21 & 0x3
	immhi := (imm21 >> 2) & ((1 << 19) - 1)
	return (immlo << 29) | (immhi << 5), nil
}
