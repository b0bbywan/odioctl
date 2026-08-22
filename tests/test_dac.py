import contextlib
import io
import os
import tempfile
import unittest
from unittest.mock import patch

from odioctl import cli, dac

FIXTURE = """\
# For more options and information see
# http://rptl.io/configtxt
dtparam=i2c_arm=on
dtparam=audio=on
camera_auto_detect=1

[cm4]
otg_mode=1

[pi4]
arm_boost=1

[all]
enable_uart=1
"""


class ParseTests(unittest.TestCase):
    def test_unmanaged_onboard(self):
        st = dac.parse(FIXTURE)
        self.assertEqual(st.current, dac.ONBOARD)
        self.assertFalse(st.managed)
        self.assertEqual(st.stray_lines, ["dtparam=audio=on"])

    def test_unknown_vendor_overlay_is_stray_and_disabled(self):
        # Legacy name not in the catalog: not selectable, but still an audio line.
        text = "dtoverlay=hifiberry-dacplus\n"
        st = dac.parse(text)
        self.assertIsNone(st.current)
        self.assertIn("dtoverlay=hifiberry-dacplus", st.stray_lines)
        applied = dac.apply(text, dac.BY_ID["hifiberry-dac"])
        self.assertIn(dac.DISABLED_PREFIX + "dtoverlay=hifiberry-dacplus", applied)
        kms = dac.apply("dtoverlay=vc4-kms-v3d\n", dac.BY_ID["hifiberry-dac"])
        self.assertNotIn(dac.DISABLED_PREFIX, kms)
        self.assertFalse(dac.is_audio_overlay("vc4-kms-v3d"))

    def test_unmanaged_known_overlay(self):
        text = FIXTURE + "dtoverlay=hifiberry-dacplus-std\n"
        st = dac.parse(text)
        self.assertEqual(st.current, "hifiberry-dacplus-std")
        self.assertFalse(st.managed)

    def test_unknown_overlay_is_ignored(self):
        text = "dtoverlay=vc4-kms-v3d\ndtparam=audio=off\n"
        st = dac.parse(text)
        self.assertIsNone(st.current)
        self.assertEqual(st.stray_lines, ["dtparam=audio=off"])

    def test_managed_block_wins_over_outside(self):
        text = dac.apply(FIXTURE + "dtoverlay=hifiberry-dac\n", dac.BY_ID["iqaudio-dacplus"])
        st = dac.parse(text)
        self.assertEqual(st.current, "iqaudio-dacplus")
        self.assertTrue(st.managed)
        self.assertEqual(st.stray_lines, [])  # disabled lines are comments now


class ApplyTests(unittest.TestCase):
    def test_appends_block_and_disables_stray_lines(self):
        out = dac.apply(FIXTURE, dac.BY_ID["hifiberry-dacplus-std"])
        self.assertIn(dac.DISABLED_PREFIX + "dtparam=audio=on", out)
        self.assertNotIn("\ndtparam=audio=on\n", out)
        tail = out.splitlines()[-5:]
        self.assertEqual(
            tail,
            [dac.BEGIN, "[all]", "dtparam=audio=off", "dtoverlay=hifiberry-dacplus-std", dac.END],
        )
        self.assertTrue(out.endswith("\n"))
        # untouched lines preserved verbatim
        self.assertIn("[pi4]\narm_boost=1\n", out)

    def test_idempotent_and_switch(self):
        once = dac.apply(FIXTURE, dac.BY_ID["hifiberry-dacplus-std"])
        twice = dac.apply(once, dac.BY_ID["hifiberry-dacplus-std"])
        self.assertEqual(once, twice)
        switched = dac.apply(once, dac.BY_ID["allo-boss-dac-pcm512x-audio"])
        self.assertEqual(switched.count(dac.BEGIN), 1)
        self.assertIn("dtoverlay=allo-boss-dac-pcm512x-audio", switched)
        self.assertNotIn("dtoverlay=hifiberry-dacplus-std", switched)

    def test_onboard_sets_audio_on_without_overlay(self):
        out = dac.apply(FIXTURE, dac.BY_ID[dac.ONBOARD])
        block = out.split(dac.BEGIN, 1)[1]
        self.assertIn("dtparam=audio=on", block)
        self.assertNotIn("dtoverlay=", block)

    def test_params_are_rendered(self):
        entry = dac.DacEntry("x", "X", "hifiberry-dacplus-std", "slave")
        self.assertIn("dtoverlay=hifiberry-dacplus-std,slave", dac.apply("", entry))

    def test_empty_file(self):
        out = dac.apply("", dac.BY_ID["hifiberry-dac"])
        self.assertTrue(out.startswith(dac.BEGIN))

    def test_unapply_restores_original(self):
        applied = dac.apply(FIXTURE, dac.BY_ID["hifiberry-dacplus-std"])
        self.assertEqual(dac.unapply(applied), FIXTURE)

    def test_unapply_without_block_is_noop(self):
        self.assertEqual(dac.unapply(FIXTURE), FIXTURE)


class DacIoAndCliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.cfg = os.path.join(self.tmp.name, "config.txt")
        with open(self.cfg, "w") as f:
            f.write(FIXTURE)
        self.flag = os.path.join(self.tmp.name, "run", "reboot-required")
        p = patch.object(dac, "REBOOT_FLAG", self.flag)
        p.start()
        self.addCleanup(p.stop)

    def _run(self, *argv: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                rc = cli.main(list(argv))
            except SystemExit as e:  # argparse errors
                rc = int(e.code or 0)
        return rc, out.getvalue(), err.getvalue()

    def test_list_contains_catalog(self):
        rc, out, _ = self._run("dac", "list")
        self.assertEqual(rc, 0)
        self.assertIn("hifiberry-dacplus", out)
        self.assertIn(dac.ONBOARD, out)

    def test_status_unmanaged(self):
        rc, out, _ = self._run("dac", "status", "--config", self.cfg)
        self.assertEqual(rc, 0)
        self.assertIn("current:  onboard", out)
        self.assertIn("managed:  no", out)

    def test_status_no_config(self):
        rc, out, _ = self._run("dac", "status", "--config", "/nonexistent")
        self.assertEqual(rc, 0)
        self.assertIn("no config.txt", out)

    def test_set_writes_backup_and_flag(self):
        rc, out, _ = self._run("dac", "set", "hifiberry-dacplus-std", "--config", self.cfg)
        self.assertEqual(rc, 0)
        self.assertIn("reboot required", out)
        with open(self.cfg) as f:
            self.assertEqual(dac.parse(f.read()).current, "hifiberry-dacplus-std")
        with open(self.cfg + ".odioctl.bak") as f:
            self.assertEqual(f.read(), FIXTURE)
        self.assertTrue(os.path.exists(self.flag))
        self.assertTrue(dac.status(self.cfg)["reboot_required"])

    def test_dry_run_writes_nothing(self):
        rc, out, _ = self._run(
            "dac", "set", "hifiberry-dacplus-std", "--config", self.cfg, "--dry-run"
        )
        self.assertEqual(rc, 0)
        self.assertIn(dac.BEGIN, out)
        with open(self.cfg) as f:
            self.assertEqual(f.read(), FIXTURE)
        self.assertFalse(os.path.exists(self.flag))

    def test_unknown_id_rejected_by_argparse(self):
        rc, _, err = self._run("dac", "set", "not-a-dac", "--config", self.cfg)
        self.assertEqual(rc, 2)
        self.assertIn("invalid choice", err)

    def test_set_then_unset_restores(self):
        self._run("dac", "set", "hifiberry-dacplus-std", "--config", self.cfg)
        rc, _, _ = self._run("dac", "unset", "--config", self.cfg)
        self.assertEqual(rc, 0)
        with open(self.cfg) as f:
            self.assertEqual(f.read(), FIXTURE)

    def test_no_change_when_already_set(self):
        self._run("dac", "set", "hifiberry-dacplus-std", "--config", self.cfg)
        rc, out, _ = self._run("dac", "set", "hifiberry-dacplus-std", "--config", self.cfg)
        self.assertEqual(rc, 0)
        self.assertIn("no change", out)

    def test_missing_config_returns_2(self):
        rc, _, err = self._run("dac", "set", "hifiberry-dac", "--config", "/nonexistent")
        self.assertEqual(rc, 2)
        self.assertIn("no config.txt", err)
