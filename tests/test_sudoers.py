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
            if line.startswith("%odio"):
                self.assertNotIn("*", line)
                self.assertNotIn("?", line)
