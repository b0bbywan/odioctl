import contextlib
import io
import os
import tempfile
import unittest

from odioctl import fsutil


class AtomicWriteTests(unittest.TestCase):
    def test_round_trip_and_no_temp_left_behind(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "f.txt")
            fsutil.atomic_write_text(path, "héllo\n")
            with open(path, encoding="utf-8") as f:
                self.assertEqual(f.read(), "héllo\n")
            self.assertEqual(os.listdir(d), ["f.txt"])

    def test_preserves_mode_of_existing_file(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "f.txt")
            with open(path, "w") as f:
                f.write("old")
            os.chmod(path, 0o640)
            fsutil.atomic_write_text(path, "new")
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o640)

    def test_falls_back_to_in_place_when_dir_not_writable(self):
        if os.geteuid() == 0:
            self.skipTest("root ignores directory permissions")
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "f.txt")
            with open(path, "w") as f:
                f.write("old")
            os.chmod(d, 0o500)
            try:
                err = io.StringIO()
                with contextlib.redirect_stderr(err):
                    fsutil.atomic_write_text(path, "new")
                with open(path) as f:
                    self.assertEqual(f.read(), "new")
                self.assertIn("rewriting", err.getvalue())
            finally:
                os.chmod(d, 0o700)

    def test_raises_actionable_error_when_nothing_writable(self):
        if os.geteuid() == 0:
            self.skipTest("root ignores permissions")
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "f.txt")
            with open(path, "w") as f:
                f.write("old")
            os.chmod(path, 0o440)
            os.chmod(d, 0o500)
            try:
                with self.assertRaises(PermissionError) as cm:
                    fsutil.atomic_write_text(path, "new")
                self.assertIn("not writable", str(cm.exception))
            finally:
                os.chmod(d, 0o700)

    def test_atomic_write_json(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "f.json")
            fsutil.atomic_write_json(path, {"b": 1, "a": [1, 2]})
            with open(path) as f:
                self.assertEqual(
                    f.read(), '{\n    "a": [\n        1,\n        2\n    ],\n    "b": 1\n}\n'
                )


class NewFileModeTests(unittest.TestCase):
    def test_new_file_gets_umask_default_not_0600(self):
        old = os.umask(0o022)
        try:
            with tempfile.TemporaryDirectory() as d:
                path = os.path.join(d, "new.txt")
                fsutil.atomic_write_text(path, "x")
                self.assertEqual(os.stat(path).st_mode & 0o777, 0o644)
        finally:
            os.umask(old)
