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
import importlib.util
import os
import socket
import struct
import tempfile
import threading
import unittest
from unittest import mock

import trinpush as tp


def _load_sd_push():
    """Import sd-push.py (dashed filename) as a module."""
    path = os.path.join(os.path.dirname(__file__), "sd-push.py")
    spec = importlib.util.spec_from_file_location("sd_push_launcher", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


sd_push = _load_sd_push()

import mgt_patch

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SERVE_BIN = os.path.join(REPO, "build", "netboot_serve_boot.bin")
SERVE_MAP = os.path.join(REPO, "build", "netboot_serve_boot.map")
DEMO_MGT = os.path.join(REPO, "build", "assemble_first_serve_record.mgt")
RENDER_MAP = os.path.join(REPO, "build", "render_chain.map")
NBSRV_MAP = os.path.join(REPO, "build", "netboot_server.map")


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


class TestFinalizeRetry(unittest.TestCase):
    """i335: a lost finalize reply must not misreport a completed push.

    finalize() runs against a loopback UDP responder scripted per test; the
    launcher socket uses a short timeout so the retry paths run fast."""

    def setUp(self):
        self.responder = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.responder.bind(("127.0.0.1", 0))
        self.responder.settimeout(2.0)
        self.port = self.responder.getsockname()[1]
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.settimeout(0.2)
        # finalize() sends to (dst, PORT); aim the module's PORT at the responder.
        self._orig_port = sd_push.PORT
        sd_push.PORT = self.port

    def tearDown(self):
        sd_push.PORT = self._orig_port
        self.sock.close()
        self.responder.close()

    def _respond(self, script):
        """Answer datagrams per `script`: each entry is a bytes reply or None
        (receive but stay silent). Runs in a thread; returns it for join()."""
        def run():
            for reply in script:
                data, addr = self.responder.recvfrom(16)
                if reply is not None:
                    self.responder.sendto(reply, addr)
        t = threading.Thread(target=run)
        t.start()
        return t

    def test_first_reply(self):
        t = self._respond([b"D\x91\x00"])
        status, record = sd_push.finalize(self.sock, "127.0.0.1")
        t.join()
        self.assertEqual((status, record), (b"D", 145))

    def test_lost_f_retried(self):
        # First 'F' swallowed, second answered — the retry must land it.
        t = self._respond([None, b"D\x05\x00"])
        status, record = sd_push.finalize(self.sock, "127.0.0.1")
        t.join()
        self.assertEqual((status, record), (b"D", 5))

    def test_stale_ack_skipped(self):
        # A stale block ack ('.') arrives before the real reply: must be
        # consumed and skipped, not returned as the finalize status.
        def run():
            data, addr = self.responder.recvfrom(16)
            self.responder.sendto(b".", addr)
            self.responder.sendto(b"E\x07\x00", addr)
        t = threading.Thread(target=run)
        t.start()
        status, record = sd_push.finalize(self.sock, "127.0.0.1")
        t.join()
        self.assertEqual((status, record), (b"E", 7))

    def test_all_lost(self):
        t = self._respond([None, None, None])
        status, record = sd_push.finalize(self.sock, "127.0.0.1", attempts=3)
        t.join()
        self.assertEqual((status, record), (None, None))


class TestStreamRetransmit(unittest.TestCase):
    """i348: a lost '@' frame OR a lost ack must not fail the whole push.

    stream_mgt() runs against a loopback responder that emulates sd_push's ack
    behaviour (ack each block with '.' + linearSec, recording the UNIQUE sectors
    'written') and can drop the first arrival of chosen sectors (lost frame) or the
    first ack of chosen sectors (lost ack). The host must retransmit until every
    sector is acked, and the responder must end up with every distinct sector."""

    def setUp(self):
        self.responder = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.responder.bind(("127.0.0.1", 0))
        self.responder.settimeout(2.0)
        self.port = self.responder.getsockname()[1]
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.settimeout(0.2)
        self._orig_port = sd_push.PORT
        sd_push.PORT = self.port

    def tearDown(self):
        sd_push.PORT = self._orig_port
        self.sock.close()
        self.responder.close()

    def _serve(self, nsec, lose_block=frozenset(), lose_ack=frozenset()):
        """Start the sd_push-emulating responder thread. Returns (thread, written)
        where `written` is the set of DISTINCT sectors it has received+'written'."""
        written = set()
        acked_once = set()
        block_seen = {}
        ack_sent = {}

        def run():
            while len(acked_once) < nsec:
                try:
                    dgram, addr = self.responder.recvfrom(600)
                except socket.timeout:
                    break  # the host gave up sending
                if dgram[:1] != b"@":
                    continue
                s = struct.unpack("<H", dgram[1:3])[0]
                block_seen[s] = block_seen.get(s, 0) + 1
                if s in lose_block and block_seen[s] == 1:
                    continue  # lost frame: no write, no ack
                written.add(s)  # idempotent absolute-LBA write
                ack_sent[s] = ack_sent.get(s, 0) + 1
                if s in lose_ack and ack_sent[s] == 1:
                    continue  # lost ack: sector landed but the host sees nothing
                self.responder.sendto(b"." + struct.pack("<H", s) + b"x", addr)
                acked_once.add(s)

        t = threading.Thread(target=run)
        t.start()
        return t, written

    def _run(self, nsec, **loss):
        t, written = self._serve(nsec, **loss)
        n, acked = sd_push.stream_mgt(self.sock, "127.0.0.1", bytes(nsec * sd_push.SECTOR))
        t.join(timeout=5)
        return n, acked, written

    def test_clean_stream_all_acked(self):
        n, acked, written = self._run(20)
        self.assertEqual((n, acked), (20, 20))
        self.assertEqual(written, set(range(20)))

    def test_lost_frames_retransmitted(self):
        n, acked, written = self._run(20, lose_block={3, 7, 15})
        self.assertEqual(acked, 20)
        self.assertEqual(written, set(range(20)))

    def test_lost_acks_recovered_without_double_write(self):
        n, acked, written = self._run(20, lose_ack={2, 9})
        self.assertEqual(acked, 20)
        self.assertEqual(written, set(range(20)))

    def test_lost_frames_and_acks_together(self):
        n, acked, written = self._run(24, lose_block={1, 20}, lose_ack={5, 12, 23})
        self.assertEqual(acked, 24)
        self.assertEqual(written, set(range(24)))


class TestLostReplyVerdict(unittest.TestCase):
    """i335: the post-finalize '?' probe decides verify-vs-failure."""

    def test_trinload_back_means_verify(self):
        code, message = sd_push.lost_reply_verdict(b"!")
        self.assertEqual(code, 3)
        self.assertIn("list-records", message)

    def test_sd_push_still_serving_is_failure(self):
        code, message = sd_push.lost_reply_verdict(b"!SP")
        self.assertEqual(code, 1)
        self.assertIn("sd_push", message)

    def test_silence_is_failure(self):
        code, message = sd_push.lost_reply_verdict(None)
        self.assertEqual(code, 1)
        self.assertIn("wedged", message)


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


@unittest.skipUnless(
    os.path.exists(DEMO_MGT) and os.path.exists(RENDER_MAP) and os.path.exists(NBSRV_MAP),
    "demo record .mgt / maps not built (run `make netboot-assemble-first-serve-record "
    "netboot-render-chain netboot-server`)")
class TestDemoRecordPatch(unittest.TestCase):
    """i365e: patch the record number into the REAL demo serve .mgt's overlays.

    Mirrors the in-memory patch the emulation gate does
    (assemble_first_serve_faithful_test.go): RDB_CFG_RECORD (LE16) in the
    'render' overlay and NB_BOOT_RECORD (byte) in the 'nbsrv' overlay. A wrong
    value is a shared-card data-safety hazard, so these assert the tool hits the
    exact config bytes (their compile-time defaults) and nothing else."""

    RENDER_DEFAULT = 1  # RDB_CONFIG: RDB_CFG_RECORD defw 1
    NBSRV_DEFAULT = 0   # NB_BOOT_RECORD default 0

    def setUp(self):
        with open(DEMO_MGT, "rb") as f:
            self.orig = f.read()
        # Single source: build specs from mgt_patch.DEMO_RECORD_SPECS.
        self.specs = mgt_patch.load_demo_specs(REPO)
        by_name = {name: mt for (name, mt, _sym, _w) in self.specs}
        self.render_map = by_name["render"]
        self.nbsrv_map = by_name["nbsrv"]

    def test_baked_defaults_are_config(self):
        # The bytes the tool will overwrite currently hold the compile-time
        # defaults — proof the map-symbol offsets land on the config, not code.
        self.assertEqual(
            mgt_patch.read_record_overlay(bytearray(self.orig), "render", self.render_map,
                                          "RDB_CFG_RECORD", 2), self.RENDER_DEFAULT)
        self.assertEqual(
            mgt_patch.read_record_overlay(bytearray(self.orig), "nbsrv", self.nbsrv_map,
                                          "NB_BOOT_RECORD", 1), self.NBSRV_DEFAULT)

    def test_patch_reads_back_and_is_precise(self):
        for record in (2, 7, 145, 255):
            mgt = bytearray(self.orig)
            mgt_patch.patch_record_overlays(mgt, record, self.specs)
            # Reads back at both overlays.
            self.assertEqual(
                mgt_patch.read_record_overlay(mgt, "render", self.render_map,
                                              "RDB_CFG_RECORD", 2), record)
            self.assertEqual(
                mgt_patch.read_record_overlay(mgt, "nbsrv", self.nbsrv_map,
                                              "NB_BOOT_RECORD", 1), record)
            # Precise: only the config bytes differ from the original. A stray
            # write would corrupt code/data and silently mis-serve on hardware.
            changed = [i for i in range(len(mgt)) if mgt[i] != self.orig[i]]
            # render low byte always differs (default 1 -> record); its high byte
            # differs only when record > 255 (never here); nbsrv byte differs when
            # record != 0. So 1 or 2 changed bytes, all == the LE record bytes.
            self.assertLessEqual(len(changed), 2)
            self.assertGreaterEqual(len(changed), 1)

    def test_record_out_of_byte_range_rejected(self):
        # NB_BOOT_RECORD is one byte; a record that does not fit must raise, not
        # truncate (a truncated record => raw CMD24 to the wrong LBA band).
        with self.assertRaises(ValueError):
            mgt_patch.patch_record_overlays(bytearray(self.orig), 256, self.specs)

    def test_unknown_overlay_rejected(self):
        with self.assertRaises(KeyError):
            mgt_patch.patch_record_overlays(
                bytearray(self.orig), 7, [("nosuch", self.render_map, "RDB_CFG_RECORD", 2)])

    def test_wrong_size_rejected(self):
        with self.assertRaises(ValueError):
            mgt_patch.patch_record_overlays(bytearray(self.orig[:1000]), 7, self.specs)


if __name__ == "__main__":
    unittest.main()


def _load_boot_record():
    """Import boot-record.py (dashed filename) as a module."""
    path = os.path.join(os.path.dirname(__file__), "boot-record.py")
    spec = importlib.util.spec_from_file_location("boot_record_launcher", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


boot_record = _load_boot_record()


def _mgt_with_auto(name, filetype, load, length, track=0, slot=1, fillers=True):
    """Compose a minimal .mgt directory holding one AUTO entry at (track, slot)
    in the side-interleaved layout (side-0 track t at offset t*10240), with the
    SAM dir-entry address fields set from load/length. Slots before it are
    filled with in-use non-AUTO entries (B-DOS terminates its scan at the first
    never-used slot) unless fillers=False."""
    if isinstance(name, str):
        name = name.encode("ascii")
    img = bytearray(4 * 10240)
    img[0x000] = 0x13
    img[0x001:0x00B] = b"samdos2   "
    if fillers:
        for t in range(track + 1):
            for s in range(20 if t < track else slot):
                off = t * 10240 + s * 256
                if img[off] == 0:
                    img[off] = 0x13
                    img[off + 1:off + 11] = b"filler    "
    off = track * 10240 + slot * 256
    img[off] = filetype
    img[off + 1:off + 11] = name + b" " * (10 - len(name))
    start_page = (load >> 14) - 1
    off_form = (load & 0x3FFF) | 0x8000
    img[off + 0xEC] = start_page
    img[off + 0xED], img[off + 0xEE] = off_form & 0xFF, off_form >> 8
    img[off + 0xEF] = length >> 14
    img[off + 0xF0], img[off + 0xF1] = length & 0xFF, (length & 0x3FFF) >> 8
    return bytes(img)


class TestBootHazard(unittest.TestCase):
    """i334: refuse the known will-not-boot record shapes before firing ALHK."""

    def test_vessel_class_is_clean(self):
        auto = boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("AUTOasm", 0x13, 0x8000, 20391))
        self.assertEqual(auto, ("AUTOasm", 0x13, 0x8000, 20391))
        self.assertIsNone(boot_record.boot_hazard(auto))

    def test_stack_overlap_refused(self):
        # trinload's shape: 11 KB at &6000 reaches &8C00, through the &7FE0
        # continuation stack (the i331 record-3 finding).
        auto = boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("AUTOtrin.O", 0x13, 0x6000, 0x2C00))
        hazard = boot_record.boot_hazard(auto)
        self.assertIsNotNone(hazard)
        self.assertIn("i331", hazard)

    def test_below_stack_is_clean(self):
        # &6000 + &1FE0 ends exactly AT the stack floor — no overlap.
        auto = boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("AUTOx", 0x13, 0x6000, 0x1FE0))
        self.assertIsNone(boot_record.boot_hazard(auto))

    def test_basic_auto_refused(self):
        hazard = boot_record.boot_hazard(
            boot_record.parse_mgt_auto_entry(_mgt_with_auto("AUTO", 0x10, 0x8000, 100)))
        self.assertIsNotNone(hazard)
        self.assertIn("i332", hazard)

    def test_no_auto_refused(self):
        img = bytearray(4 * 10240)
        img[0x000] = 0x13
        img[0x001:0x00B] = b"samdos2   "
        hazard = boot_record.boot_hazard(boot_record.parse_mgt_auto_entry(bytes(img)))
        self.assertIsNotNone(hazard)
        self.assertIn("no AUTO*", hazard)

    def test_lowercase_auto_matches(self):
        # B-DOS's AUTO* match is case-insensitive (XOR;AND %11011111 — PR 814
        # review): a lowercase "auto" BASIC is the REAL i332 livelock shape and
        # must be diagnosed as such, not as "no AUTO* file".
        hazard = boot_record.boot_hazard(boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("auto", 0x10, 0x8000, 100)))
        self.assertIsNotNone(hazard)
        self.assertIn("i332", hazard)

    def test_lowercase_auto_code_vessel_clean(self):
        # ...and a lowercase CODE vessel must NOT be falsely refused.
        self.assertIsNone(boot_record.boot_hazard(boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("autoserve", 0x13, 0x8000, 18217))))

    def test_bit7_name_does_not_match(self):
        # Bit 7 stays significant in B-DOS's match: an inverse-video first
        # char is not an AUTO file.
        name = bytes([ord("A") | 0x80]) + b"UTO"
        self.assertIsNone(boot_record.parse_mgt_auto_entry(
            _mgt_with_auto(name, 0x13, 0x8000, 100)))

    def test_dir_track_interleave(self):
        # The directory's later tracks live at t*10240 in the side-interleaved
        # .mgt layout: an AUTO entry on dir track 1 (slot 20+) must be found.
        auto = boot_record.parse_mgt_auto_entry(
            _mgt_with_auto("AUTOx", 0x13, 0x8000, 100, track=1, slot=3))
        self.assertEqual(auto, ("AUTOx", 0x13, 0x8000, 100))

    def test_scan_stops_at_never_used_slot(self):
        # B-DOS terminates its scan at the first never-used slot; an AUTO
        # entry past such a hole is invisible to it — and so to the guard.
        img = _mgt_with_auto("AUTOx", 0x13, 0x8000, 100, track=0, slot=5,
                             fillers=False)
        self.assertIsNone(boot_record.parse_mgt_auto_entry(img))

    def test_real_serve_floppy_is_refused(self):
        # The REAL netboot_serve.mgt (built by make netboot-serve-disk) is the
        # exact artifact whose record 186 livelocked on hardware (i332): a
        # lowercase-"auto" BASIC. The guard must refuse it AS the i332 shape.
        path = os.path.join(REPO, "build", "netboot_serve.mgt")
        if not os.path.exists(path):
            self.fail(f"{path} not built — run `make netboot-serve-disk`")
        hazard = boot_record.boot_hazard(
            boot_record.parse_mgt_auto_entry(open(path, "rb").read()))
        self.assertIsNotNone(hazard, "the BASIC-auto floppy vessel must be refused")
        self.assertIn("i332", hazard)

    def test_real_assembler_vessel(self):
        # The real artifact (built by `make disk-record`) must parse to the
        # documented shape and be clean.
        path = os.path.join(REPO, "build", "test_record.mgt")
        if not os.path.exists(path):
            self.fail(f"{path} not built — run `make disk-record`")
        auto = boot_record.parse_mgt_auto_entry(open(path, "rb").read())
        self.assertIsNotNone(auto, "no AUTO* entry in the real vessel")
        name, filetype, load, length = auto
        self.assertEqual((name, filetype, load), ("AUTOasm", 0x13, 0x8000))
        self.assertEqual(length, 20391)
        self.assertIsNone(boot_record.boot_hazard(auto))


def _load_push_and_boot():
    """Import push-and-boot.py (dashed filename) as a module."""
    path = os.path.join(os.path.dirname(__file__), "push-and-boot.py")
    spec = importlib.util.spec_from_file_location("push_and_boot_launcher", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


push_and_boot = _load_push_and_boot()


class TestPushAndBoot(unittest.TestCase):
    """i284: the one-command wrapper's ORCHESTRATION — the record claimed by the
    push must thread into the boot, the bootability pre-check must gate BEFORE the
    push, and every push outcome must route correctly. The two legs themselves
    (sd_push write, boot_record boot) are hardware-proven separately and mocked
    here; only the gluing logic is under test."""

    def _write_mgt(self, img):
        """Write a composed .mgt to a temp file (auto-removed) and return its path."""
        fd, path = tempfile.mkstemp(suffix=".mgt")
        os.write(fd, img)
        os.close(fd)
        self.addCleanup(os.remove, path)
        return path

    def _clean_mgt(self):
        # A CODE AUTO* at &8000 — the clean vessel class (no i331/i332 hazard).
        return self._write_mgt(_mgt_with_auto("AUTOgame", 0x13, 0x8000, 20000))

    def _basic_mgt(self):
        # A BASIC AUTO* — the i332 livelock shape the pre-check must refuse.
        return self._write_mgt(_mgt_with_auto("AUTO", 0x10, 0x8000, 100))

    def test_claimed_record_threads_into_boot(self):
        mgt = self._clean_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(0, 7)) as pm, \
             mock.patch.object(push_and_boot.boot_record, "main",
                               return_value=0) as bm:
            rc = push_and_boot.run("1.2.3.4", mgt)
        self.assertEqual(rc, 0)
        pm.assert_called_once()
        bm.assert_called_once()
        argv = bm.call_args.args[0]
        # The record claimed by the push (7) is booted, and the pushed image is
        # passed as --image so boot-record re-applies its i331/i332 guard.
        self.assertEqual(argv[1], "7")
        self.assertIn("--image", argv)
        self.assertEqual(argv[argv.index("--image") + 1], mgt)

    def test_no_boot_stops_after_push(self):
        mgt = self._clean_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(0, 7)) as pm, \
             mock.patch.object(push_and_boot.boot_record, "main") as bm:
            rc = push_and_boot.run("1.2.3.4", mgt, no_boot=True)
        self.assertEqual(rc, 0)
        pm.assert_called_once()
        bm.assert_not_called()

    def test_hazard_refused_before_push(self):
        # A will-not-boot disk must be refused WITHOUT spending the ~2-min push.
        mgt = self._basic_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt") as pm, \
             mock.patch.object(push_and_boot.boot_record, "main") as bm:
            rc = push_and_boot.run("1.2.3.4", mgt)
        self.assertEqual(rc, 1)
        pm.assert_not_called()
        bm.assert_not_called()

    def test_force_overrides_hazard(self):
        mgt = self._basic_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(0, 5)) as pm, \
             mock.patch.object(push_and_boot.boot_record, "main",
                               return_value=0) as bm:
            rc = push_and_boot.run("1.2.3.4", mgt, force=True)
        self.assertEqual(rc, 0)
        pm.assert_called_once()
        self.assertIn("--force", bm.call_args.args[0])

    def test_push_failure_does_not_boot(self):
        mgt = self._clean_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(1, None)), \
             mock.patch.object(push_and_boot.boot_record, "main") as bm:
            rc = push_and_boot.run("1.2.3.4", mgt)
        self.assertEqual(rc, 1)
        bm.assert_not_called()

    def test_lost_finalize_reply_cannot_boot(self):
        # code 3 + record None (i335): the push likely landed but the record is
        # unknown, so we cannot auto-boot — propagate 3, do not boot.
        mgt = self._clean_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(3, None)), \
             mock.patch.object(push_and_boot.boot_record, "main") as bm:
            rc = push_and_boot.run("1.2.3.4", mgt)
        self.assertEqual(rc, 3)
        bm.assert_not_called()

    def test_boot_failure_propagates(self):
        mgt = self._clean_mgt()
        with mock.patch.object(push_and_boot.sd_push, "push_mgt",
                               return_value=(0, 9)), \
             mock.patch.object(push_and_boot.boot_record, "main", return_value=1):
            rc = push_and_boot.run("1.2.3.4", mgt)
        self.assertEqual(rc, 1)


if __name__ == "__main__":
    unittest.main()
