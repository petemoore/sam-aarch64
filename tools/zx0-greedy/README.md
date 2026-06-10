# `tools/zx0-greedy` — Z80-feasible greedy ZX0-format compressor

Go authority implementation of a greedy ZX0-format compressor. The data
structures (hash table + chain array, 16-bit indices, flat arrays) are chosen
so this code is a direct port spec for the forthcoming Z80 assembler (i60b-2).
Output is accepted by the unmodified upstream `dzx0_standard` / `dzx0_turbo`
Z80 decoders. Oracle validation runs in `tools/z80-test-harness-go`.

```go
import "github.com/petemoore/sam-aarch64/tools/zx0-greedy"

out := zx0greedy.Compress(data, zx0greedy.DefaultParams)
```
