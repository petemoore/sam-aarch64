module github.com/petemoore/sam-aarch64/tools/editor-prototype

go 1.26.1

replace (
	github.com/petemoore/sam-aarch64/tools/aarch64dec => ../aarch64dec
	github.com/petemoore/sam-aarch64/tools/aarch64enc => ../aarch64enc
	github.com/petemoore/sam-aarch64/tools/sam-aarch64 => ../sam-aarch64
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format
)

require (
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/petemoore/sam-aarch64/tools/sam-aarch64 v0.0.0-00010101000000-000000000000
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000
	github.com/petemoore/samfile/v3 v3.0.1-0.20260510102753-c36d084d3c11
)

require (
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/petemoore/sam-aarch64/tools/aarch64dec v0.0.0-00010101000000-000000000000 // indirect
	github.com/petemoore/sam-aarch64/tools/aarch64enc v0.0.0-00010101000000-000000000000 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/term v0.37.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)
