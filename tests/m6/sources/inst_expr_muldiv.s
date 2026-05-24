// inst_expr_muldiv.s — M6 closure PR-3c fixture.
//
// Exercises the constant-expression MUL (0x12) and DIV (0x13) operators
// in the .tbn expression bytecode evaluator (src/m3/expr_eval.asm).
// release-stripped's expression streams contain 9 MUL and 6 DIV opcodes
// (confirmed by scanning build/release-stripped.tbn through the
// tools/sam-aarch64-format reader); the SAM evaluator previously rejected
// both with `jp fail`.
//
// text2bin constant-folds expressions whose operands are all literals, so
// to drive a *surviving* MUL/DIV bytecode through the SAM evaluator the
// operands must be symbolic (resolved on-SAM, not at text2bin time) — this
// also mirrors release-stripped, whose MUL streams look like
// `PUSH_SYM .. PUSH_IMM8 MUL`.  We use label differences (assemble-time
// constants for GNU, symbolic bytecode for SAM) scaled by literals.
//
// Semantics are grounded in the authoritative Go evaluators
// (tools/sam-aarch64-format/expr.go::applyBinary and
// tools/aarch64enc/expr.go::applyBinaryEval, both identical):
//   MUL : a * b           — low 64 bits of the signed product
//   DIV : b==0 ? 0 : a/b  — signed 64-bit division, truncate toward zero
// GNU `aarch64-*-as` computes the same constant (64-bit offsetT, C integer
// division which truncates toward zero), so the emitted bytes byte-match.

        .text
start:
        nop                         // 4 bytes
        nop                         // 4 bytes
        nop                         // 4 bytes
mid:
        nop                         // 4 bytes
        nop                         // 4 bytes
end:
        // (mid - start) = 12, (end - start) = 20, (end - mid) = 8.

        // --- MUL: (mid - start) * 4 = 48 ---
        mov     x0, #(mid - start) * 4

        // --- MUL: 7 * (end - mid) = 56 ---
        add     x1, x1, #7 * (end - mid)

        // --- DIV: (end - start) / 4 = 5 ---
        mov     x2, #(end - start) / 4

        // --- DIV: (end - start) / 3 = 6  (truncates toward zero) ---
        add     x3, x3, #(end - start) / 3

        // --- mixed: (end - start) * 2 - (mid - start) / 4 = 40 - 3 = 37 ---
        mov     x4, #(end - start) * 2 - (mid - start) / 4
