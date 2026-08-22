import contextlib
import io
import json
import os
import tempfile
import unittest
from unittest.mock import patch

from odioctl import cli, manifest
from odioctl.manifest import Manifest
from odioctl.state import State
from odioctl.upgrade import apply
from tests._helpers import make_state, write_state


class DeriveInstallEnvTests(unittest.TestCase):
    def test_installed_role_maps_to_Y(self):
        env = apply.derive_install_env(make_state(roles={"pulseaudio": "x"}))
        self.assertEqual(env["INSTALL_PULSEAUDIO"], "Y")

    def test_excluded_role_maps_to_N(self):
        env = apply.derive_install_env(make_state(roles_excluded=["spotifyd"]))
        self.assertEqual(env["INSTALL_SPOTIFYD"], "N")

    def test_role_absent_from_both_is_not_emitted(self):
        # Opt-out semantic: anything not in roles/roles_excluded is left unset
        # so install.sh's own defaults (Y for optionals in upgrade-era releases)
        # take over. That's how a role added after odioctl was written
        # self-installs on upgrade.
        env = apply.derive_install_env(make_state())
        self.assertNotIn("INSTALL_BRANDING", env)
        self.assertNotIn("INSTALL_MPD", env)

    def test_feature_absent_from_both_is_not_emitted(self):
        env = apply.derive_install_env(make_state())
        self.assertNotIn("INSTALL_TIDAL", env)

    def test_excluded_feature_maps_to_N(self):
        env = apply.derive_install_env(make_state(features_excluded=["tidal"]))
        self.assertEqual(env["INSTALL_TIDAL"], "N")

    def test_feature_in_features_maps_to_Y(self):
        env = apply.derive_install_env(make_state(features=["tidal"]))
        self.assertEqual(env["INSTALL_TIDAL"], "Y")

    def test_branding_role_maps_to_install_branding(self):
        env = apply.derive_install_env(make_state(roles={"branding": "x"}))
        self.assertEqual(env["INSTALL_BRANDING"], "Y")

    def test_empty_state_emits_nothing(self):
        # No information in state.json → no INSTALL_* keys; install.sh's own
        # defaults govern every flag.
        self.assertEqual(apply.derive_install_env(make_state()), {})


class DeriveRunEnvTests(unittest.TestCase):
    @staticmethod
    def _state(
        roles: dict[str, str],
        excluded: list[str] | None = None,
        odios: str = "2026.5.0",
    ) -> State:
        return make_state(roles=roles, roles_excluded=excluded, odios=odios)

    @staticmethod
    def _manifest(roles: dict[str, str], odios: str = "2026.5.0") -> Manifest:
        return {"odios": odios, "roles": roles}

    def _install_env(self, **flags: str) -> dict[str, str]:
        return {f"INSTALL_{r.upper()}": v for r, v in flags.items()}

    def test_no_manifest_returns_empty(self):
        # Manifest fetch failed → emit nothing → install.sh defaults to
        # RUN_X=INSTALL_X for every role.
        self.assertEqual(apply.derive_run_env(self._state({"mpd": "2026.5.0"}), None, {}), {})

    def test_unchanged_role_emits_run_n(self):
        st = self._state({"mpd": "2026.5.0"})
        man = self._manifest({"mpd": "2026.5.0"})
        env = apply.derive_run_env(st, man, self._install_env(mpd="Y"))
        self.assertEqual(env.get("RUN_MPD"), "N")

    def test_bumped_role_is_not_emitted(self):
        # Asymmetric contract: only N is exported. A bumped role gets no
        # RUN_X key — install.sh's RUN_X=${RUN_X:-$INSTALL_X} default keeps
        # it Y so the role runs.
        st = self._state({"mpd": "2026.4.0"})
        man = self._manifest({"mpd": "2026.5.0"})
        env = apply.derive_run_env(st, man, self._install_env(mpd="Y"))
        self.assertNotIn("RUN_MPD", env)

    def test_excluded_role_is_skipped(self):
        st = self._state({}, excluded=["spotifyd"])
        man = self._manifest({"spotifyd": "0.4.4"})
        env = apply.derive_run_env(st, man, self._install_env(spotifyd="N"))
        self.assertNotIn("RUN_SPOTIFYD", env)

    def test_role_new_in_target_is_not_emitted(self):
        # Role exists in target manifest but missing from state.json (a new
        # role added in this release). installed=None → no RUN_X=N → install.sh
        # default Y → role runs and gets installed for the first time.
        st = self._state({"mpd": "2026.5.0"})
        man = self._manifest({"mpd": "2026.5.0", "spotifyd": "0.4.4"})
        env = apply.derive_run_env(st, man, self._install_env(mpd="Y"))
        self.assertNotIn("RUN_SPOTIFYD", env)

    def test_role_missing_from_manifest_is_not_emitted(self):
        st = self._state({"snapclient": "0.27.0"})
        man = self._manifest({})
        env = apply.derive_run_env(st, man, self._install_env(snapclient="Y"))
        self.assertNotIn("RUN_SNAPCLIENT", env)

    def test_common_emission_is_overridden_by_install_sh(self):
        # `derive_run_env` itself emits RUN_COMMON=N for an unchanged common
        # — the loop has no special case. install.sh hard-sets RUN_COMMON=Y
        # *after* sourcing odioctl's env, which guarantees common always runs.
        st = self._state({"common": "2026.5.0"})
        man = self._manifest({"common": "2026.5.0"})
        env = apply.derive_run_env(st, man, {})
        self.assertEqual(env.get("RUN_COMMON"), "N")

    def test_emits_n_for_every_unchanged_role(self):
        roles = {"mpd": "2026.5.0", "odio_api": "2026.5.0", "upgrade": "2026.5.0"}
        install_env = {f"INSTALL_{r.upper()}": "Y" for r in roles}
        env = apply.derive_run_env(self._state(roles), self._manifest(roles), install_env)
        for role in roles:
            self.assertEqual(env[f"RUN_{role.upper()}"], "N", role)

    def test_unparseable_version_string_is_treated_as_outdated(self):
        # parse_version("garbage") → (0,) → always less than the manifest
        # version → role re-runs (no RUN_X=N emitted).
        st = self._state({"mpd": "garbage"})
        man = self._manifest({"mpd": "2026.5.0"})
        env = apply.derive_run_env(st, man, self._install_env(mpd="Y"))
        self.assertNotIn("RUN_MPD", env)

    def test_role_ahead_of_state_odios_re_runs(self):
        # PR-iteration trap: role bumped to 2026.5.0b1 on a previous PR run,
        # state.odios stayed at the pre-tag dev describe. Manifest still says
        # 2026.5.0b1, so the bare target == installed comparison would skip —
        # but `installed` is ahead of state.odios, meaning the role file is in
        # flight. Force re-run.
        st = self._state({"bluetooth": "2026.5.0b1"}, odios="2026.4.2b2-8-g6375a44")
        man = self._manifest({"bluetooth": "2026.5.0b1"})
        env = apply.derive_run_env(st, man, self._install_env(bluetooth="Y"))
        self.assertNotIn("RUN_BLUETOOTH", env)

    def test_role_at_or_below_state_odios_still_skips_when_unchanged(self):
        st = self._state({"shairport_sync": "2026.4.1rc1"}, odios="2026.4.2b2")
        man = self._manifest({"shairport_sync": "2026.4.1rc1"})
        env = apply.derive_run_env(st, man, self._install_env(shairport_sync="Y"))
        self.assertEqual(env.get("RUN_SHAIRPORT_SYNC"), "N")


class LoadStateTests(unittest.TestCase):
    def test_opts_state_valid_returns_path_and_state(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(roles={"mpd": "2026.4.0"}, target_user="alice"))
            with contextlib.redirect_stdout(io.StringIO()):
                result = apply._load_state(apply.ApplyOptions(state=path))
        assert result is not None
        got_path, st = result
        self.assertEqual(got_path, path)
        self.assertEqual(st["target_user"], "alice")
        self.assertEqual(st["roles"], {"mpd": "2026.4.0"})

    def test_opts_state_missing_returns_none_and_writes_to_stderr(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            result = apply._load_state(apply.ApplyOptions(state="/missing/state.json"))
        self.assertIsNone(result)
        self.assertIn("Error reading /missing/state.json", err.getvalue())

    def test_opts_state_invalid_schema_returns_none(self):
        # Legacy schemas (no target_user, features as dict, …) are not
        # supported: refuse loudly instead of guessing.
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "state.json")
            with open(path, "w") as f:
                json.dump({"odios": "2026.4.0", "roles": {}}, f)
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                result = apply._load_state(apply.ApplyOptions(state=path))
        self.assertIsNone(result)
        self.assertIn("missing required fields", err.getvalue())

    def test_no_opts_state_uses_system_path(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(target_user="alice"))
            with (
                patch.object(apply.state, "SYSTEM_STATE_PATH", path),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                result = apply._load_state(apply.ApplyOptions())
        assert result is not None
        got_path, st = result
        self.assertEqual(got_path, path)
        self.assertEqual(st["target_user"], "alice")

    def test_prints_summary(self):
        with tempfile.TemporaryDirectory() as d:
            path = write_state(d, make_state(roles={"mpd": "x"}, roles_excluded=["spotifyd"]))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                apply._load_state(apply.ApplyOptions(state=path))
        text = out.getvalue()
        self.assertIn(f"state.json read from {path}:", text)
        self.assertIn("roles_excluded:    spotifyd", text)


class BuildApplyEnvTests(unittest.TestCase):
    def test_skipped_roles_are_listed_and_run_n_emitted(self):
        st = make_state(roles={"mpd": "2026.5.0"}, odios="2026.5.0")
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        out = io.StringIO()
        with (
            patch.object(manifest, "fetch_manifest", return_value=man),
            contextlib.redirect_stdout(out),
        ):
            env = apply._build_apply_env(
                st, "2026.5.0", "alice", "/nonexistent/upgrades.json", apply.ApplyOptions()
            )
        self.assertEqual(env["TARGET_USER"], "alice")
        self.assertEqual(env["ODIOS_VERSION"], "2026.5.0")
        self.assertEqual(env.get("RUN_MPD"), "N")
        self.assertIn("skipping unchanged roles: mpd", out.getvalue())

    def test_no_manifest_logs_unavailable_and_emits_no_run_overrides(self):
        st = make_state(roles={"mpd": "2026.4.0"}, odios="2026.4.0")
        out = io.StringIO()
        with (
            patch.object(manifest, "fetch_manifest", return_value=None),
            contextlib.redirect_stdout(out),
        ):
            env = apply._build_apply_env(
                st, "2026.5.0", "alice", "/nonexistent/upgrades.json", apply.ApplyOptions()
            )
        self.assertNotIn("RUN_MPD", env)
        self.assertIn("manifest unavailable", out.getvalue())

    def test_all_roles_bumped_logs_running_everything(self):
        st = make_state(roles={"mpd": "2026.4.0"}, odios="2026.4.0")
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        out = io.StringIO()
        with (
            patch.object(manifest, "fetch_manifest", return_value=man),
            contextlib.redirect_stdout(out),
        ):
            env = apply._build_apply_env(
                st, "2026.5.0", "alice", "/nonexistent/upgrades.json", apply.ApplyOptions()
            )
        self.assertNotIn("RUN_MPD", env)
        self.assertIn("all roles bumped", out.getvalue())

    def test_reinstall_emits_force_scaffold_and_no_run_skips(self):
        # Even with an up-to-date manifest (mpd would normally get RUN_MPD=N),
        # --reinstall suppresses the skip and sets the scaffold force flag.
        st = make_state(roles={"mpd": "2026.5.0"}, odios="2026.5.0")
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        out = io.StringIO()
        with (
            patch.object(manifest, "fetch_manifest", return_value=man),
            contextlib.redirect_stdout(out),
        ):
            env = apply._build_apply_env(
                st,
                "2026.5.0",
                "alice",
                "/nonexistent/upgrades.json",
                apply.ApplyOptions(reinstall=True),
            )
        self.assertNotIn("RUN_MPD", env)
        self.assertEqual(env["ODIOS_FORCE_SCAFFOLD"], "Y")
        self.assertIn("reinstall: running all roles", out.getvalue())

    def test_progress_emits_env(self):
        st = make_state(roles={"mpd": "2026.5.0"}, odios="2026.5.0")
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with (
            patch.object(manifest, "fetch_manifest", return_value=man),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            env = apply._build_apply_env(
                st,
                "2026.5.0",
                "alice",
                "/nonexistent/upgrades.json",
                apply.ApplyOptions(progress=True),
            )
        self.assertEqual(env["ODIOS_PROGRESS"], "Y")

    def test_progress_absent_by_default(self):
        st = make_state(roles={"mpd": "2026.5.0"}, odios="2026.5.0")
        man: Manifest = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with (
            patch.object(manifest, "fetch_manifest", return_value=man),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            env = apply._build_apply_env(
                st, "2026.5.0", "alice", "/nonexistent/upgrades.json", apply.ApplyOptions()
            )
        self.assertNotIn("ODIOS_PROGRESS", env)


class RunApplyTests(unittest.TestCase):
    def _run(self, d: str, **opts: object) -> tuple[int, str]:
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            rc = apply.run_apply(apply.ApplyOptions(state=os.path.join(d, "state.json"), **opts))  # type: ignore[arg-type]
        return rc, out.getvalue()

    def test_dry_run_prints_env_and_does_not_invoke(self):
        with tempfile.TemporaryDirectory() as d:
            write_state(
                d,
                make_state(
                    odios="2026.5.0", roles={"mpd": "2026.5.0"}, roles_excluded=["spotifyd"]
                ),
            )
            with (
                patch.object(manifest, "fetch_manifest", return_value=None),
                patch.object(apply.subprocess, "run") as run,
            ):
                rc, text = self._run(d, dry_run=True, force=True, version="2026.6.0")
        self.assertEqual(rc, 0)
        run.assert_not_called()
        self.assertIn("Upgrading to 2026.6.0 via", text)
        self.assertIn("INSTALL_SPOTIFYD=N", text)
        self.assertIn("TARGET_USER=odio", text)
        self.assertIn("(dry-run, not invoking)", text)

    def test_no_upgrade_reported_returns_0_without_force(self):
        with tempfile.TemporaryDirectory() as d:
            write_state(d, make_state())
            with open(os.path.join(d, "upgrades.json"), "w") as f:
                json.dump({"upgrade_available": False, "latest": "2026.5.0"}, f)
            rc, text = self._run(d)
        self.assertEqual(rc, 0)
        self.assertIn("No upgrade reported", text)

    def test_refuses_downgrade(self):
        with tempfile.TemporaryDirectory() as d:
            write_state(d, make_state(odios="2026.5.0"))
            rc, text = self._run(d, version="2026.4.0", dry_run=True)
        self.assertEqual(rc, 2)
        self.assertIn("Refusing to downgrade", text)

    def test_uses_sibling_upgrades_json_of_explicit_state(self):
        # --state /path/state.json → /path/upgrades.json is the cache; its
        # `latest` drives the target and its manifest is reused offline.
        man: Manifest = {"odios": "2026.6.0", "roles": {"mpd": "2026.6.0"}}
        with tempfile.TemporaryDirectory() as d:
            write_state(d, make_state(odios="2026.5.0", roles={"mpd": "2026.5.0"}))
            with open(os.path.join(d, "upgrades.json"), "w") as f:
                json.dump({"upgrade_available": True, "latest": "2026.6.0", "manifest": man}, f)
            with patch.object(manifest, "fetch_manifest") as fetch:
                rc, text = self._run(d, dry_run=True)
        self.assertEqual(rc, 0)
        fetch.assert_not_called()
        self.assertIn("ODIOS_VERSION=2026.6.0", text)

    def test_missing_state_returns_2(self):
        with tempfile.TemporaryDirectory() as d:
            rc, _ = self._run(d, force=True)
        self.assertEqual(rc, 2)


class CmdApplyArgsTests(unittest.TestCase):
    """`odioctl upgrade apply` argv → ApplyOptions."""

    def _apply(self, *argv: str) -> apply.ApplyOptions:
        with patch.object(apply, "run_apply", return_value=0) as run:
            cli.main(["upgrade", "apply", *argv])
        opts = run.call_args.args[0]
        assert isinstance(opts, apply.ApplyOptions)
        return opts

    def test_reinstall_flag_flows_into_apply_options(self):
        self.assertTrue(self._apply("--reinstall").reinstall)

    def test_reinstall_defaults_false(self):
        self.assertFalse(self._apply().reinstall)

    def test_progress_flag_forces_on(self):
        # explicit --progress wins even with no odio-api socket
        with patch.object(apply, "_odio_api_listening", return_value=False):
            self.assertTrue(self._apply("--progress").progress)

    def test_no_progress_flag_forces_off(self):
        # explicit --no-progress wins even on an instance
        with patch.object(apply, "_odio_api_listening", return_value=True):
            self.assertFalse(self._apply("--no-progress").progress)

    def test_progress_auto_on_when_odio_api_listening(self):
        with patch.object(apply, "_odio_api_listening", return_value=True):
            self.assertTrue(self._apply().progress)

    def test_progress_auto_off_in_ci(self):
        with patch.object(apply, "_odio_api_listening", return_value=False):
            self.assertFalse(self._apply().progress)

    def test_version_state_dry_run_force_flow(self):
        opts = self._apply("--version", "2026.5.0", "--state", "/s", "--dry-run", "--force")
        self.assertEqual(
            (opts.version, opts.state, opts.dry_run, opts.force), ("2026.5.0", "/s", True, True)
        )


class OdioApiListeningTests(unittest.TestCase):
    def test_true_when_socket_exists(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "odio-api"))
            open(os.path.join(d, "odio-api", "upgrade.sock"), "w").close()
            with patch.dict(os.environ, {"XDG_RUNTIME_DIR": d}):
                self.assertTrue(apply._odio_api_listening())

    def test_false_when_socket_missing(self):
        with tempfile.TemporaryDirectory() as d, patch.dict(os.environ, {"XDG_RUNTIME_DIR": d}):
            self.assertFalse(apply._odio_api_listening())

    def test_false_when_runtime_dir_unset(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertFalse(apply._odio_api_listening())
