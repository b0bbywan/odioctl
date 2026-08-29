import contextlib
import io
import json
import tempfile
import unittest

from odioctl import cli, components
from odioctl.manifest import Manifest
from odioctl.state import State
from odioctl.upgrade import apply
from tests._helpers import make_state, write_state


def role(st: State, name: str) -> components.Component:
    return next(c for c in components.list_components(st) if c.kind == "role" and c.name == name)


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


class OptInRoleTests(unittest.TestCase):
    """Roles install.sh asks [y/N] for (qbzd): state must carry the Y explicitly."""

    def test_catalog_marks_qbzd_opt_in(self):
        self.assertFalse(components.ROLE_CATALOG["qbzd"].default_install)
        self.assertTrue(components.ROLE_CATALOG["spotifyd"].default_install)

    def test_absent_from_both_lists_reads_as_off(self):
        # A box installed before qbzd existed has it in neither list. install.sh
        # would answer N, so the row must not promise an install (nor go pending).
        st = make_state(roles={"mpd": "1"}, features=["mympd"])
        c = role(st, "qbzd")
        self.assertEqual(c.status, "excluded")
        self.assertFalse(c.enabled)
        self.assertEqual(components.pending_components(st, {"mpd", "qbzd"}), [])

    def test_enable_records_an_explicit_install_y(self):
        st = make_state(roles={"mpd": "1"}, roles_excluded=["qbzd"])
        new = components.set_component(st, "role", "qbzd", True)
        self.assertEqual(new["roles_excluded"], [])
        self.assertEqual(new["roles"]["qbzd"], components.REQUESTED_VERSION)
        # clearing the exclusion is not enough here — install.sh's default is N
        self.assertEqual(apply.derive_install_env(new)["INSTALL_QBZD"], "Y")
        c = role(new, "qbzd")
        self.assertEqual(c.status, "default")
        self.assertTrue(c.enabled)
        self.assertIsNone(c.installed_version)  # placeholder version never shown
        self.assertIn("role:qbzd", components.pending_components(new, {"mpd", "qbzd"}))
        self.assertNotIn("qbzd", st["roles"])  # original untouched

    def test_requested_role_is_not_skipped_by_the_smart_upgrade(self):
        new = components.set_component(make_state(), "role", "qbzd", True)
        man: Manifest = {"odios": "2026.9.0b1", "roles": {"qbzd": "2026.9.0b1"}}
        env = apply.derive_run_env(new, man, apply.derive_install_env(new))
        self.assertNotIn("RUN_QBZD", env)

    def test_enable_is_idempotent_and_never_clobbers_a_real_version(self):
        once = components.set_component(make_state(), "role", "qbzd", True)
        self.assertEqual(
            components.set_component(once, "role", "qbzd", True)["roles"], {"qbzd": ""}
        )
        installed = make_state(roles={"qbzd": "2026.9.0b1"})
        again = components.set_component(installed, "role", "qbzd", True)
        self.assertEqual(again["roles"]["qbzd"], "2026.9.0b1")

    def test_disable_after_enable_round_trips(self):
        enabled = components.set_component(make_state(), "role", "qbzd", True)
        back = components.set_component(enabled, "role", "qbzd", False)
        self.assertNotIn("qbzd", back["roles"])
        self.assertEqual(back["roles_excluded"], ["qbzd"])
        self.assertEqual(apply.derive_install_env(back)["INSTALL_QBZD"], "N")
        self.assertEqual(role(back, "qbzd").status, "excluded")

    def test_installed_qbzd_looks_like_any_other_role(self):
        st = make_state(roles={"qbzd": "2026.9.0b1"})
        c = role(st, "qbzd")
        self.assertEqual(c.status, "installed")
        self.assertEqual(c.installed_version, "2026.9.0b1")
        self.assertEqual(components.pending_components(st, {"qbzd"}), [])

    def test_default_y_roles_still_only_clear_the_exclusion(self):
        new = components.set_component(
            make_state(roles_excluded=["spotifyd"]), "role", "spotifyd", True
        )
        self.assertNotIn("spotifyd", new["roles"])
        self.assertNotIn("INSTALL_SPOTIFYD", apply.derive_install_env(new))


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
