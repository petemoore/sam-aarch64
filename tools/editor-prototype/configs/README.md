# editor-prototype rendering-lab configs

Starter config files for the configurable rendering lab (`editor-prototype
-config FILE`). Each is a hand-editable `key = value` file (`#` comments, zero
deps) where every key is one rendering feature; the leading comments document
every key inline. Copy one, edit, relaunch — or snapshot a live lab session
(`S`) to a new `lab-snapshot-N.config` here.

- `binutils-baseline.config` — R0, every feature off (reads like `objdump`).
- `compressed.config` — the iteration-2 compressed-rendering recommendation.
- `comet-minimal.config` — a chromeless, semicolon-comment homage.
- `dreamer.config` — `relax_palette` demo (off-SAM colours; no SAM PNG).

Lab usage and the full key reference: `../README.md`. Design authority:
`docs/specs/editor-tui-prototype-design.md`.
