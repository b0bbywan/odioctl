import contextlib
import io
import json
import os
import tempfile
import unittest
from unittest.mock import patch

from odioctl import manifest
from odioctl.manifest import Manifest
from odioctl.upgrade import check
from tests._helpers import make_state, write_state


class ComputeRoleUpgradesTests(unittest.TestCase):
    @staticmethod
    def _manifest(roles: dict[str, str]) -> Manifest:
        return {"odios": "2026.5.0", "roles": roles}

    def test_role_with_newer_manifest_version_is_listed(self):
        upgrades = check._compute_role_upgrades(
            make_state(roles={"mpd": "2026.4.0"}),
            self._manifest({"mpd": "2026.5.0"}),
        )
        self.assertEqual(
            upgrades,
            [{"name": "mpd", "installed": "2026.4.0", "available": "2026.5.0"}],
        )

    def test_role_unchanged_is_excluded(self):
        upgrades = check._compute_role_upgrades(
            make_state(roles={"mpd": "2026.5.0"}),
            self._manifest({"mpd": "2026.5.0"}),
        )
        self.assertEqual(upgrades, [])

    def test_role_with_older_manifest_version_is_excluded(self):
        # Downgrade is not an "upgrade" — keep it out of the report so the
        # CLI summary doesn't lie about what's pending.
        upgrades = check._compute_role_upgrades(
            make_state(roles={"mpd": "2026.5.0"}),
            self._manifest({"mpd": "2026.4.0"}),
        )
        self.assertEqual(upgrades, [])

    def test_role_missing_from_manifest_is_excluded(self):
        upgrades = check._compute_role_upgrades(
            make_state(roles={"snapclient": "0.27.0"}),
            self._manifest({}),
        )
        self.assertEqual(upgrades, [])

    def test_role_awaiting_its_first_install_is_excluded(self):
        # components.REQUESTED_VERSION: enabled in the UI, never installed — it
        # belongs to pending_components, not to the motd role delta.
        upgrades = check._compute_role_upgrades(
            make_state(roles={"qbzd": ""}),
            self._manifest({"qbzd": "2026.9.0b1"}),
        )
        self.assertEqual(upgrades, [])

    def test_results_are_sorted_alphabetically(self):
        upgrades = check._compute_role_upgrades(
            make_state(roles={"zzz": "2026.4.0", "aaa": "2026.4.0"}),
            self._manifest({"zzz": "2026.5.0", "aaa": "2026.5.0"}),
        )
        self.assertEqual([u["name"] for u in upgrades], ["aaa", "zzz"])


class BuildUpgradesReportTests(unittest.TestCase):
    def test_upgrade_available_when_a_role_is_bumped(self):
        st = make_state(odios="2026.5.0", roles={"mpd": "2026.4.0"})
        report = check._build_upgrades_report(
            st, {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        )
        self.assertTrue(report["upgrade_available"])
        self.assertEqual(report["current"], "2026.5.0")
        self.assertEqual(report["latest"], "2026.5.0")
        self.assertEqual(len(report["roles"]), 1)

    def test_upgrade_available_when_only_odios_is_bumped(self):
        # Installer-only releases (umbrella metadata, no role bumps) must
        # still surface as an upgrade — that's the OR in upgrade_available.
        st = make_state(odios="2026.4.0", roles={"mpd": "2026.5.0"})
        report = check._build_upgrades_report(
            st, {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        )
        self.assertTrue(report["upgrade_available"])
        self.assertEqual(report["roles"], [])

    def test_up_to_date_when_neither_odios_nor_roles_bumped(self):
        st = make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"}, features=["mympd"])
        report = check._build_upgrades_report(
            st, {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        )
        self.assertFalse(report["upgrade_available"])
        self.assertEqual(report["roles"], [])
        self.assertEqual(report["pending_components"], [])

    def test_pending_component_alone_makes_upgrade_available(self):
        # mympd not installed, not excluded, parent mpd installed → next apply installs it.
        st = make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"})
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        report = check._build_upgrades_report(st, man)
        self.assertTrue(report["upgrade_available"])
        self.assertEqual(report["pending_components"], ["feature:mympd"])
        self.assertEqual(report["roles"], [])  # motd delta untouched

    def test_pending_roles_limited_to_what_the_manifest_ships(self):
        st = make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"}, features=["mympd"])
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0", "spotifyd": "1"}}
        report = check._build_upgrades_report(st, man)
        self.assertEqual(report["pending_components"], ["role:spotifyd"])
        excluded = make_state(
            odios="2026.5.0",
            roles={"mpd": "2026.5.0"},
            features=["mympd"],
            roles_excluded=["spotifyd"],
        )
        self.assertEqual(check._build_upgrades_report(excluded, man)["pending_components"], [])

    def test_opt_in_role_only_goes_pending_once_enabled(self):
        man: Manifest = {
            "odios": "2026.9.0b1",
            "roles": {"mpd": "2026.9.0b1", "qbzd": "2026.9.0b1"},
        }
        off = make_state(odios="2026.9.0b1", roles={"mpd": "2026.9.0b1"}, features=["mympd"])
        self.assertFalse(check._build_upgrades_report(off, man)["upgrade_available"])
        enabled = make_state(
            odios="2026.9.0b1",
            roles={"mpd": "2026.9.0b1", "qbzd": ""},
            features=["mympd"],
        )
        report = check._build_upgrades_report(enabled, man)
        self.assertTrue(report["upgrade_available"])
        self.assertEqual(report["pending_components"], ["role:qbzd"])
        self.assertEqual(report["roles"], [])  # motd delta untouched

    def test_full_manifest_is_cached_alongside_delta(self):
        # `roles` stays delta-only (consumed by odio-motd in the {name,
        # installed, available} shape); the full target manifest is cached
        # under `manifest` so `apply` skips refetching.
        st = make_state(odios="2026.4.0", roles={"mpd": "2026.4.0"})
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0", "spotifyd": "0.4.4"}}
        report = check._build_upgrades_report(st, man)
        self.assertEqual(report["manifest"], man)
        self.assertEqual([r["name"] for r in report["roles"]], ["mpd"])
        # motd contract: each delta entry must keep the {name, installed,
        # available} keys in that exact shape — odio-motd reads them by
        # name and breaks silently if the field set drifts.
        for entry in report["roles"]:
            self.assertEqual(set(entry.keys()), {"name", "installed", "available"})


# What `pr-84` publishes: it names itself by version, never by its tag.
PRERELEASE_MAN: Manifest = {"odios": "2026.7.0rc2-9-gcad916c", "roles": {"qbzd": "2026.9.0b1"}}


class TargetTagTests(unittest.TestCase):
    """upgrades.json carries the release *tag* alongside the version, because a
    pre-release is published under a tag ("pr-84") that its own manifest never
    mentions — `apply` builds its install.sh URL from it."""

    def test_tag_defaults_to_the_version_for_a_normal_release(self):
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        report = check._build_upgrades_report(make_state(features=["mympd"]), man)
        self.assertEqual(report["target_tag"], "2026.5.0")

    def test_requested_tag_is_recorded(self):
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(roles={"mpd": "2026.5.0"}, features=["mympd"]))
            out = os.path.join(d, "upgrades.json")
            opts = check.CheckOptions(state=state_path, version="pr-84", output=out)
            with patch.object(manifest, "fetch_manifest", return_value=PRERELEASE_MAN) as fetch:
                report = check.refresh(opts)
            fetch.assert_called_once_with(manifest.manifest_url("pr-84"))
        assert report is not None
        self.assertEqual(report["target_tag"], "pr-84")
        self.assertEqual(report["latest"], "2026.7.0rc2-9-gcad916c")

    def test_env_selects_the_release_for_the_daily_check(self):
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(roles={"mpd": "2026.5.0"}, features=["mympd"]))
            out = os.path.join(d, "upgrades.json")
            with (
                patch.dict(os.environ, {manifest.ODIOS_VERSION_ENV: "pr-84"}),
                patch.object(manifest, "fetch_manifest", return_value=PRERELEASE_MAN),
                contextlib.redirect_stdout(io.StringIO()) as text,
            ):
                check.run_check(check.CheckOptions(state=state_path, output=out))
            with open(out) as f:
                report = json.load(f)
        self.assertEqual(report["target_tag"], "pr-84")
        self.assertIn("Comparing against release pr-84", text.getvalue())

    def test_offline_refresh_keeps_the_release_it_cached(self):
        # Losing the network must not silently move the box back onto the
        # published latest: the cached manifest and its tag go together.
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(roles={"mpd": "2026.5.0"}, features=["mympd"]))
            out = os.path.join(d, "upgrades.json")
            with patch.object(manifest, "fetch_manifest", return_value=PRERELEASE_MAN):
                check.refresh(check.CheckOptions(state=state_path, version="pr-84", output=out))
            with patch.object(manifest, "fetch_manifest", return_value=None):
                second = check.refresh(check.CheckOptions(state=state_path, output=out))
        assert second is not None
        self.assertEqual(second["target_tag"], "pr-84")

    def test_unusable_version_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state())
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                rc = check.run_check(
                    check.CheckOptions(
                        state=state_path,
                        version="../../evil/repo",
                        output=os.path.join(d, "upgrades.json"),
                    )
                )
        self.assertEqual(rc, 2)
        self.assertIn("not a release tag", err.getvalue())

    def test_report_without_a_tag_reads_as_its_own_version(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "upgrades.json")
            with open(out, "w") as f:
                json.dump({"latest": "2026.5.0", "manifest": PRERELEASE_MAN}, f)
            report = check.read_report(out)
        assert report is not None
        self.assertEqual(report["target_tag"], "2026.5.0")


class RefreshTests(unittest.TestCase):
    def test_offline_refresh_reuses_cached_manifest(self):
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(roles={"mpd": "2026.5.0"}, features=["mympd"]))
            out = os.path.join(d, "upgrades.json")
            opts = check.CheckOptions(state=state_path, output=out)
            with patch.object(manifest, "fetch_manifest", return_value=man):
                first = check.refresh(opts)
            assert first is not None
            self.assertFalse(first["upgrade_available"])
            # user enables tidal (parent upmpdcli not installed → not pending) and
            # removes mympd; network gone
            write_state(d, make_state(roles={"mpd": "2026.5.0"}))
            with patch.object(manifest, "fetch_manifest", return_value=None):
                second = check.refresh(opts)
            assert second is not None
            self.assertTrue(second["upgrade_available"])
            self.assertEqual(second["pending_components"], ["feature:mympd"])
            self.assertEqual(second["manifest"], man)
            self.assertEqual(check.read_report(out), second)

    def test_offline_without_cache_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(roles={"mpd": "1"}))
            out = os.path.join(d, "upgrades.json")
            with patch.object(manifest, "fetch_manifest", return_value=None):
                self.assertIsNone(check.refresh(check.CheckOptions(state=state_path, output=out)))
            self.assertFalse(os.path.exists(out))
            self.assertIsNone(check.read_report(out))


class RunCheckTests(unittest.TestCase):
    def test_writes_report_and_returns_1_when_upgrade_available(self):
        man: Manifest = {"odios": "2026.6.0", "roles": {"mpd": "2026.6.0"}}
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"}))
            out = os.path.join(d, "cache", "upgrades.json")
            with (
                patch.object(manifest, "fetch_manifest", return_value=man),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                rc = check.run_check(check.CheckOptions(state=state_path, output=out))
            self.assertEqual(rc, 1)
            with open(out) as f:
                report = json.load(f)
        self.assertEqual(report["latest"], "2026.6.0")
        self.assertEqual(report["roles"][0]["name"], "mpd")
        self.assertEqual(report["manifest"], man)

    def test_returns_0_when_up_to_date(self):
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(
                d, make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"}, features=["mympd"])
            )
            out = os.path.join(d, "upgrades.json")
            with (
                patch.object(manifest, "fetch_manifest", return_value=man),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                rc = check.run_check(check.CheckOptions(state=state_path, output=out))
        self.assertEqual(rc, 0)

    def test_invalid_state_returns_2(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "state.json")
            with open(path, "w") as f:
                json.dump({"odios": "2026.5.0"}, f)
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                rc = check.run_check(check.CheckOptions(state=path, output=path + ".out"))
        self.assertEqual(rc, 2)
        self.assertIn("Error reading state", err.getvalue())

    def test_manifest_fetch_failure_returns_2(self):
        with tempfile.TemporaryDirectory() as d:
            state_path = write_state(d, make_state())
            with patch.object(manifest, "fetch_manifest", return_value=None):
                rc = check.run_check(check.CheckOptions(state=state_path, output="/x"))
        self.assertEqual(rc, 2)
