package format

import (
	"fmt"
	"strconv"
	"strings"
)

// SysReg is a tuple identifying an AArch64 system register by its
// (op0, op1, CRn, CRm, op2) encoding fields. Used by `mrs` and the
// register form of `msr`.
//
// The same tuple kind also identifies the (op1, CRn, CRm, op2) slot
// of `dc` and `tlbi` operations; op0 is fixed at 1 for those (the
// "system instruction" encoding space) and is reused here as a
// convenient carrier. Callers that emit DC/TLBI need only the lower
// four fields.
type SysReg struct {
	Op0 byte
	Op1 byte
	CRn byte
	CRm byte
	Op2 byte
}

// namedSysRegs lists the system registers spectrum4 names by their
// architectural mnemonic. Add new entries here as they appear in the
// source tree.
//
// HAND-SYNCED with the Z80 copy in src/sysreg_data.asm (the page-13
// payload the SAM assembler walks at runtime). That file carries a SUBSET
// of these entries — only the ones the M5/M6 fixtures exercise — but every
// entry it does carry MUST match the encoding here byte-for-byte. The
// sync guard sysregs_z80sync_test.go (CI job `sysreg-sync`, make target
// `sysreg-sync-check`) enforces this; if you edit an encoding here, update
// sysreg_data.asm too or the guard will fail. (repo-audit 2026-05-29 #9.)
//
// References are to the ARM ARM (D17 chapter for general-purpose
// system registers, D17.11 for generic timer, D17.12 for debug).
var namedSysRegs = map[string]SysReg{
	// Application-level (PSTATE projection).
	"nzcv":      {Op0: 3, Op1: 3, CRn: 4, CRm: 2, Op2: 0}, // D17.2.92
	"daif":      {Op0: 3, Op1: 3, CRn: 4, CRm: 2, Op2: 1},
	"currentel": {Op0: 3, Op1: 0, CRn: 4, CRm: 2, Op2: 2}, // D17.2.62
	"spsel":     {Op0: 3, Op1: 0, CRn: 4, CRm: 2, Op2: 0},

	// EL1 control & status.
	"sctlr_el1":  {Op0: 3, Op1: 0, CRn: 1, CRm: 0, Op2: 0},
	"actlr_el1":  {Op0: 3, Op1: 0, CRn: 1, CRm: 0, Op2: 1},
	"cpacr_el1":  {Op0: 3, Op1: 0, CRn: 1, CRm: 0, Op2: 2},
	"ttbr0_el1":  {Op0: 3, Op1: 0, CRn: 2, CRm: 0, Op2: 0},
	"ttbr1_el1":  {Op0: 3, Op1: 0, CRn: 2, CRm: 0, Op2: 1},
	"tcr_el1":    {Op0: 3, Op1: 0, CRn: 2, CRm: 0, Op2: 2},
	"esr_el1":    {Op0: 3, Op1: 0, CRn: 5, CRm: 2, Op2: 0},
	"far_el1":    {Op0: 3, Op1: 0, CRn: 6, CRm: 0, Op2: 0},
	"mair_el1":   {Op0: 3, Op1: 0, CRn: 10, CRm: 2, Op2: 0},
	"amair_el1":  {Op0: 3, Op1: 0, CRn: 10, CRm: 3, Op2: 0},
	"vbar_el1":   {Op0: 3, Op1: 0, CRn: 12, CRm: 0, Op2: 0},
	"contextidr_el1": {Op0: 3, Op1: 0, CRn: 13, CRm: 0, Op2: 1},
	"tpidr_el0":  {Op0: 3, Op1: 3, CRn: 13, CRm: 0, Op2: 2},
	"tpidr_el1":  {Op0: 3, Op1: 0, CRn: 13, CRm: 0, Op2: 4},
	"elr_el1":    {Op0: 3, Op1: 0, CRn: 4, CRm: 0, Op2: 1},
	"spsr_el1":   {Op0: 3, Op1: 0, CRn: 4, CRm: 0, Op2: 0},
	"sp_el0":     {Op0: 3, Op1: 0, CRn: 4, CRm: 1, Op2: 0},

	// EL1 identification.
	"midr_el1":   {Op0: 3, Op1: 0, CRn: 0, CRm: 0, Op2: 0}, // D17.2.100
	"mpidr_el1":  {Op0: 3, Op1: 0, CRn: 0, CRm: 0, Op2: 5},
	"revidr_el1": {Op0: 3, Op1: 0, CRn: 0, CRm: 0, Op2: 6},

	// EL2 / EL3.
	"hcr_el2":  {Op0: 3, Op1: 4, CRn: 1, CRm: 1, Op2: 0},
	"scr_el3":  {Op0: 3, Op1: 6, CRn: 1, CRm: 1, Op2: 0},
	"elr_el2":  {Op0: 3, Op1: 4, CRn: 4, CRm: 0, Op2: 1},
	"elr_el3":  {Op0: 3, Op1: 6, CRn: 4, CRm: 0, Op2: 1},
	"spsr_el2": {Op0: 3, Op1: 4, CRn: 4, CRm: 0, Op2: 0},
	"spsr_el3": {Op0: 3, Op1: 6, CRn: 4, CRm: 0, Op2: 0},

	// Generic timer (CNTPCT_EL0 etc.) — D17.11.
	"cntpct_el0":    {Op0: 3, Op1: 3, CRn: 14, CRm: 0, Op2: 1},
	"cntvct_el0":    {Op0: 3, Op1: 3, CRn: 14, CRm: 0, Op2: 2},
	"cntfrq_el0":    {Op0: 3, Op1: 3, CRn: 14, CRm: 0, Op2: 0},
	"cntp_ctl_el0":  {Op0: 3, Op1: 3, CRn: 14, CRm: 2, Op2: 1},
	"cntp_cval_el0": {Op0: 3, Op1: 3, CRn: 14, CRm: 2, Op2: 2},
	"cntp_tval_el0": {Op0: 3, Op1: 3, CRn: 14, CRm: 2, Op2: 0},
	"cntv_ctl_el0":  {Op0: 3, Op1: 3, CRn: 14, CRm: 3, Op2: 1},
	"cntv_cval_el0": {Op0: 3, Op1: 3, CRn: 14, CRm: 3, Op2: 2},
	"cntv_tval_el0": {Op0: 3, Op1: 3, CRn: 14, CRm: 3, Op2: 0},
}

// ParseSysReg resolves a system-register name to its (op0,op1,CRn,CRm,op2)
// tuple. It accepts:
//
//   - Architectural names from the namedSysRegs table (e.g. "sctlr_el1").
//   - The generic form `Sn_op1_Cm_Cn_op2` (case-insensitive) used by
//     GNU as to spell an unnamed implementation-defined register. The
//     spelling is `S<op0>_<op1>_C<CRn>_C<CRm>_<op2>`, e.g.
//     `s3_1_c11_c0_2` → (3, 1, 11, 0, 2).
//
// Returns ok=false if the name is neither recognised nor a valid
// generic spelling.
func ParseSysReg(name string) (SysReg, bool) {
	lc := strings.ToLower(name)
	if sr, ok := namedSysRegs[lc]; ok {
		return sr, true
	}
	return parseGenericSysReg(lc)
}

// sysRegNames is the reverse of namedSysRegs, built once on first use.
// Keyed by the (op0,op1,CRn,CRm,op2) tuple. namedSysRegs has no two
// names sharing a tuple (verified), so the reverse is unambiguous.
var sysRegNames map[SysReg]string

// SysRegName returns the architectural name for a (op0,op1,CRn,CRm,op2)
// tuple, or the generic `s<op0>_<op1>_c<CRn>_c<CRm>_<op2>` spelling
// (matching GNU as / objdump) when the tuple is unnamed. It is the
// reverse of ParseSysReg and shares its name table, so the assembler
// and disassembler cannot drift.
func SysRegName(sr SysReg) string {
	if sysRegNames == nil {
		sysRegNames = make(map[SysReg]string, len(namedSysRegs))
		for name, tup := range namedSysRegs {
			sysRegNames[tup] = name
		}
	}
	if name, ok := sysRegNames[sr]; ok {
		return name
	}
	return fmt.Sprintf("s%d_%d_c%d_c%d_%d", sr.Op0, sr.Op1, sr.CRn, sr.CRm, sr.Op2)
}

// pstateNames is the reverse of pstateFields, built once on first use.
var pstateNames map[PState]string

// PStateName returns the PSTATE field keyword for a (op1,op2) pair, or
// ("", false) if the pair names no known field. Reverse of ParsePState.
func PStateName(pf PState) (string, bool) {
	if pstateNames == nil {
		pstateNames = make(map[PState]string, len(pstateFields))
		for name, tup := range pstateFields {
			pstateNames[tup] = name
		}
	}
	name, ok := pstateNames[pf]
	return name, ok
}

// dcKey / tlbiKey index the SYS-instruction reverse tables by their
// (op1,CRn,CRm,op2) encoding.
type sysOpKey struct{ Op1, CRn, CRm, Op2 byte }

var (
	dcNames   map[sysOpKey]string
	tlbiNames map[sysOpKey]string
)

// DCName returns the DC op name for an (op1,CRn,CRm,op2) tuple, with a
// NeedsXt flag; ok=false if unrecognised. Reverse of ParseDC.
func DCName(op1, crn, crm, op2 byte) (name string, needsXt, ok bool) {
	if dcNames == nil {
		dcNames = make(map[sysOpKey]string, len(dcOps))
		for n, op := range dcOps {
			dcNames[sysOpKey{op.Op1, op.CRn, op.CRm, op.Op2}] = n
		}
	}
	n, found := dcNames[sysOpKey{op1, crn, crm, op2}]
	if !found {
		return "", false, false
	}
	op, _ := dcOps[n]
	return n, op.NeedsXt, true
}

// TLBIName returns the TLBI op name for an (op1,CRn,CRm,op2) tuple, with
// a NeedsXt flag; ok=false if unrecognised. Reverse of ParseTLBI.
func TLBIName(op1, crn, crm, op2 byte) (name string, needsXt, ok bool) {
	if tlbiNames == nil {
		tlbiNames = make(map[sysOpKey]string, len(tlbiOps))
		for n, op := range tlbiOps {
			tlbiNames[sysOpKey{op.Op1, op.CRn, op.CRm, op.Op2}] = n
		}
	}
	n, found := tlbiNames[sysOpKey{op1, crn, crm, op2}]
	if !found {
		return "", false, false
	}
	op, _ := tlbiOps[n]
	return n, op.NeedsXt, true
}

// parseGenericSysReg parses the `Sn_op1_Cm_Cn_op2` form. The leading
// `s` and the `c` prefixes on the CRn / CRm components are required;
// other separators are not accepted.
func parseGenericSysReg(name string) (SysReg, bool) {
	if len(name) < 2 || name[0] != 's' {
		return SysReg{}, false
	}
	parts := strings.Split(name[1:], "_")
	if len(parts) != 5 {
		return SysReg{}, false
	}
	op0, ok := parseSmallUint(parts[0], 3)
	if !ok {
		return SysReg{}, false
	}
	op1, ok := parseSmallUint(parts[1], 7)
	if !ok {
		return SysReg{}, false
	}
	if len(parts[2]) < 2 || (parts[2][0] != 'c') {
		return SysReg{}, false
	}
	crn, ok := parseSmallUint(parts[2][1:], 15)
	if !ok {
		return SysReg{}, false
	}
	if len(parts[3]) < 2 || (parts[3][0] != 'c') {
		return SysReg{}, false
	}
	crm, ok := parseSmallUint(parts[3][1:], 15)
	if !ok {
		return SysReg{}, false
	}
	op2, ok := parseSmallUint(parts[4], 7)
	if !ok {
		return SysReg{}, false
	}
	return SysReg{Op0: op0, Op1: op1, CRn: crn, CRm: crm, Op2: op2}, true
}

func parseSmallUint(s string, max byte) (byte, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 8)
	if err != nil || v > uint64(max) {
		return 0, false
	}
	return byte(v), true
}

// PState identifies a PSTATE field for the `msr <pstatefield>, #imm`
// form. The tuple (op1, op2) selects the field; the immediate becomes
// CRm in the encoding.
type PState struct {
	Op1 byte
	Op2 byte
}

// pstateFields maps the GNU as keyword to its (op1, op2) encoding.
// References: ARM ARM C5.2 (MSR (immediate)) Table C5-9.
var pstateFields = map[string]PState{
	"spsel":   {Op1: 0, Op2: 5},
	"daifset": {Op1: 3, Op2: 6},
	"daifclr": {Op1: 3, Op2: 7},
	"uao":     {Op1: 0, Op2: 3},
	"pan":     {Op1: 0, Op2: 4},
	"dit":     {Op1: 3, Op2: 2},
	"tco":     {Op1: 3, Op2: 4},
	"ssbs":    {Op1: 3, Op2: 1},
}

// ParsePState resolves a PSTATE field keyword to its (op1, op2)
// encoding. ok=false if the name is not a known field.
func ParsePState(name string) (PState, bool) {
	pf, ok := pstateFields[strings.ToLower(name)]
	return pf, ok
}

// DCOp identifies a data cache maintenance operation by its
// (op1, CRn, CRm, op2) encoding. op0 is fixed at 1 for the SYS
// instruction encoding.
type DCOp struct {
	Op1 byte
	CRn byte
	CRm byte
	Op2 byte
	// NeedsXt is true when the op takes an Xt register operand.
	// (All standard DC ops do; kept for symmetry with TLBI.)
	NeedsXt bool
}

// dcOps maps the canonical DC op name (lower case) to its encoding.
// References: ARM ARM C5.3.6 "DC" system instruction.
var dcOps = map[string]DCOp{
	// Operate by virtual address.
	"ivac":  {Op1: 0, CRn: 7, CRm: 6, Op2: 1, NeedsXt: true},
	"isw":   {Op1: 0, CRn: 7, CRm: 6, Op2: 2, NeedsXt: true},
	"csw":   {Op1: 0, CRn: 7, CRm: 10, Op2: 2, NeedsXt: true},
	"cisw":  {Op1: 0, CRn: 7, CRm: 14, Op2: 2, NeedsXt: true},
	"zva":   {Op1: 3, CRn: 7, CRm: 4, Op2: 1, NeedsXt: true},
	"cvac":  {Op1: 3, CRn: 7, CRm: 10, Op2: 1, NeedsXt: true},
	"cvau":  {Op1: 3, CRn: 7, CRm: 11, Op2: 1, NeedsXt: true},
	"civac": {Op1: 3, CRn: 7, CRm: 14, Op2: 1, NeedsXt: true},
	"cvap":  {Op1: 3, CRn: 7, CRm: 12, Op2: 1, NeedsXt: true},
}

// ParseDC resolves a DC operation name (e.g. "civac") to its encoding.
func ParseDC(name string) (DCOp, bool) {
	op, ok := dcOps[strings.ToLower(name)]
	return op, ok
}

// TLBIOp identifies a TLB-invalidate operation by its (op1, CRn, CRm,
// op2) encoding. op0 is fixed at 1 for the SYS instruction encoding.
// NeedsXt is true when the op takes an Xt register operand; some
// variants (e.g. VMALLE1, ALLE1, ALLE2, ALLE3) are register-free.
type TLBIOp struct {
	Op1 byte
	CRn byte
	CRm byte
	Op2 byte
	// NeedsXt is true when the op takes an Xt register operand.
	NeedsXt bool
}

// tlbiOps maps the canonical TLBI op name (lower case) to its
// encoding. References: ARM ARM C5.3.16 "TLBI" system instruction.
var tlbiOps = map[string]TLBIOp{
	// EL1, no Xt.
	"vmalle1is": {Op1: 0, CRn: 8, CRm: 3, Op2: 0},
	"vmalle1":   {Op1: 0, CRn: 8, CRm: 7, Op2: 0},
	"alle1is":   {Op1: 4, CRn: 8, CRm: 3, Op2: 4},
	"alle1":     {Op1: 4, CRn: 8, CRm: 7, Op2: 4},
	"alle2is":   {Op1: 4, CRn: 8, CRm: 3, Op2: 0},
	"alle2":     {Op1: 4, CRn: 8, CRm: 7, Op2: 0},
	"alle3is":   {Op1: 6, CRn: 8, CRm: 3, Op2: 0},
	"alle3":     {Op1: 6, CRn: 8, CRm: 7, Op2: 0},
	"vmalls12e1is": {Op1: 4, CRn: 8, CRm: 3, Op2: 6},
	"vmalls12e1":   {Op1: 4, CRn: 8, CRm: 7, Op2: 6},

	// EL1, with Xt.
	"vae1is":  {Op1: 0, CRn: 8, CRm: 3, Op2: 1, NeedsXt: true},
	"aside1is": {Op1: 0, CRn: 8, CRm: 3, Op2: 2, NeedsXt: true},
	"vaae1is": {Op1: 0, CRn: 8, CRm: 3, Op2: 3, NeedsXt: true},
	"vale1is": {Op1: 0, CRn: 8, CRm: 3, Op2: 5, NeedsXt: true},
	"vaale1is": {Op1: 0, CRn: 8, CRm: 3, Op2: 7, NeedsXt: true},
	"vae1":   {Op1: 0, CRn: 8, CRm: 7, Op2: 1, NeedsXt: true},
	"aside1": {Op1: 0, CRn: 8, CRm: 7, Op2: 2, NeedsXt: true},
	"vaae1":  {Op1: 0, CRn: 8, CRm: 7, Op2: 3, NeedsXt: true},
	"vale1":  {Op1: 0, CRn: 8, CRm: 7, Op2: 5, NeedsXt: true},
	"vaale1": {Op1: 0, CRn: 8, CRm: 7, Op2: 7, NeedsXt: true},

	// EL2 / EL3, with Xt.
	"ipas2e1is":  {Op1: 4, CRn: 8, CRm: 0, Op2: 1, NeedsXt: true},
	"ipas2le1is": {Op1: 4, CRn: 8, CRm: 0, Op2: 5, NeedsXt: true},
	"vae2is":     {Op1: 4, CRn: 8, CRm: 3, Op2: 1, NeedsXt: true},
	"vale2is":    {Op1: 4, CRn: 8, CRm: 3, Op2: 5, NeedsXt: true},
	"vae3is":     {Op1: 6, CRn: 8, CRm: 3, Op2: 1, NeedsXt: true},
	"vale3is":    {Op1: 6, CRn: 8, CRm: 3, Op2: 5, NeedsXt: true},
	"ipas2e1":    {Op1: 4, CRn: 8, CRm: 4, Op2: 1, NeedsXt: true},
	"ipas2le1":   {Op1: 4, CRn: 8, CRm: 4, Op2: 5, NeedsXt: true},
	"vae2":       {Op1: 4, CRn: 8, CRm: 7, Op2: 1, NeedsXt: true},
	"vale2":      {Op1: 4, CRn: 8, CRm: 7, Op2: 5, NeedsXt: true},
	"vae3":       {Op1: 6, CRn: 8, CRm: 7, Op2: 1, NeedsXt: true},
	"vale3":      {Op1: 6, CRn: 8, CRm: 7, Op2: 5, NeedsXt: true},
}

// ParseTLBI resolves a TLBI operation name to its encoding.
func ParseTLBI(name string) (TLBIOp, bool) {
	op, ok := tlbiOps[strings.ToLower(name)]
	return op, ok
}

// The accessors below expose the four System-group name→encoding maps so
// the table generator (tools/tables-gen) can project them into the Z80
// src/sysreg_tables.inc byte streams. They return defensive copies so a
// caller cannot mutate the package's authority maps.

// NamedSysRegs returns a copy of the architectural-name → SysReg map.
func NamedSysRegs() map[string]SysReg {
	out := make(map[string]SysReg, len(namedSysRegs))
	for k, v := range namedSysRegs {
		out[k] = v
	}
	return out
}

// PStateFields returns a copy of the PSTATE-field-name → PState map.
func PStateFields() map[string]PState {
	out := make(map[string]PState, len(pstateFields))
	for k, v := range pstateFields {
		out[k] = v
	}
	return out
}

// DCOps returns a copy of the DC-op-name → DCOp map.
func DCOps() map[string]DCOp {
	out := make(map[string]DCOp, len(dcOps))
	for k, v := range dcOps {
		out[k] = v
	}
	return out
}

// TLBIOps returns a copy of the TLBI-op-name → TLBIOp map.
func TLBIOps() map[string]TLBIOp {
	out := make(map[string]TLBIOp, len(tlbiOps))
	for k, v := range tlbiOps {
		out[k] = v
	}
	return out
}
