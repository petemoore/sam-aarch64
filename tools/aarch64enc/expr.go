package aarch64enc

import (
	"encoding/binary"
	"fmt"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// EvalContext resolves the symbol-leaf opcodes during evaluation.
type EvalContext struct {
	PC         int64
	Symbol     func(id uint16) (int64, bool)
	LocalLabel func(digit, dir byte) (int64, bool)
}

// Eval runs the bytecode in ctx and returns its value. Generalises
// M1's EvalConst with symbol/PC resolution callbacks.
func Eval(buf []byte, ctx EvalContext) (int64, error) {
	r := format.NewExprReader(buf)
	stack := make([]int64, 0, 8)
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return 0, err
		}
		switch op {
		case format.OpPushImm8:
			stack = append(stack, int64(int8(operand[0])))
		case format.OpPushImm16:
			stack = append(stack, int64(int16(binary.LittleEndian.Uint16(operand))))
		case format.OpPushImm32:
			stack = append(stack, int64(int32(binary.LittleEndian.Uint32(operand))))
		case format.OpPushImm64:
			stack = append(stack, int64(binary.LittleEndian.Uint64(operand)))
		case format.OpPushSym:
			id := binary.LittleEndian.Uint16(operand)
			if ctx.Symbol == nil {
				return 0, fmt.Errorf("eval: PUSH_SYM with no symbol resolver")
			}
			v, ok := ctx.Symbol(id)
			if !ok {
				return 0, fmt.Errorf("eval: undefined symbol id %d", id)
			}
			stack = append(stack, v)
		case format.OpPushLocal:
			digit := operand[0]
			dir := operand[1]
			if ctx.LocalLabel == nil {
				return 0, fmt.Errorf("eval: PUSH_LOCAL with no local resolver")
			}
			v, ok := ctx.LocalLabel(digit, dir)
			if !ok {
				dirCh := byte('f')
				if dir == 1 {
					dirCh = 'b'
				}
				return 0, fmt.Errorf("eval: no %d%c", digit, dirCh)
			}
			stack = append(stack, v)
		case format.OpPushPC:
			stack = append(stack, ctx.PC)
		case format.OpAdd, format.OpSub, format.OpMul, format.OpDiv,
			format.OpAnd, format.OpOr, format.OpXor, format.OpShl, format.OpShr:
			if len(stack) < 2 {
				return 0, fmt.Errorf("eval: stack underflow at %v", op)
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, applyBinaryEval(op, a, b))
		case format.OpNeg:
			stack[len(stack)-1] = -stack[len(stack)-1]
		case format.OpNot:
			stack[len(stack)-1] = ^stack[len(stack)-1]
		case format.OpRelLo12:
			stack[len(stack)-1] = stack[len(stack)-1] & 0xFFF
		case format.OpRelHi12:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 12) & 0xFFF
		case format.OpRelAbsG0, format.OpRelAbsG0NC:
			stack[len(stack)-1] = stack[len(stack)-1] & 0xFFFF
		case format.OpRelAbsG1, format.OpRelAbsG1NC:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 16) & 0xFFFF
		case format.OpRelAbsG2, format.OpRelAbsG2NC:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 32) & 0xFFFF
		case format.OpRelAbsG3:
			stack[len(stack)-1] = (stack[len(stack)-1] >> 48) & 0xFFFF
		default:
			return 0, fmt.Errorf("eval: unknown opcode %v", op)
		}
	}
	if len(stack) != 1 {
		return 0, fmt.Errorf("eval: stack ended with %d values", len(stack))
	}
	return stack[0], nil
}

func applyBinaryEval(op format.ExprOp, a, b int64) int64 {
	switch op {
	case format.OpAdd:
		return a + b
	case format.OpSub:
		return a - b
	case format.OpMul:
		return a * b
	case format.OpDiv:
		if b == 0 {
			return 0
		}
		return a / b
	case format.OpAnd:
		return a & b
	case format.OpOr:
		return a | b
	case format.OpXor:
		return a ^ b
	case format.OpShl:
		return a << uint64(b)
	case format.OpShr:
		return a >> uint64(b)
	}
	return 0
}
