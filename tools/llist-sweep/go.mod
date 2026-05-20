module github.com/petemoore/sam-aarch64/tools/llist-sweep

go 1.22

require (
	github.com/petemoore/sam-aarch64/tools/llist-capture v0.0.0-00010101000000-000000000000
	github.com/petemoore/samfile/v3 v3.0.1-0.20260510102753-c36d084d3c11
)

replace github.com/petemoore/samfile/v3 => /Users/pmoore/git/samfile

replace github.com/petemoore/sam-aarch64/tools/llist-capture => ../llist-capture
