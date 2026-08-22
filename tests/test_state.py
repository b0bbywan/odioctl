import json
import os
import tempfile
import unittest

from odioctl import state
from tests._helpers import make_state, write_state


class ValidateStateTests(unittest.TestCase):
    def test_current_schema_round_trips(self):
        st = make_state(roles={"mpd": "2026.5.0"}, features=["tidal"])
        self.assertEqual(state.validate_state(dict(st)), st)

    def test_not_an_object(self):
        with self.assertRaises(state.StateError):
            state.validate_state(["nope"])

    def test_missing_fields_are_listed(self):
        with self.assertRaises(state.StateError) as cm:
            state.validate_state({"odios": "2026.5.0", "roles": {}})
        msg = str(cm.exception)
        self.assertIn("target_user", msg)
        self.assertIn("release_history", msg)

    def test_legacy_features_dict_is_rejected(self):
        # rc3-era shape ({name: bool}) is legacy — not supported any more.
        raw = dict(make_state())
        raw["features"] = {"tidal": True}
        with self.assertRaises(state.StateError):
            state.validate_state(raw)

    def test_roles_must_map_str_to_str(self):
        raw = dict(make_state())
        raw["roles"] = {"mpd": 1}
        with self.assertRaises(state.StateError):
            state.validate_state(raw)

    def test_empty_target_user_is_rejected(self):
        raw = dict(make_state())
        raw["target_user"] = ""
        with self.assertRaises(state.StateError):
            state.validate_state(raw)


class ReadWriteStateTests(unittest.TestCase):
    def test_read_state_valid_file(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(roles={"mpd": "x"}))
            self.assertEqual(state.read_state(path)["roles"], {"mpd": "x"})

    def test_read_state_invalid_json_raises(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "state.json")
            with open(path, "w") as f:
                f.write("{not json")
            with self.assertRaises(json.JSONDecodeError):
                state.read_state(path)

    def test_write_state_file_matches_ansible_to_nice_json(self):
        st = make_state(roles={"mpd": "x"}, features=["tidal"])
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "state.json")
            state.write_state_file(path, st)
            with open(path) as f:
                text = f.read()
        self.assertEqual(text, json.dumps(st, indent=4, sort_keys=True) + "\n")
        self.assertEqual(json.loads(text), st)

    def test_write_state_file_preserves_mode(self):
        st = make_state()
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, st)
            os.chmod(path, 0o660)
            state.write_state_file(path, st)
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o660)
