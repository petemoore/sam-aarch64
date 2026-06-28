#!/usr/bin/env python3
"""Host unit + integration tests for the trinpush launcher library.

The strategy->record-placement EFFECT is emulation-tested in Go
(netboot_serve_wrq_record_test.go). These tests cover what the Python launchers
uniquely own: the discovery-reply identity rules (i329 — who owns port 0xEDB0,
and the stage-1 refusal that keeps a push away from a live tool), parsing the
pyz80 mapfile, the offset math (addr - org), the magic sanity-check, and the
patched bytes — including against the REAL built serve binary so map-format
drift or an offset slip is caught.

Run via `make netboot-trinpush-test` (builds netboot_serve_boot.bin first) or
`python3 -m unittest` from this directory.
"""
import os
import struct
import unittest

import trinpush as tp

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SERVE_BIN = os.path.join(REPO, "build", "netboot_serve_boot.bin")
SERVE_MAP = os.path.join(REPO, "build", "netboot_serve_boot.map")


class TestDiscoveryIdentity(unittest.TestCase):
    """i329: only trinload's bare '!' may receive a stage-1 push; tools are named
    by their 2-byte tag so refusals and stage-2 checks can say WHO is serving."""

    def test_identify(self):
        self.assertEqual(tp.identify(b"!"), "trinload")
        self.assertEqual(tp.identify(b"!SP"), "sd_push")
        self.assertEqual(tp.identify(b"!LR\x90\x01"), "list_records")
        self.assertIn("unknown tool", tp.identify(b"!XY"))
        self.assertIn("unknown responder", tp.identify(b"?"))
        self.assertIn("unknown responder", tp.identify(b"!S"))

    def test_stage1_accepts_only_bare_trinload(self):
        self.assertIsNone(tp.stage1_refusal(b"!"))

    def test_stage1_refuses_live_tools(self):
        for reply, who in ((b"!SP", "sd_push"), (b"!LR\x05\x00", "list_records"),
                           (b"!XY", "unknown tool")):
            refusal = tp.stage1_refusal(reply)
            self.assertIsNotNone(refusal, f"{reply!r} must be refused")
            self.assertIn(who, refusal)


class TestParseStrategy(unittest.TestCase):
    def test_named(self):
        self.assertEqual(tp.parse_strategy("highest"), (tp.STRAT_HIGHEST, 0))
        self.assertEqual(tp.parse_strategy("lowest"), (tp.STRAT_LOWEST, 0))
        self.assertEqual(tp.parse_strategy("LOWEST"), (tp.STRAT_LOWEST, 0))

    def test_explicit(self):
        self.assertEqual(tp.parse_strategy("explicit:4"), (tp.STRAT_EXPLICIT, 4))
        self.assertEqual(tp.parse_strategy("explicit:0x10"), (tp.STRAT_EXPLICIT, 16))

    def test_bad(self):
        for spec in ("bogus", "explicit:", "explicit:0", "explicit:99999", "", "highest:1"):
            with self.assertRaises(ValueError):
                tp.parse_strategy(spec)


class TestParseMapAndOffset(unittest.TestCase):
    MAP = "8000=start\nC2E7=SERVE_CONFIG\nC2E8=SERVE_CFG_STRATEGY\n# junk\nbad line\n"

    def test_parse(self):
        syms = tp.parse_map(self.MAP)
        self.assertEqual(syms["SERVE_CONFIG"], 0xC2E7)
        self.assertEqual(syms["start"], 0x8000)
        self.assertNotIn("bad line", syms)

    def test_offset(self):
        syms = tp.parse_map(self.MAP)
        self.assertEqual(tp.config_offset(syms), 0xC2E7 - 0x8000)

    def test_missing_symbol(self):
        with self.assertRaises(KeyError):
            tp.config_offset(tp.parse_map("8000=start\n"))


class TestPatchConfig(unittest.TestCase):
    def _block(self, strat=tp.STRAT_HIGHEST, rec=0xFFFF):
        # a 16-byte buffer whose last 4 bytes are a SERVE_CONFIG block at offset 12
        body = bytes(range(12))
        return body + bytes([tp.SERVE_CFG_MAGIC, strat]) + struct.pack("<H", rec), 12

    def test_highest(self):
        data, off = self._block()
        out = tp.patch_config(data, off, tp.STRAT_HIGHEST, 0)
        self.assertEqual(out[off], tp.SERVE_CFG_MAGIC)      # magic preserved
        self.assertEqual(out[off + 1], tp.STRAT_HIGHEST)
        self.assertEqual(out[:off], data[:off])             # body untouched

    def test_lowest(self):
        data, off = self._block()
        out = tp.patch_config(data, off, tp.STRAT_LOWEST, 0)
        self.assertEqual(out[off + 1], tp.STRAT_LOWEST)

    def test_explicit_writes_record(self):
        data, off = self._block()
        out = tp.patch_config(data, off, tp.STRAT_EXPLICIT, 4)
        self.assertEqual(out[off + 1], tp.STRAT_EXPLICIT)
        self.assertEqual(struct.unpack("<H", out[off + 2:off + 4])[0], 4)

    def test_non_explicit_leaves_record(self):
        data, off = self._block(rec=0xFFFF)
        out = tp.patch_config(data, off, tp.STRAT_LOWEST, 0)
        self.assertEqual(struct.unpack("<H", out[off + 2:off + 4])[0], 0xFFFF)

    def test_bad_magic(self):
        data, off = self._block()
        data = bytearray(data)
        data[off] = 0x00
        with self.assertRaises(ValueError):
            tp.patch_config(bytes(data), off, tp.STRAT_HIGHEST, 0)

    def test_offset_out_of_range(self):
        with self.assertRaises(ValueError):
            tp.patch_config(b"\x5a\x00\x00\x00", 4, tp.STRAT_HIGHEST, 0)


@unittest.skipUnless(os.path.exists(SERVE_BIN) and os.path.exists(SERVE_MAP),
                     "build/netboot_serve_boot.bin not built (run `make netboot-serve-boot`)")
class TestRealBinary(unittest.TestCase):
    """Integration: patch the REAL built serve binary via its REAL mapfile."""

    def setUp(self):
        with open(SERVE_BIN, "rb") as f:
            self.data = f.read()
        with open(SERVE_MAP) as f:
            self.syms = tp.parse_map(f.read())
        self.off = tp.config_offset(self.syms)

    def test_offset_in_range_and_magic(self):
        self.assertTrue(0 <= self.off + 4 <= len(self.data))
        self.assertEqual(self.data[self.off], tp.SERVE_CFG_MAGIC,
                         "magic mismatch: SERVE_CONFIG symbol/offset drifted")

    def test_baked_default_is_highest(self):
        self.assertEqual(self.data[self.off + 1], tp.STRAT_HIGHEST,
                         "baked default should be highest-free")

    def test_patch_each_strategy(self):
        for spec, want_strat, want_rec in (
            ("highest", tp.STRAT_HIGHEST, None),
            ("lowest", tp.STRAT_LOWEST, None),
            ("explicit:4", tp.STRAT_EXPLICIT, 4),
        ):
            strat, rec = tp.parse_strategy(spec)
            out = tp.patch_config(self.data, self.off, strat, rec)
            self.assertEqual(out[self.off], tp.SERVE_CFG_MAGIC)
            self.assertEqual(out[self.off + 1], want_strat)
            if want_rec is not None:
                self.assertEqual(struct.unpack("<H", out[self.off + 2:self.off + 4])[0], want_rec)
            # only the 4-byte config block changes
            self.assertEqual(out[:self.off], self.data[:self.off])
            self.assertEqual(out[self.off + 4:], self.data[self.off + 4:])


if __name__ == "__main__":
    unittest.main()
