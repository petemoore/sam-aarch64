package z80_test

import (
	"testing"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// zz_ measurement: isolate one sha256_compress by differencing a 128-byte
// (2 compress in update) vs 64-byte (1 compress in update) message, both with
// the SAME final cost. update(128)-update(64) = exactly one extra compress.
func TestZZCompressOnly(t *testing.T) {
	mac := loadSHA256Machine(t)
	measure := func(n int) uint64 {
		msg := make([]byte, n)
		for i := range msg {
			msg[i] = byte(i*7 + 3)
		}
		if _, err := mac.Call("sha256_init"); err != nil {
			t.Fatal(err)
		}
		mac.Write(shaInputStage, msg)
		res, err := mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: uint16(n)})
		if err != nil {
			t.Fatal(err)
		}
		return res.TStates
	}
	u128 := measure(128)
	u64 := measure(64)
	t.Logf("update(128)=%d update(64)=%d  => one compress = %d T-states", u128, u64, u128-u64)
}
