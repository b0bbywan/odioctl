import contextlib
import io
import json
import tempfile
import unittest

from odioctl import cli, components
from odioctl.upgrade import apply
from tests._helpers import make_state, write_state


class ListComponentsTests(unittest.TestCase):
    def test_statuses(self):
        st = make_state(
            roles={"mpd": "1", "common": "1"},
            roles_excluded=["spotifyd"],
            features=["tidal"],
            features_excluded=["mympd"],
        )
        by_name = {(c.kind, c.name): c for c in components.list_components(st)}
        self.assertEqual(by_name[("role", "mpd")].status, "installed")
        self.assertEqual(by_name[("role", "mpd")].installed_version, "1")
        self.assertEqual(by_name[("role", "spotifyd")].status, "excluded")
        self.assertFalse(by_name[("role", "spotifyd")].enabled)
        self.assertEqual(by_name[("role", "snapclient")].status, "default")
        self.assertTrue(by_name[("role", "snapclient")].enabled)
        self.assertFalse(by_name[("role", "common")].toggleable)
        self.assertEqual(by_name[("feature", "tidal")].status, "installed")
        self.assertEqual(by_name[("feature", "tidal")].parent, "upmpdcli")
        self.assertEqual(by_name[("feature", "mympd")].status, "excluded")
        self.assertEqual(by_name[("feature", "qobuz")].status, "default")

    def test_unknown_names_from_state_are_listed(self):
        # A role odios adds later must show up even if this odioctl predates it.
        st = make_state(roles={"newthing": "1"}, features_excluded=["newplugin"])
        names = {(c.kind, c.name): c for c in components.list_components(st)}
        self.assertEqual(names[("role", "newthing")].label, "newthing")
        self.assertEqual(names[("feature", "newplugin")].status, "excluded")

    def test_to_dict_includes_enabled(self):
        st = make_state(roles_excluded=["spotifyd"])
        c = next(c for c in components.list_components(st) if c.name == "spotifyd")
        d = c.to_dict()
        self.assertEqual(d["enabled"], False)
        self.assertEqual(d["kind"], "role")


class SetComponentTests(unittest.TestCase):
    def test_disable_role_moves_it_out_of_roles_and_into_excluded(self):
        st = make_state(roles={"mpd": "1", "spotifyd": "1"})
        new = components.set_component(st, "role", "spotifyd", False)
        self.assertNotIn("spotifyd", new["roles"])
        self.assertEqual(new["roles_excluded"], ["spotifyd"])
        # derive_install_env must now emit N (and no Y overriding it)
        self.assertEqual(apply.derive_install_env(new)["INSTALL_SPOTIFYD"], "N")
        # original untouched
        self.assertIn("spotifyd", st["roles"])

    def test_enable_role_only_clears_exclusion(self):
        st = make_state(roles_excluded=["spotifyd", "snapclient"])
        new = components.set_component(st, "role", "spotifyd", True)
        self.assertEqual(new["roles_excluded"], ["snapclient"])
        self.assertNotIn("spotifyd", new["roles"])
        # neither Y nor N → install.sh default (Y) installs it on next apply
        self.assertNotIn("INSTALL_SPOTIFYD", apply.derive_install_env(new))

    def test_disable_feature(self):
        st = make_state(features=["tidal", "qobuz"])
        new = components.set_component(st, "feature", "tidal", False)
        self.assertEqual(new["features"], ["qobuz"])
        self.assertEqual(new["features_excluded"], ["tidal"])

    def test_enable_feature_clears_exclusion(self):
        st = make_state(features_excluded=["mympd"])
        new = components.set_component(st, "feature", "mympd", True)
        self.assertEqual(new["features_excluded"], [])
        self.assertEqual(new["features"], [])

    def test_idempotent(self):
        st = make_state(roles_excluded=["spotifyd"])
        again = components.set_component(st, "role", "spotifyd", False)
        self.assertEqual(again["roles_excluded"], ["spotifyd"])

    def test_infra_role_rejected(self):
        with self.assertRaises(components.ComponentError):
            components.set_component(make_state(roles={"common": "1"}), "role", "common", False)

    def test_unknown_name_rejected(self):
        with self.assertRaises(components.ComponentError):
            components.set_component(make_state(), "role", "nope", False)
        with self.assertRaises(components.ComponentError):
            components.set_component(make_state(), "feature", "nope", True)

    def test_unknown_name_present_in_state_accepted(self):
        st = make_state(roles={"newthing": "1"})
        new = components.set_component(st, "role", "newthing", False)
        self.assertEqual(new["roles_excluded"], ["newthing"])


class ComponentsCliTests(unittest.TestCase):
    def test_list_json(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(roles={"mpd": "1"}))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = cli.main(["components", "--state", path, "list", "--json"])
        self.assertEqual(rc, 0)
        data = json.loads(out.getvalue())
        self.assertIn(
            {"kind": "role", "name": "mpd"}.items(),
            [{k: v for k, v in c.items() if k in ("kind", "name")}.items() for c in data],
        )

    def test_disable_and_enable_round_trip_keeps_verify_happy(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(
                d, make_state(roles={"mpd": "1", "spotifyd": "1"}, features=["tidal"])
            )
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                self.assertEqual(
                    cli.main(["components", "--state", path, "disable", "spotifyd"]), 0
                )
                self.assertEqual(cli.main(["components", "--state", path, "disable", "tidal"]), 0)
                self.assertEqual(cli.main(["upgrade", "verify", "--state", path]), 0)
                self.assertEqual(cli.main(["components", "--state", path, "enable", "spotifyd"]), 0)
            with open(path) as f:
                st = json.load(f)
        self.assertEqual(st["roles_excluded"], [])
        self.assertNotIn("spotifyd", st["roles"])
        self.assertEqual(st["features_excluded"], ["tidal"])
        self.assertIn("next upgrade", out.getvalue())

    def test_infra_role_returns_2(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(roles={"common": "1"}))
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                self.assertEqual(cli.main(["components", "--state", path, "disable", "common"]), 2)
        self.assertIn("infrastructure", err.getvalue())

    def test_missing_state_returns_2(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(cli.main(["components", "--state", "/nonexistent", "list"]), 2)
