"""data/sudoers/odioctl is generated from dac.CATALOG — fail on drift."""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def _load_gen():
    spec = importlib.util.spec_from_file_location(
        "gen_sudoers", ROOT / "scripts" / "gen-sudoers.py"
    )
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class SudoersTests(unittest.TestCase):
    def test_file_matches_catalog(self):
        gen = _load_gen()
        self.assertEqual((ROOT / "data" / "sudoers" / "odioctl").read_text(), gen.render())

    def test_no_wildcards(self):
        text = (ROOT / "data" / "sudoers" / "odioctl").read_text()
        for line in text.splitlines():
            if line.startswith("%odioctl"):
                self.assertNotIn("*", line)
                self.assertNotIn("?", line)

    def test_grants_the_odioctl_group_only(self):
        """`odio` is odios' state-access group, and the installing user is in it
        too — keying root on it would hand root to every state reader.
        """
        text = (ROOT / "data" / "sudoers" / "odioctl").read_text()
        rules = [ln for ln in text.splitlines() if ln and not ln.startswith("#")]
        self.assertTrue(rules)
        for line in rules:
            self.assertRegex(line, r"^(%odioctl |Defaults:%odioctl )")
