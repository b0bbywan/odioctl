import contextlib
import io
import json
import os
import tempfile
import unittest

from odioctl.upgrade import verify
from tests._helpers import make_state, write_state


class VerifyChecksTests(unittest.TestCase):
    def test_features_known_passes_for_subset(self):
        s = make_state(features=["tidal"], features_excluded=["qobuz"])
        self.assertIsNone(verify._check_features_known(s))

    def test_features_known_flags_unknown(self):
        s = make_state(features=["tidal", "bogus"])
        err = verify._check_features_known(s)
        assert err is not None
        self.assertIn("bogus", err)

    def test_features_no_overlap_passes_for_disjoint(self):
        s = make_state(features=["tidal"], features_excluded=["qobuz"])
        self.assertIsNone(verify._check_features_no_overlap(s))

    def test_features_no_overlap_flags_collision(self):
        s = make_state(features=["tidal"], features_excluded=["tidal"])
        err = verify._check_features_no_overlap(s)
        assert err is not None
        self.assertIn("tidal", err)

    def test_roles_no_overlap_flags_collision(self):
        s = make_state(roles={"mpd": "x"}, roles_excluded=["mpd"])
        err = verify._check_roles_no_overlap(s)
        assert err is not None
        self.assertIn("mpd", err)

    def test_history_matches_passes_when_last_equals_odios(self):
        s = make_state(odios="2026.5.0", release_history=["2026.4.0", "2026.5.0"])
        self.assertIsNone(verify._check_history_matches_odios(s))

    def test_history_matches_passes_when_history_empty(self):
        s = make_state(odios="2026.5.0", release_history=[])
        self.assertIsNone(verify._check_history_matches_odios(s))

    def test_history_matches_flags_drift(self):
        s = make_state(odios="2026.5.0", release_history=["2026.4.0"])
        err = verify._check_history_matches_odios(s)
        assert err is not None
        self.assertIn("2026.4.0", err)
        self.assertIn("2026.5.0", err)

    def test_expected_version_release_tag_exact_match(self):
        s = make_state(odios="2026.5.0")
        self.assertIsNone(verify._check_expected_version(s, "2026.5.0"))

    def test_expected_version_release_tag_mismatch(self):
        s = make_state(odios="2026.4.2b2")
        err = verify._check_expected_version(s, "2026.5.0")
        assert err is not None
        self.assertIn("2026.4.2b2", err)

    def test_expected_version_pr_accepts_git_describe(self):
        s = make_state(odios="2026.4.2b2-20-g7c1f6c4")
        self.assertIsNone(verify._check_expected_version(s, "pr-56"))

    def test_expected_version_pr_rejects_garbage_string(self):
        s = make_state(odios="not-a-version")
        err = verify._check_expected_version(s, "pr-56")
        assert err is not None
        self.assertIn("not-a-version", err)


class RunVerifyTests(unittest.TestCase):
    def test_valid_state_returns_0(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(odios="2026.5.0", features=["tidal"]))
            self.assertEqual(verify.run_verify(path, "2026.5.0"), 0)

    def test_check_failure_returns_1(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(odios="2026.5.0"))
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                self.assertEqual(verify.run_verify(path, "2026.6.0"), 1)
        self.assertIn("expected 2026.6.0", err.getvalue())

    def test_missing_state_returns_2(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(verify.run_verify("/nonexistent/state.json", None), 2)

    def test_invalid_schema_returns_1(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "state.json")
            with open(path, "w") as f:
                json.dump({"odios": "2026.5.0", "roles": {}}, f)
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                self.assertEqual(verify.run_verify(path, None), 1)
        self.assertIn("missing required fields", err.getvalue())
