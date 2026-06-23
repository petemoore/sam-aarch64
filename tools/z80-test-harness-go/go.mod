module github.com/petemoore/sam-aarch64/tools/z80-test-harness-go

go 1.26.1

require (
	github.com/koron-go/z80 v0.10.2
	github.com/petemoore/sam-aarch64/tools/aarch64dec v0.0.0
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000
	github.com/petemoore/sam-aarch64/tools/sampage v0.0.0
	github.com/petemoore/sam-aarch64/tools/zx0-greedy v0.0.0
)

require github.com/petemoore/sam-aarch64/tools/aarch64enc v0.0.0-00010101000000-000000000000 // indirect

replace github.com/petemoore/sam-aarch64/tools/aarch64dec => ../aarch64dec

replace github.com/petemoore/sam-aarch64/tools/zx0-greedy => ../zx0-greedy

// Transitive replaces: aarch64dec imports aarch64enc + sam-aarch64-format,
// both local modules (see tools/aarch64dec/go.mod).
replace github.com/petemoore/sam-aarch64/tools/aarch64enc => ../aarch64enc

replace github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format

replace github.com/petemoore/sam-aarch64/tools/sampage => ../sampage
