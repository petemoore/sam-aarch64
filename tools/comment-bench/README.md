# `tools/comment-bench` — comment-corpus compression benchmark

Benchmarks Z80-feasible compression schemes against the real unstripped
spectrum4 release comment corpus (editor-region sidecar, ~291 KB body text,
~7,012 comments extracted from the compact `.tbn` via the format library).

## Running

```
make comment-bench           # builds sam-aarch64, the .tbn, and the bench
make release-unstripped-tbn  # rebuild the input .tbn only
```

## Schemes

Static word dictionary (N=128/256/512/1024), digram/BPE (256 entries),
canonical Huffman, dictionary+Huffman hybrid, greedy LZ77 with ZX0-style
Elias-gamma costs (labelled "greedy stand-in"), and flate level-9 as a
not-Z80-feasible ceiling.  All compressed sizes include table/dictionary
overhead.  See `docs/notes/comment-compression-research.md` for results,
decoder-cost table, and capacity analysis.
