module github.com/petemoore/sam-aarch64/tools/bin2text

go 1.26.1

require github.com/petemoore/sam-aarch64/tools/sam-aarch64 v0.0.0-00010101000000-000000000000

require (
	github.com/petemoore/sam-aarch64/tools/aarch64dec v0.0.0-00010101000000-000000000000 // indirect
	github.com/petemoore/sam-aarch64/tools/aarch64enc v0.0.0-00010101000000-000000000000 // indirect
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format v0.0.0-00010101000000-000000000000 // indirect
)

replace (
	github.com/petemoore/sam-aarch64/tools/aarch64dec => ../aarch64dec
	github.com/petemoore/sam-aarch64/tools/aarch64enc => ../aarch64enc
	github.com/petemoore/sam-aarch64/tools/sam-aarch64 => ../sam-aarch64
	github.com/petemoore/sam-aarch64/tools/sam-aarch64-format => ../sam-aarch64-format
)
