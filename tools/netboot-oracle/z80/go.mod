module github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80

go 1.26.1

require (
	github.com/koron-go/z80 v0.10.2
	github.com/petemoore/sam-aarch64/tools/netboot-oracle v0.0.0
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0
	github.com/petemoore/sam-aarch64/tools/sampage v0.0.0
)

require github.com/petemoore/sam-aarch64/tools/aarch64enc v0.0.0-00010101000000-000000000000 // indirect

// The Go authority (frame/dhcp/tftp/golden) lives in the parent module; the
// Z80 harness/tests are a nested module so the parent's pure-Go CI job does not
// pull in the koron-go/z80 dependency.
replace github.com/petemoore/sam-aarch64/tools/netboot-oracle => ..

// sam-aarch64-format is the authority for the i48c parser tables (the mnemonic
// name→id map). It is a pure-stdlib leaf module, so the asmparse harness test
// imports it directly to compare the Z80 lookup against format.MnemonicID —
// unlike the heavyweight frontend package, which asmlex_test.go transcribes.
replace github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../../sam-aarch64-format

// i48c-b8a: the Pass1-over-IR test verifies against the real authority
// (frontend.Translate + assemble.Pass1), so it imports the parent
// tools/sam-aarch64 module and its encoder deps directly.
require github.com/petemoore/sam-aarch64/tools/sam-aarch64 v0.0.0

replace github.com/petemoore/sam-aarch64/tools/sam-aarch64 => ../../sam-aarch64

replace github.com/petemoore/sam-aarch64/tools/aarch64enc => ../../aarch64enc

replace github.com/petemoore/sam-aarch64/tools/aarch64dec => ../../aarch64dec

replace github.com/petemoore/sam-aarch64/tools/sampage => ../../sampage
