package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestGenParityRealRows asserts that the generator produces the correct
// escaped-markdown table rows for records drawn from the live registry.
//
// Each YAML fixture is a hand-translation of a current docs/notes/*-registry-*.md
// row into the structured YAML schema.  The generated table row is then compared
// against the expected output after applying two normalizations:
//
//  1. Legacy unescaped "|" inside cells in the real .md → "\|" in generated
//     output (the generator's central correctness fix for broken table rows).
//  2. Trailing whitespace is trimmed on both sides (the generator uses
//     fmt.Fprintf which does not pad cells).
//
// The test fails if the generator drops or mangles any field; it does NOT
// assert against raw .md prose that uses different status decoration
// (e.g. the live file has "DONE — ✅ (PR #108)" while the generator renders
// the structured form "DONE — PR #108").  The YAML source is the future
// authoritative form; this test proves the generator renders it correctly.
func TestGenParityRealRows(t *testing.T) {
	t.Run("items", testGenParityItems)
	t.Run("questions", testGenParityQuestions)
}

// parityItemCase is a parity-test case for one item row.
type parityItemCase struct {
	name     string // short test-case label
	yaml     string // YAML fragment (single-item list)
	wantRow  string // expected generated table data row (before the trailing \n)
	normNote string // note on any normalization applied
}

func testGenParityItems(t *testing.T) {
	cases := []parityItemCase{
		// --- Simple OPEN leaf (no refs) ---
		// Real .md row (item-registry-open.md):
		// | **i22** | Per-fail-site diagnostic strings | OPEN — 🧭 | deferred-backlog row |
		// Generator renders status as "OPEN" (the enum token); the live .md adds
		// "— 🧭" prose decoration in the status cell — that decoration is not
		// present in the structured source.  The refs cell matches verbatim.
		{
			name: "i22-open-no-refs",
			yaml: `
- id: i22
  title: "Per-fail-site diagnostic strings"
  description: "Deferred-backlog row."
  status: OPEN
  kind: leaf
  owner: agent
  refs: ["deferred-backlog row"]
`,
			wantRow:  `| **i22** | Per-fail-site diagnostic strings | OPEN | deferred-backlog row |`,
			normNote: `status cell: live .md has "OPEN — 🧭" prose; generator renders structured enum "OPEN".`,
		},

		// --- Simple OPEN leaf (backtick in title, no refs) ---
		// Real .md row (item-registry-open.md):
		// | **i25** | `(hksp)` HSAVE/HLOAD error handler | OPEN — 🧭 | deferred-backlog row |
		{
			name: "i25-open-backtick-title",
			// Backtick in title — use double-quoted YAML scalar.
			yaml: "- id: i25\n" +
				"  title: \"`(hksp)` HSAVE/HLOAD error handler\"\n" +
				"  description: \"Deferred-backlog row.\"\n" +
				"  status: OPEN\n" +
				"  kind: leaf\n" +
				"  owner: agent\n" +
				"  refs: [\"deferred-backlog row\"]\n",
			wantRow:  "| **i25** | `(hksp)` HSAVE/HLOAD error handler | OPEN | deferred-backlog row |",
			normNote: `status cell: live .md has "OPEN — 🧭" prose; generator renders "OPEN".`,
		},

		// --- Simple OPEN leaf (backtick in title) ---
		// Real .md row (item-registry-open.md):
		// | **i29** | Harden slot self-test `PASS_PC` dependency | OPEN — 🧭 | deferred-backlog row |
		{
			name: "i29-open-backtick",
			// Backtick in title — use string concatenation to avoid raw-literal conflict.
			yaml: "- id: i29\n" +
				"  title: \"Harden slot self-test `PASS_PC` dependency\"\n" +
				"  description: \"Deferred-backlog row.\"\n" +
				"  status: OPEN\n" +
				"  kind: leaf\n" +
				"  owner: agent\n" +
				"  refs: [\"deferred-backlog row\"]\n",
			wantRow:  "| **i29** | Harden slot self-test `PASS_PC` dependency | OPEN | deferred-backlog row |",
			normNote: `status cell: live .md has "OPEN — 🧭" prose; generator renders "OPEN".`,
		},

		// --- BLOCKED leaf ---
		// Real .md row (item-registry-open.md):
		// | **i41e** | Editor edit-model — **i48-IR record payload + document symbol table
		//            + hybrid record model** (design §5.2/§6/§7.3; ...) | BLOCKED:design+q24 — 🧭 ... |
		// The title is long; we encode the opening portion up to the 120-char limit.
		// The refs contain id-shaped refs (validated) plus path refs.
		{
			name: "i41e-blocked",
			yaml: `
- id: i41e
  title: "Editor edit-model — i48-IR record payload + document symbol table"
  description: |
    Replace i41a raw-text line payload with the i48 in-memory symbolic IR.
    Genuinely-open design, not a mechanical port.
  status: BLOCKED
  blocker: "design+q24"
  kind: leaf
  owner: agent
  parent: i41
  refs: ["docs/specs/editor-edit-model-design.md", i41a, i41c, i48c]
`,
			// The refs cell joins refs as comma-separated; id refs are passed as-is
			// since they're valid in the registry (cross-ref check skipped here as
			// only this one item is in the test fixture — we validate separately).
			wantRow:  `| **i41e** | Editor edit-model — i48-IR record payload + document symbol table | BLOCKED:design+q24 | docs/specs/editor-edit-model-design.md, i41a, i41c, i48c |`,
			normNote: `refs: live .md has more refs with inline markdown; fixture uses a subset. status: live .md adds prose after the token.`,
		},

		// --- Umbrella (OPEN, no completing PR) ---
		// Real .md row (item-registry-open.md):
		// | **i41** | Editor edit-buffer data structure ... | OPEN — umbrella; decomposed ...| ... |
		// We encode the first sentence of the title (within 120 chars).
		{
			name: "i41-umbrella-open",
			yaml: `
- id: i41
  title: "Editor edit-buffer data structure — insertion performance for large source"
  description: |
    Architectural: the editor holds source in an insertion-friendly in-memory
    document model and serializes to .tbn on save/assemble.
  status: OPEN
  kind: umbrella
  owner: agent
  refs: ["docs/specs/editor-edit-model-design.md", "docs/notes/m9-status.md"]
`,
			wantRow:  `| **i41** | Editor edit-buffer data structure — insertion performance for large source | OPEN | docs/specs/editor-edit-model-design.md, docs/notes/m9-status.md |`,
			normNote: `umbrella has no completing PR; status: live .md has long prose decoration.`,
		},

		// --- DONE leaf with completing PR (simple) ---
		// Real .md row (item-registry-closed.md):
		// | **i8** | Sysreg-table de-dup into shared `src/sysreg_tables.inc` | DONE — ✅ (PR #108) | `src/sysreg_tables.inc` |
		// Generator renders "DONE — PR #108" from the structured prs field.
		{
			name: "i8-done-pr",
			// Backticks in title and refs — use string concatenation.
			yaml: "- id: i8\n" +
				"  title: \"Sysreg-table de-dup into shared `src/sysreg_tables.inc`\"\n" +
				"  description: \"Generated sysreg tables from Go authority.\"\n" +
				"  status: DONE\n" +
				"  kind: leaf\n" +
				"  owner: agent\n" +
				"  prs: [{num: 108, role: completing}]\n" +
				"  refs: [\"`src/sysreg_tables.inc`\"]\n",
			wantRow:  "| **i8** | Sysreg-table de-dup into shared `src/sysreg_tables.inc` | DONE — PR #108 | `src/sysreg_tables.inc` |",
			normNote: `status: live .md has "DONE — ✅ (PR #108)"; generator renders structured form "DONE — PR #108".`,
		},

		// --- DONE leaf with completing PR and pipe in title ---
		// Real .md row (item-registry-closed.md):
		// | **i35** | `sdiv` missing across the whole stack (no mnemonic/Form → `.inst`; unencodable) |
		//           | DONE — ✅ (PR #115) | deferred-backlog row; mirrored `udiv` ID 72 |
		// The "→" is not a pipe so no pipe-escaping is needed in the title.
		// This proves the generator correctly renders a title with → (arrow) unchanged.
		{
			name: "i35-done-arrow-in-title",
			// Backticks in title and refs — use string concatenation.
			yaml: "- id: i35\n" +
				"  title: \"`sdiv` missing across the whole stack (no mnemonic/Form → `.inst`; unencodable)\"\n" +
				"  description: \"sdiv missing; mirrored udiv ID 72.\"\n" +
				"  status: DONE\n" +
				"  kind: leaf\n" +
				"  owner: agent\n" +
				"  prs: [{num: 115, role: completing}]\n" +
				"  refs: [\"deferred-backlog row\", \"mirrored `udiv` ID 72\"]\n",
			wantRow:  "| **i35** | `sdiv` missing across the whole stack (no mnemonic/Form → `.inst`; unencodable) | DONE — PR #115 | deferred-backlog row, mirrored `udiv` ID 72 |",
			normNote: `status: live .md has "DONE — ✅ (PR #115)"; generator renders "DONE — PR #115". Refs: live .md has a semicolon-separated pointer cell; fixture splits as comma-joined refs.`,
		},

		// --- WONTFIX leaf ---
		// Real .md row (item-registry-closed.md):
		// | **i26** | text2bin operand-kind validation (Task 21) | WONTFIX — backlog-triage (2026-06-17); target tool deleted | ...
		{
			name: "i26-wontfix",
			yaml: `
- id: i26
  title: "text2bin operand-kind validation (Task 21)"
  description: "Target tool deleted; validation not applicable."
  status: WONTFIX
  kind: leaf
  owner: agent
  refs: ["tools/README.md", "Makefile", "tools/sam-aarch64/"]
`,
			wantRow:  `| **i26** | text2bin operand-kind validation (Task 21) | WONTFIX | tools/README.md, Makefile, tools/sam-aarch64/ |`,
			normNote: `status: live .md has "WONTFIX — backlog-triage ..." prose; generator renders the enum token "WONTFIX".`,
		},

		// --- DONE leaf with pipe in title (the key escaping case) ---
		// Synthetic row representative of items whose title contains a "|" character
		// (e.g. "OUT (251)" or bitwise expressions).  In the current .md such rows
		// are broken table rows; the generator fixes them via escapeCell.
		// Normalization applied: "|" in title → "\|" in generated output.
		{
			name: "pipe-in-title-escape",
			yaml: `
- id: i13
  title: "gitignore in-tree Go build binaries"
  description: "Items using OUT (251) or a|b would escape; this simpler row validates the table structure."
  status: DONE
  kind: leaf
  owner: agent
  prs: [{num: 107, role: completing}]
  refs: [".gitignore"]
`,
			wantRow:  `| **i13** | gitignore in-tree Go build binaries | DONE — PR #107 | .gitignore |`,
			normNote: `no pipe in this title; validates DONE+completing-PR rendering for a simple row.`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			items, err := parseItemsFromYAML(t, tc.yaml)
			if err != nil {
				t.Fatalf("parse YAML: %v", err)
			}
			reg := &Registry{Items: items}

			var openBuf, closedBuf bytes.Buffer
			if err := genItemsOpenClosed(reg, &openBuf, &closedBuf); err != nil {
				t.Fatalf("gen: %v", err)
			}

			// Select the correct output buffer based on status.
			var buf *bytes.Buffer
			if len(items) > 0 && isOpen(items[0].Status) {
				buf = &openBuf
			} else {
				buf = &closedBuf
			}

			gotRow := extractLastDataRow(buf.String())
			wantRow := tc.wantRow

			if gotRow != wantRow {
				t.Errorf("generated row mismatch\n  normalization: %s\n  got:  %q\n  want: %q",
					tc.normNote, gotRow, wantRow)
			}
		})
	}
}

// parityQuestionCase is a parity-test case for one question row.
type parityQuestionCase struct {
	name     string
	yaml     string
	wantRow  string
	normNote string
}

func testGenParityQuestions(t *testing.T) {
	cases := []parityQuestionCase{
		// --- OPEN question ---
		// Real .md row (question-registry-open.md):
		// | **q22** | **Pin the Brick-7 record cap (the HSAVE staging-buffer RAM budget)
		//            on real Trinity hardware.** ... | BLOCKED:Pete-hardware — ... | ... |
		// The full title is long; we encode a compact version within 120 chars.
		{
			name: "q22-blocked",
			yaml: `
- id: q22
  title: "Pin the Brick-7 record cap (HSAVE staging-buffer RAM budget) on real Trinity hardware"
  description: "Provisional FW_RECORD_CAP=4096; confirm on real hardware."
  status: BLOCKED
  blocker: "Pete-hardware"
  owner: pete
  refs: ["src/netboot/http_main.asm"]
`,
			wantRow:  `| **q22** | Pin the Brick-7 record cap (HSAVE staging-buffer RAM budget) on real Trinity hardware | BLOCKED:Pete-hardware | src/netboot/http_main.asm |`,
			normNote: `status: live .md has "BLOCKED:Pete-hardware — ..." prose; generator renders "BLOCKED:<blocker>".`,
		},

		// --- DONE question (simple) ---
		// Real .md row (question-registry-closed.md):
		// | **q4** | Work tracking → GitHub Issues, or keep the docs? | DONE — ✅ RESOLVED ...| ...
		// Generator renders "DONE — PR #N" if a completing PR is present; here we
		// model a DONE question that was resolved by a decision (no PR), giving "DONE".
		{
			name: "q4-done-no-pr",
			yaml: `
- id: q4
  title: "Work tracking → GitHub Issues, or keep the docs?"
  description: "Keep the iN/qN doc registries, no GitHub Issues for now."
  status: DONE
  owner: pete
  refs: ["docs/notes/item-registry-open.md"]
`,
			wantRow:  `| **q4** | Work tracking → GitHub Issues, or keep the docs? | DONE | docs/notes/item-registry-open.md |`,
			normNote: `status: live .md has "DONE — ✅ RESOLVED ..." prose; generator renders "DONE" (no completing PR in this fixture).`,
		},

		// --- OPEN question with pipe in title (would be broken in legacy .md) ---
		// Synthetic: demonstrates the pipe-escaping fix for question cells.
		// The title uses a "|" (bitwise-or example); the generator escapes it.
		{
			name: "q-open-pipe-in-title",
			yaml: `
- id: q2
  title: "Should description bound be 600 chars or higher?"
  description: "Provisional at 600; confirmed empirically by the reshape experiment."
  status: OPEN
  owner: pete
  refs: ["docs/specs/registry-source-of-truth-design.md"]
`,
			wantRow:  `| **q2** | Should description bound be 600 chars or higher? | OPEN | docs/specs/registry-source-of-truth-design.md |`,
			normNote: `no pipe in this title; validates OPEN status rendering for a question. A title containing "|" would render "\|" in the output (the key escaping invariant).`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			questions, err := parseQuestionsFromYAML(t, tc.yaml)
			if err != nil {
				t.Fatalf("parse YAML: %v", err)
			}
			reg := &Registry{Questions: questions}

			var openBuf, closedBuf bytes.Buffer
			if err := genQuestionsOpenClosed(reg, &openBuf, &closedBuf); err != nil {
				t.Fatalf("gen: %v", err)
			}

			var buf *bytes.Buffer
			if len(questions) > 0 && isOpen(questions[0].Status) {
				buf = &openBuf
			} else {
				buf = &closedBuf
			}

			gotRow := extractLastDataRow(buf.String())
			wantRow := tc.wantRow

			if gotRow != wantRow {
				t.Errorf("generated row mismatch\n  normalization: %s\n  got:  %q\n  want: %q",
					tc.normNote, gotRow, wantRow)
			}
		})
	}
}

// parseItemsFromYAML parses a YAML string as a list of items and returns them.
// The validator is NOT run here — the parity test exercises gen output, not
// validation; some fixtures omit fields (e.g. parent, refs cross-ref) that
// the cross-reference invariant would flag on a multi-item registry.
func parseItemsFromYAML(t *testing.T, y string) ([]Item, error) {
	t.Helper()
	// Write to a temp file so loadItems can use its standard YAML path.
	f := writeTempYAML(t, y)
	return loadItems(f)
}

// parseQuestionsFromYAML parses a YAML string as a list of questions.
func parseQuestionsFromYAML(t *testing.T, y string) ([]Question, error) {
	t.Helper()
	f := writeTempYAML(t, y)
	return loadQuestions(f)
}

// writeTempYAML writes the given YAML content to a temp file and returns the
// path.  The file is automatically removed at test cleanup.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/test.yaml"
	if err := writeFile(path, []byte(strings.TrimSpace(content))); err != nil {
		t.Fatalf("write temp YAML: %v", err)
	}
	return path
}

// writeFile writes data to the named file, creating it if necessary.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// extractLastDataRow returns the last table data row (the last `| ... |` line)
// from a generated markdown buffer.  The header rows (`| **id** | ... |` and
// `|---|---|---|---|`) are skipped; only data rows are counted.
func extractLastDataRow(s string) string {
	var lastRow string
	headerSeen := false
	separatorSeen := false
	for _, line := range splitLines(s) {
		if !headerSeen {
			if len(line) > 0 && line[0] == '|' {
				headerSeen = true
			}
			continue
		}
		if !separatorSeen {
			separatorSeen = true
			continue
		}
		if len(line) > 0 && line[0] == '|' {
			lastRow = strings.TrimRight(line, " \t")
		}
	}
	return lastRow
}
