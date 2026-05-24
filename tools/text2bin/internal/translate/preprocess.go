package translate

// Preprocess is a GNU-as–style text-substitution pass that runs in front of
// Lex. It handles the subset of GNU `as` directives that spectrum4 actually
// uses (see docs/superpowers/specs/2026-05-25-macro-expansion-research.md):
//
//   • .include "file.s"           — inline another file, searched along an
//                                   include path (including-file's dir plus
//                                   user-supplied -I dirs).
//   • .macro NAME a, b, c         — define a macro with positional named args.
//     <body>
//     .endm
//   • <name> arg1, arg2, ...      — invoke a previously-defined macro; \arg
//                                   substitutions are performed on the body.
//                                   Substitution is purely textual and works
//                                   inside identifiers (token-paste) and
//                                   inside string literals.
//   • .if SYMBOL / .else / .endif — conditional assembly. SYMBOL must have
//                                   been defined by a previous .set; the
//                                   condition is truthy if its integer value
//                                   is non-zero. .if SYMBOL is the only
//                                   condition form supported. Nested .ifs
//                                   work.
//   • .set NAME, INT              — record an integer constant. Only literal
//                                   integers (with the usual 0x/0b/'/decimal
//                                   syntax) are evaluated here; the directive
//                                   text is also passed through so the parser
//                                   sees the original .set.
//
// Position tracking. The preprocessor emits cpp-style line directives
//
//     # <line> "<file>"
//
// at every file boundary and after every macro expansion. The Lex pass
// recognises this syntax at the start of a line (see lexer.go) and updates
// its line/file accordingly, so parse errors still point at the original
// source.
//
// Out of scope (no spectrum4 callers): \@ uniquifier, .rept/.irp/.endr,
// .altmacro, .purgem, .exitm, .ifdef/.ifndef/.ifc, macro default values,
// :req / :vararg, .if <expression>.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PreprocessOptions configures Preprocess. IncludeDirs is the list of
// directories searched for .include "file" (in addition to the directory of
// the including file, which is always searched first).
type PreprocessOptions struct {
	IncludeDirs []string
}

// Preprocess runs the macro / include / conditional-assembly pass on src and
// returns the expanded source bytes, with embedded `# line "file"` markers so
// the lexer can map positions back to the original source.
func Preprocess(src []byte, path string, opts PreprocessOptions) ([]byte, error) {
	pp := &preprocessor{
		macros:    map[string]*macroDef{},
		setvars:   map[string]int64{},
		incdirs:   opts.IncludeDirs,
		expanding: map[string]bool{},
	}
	var out strings.Builder
	if err := pp.processFile(src, path, &out); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

type macroDef struct {
	name   string
	params []string
	body   []string // raw body lines, NOT including .macro/.endm
	defPos preprocPos
}

type preprocPos struct {
	file string
	line int
}

func (p preprocPos) String() string { return fmt.Sprintf("%s:%d", p.file, p.line) }

type preprocessor struct {
	macros    map[string]*macroDef
	setvars   map[string]int64
	incdirs   []string
	expanding map[string]bool
}

// processFile preprocesses the bytes of src (the contents of `path`) into out.
// It emits a leading line directive so subsequent parse errors are attributed
// to `path`.
func (pp *preprocessor) processFile(src []byte, path string, out *strings.Builder) error {
	lines := splitLines(src)
	emitLineDirective(out, path, 1)
	return pp.processLines(lines, path, 1, out, ifFrame{active: true})
}

// processLines processes a sequence of source lines that all belong to
// `path`, starting at source line `startLine`. The lines slice contains
// physical lines (no trailing newline); the function emits expanded text plus
// line directives so the lexer can keep positions accurate.
//
// frame is the current conditional-assembly context: if frame.active is false
// the lines are scanned for nested .if/.endif but no output is produced.
func (pp *preprocessor) processLines(lines []string, path string, startLine int, out *strings.Builder, frame ifFrame) error {
	// Conditional-assembly stack. Each frame says whether we are currently
	// emitting (active) and whether any prior branch in this .if/.elseif
	// chain has been taken (taken — relevant for .else).
	stack := []ifFrame{frame}
	active := func() bool {
		for _, f := range stack {
			if !f.active {
				return false
			}
		}
		return true
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		srcLine := startLine + i
		pos := preprocPos{file: path, line: srcLine}
		trimmed := strings.TrimLeft(line, " \t")

		switch {
		case strings.HasPrefix(trimmed, ".if "), trimmed == ".if", strings.HasPrefix(trimmed, ".if\t"):
			// Push a new frame. If our parent is inactive, the new frame is
			// inactive too (we still track depth so .endif matches up).
			if !active() {
				stack = append(stack, ifFrame{active: false, taken: true, hadElse: false})
				continue
			}
			cond, err := pp.evalIfCondition(trimmed, pos)
			if err != nil {
				return err
			}
			stack = append(stack, ifFrame{active: cond, taken: cond, hadElse: false})

		case trimmed == ".else" || strings.HasPrefix(trimmed, ".else "):
			if len(stack) < 2 {
				return fmt.Errorf("%s: .else outside .if", pos)
			}
			top := &stack[len(stack)-1]
			if top.hadElse {
				return fmt.Errorf("%s: duplicate .else", pos)
			}
			top.hadElse = true
			// Only flip active iff the parent context is active.
			parentActive := true
			for j := 0; j < len(stack)-1; j++ {
				if !stack[j].active {
					parentActive = false
					break
				}
			}
			if parentActive && !top.taken {
				top.active = true
				top.taken = true
			} else {
				top.active = false
			}

		case trimmed == ".endif" || strings.HasPrefix(trimmed, ".endif "):
			if len(stack) < 2 {
				return fmt.Errorf("%s: .endif outside .if", pos)
			}
			stack = stack[:len(stack)-1]

		case strings.HasPrefix(trimmed, ".macro ") || strings.HasPrefix(trimmed, ".macro\t"):
			// Collect the macro body up to the next .endm. Macro definitions
			// inside an inactive .if are skipped entirely (parser would never
			// see them anyway).
			body, consumed, err := collectMacroBody(lines[i+1:], path, srcLine+1)
			if err != nil {
				return err
			}
			if active() {
				m, err := parseMacroHeader(trimmed, pos)
				if err != nil {
					return err
				}
				m.body = body
				m.defPos = pos
				pp.macros[m.name] = m
			}
			i += consumed // skip the body and the .endm line

		case trimmed == ".endm" || strings.HasPrefix(trimmed, ".endm "):
			return fmt.Errorf("%s: .endm outside .macro", pos)

		default:
			if !active() {
				continue
			}
			// .set NAME, INT — capture into setvars (best-effort: only
			// literal integers; non-literal .set values still pass through
			// to the parser).
			if name, val, ok := tryParseSet(trimmed); ok {
				pp.setvars[name] = val
				// Fall through: also emit the line so the parser sees it.
			}
			// .include "file"
			if path2, ok := tryParseInclude(trimmed); ok {
				if err := pp.handleInclude(path2, path, pos, out); err != nil {
					return err
				}
				// After the include's content, restore line directive for
				// the current file so subsequent lines have correct attrib.
				emitLineDirective(out, path, srcLine+1)
				continue
			}
			// Macro invocation? Identify the first word after optional
			// whitespace; if it matches a known macro name, expand.
			if ok, err := pp.tryExpandMacroInvocation(line, pos, out); err != nil {
				return err
			} else if ok {
				continue
			}
			// Plain pass-through.
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if len(stack) > 1 {
		return fmt.Errorf("%s: unterminated .if (depth=%d)", preprocPos{file: path, line: startLine + len(lines)}, len(stack)-1)
	}
	return nil
}

type ifFrame struct {
	active  bool // emitting lines at the moment?
	taken   bool // has any branch in this .if/.elseif chain emitted?
	hadElse bool
}

// splitLines splits src on '\n'. The trailing newline (if any) does not
// produce a final empty element — matching the loop in processLines.
func splitLines(src []byte) []string {
	s := string(src)
	if s == "" {
		return nil
	}
	// Normalise CRLF → LF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	out := strings.Split(s, "\n")
	// strings.Split("a\n", "\n") -> ["a", ""]; drop the empty tail.
	if n := len(out); n > 0 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

func emitLineDirective(out *strings.Builder, path string, line int) {
	out.WriteString(fmt.Sprintf("# %d \"%s\"\n", line, path))
}

// parseMacroHeader parses ".macro NAME a, b, c" into a macroDef (without body
// yet). Whitespace and an optional comma between NAME and the first arg are
// tolerated, matching GNU as.
func parseMacroHeader(line string, pos preprocPos) (*macroDef, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, ".macro"))
	if rest == "" {
		return nil, fmt.Errorf("%s: .macro: missing name", pos)
	}
	// Split name from args: first identifier, then optional comma, then args.
	i := 0
	for i < len(rest) && isIdentCont(rest[i]) {
		i++
	}
	if i == 0 {
		return nil, fmt.Errorf("%s: .macro: expected name", pos)
	}
	name := rest[:i]
	argStr := strings.TrimSpace(rest[i:])
	// Drop optional ',' separating name and first arg.
	argStr = strings.TrimPrefix(argStr, ",")
	argStr = strings.TrimSpace(argStr)
	// Strip a trailing comment if present (// or # or /*...*/).
	argStr = stripTrailingComment(argStr)
	var params []string
	if argStr != "" {
		for _, p := range strings.Split(argStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			params = append(params, p)
		}
	}
	return &macroDef{name: name, params: params}, nil
}

// collectMacroBody collects lines up to (but not including) the matching
// .endm. Returns (body, consumed) where consumed is the number of source
// lines used (body lines + 1 for the .endm). Nested .macro inside .macro is
// not supported by GNU as either; we error out.
func collectMacroBody(lines []string, path string, startLine int) ([]string, int, error) {
	var body []string
	for i, ln := range lines {
		t := strings.TrimLeft(ln, " \t")
		if strings.HasPrefix(t, ".macro ") || strings.HasPrefix(t, ".macro\t") {
			return nil, 0, fmt.Errorf("%s:%d: nested .macro is unsupported", path, startLine+i)
		}
		if t == ".endm" || strings.HasPrefix(t, ".endm ") {
			return body, i + 1, nil
		}
		body = append(body, ln)
	}
	return nil, 0, fmt.Errorf("%s:%d: unterminated .macro", path, startLine)
}

// evalIfCondition parses ".if SYMBOL" and returns its truthiness based on the
// current setvars table. Only bare-symbol conditions are supported.
func (pp *preprocessor) evalIfCondition(line string, pos preprocPos) (bool, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, ".if"))
	rest = stripTrailingComment(rest)
	if rest == "" {
		return false, fmt.Errorf("%s: .if: missing symbol", pos)
	}
	// Only bare identifiers (no expressions).
	if !isBareIdent(rest) {
		return false, fmt.Errorf("%s: .if: only bare-symbol conditions supported (got %q)", pos, rest)
	}
	v, ok := pp.setvars[rest]
	if !ok {
		// GNU as treats unknown symbols as 0 (false). Mirror that for
		// robustness — spectrum4 always defines its flag symbols before use,
		// but a missing .set at the top of an input file would otherwise
		// hard-fail.
		return false, nil
	}
	return v != 0, nil
}

func isBareIdent(s string) bool {
	if s == "" {
		return false
	}
	if !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentCont(s[i]) {
			return false
		}
	}
	return true
}

// tryParseSet recognises ".set NAME, <int>" lines and returns the parsed
// integer. Non-integer values cause ok=false (the .set will still be passed
// through to the parser as normal).
func tryParseSet(line string) (string, int64, bool) {
	if !strings.HasPrefix(line, ".set ") && !strings.HasPrefix(line, ".set\t") {
		return "", 0, false
	}
	rest := strings.TrimSpace(line[len(".set"):])
	rest = stripTrailingComment(rest)
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", 0, false
	}
	name := strings.TrimSpace(rest[:comma])
	val := strings.TrimSpace(rest[comma+1:])
	if !isBareIdent(name) {
		return "", 0, false
	}
	n, ok := parseIntLiteral(val)
	if !ok {
		return "", 0, false
	}
	return name, n, true
}

// parseIntLiteral parses an integer literal in C-style notation (0x.., 0b..,
// or decimal). Returns ok=false for any other form.
func parseIntLiteral(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	base := 10
	switch {
	case strings.HasPrefix(s, "0x"), strings.HasPrefix(s, "0X"):
		base = 16
		s = s[2:]
	case strings.HasPrefix(s, "0b"), strings.HasPrefix(s, "0B"):
		base = 2
		s = s[2:]
	}
	if s == "" {
		return 0, false
	}
	v, err := parseIntInBase(s, base)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}

// tryParseInclude recognises `.include "file"` lines. Returns the (unquoted)
// path on success.
func tryParseInclude(line string) (string, bool) {
	if !strings.HasPrefix(line, ".include ") && !strings.HasPrefix(line, ".include\t") {
		return "", false
	}
	rest := strings.TrimSpace(line[len(".include"):])
	rest = stripTrailingComment(rest)
	if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}

// stripTrailingComment removes a trailing line comment (// or # at column 0
// is handled elsewhere; here we strip "//..." or "/*...*/" appearing on the
// rest of a directive line). It also strips a trailing "# ..." comment when
// preceded by whitespace, to mirror GNU as.
func stripTrailingComment(s string) string {
	// Be careful with quoted strings: don't trim inside one.
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			return strings.TrimRight(s[:i], " \t")
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			// Find */; if not found, treat as no trim.
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return s
			}
			s = s[:i] + s[i+2+end+2:]
			i--
		}
	}
	return strings.TrimRight(s, " \t")
}

// handleInclude opens the include file (searching directory of including
// file first, then -I dirs), preprocesses it, and writes the expansion to
// out.
func (pp *preprocessor) handleInclude(includePath, fromPath string, pos preprocPos, out *strings.Builder) error {
	// 1. Directory of the including file.
	candidates := []string{filepath.Join(filepath.Dir(fromPath), includePath)}
	for _, d := range pp.incdirs {
		candidates = append(candidates, filepath.Join(d, includePath))
	}
	var (
		data    []byte
		resolved string
		err     error
	)
	for _, c := range candidates {
		data, err = os.ReadFile(c)
		if err == nil {
			resolved = c
			break
		}
	}
	if err != nil {
		return fmt.Errorf("%s: .include %q: not found in search path (%v)", pos, includePath, candidates)
	}
	return pp.processFile(data, resolved, out)
}

// tryExpandMacroInvocation checks whether `line` invokes a macro. If so, it
// expands the macro body (with \arg substitution + recursive expansion) and
// writes the result to out. Returns (ok=true) if the line was consumed as a
// macro call.
//
// A macro invocation looks like:    <indent>NAME arg1, arg2, ...
// where NAME is a known macro name, and the optional arguments are
// comma-separated and run to end-of-line (minus a trailing comment).
func (pp *preprocessor) tryExpandMacroInvocation(line string, pos preprocPos, out *strings.Builder) (bool, error) {
	_, rest := splitIndent(line)
	if rest == "" {
		return false, nil
	}
	// Identify the first word.
	i := 0
	for i < len(rest) && isIdentCont(rest[i]) {
		i++
	}
	if i == 0 {
		return false, nil
	}
	name := rest[:i]
	m, ok := pp.macros[name]
	if !ok {
		return false, nil
	}
	// Parse arguments: everything after NAME, up to end-of-line minus comment.
	argText := stripTrailingComment(strings.TrimSpace(rest[i:]))
	args := splitMacroArgs(argText)
	if len(args) != len(m.params) {
		return false, fmt.Errorf("%s: macro %q: expects %d args, got %d", pos, name, len(m.params), len(args))
	}
	if pp.expanding[name] {
		return false, fmt.Errorf("%s: macro %q: recursive expansion (cycle)", pos, name)
	}
	pp.expanding[name] = true
	defer delete(pp.expanding, name)

	// Substitute \param → arg in every body line, then recursively
	// preprocess the result. Longest-param-first so e.g. \address is
	// substituted before \addr.
	sub := buildSubstituter(m.params, args)
	substituted := make([]string, 0, len(m.body))
	for _, bl := range m.body {
		substituted = append(substituted, sub(bl))
	}

	// Mark expansion start with a synthetic line directive that points at the
	// macro definition file/line. After expansion the caller restores the
	// outer file's line directive. We point at defPos.line+1 because the
	// macro's first body line is one past the `.macro` header.
	emitLineDirective(out, m.defPos.file, m.defPos.line+1)
	// Recurse: the expanded body might invoke other macros / contain .if
	// blocks. We deliberately re-enter processLines with frame.active=true
	// since the call site itself is already known active (we checked above).
	if err := pp.processLines(substituted, m.defPos.file, m.defPos.line+1, out, ifFrame{active: true}); err != nil {
		return false, err
	}
	// Restore the caller's file/line for subsequent output.
	emitLineDirective(out, pos.file, pos.line+1)
	return true, nil
}

// splitIndent splits a line into leading whitespace + the rest.
func splitIndent(line string) (string, string) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[:i], line[i:]
}

// splitMacroArgs splits the argument list of a macro invocation. Commas
// inside parentheses are not separators (e.g. `_setmsk (1<<\bit), \address`).
// Whitespace around args is stripped.
func splitMacroArgs(s string) []string {
	if s == "" {
		return nil
	}
	var (
		out   []string
		depth int
		start = 0
		inStr = false
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inStr = !inStr
		case inStr:
			// nothing
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

// buildSubstituter returns a function that performs \param substitution on a
// single body line. Substitutions are sorted longest-first so e.g. \address
// matches before \addr.
//
// Substitution rules (mirroring GNU as):
//   • \name is replaced wherever it appears, including inside identifiers
//     (token-paste: msg_\reg → msg_x4) and inside string literals
//     ("\regname: " → "x4: ").
//   • The terminator for \name is the first non-isIdentCont byte (since
//     param names are themselves identifiers).
//   • A "\\" pair is a literal backslash, not the start of a substitution.
func buildSubstituter(params, args []string) func(string) string {
	// Index params by length (descending) for greedy matching.
	type pa struct{ p, a string }
	pairs := make([]pa, len(params))
	for i := range params {
		pairs[i] = pa{p: params[i], a: args[i]}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].p) > len(pairs[j].p) })
	return func(line string) string {
		var b strings.Builder
		b.Grow(len(line))
		for i := 0; i < len(line); {
			c := line[i]
			if c == '\\' && i+1 < len(line) {
				if line[i+1] == '\\' {
					// Literal backslash escape — keep as-is, advance two.
					b.WriteByte('\\')
					b.WriteByte('\\')
					i += 2
					continue
				}
				// Try to match a param.
				matched := false
				for _, p := range pairs {
					end := i + 1 + len(p.p)
					if end <= len(line) && line[i+1:end] == p.p {
						// Must be followed by a non-ident character (or EOL).
						if end == len(line) || !isIdentCont(line[end]) {
							b.WriteString(p.a)
							i = end
							matched = true
							break
						}
					}
				}
				if matched {
					continue
				}
				// Unrecognised \x — keep the literal backslash sequence as-is.
				b.WriteByte('\\')
				i++
				continue
			}
			b.WriteByte(c)
			i++
		}
		return b.String()
	}
}
