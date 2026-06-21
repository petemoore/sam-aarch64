// main_test.go — host tests for the i121i config-aware .mgt serve vessel.
//
// The bootable serve disk ships the combined RRQ+WRQ serve binary plus a small
// SERVE_CONFIG CODE file ("cfg") that the AUTO BASIC overlays at the
// SERVE_CONFIG address. These tests prove the overlaid runtime image is
// byte-identical to what the trinload vessel's host launcher (trinpush.py
// patch_config) produces for the same strategy — "the runtime image matches the
// trinload vessel exactly" (i121i), the load-bearing invariant.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petemoore/samfile/v3"
)

// testDosPath returns the repo's shipped boot DOS, found by walking up from the
// test's working directory (tools/build-disk) until DefaultDosPath resolves.
// t.Fatal (never skip) if the reference DOS is missing — it is always present.
func testDosPath(t *testing.T) string {
	t.Helper()
	dir := "."
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, DefaultDosPath)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		dir = filepath.Join("..", dir)
	}
	t.Fatalf("could not locate %s walking up from the test directory", DefaultDosPath)
	return ""
}

// TestCheckVariantPayloads is the i207 boot-payload completeness guard: a disk
// declared 'test' or 'prod' must carry every payload the boot loader HLOADs, so
// a missing one fails the build instead of silently hanging SimCoupé.
func TestCheckVariantPayloads(t *testing.T) {
	full := map[string]string{
		"-sysreg-data": "sd13.bin", "-disasm": "d15.bin", "-zx0": "zx0.bin",
		"-test-mem": "tm.bin", "-paged-call": "p14.bin", "-cluster": "cl.bin", "-enc-fix": "ef.bin",
	}
	// none: never errors, even with nothing supplied.
	if err := checkVariantPayloads("none", map[string]string{}); err != nil {
		t.Errorf("none variant should skip the check, got %v", err)
	}
	// unknown variant: error.
	if err := checkVariantPayloads("bogus", full); err == nil {
		t.Error("unknown variant should error")
	}
	// prod/test with the full set: pass.
	for _, v := range []string{"prod", "test"} {
		if err := checkVariantPayloads(v, full); err != nil {
			t.Errorf("%s with full payloads should pass, got %v", v, err)
		}
	}
	// prod missing -disasm: error naming the flag (the i69 class).
	noDisasm := map[string]string{"-sysreg-data": "sd13.bin", "-zx0": "zx0.bin"}
	if err := checkVariantPayloads("prod", noDisasm); err == nil {
		t.Error("prod missing -disasm should error")
	} else if !strings.Contains(err.Error(), "-disasm") {
		t.Errorf("error should name the missing -disasm flag, got %v", err)
	}
	// test missing -enc-fix (the actual i69 omission): error.
	noEncFix := map[string]string{
		"-sysreg-data": "sd13.bin", "-disasm": "d15.bin", "-zx0": "zx0.bin",
		"-test-mem": "tm.bin", "-paged-call": "p14.bin", "-cluster": "cl.bin",
	}
	if err := checkVariantPayloads("test", noEncFix); err == nil {
		t.Error("test missing -enc-fix should error (the i69 omission)")
	} else if !strings.Contains(err.Error(), "-enc-fix") {
		t.Errorf("error should name the missing -enc-fix flag, got %v", err)
	}
	// prod does NOT require the test-only payloads.
	if err := checkVariantPayloads("prod", map[string]string{
		"-sysreg-data": "sd13.bin", "-disasm": "d15.bin", "-zx0": "zx0.bin",
	}); err != nil {
		t.Errorf("prod should not require test-only payloads, got %v", err)
	}
}

func TestParseServeStrategy(t *testing.T) {
	cases := []struct {
		spec    string
		strat   int
		record  int
		wantErr bool
	}{
		{"highest", ServeStratHighest, 0, false},
		{"", ServeStratHighest, 0, false},
		{"LOWEST", ServeStratLowest, 0, false},
		{"explicit:5", ServeStratExplicit, 5, false},
		{"explicit:0x10", ServeStratExplicit, 16, false},
		{"explicit:0", 0, 0, true},     // record 0 out of range
		{"explicit:70000", 0, 0, true}, // record > 0xFFFF
		{"explicit:", 0, 0, true},      // no number
		{"bogus", 0, 0, true},
	}
	for _, c := range cases {
		strat, rec, err := parseServeStrategy(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("parseServeStrategy(%q): err=%v, wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if strat != c.strat || rec != c.record {
			t.Errorf("parseServeStrategy(%q) = (%d,%d), want (%d,%d)", c.spec, strat, rec, c.strat, c.record)
		}
	}
}

func TestServeConfigBlock(t *testing.T) {
	// highest/lowest force record to 0; explicit carries it LE.
	if got, want := serveConfigBlock(ServeStratHighest, 7), []byte{0x5A, 0, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("highest block = % X, want % X (record must be cleared)", got, want)
	}
	if got, want := serveConfigBlock(ServeStratLowest, 0), []byte{0x5A, 1, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("lowest block = % X, want % X", got, want)
	}
	if got, want := serveConfigBlock(ServeStratExplicit, 0x1234), []byte{0x5A, 2, 0x34, 0x12}; !bytes.Equal(got, want) {
		t.Errorf("explicit block = % X, want % X (record LE)", got, want)
	}
}

func TestServeConfigAddr(t *testing.T) {
	mapText := "8000=serve_main\nC526=SERVE_CONFIG\nB000=something_else\n"
	addr, err := serveConfigAddr(mapText)
	if err != nil {
		t.Fatalf("serveConfigAddr: %v", err)
	}
	if addr != 0xC526 {
		t.Errorf("addr = &%04X, want &C526", addr)
	}
	if _, err := serveConfigAddr("8000=serve_main\n"); err == nil {
		t.Error("expected error when SERVE_CONFIG absent from the map")
	}
}

// trinpushPatch mirrors trinpush.py patch_config: patch strategy+record in place,
// leaving the magic untouched. This is the trinload vessel's runtime block.
func trinpushPatch(bin []byte, off uint32, strat, record int) []byte {
	out := append([]byte(nil), bin...)
	out[off+1] = byte(strat)
	if strat == ServeStratExplicit {
		out[off+2] = byte(record & 0xFF)
		out[off+3] = byte((record >> 8) & 0xFF)
	}
	return out
}

// fakeServe builds a synthetic serve binary whose trailing 4 bytes are the baked
// default SERVE_CONFIG block, plus a matching mapfile. Hermetic — no dependency
// on the netboot build.
func fakeServe(t *testing.T, dir string, n int) (binPath, mapPath string, configAddr uint32, bin []byte) {
	t.Helper()
	bin = make([]byte, n)
	for i := range bin {
		bin[i] = byte(i)
	}
	off := n - ServeConfigSize
	copy(bin[off:], serveConfigBlock(ServeStratHighest, 0)) // baked default
	configAddr = LoadAddress + uint32(off)
	binPath = filepath.Join(dir, "serve.bin")
	if err := os.WriteFile(binPath, bin, 0o644); err != nil {
		t.Fatal(err)
	}
	mapPath = filepath.Join(dir, "serve.map")
	mapText := "8000=serve_main\n" + // a decoy symbol
		formatHexAddr(configAddr) + "=SERVE_CONFIG\n"
	if err := os.WriteFile(mapPath, []byte(mapText), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

func formatHexAddr(a uint32) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[(a>>12)&0xF], hex[(a>>8)&0xF], hex[(a>>4)&0xF], hex[a&0xF]})
}

// TestBuildServeDiskOverlayMatchesTrinload is the load-bearing invariant: the
// .mgt vessel's serve+cfg overlay reproduces the trinload vessel's host-patched
// image byte-for-byte, for each strategy.
func TestBuildServeDiskOverlayMatchesTrinload(t *testing.T) {
	dir := t.TempDir()
	binPath, mapPath, configAddr, bin := fakeServe(t, dir, 0x200)
	off := configAddr - LoadAddress

	mapText, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	gotAddr, err := serveConfigAddr(string(mapText))
	if err != nil {
		t.Fatal(err)
	}
	if gotAddr != configAddr {
		t.Fatalf("serveConfigAddr = &%04X, want &%04X", gotAddr, configAddr)
	}

	for _, c := range []struct {
		spec   string
		strat  int
		record int
	}{
		{"highest", ServeStratHighest, 0},
		{"lowest", ServeStratLowest, 0},
		{"explicit:42", ServeStratExplicit, 42},
	} {
		strat, rec, err := parseServeStrategy(c.spec)
		if err != nil {
			t.Fatalf("parseServeStrategy(%q): %v", c.spec, err)
		}
		cfg := &netbootConfig{name: "cfg", addr: configAddr, data: serveConfigBlock(strat, rec), strategy: c.spec}

		out := filepath.Join(dir, "serve-"+c.spec+".mgt")
		if err := buildNetbootDisk(testDosPath(t), DefaultDosName, DefaultDosLoad, binPath, "serve", out, cfg); err != nil {
			t.Fatalf("buildNetbootDisk(%s): %v", c.spec, err)
		}

		di, err := samfile.Load(out)
		if err != nil {
			t.Fatalf("Load(%s): %v", out, err)
		}
		serveFile, err := di.File("serve")
		if err != nil {
			t.Fatalf("File(serve): %v", err)
		}
		cfgFile, err := di.File("cfg")
		if err != nil {
			t.Fatalf("File(cfg): %v", err)
		}
		if !bytes.Equal(cfgFile.Body, serveConfigBlock(strat, rec)) {
			t.Errorf("%s: cfg body = % X, want % X", c.spec, cfgFile.Body, serveConfigBlock(strat, rec))
		}

		// Reconstruct the runtime image: serve binary with the cfg bytes overlaid
		// at the SERVE_CONFIG address (what the AUTO BASIC's two LOADs produce).
		runtime := append([]byte(nil), serveFile.Body...)
		copy(runtime[off:], cfgFile.Body)

		want := trinpushPatch(bin, off, strat, rec)
		if !bytes.Equal(runtime, want) {
			t.Errorf("%s: overlaid runtime image != trinload-patched image\n  runtime[%d:]=% X\n  want[%d:]=% X",
				c.spec, off, runtime[off:], off, want[off:])
		}
	}
}

// TestBuildServeDiskNoConfig: without a config, no cfg file is shipped (the
// pre-i121i behaviour — the binary's baked default applies).
func TestBuildServeDiskNoConfig(t *testing.T) {
	dir := t.TempDir()
	binPath, _, _, _ := fakeServe(t, dir, 0x200)
	out := filepath.Join(dir, "serve.mgt")
	if err := buildNetbootDisk(testDosPath(t), DefaultDosName, DefaultDosLoad, binPath, "serve", out, nil); err != nil {
		t.Fatalf("buildNetbootDisk: %v", err)
	}
	di, err := samfile.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := di.File("cfg"); err == nil {
		t.Error("a no-config build should not ship a cfg file")
	}
	if _, err := di.File("serve"); err != nil {
		t.Errorf("serve file missing: %v", err)
	}
}

// TestBuildServeDiskMagicGuard: a config addr pointing at a non-magic byte (wrong
// mapfile / wrong binary) is rejected, mirroring trinpush's magic sanity-check.
func TestBuildServeDiskMagicGuard(t *testing.T) {
	dir := t.TempDir()
	binPath, _, configAddr, _ := fakeServe(t, dir, 0x200)
	out := filepath.Join(dir, "serve.mgt")

	// Point one byte too low — lands on a non-magic byte.
	cfg := &netbootConfig{name: "cfg", addr: configAddr - 1, data: serveConfigBlock(ServeStratHighest, 0), strategy: "highest"}
	if err := buildNetbootDisk(testDosPath(t), DefaultDosName, DefaultDosLoad, binPath, "serve", out, cfg); err == nil {
		t.Error("expected the magic guard to reject a config addr off the SERVE_CONFIG block")
	}

	// Point past the end of the binary — out-of-range guard.
	cfg2 := &netbootConfig{name: "cfg", addr: LoadAddress + 0x200, data: serveConfigBlock(ServeStratHighest, 0), strategy: "highest"}
	if err := buildNetbootDisk(testDosPath(t), DefaultDosName, DefaultDosLoad, binPath, "serve", out, cfg2); err == nil {
		t.Error("expected the range guard to reject a config addr past the serve code")
	}
}
