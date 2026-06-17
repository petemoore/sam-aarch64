package editmodel

// tbn.go implements the real v2 .tbn serialize/load seam (item i41c). It
// composes the existing frontend, assemble, and render packages — which are
// already used by the CLI and share the same Go module — to give the editor a
// save/load path that speaks the project's native storage format.
//
// The EMDL Serialize/Load pair in serialize.go is untouched; it remains the
// internal exact-round-trip structural seam that the i41a/i41b property tests
// depend on.

import (
	"bytes"
	"fmt"
	"io"

	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
	"github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// SerializeTBN writes the document as a v2 compact .tbn byte stream to w.
//
// The save path mirrors the CLI: join lines → frontend.Translate →
// assemble.Pass1 → assemble.CompactTBNBytes → write. Requiring a complete,
// valid assembly is deliberate: the encoder resolves all symbols and computes
// all PCs, so a document with invalid or partial lines fails loud. The editor
// surfaces the assembly error to the user (design §7.3 "won't serialize until
// valid"). Incremental/partial-document serialize is the larger i41e IR-payload
// work, gated on an open design question.
func (d *Document) SerializeTBN(w io.Writer) error {
	// Build a single source buffer: each line's text followed by '\n'.
	var src bytes.Buffer
	n := d.LineCount()
	for i := 0; i < n; i++ {
		_, text := d.LineAt(i)
		src.Write(text)
		src.WriteByte('\n')
	}

	f, err := frontend.Translate(src.Bytes(), "<editmodel>")
	if err != nil {
		return fmt.Errorf("editmodel: SerializeTBN: %w", err)
	}

	p1, err := assemble.Pass1(f)
	if err != nil {
		return fmt.Errorf("editmodel: SerializeTBN: %w", err)
	}

	b, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		return fmt.Errorf("editmodel: SerializeTBN: %w", err)
	}

	_, err = w.Write(b)
	return err
}

// LoadTBN reads a v2 compact .tbn stream from r and returns a new Document
// whose lines carry the canonical rendered text for each record.
//
// Loaded lines carry the canonical rendered text (display = detokenize, design
// §7.3) — keystroke-exact spacing is not preserved; re-rendering from the
// format IS canonical formatting. Lines receive fresh RecordIDs: ids are an
// edit-session concept not stored in the .tbn, so the loaded document starts
// a new session with ids beginning at 1.
func LoadTBN(r io.Reader) (*Document, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("editmodel: LoadTBN: reading input: %w", err)
	}

	lines, err := render.EmitLinesFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("editmodel: LoadTBN: %w", err)
	}

	d := New()
	for _, l := range lines {
		d.InsertLine(d.LineCount(), []byte(l.Text))
	}
	return d, nil
}
