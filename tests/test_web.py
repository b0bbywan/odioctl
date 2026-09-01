import contextlib
import io
import json
import os
import socket
import subprocess
import tempfile
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import replace
from typing import Any
from unittest.mock import patch

from odioctl import components, dac, manifest
from odioctl.web import server
from odioctl.web.config import WebConfig
from odioctl.web.services import Services
from tests._helpers import make_state, write_state

CONFIG = "dtparam=audio=on\n[all]\nenable_uart=1\n"


class WebTestCase(unittest.TestCase):
    """Boots a real ThreadingHTTPServer on 127.0.0.1:0 with tmp state/config."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.state_path = write_state(
            self.tmp.name,
            make_state(
                roles={"mpd": "1", "common": "1"},
                features=["tidal", "mympd"],
                target_user="alice",
            ),
        )
        self.config = os.path.join(self.tmp.name, "config.txt")
        with open(self.config, "w") as f:
            f.write(CONFIG)
        self.flag = os.path.join(self.tmp.name, "reboot-required")
        p = patch.object(dac, "REBOOT_FLAG", self.flag)
        p.start()
        self.addCleanup(p.stop)

        self.calls: list[list[str]] = []
        self.user_calls: list[list[str]] = []
        self.upgrades = os.path.join(self.tmp.name, "upgrades.json")
        cfg = WebConfig(
            bind="127.0.0.1", port=0, state_path=self.state_path, config_txt=self.config
        )
        self.assertEqual(cfg.resolved_upgrades_path(), self.upgrades)
        # No network in tests: `check.refresh` after a toggle sees this manifest.
        self.manifest: Any = {"odios": "2026.5.0", "roles": {"mpd": "1", "common": "1"}}
        fetch_patch = patch.object(
            manifest, "fetch_manifest", side_effect=lambda _url: self.manifest
        )
        fetch_patch.start()
        self.addCleanup(fetch_patch.stop)

        def fake_user_run(args: list[str]) -> subprocess.CompletedProcess[str]:
            self.user_calls.append(args)
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        def fake_privileged(args: list[str]) -> subprocess.CompletedProcess[str]:
            # Stand-in for `sudo -n odioctl dac …`: run the same code path in-process.
            self.calls.append(args)
            from odioctl import cli

            rc = cli.main(args)
            return subprocess.CompletedProcess(args, rc, stdout="ok", stderr="")

        # Component actions run a real child process (a shell stand-in for
        # `qbzd login`): the URL is lifted off its stdout while it keeps running.
        self.spawns: list[list[str]] = []
        self.procs: list[subprocess.Popen[str]] = []
        self.script = "echo 'paste this URL:'; echo '  https://qobuz.test/oauth?id=1'; sleep 30"

        def fake_spawn(argv: list[str]) -> subprocess.Popen[str]:
            self.spawns.append(argv)
            proc = subprocess.Popen(
                ["sh", "-c", self.script],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                stdin=subprocess.DEVNULL,
                text=True,
                bufsize=1,
            )
            self.procs.append(proc)
            return proc

        self.addCleanup(self._kill_children)
        self.services = Services(
            cfg,
            privileged_run=fake_privileged,
            user_run=fake_user_run,
            action_spawn=fake_spawn,
        )
        self.srv = server.make_server(cfg, self.services)
        self.thread = threading.Thread(
            target=self.srv.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True
        )
        self.thread.start()
        self.addCleanup(self.srv.server_close)
        self.addCleanup(self.srv.shutdown)
        self.base = f"http://127.0.0.1:{self.srv.server_address[1]}"
        self.opener = urllib.request.build_opener()

    def get(self, path: str = "/") -> tuple[int, str]:
        try:
            with self.opener.open(self.base + path, timeout=5) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def post(self, path: str, form: dict[str, str], *, token: bool = True) -> tuple[int, str]:
        """Returns (status, body) — POST re-renders the page with a banner."""
        data = dict(form)
        if token:
            data.setdefault("token", self.services.token)
        body = urllib.parse.urlencode(data).encode()
        req = urllib.request.Request(
            self.base + path,
            data=body,
            method="POST",
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        try:
            with self.opener.open(req, timeout=5) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def _kill_children(self) -> None:
        for proc in self.procs:
            if proc.poll() is None:
                proc.kill()
            proc.wait()

    def write_roles(self, **roles: str) -> None:
        write_state(
            self.tmp.name,
            make_state(
                roles={"mpd": "1", "common": "1", **roles},
                features=["tidal", "mympd"],
                target_user="alice",
            ),
        )

    def state(self) -> dict[str, Any]:
        with open(self.state_path) as f:
            data: dict[str, Any] = json.load(f)
        return data


class PageTests(WebTestCase):
    def test_index_renders_components_and_dac(self):
        code, body = self.get("/")
        self.assertEqual(code, 200)
        self.assertIn("<title>odio Settings</title>", body)
        self.assertIn(">odio 2026.5.0</span>", body)
        # the logo goes back to odio-ui on the box, by the name the browser used
        self.assertIn(
            '<a href="http://127.0.0.1:8018/ui" title="odio player">'
            '<img src="/static/logo.png" alt="odio logo"></a>',
            body,
        )
        # install mode and target user belong to the components section, not the header
        self.assertIn("user <b>alice</b> (<b>image</b> install)", body)
        self.assertNotIn("pwa.odio.love", body)
        # role row with a Disable button, infra role without one
        self.assertIn('name="name" value="mpd"', body)
        self.assertIn(">Disable<", body)
        self.assertNotIn('name="name" value="common"', body)
        self.assertIn("Always installed: Base system", body)
        self.assertNotIn('name="name" value="upgrade"', body)
        self.assertIn("<h3>Playback</h3>", body)
        # Upgrade leads: it is what the operator came for, and it always
        # renders something (a warning, "up to date", or the pending list).
        self.assertLess(body.index("<h2>Upgrade</h2>"), body.index("<h2>Components</h2>"))
        self.assertLess(body.index("<h2>Components</h2>"), body.index("<h2>DAC</h2>"))
        self.assertIn(">Skip<", body)
        # feature nested under its parent role
        self.assertIn('class="card child"', body)
        # DAC select with current onboard selected
        self.assertIn('<option value="onboard" selected>', body)
        self.assertNotIn(">Reset<", body)
        # token embedded, no JS anywhere
        self.assertIn(self.services.token, body)
        self.assertNotIn("<script", body)

    def test_index_survives_broken_state(self):
        os.unlink(self.state_path)
        code, body = self.get("/")
        self.assertEqual(code, 200)
        self.assertIn("not found", body)
        self.assertIn("<h2>DAC</h2>", body)

    def test_unrecognised_unmanaged_overlay_is_shown(self):
        with open(self.config, "w") as f:
            f.write("dtparam=audio=off\n")
        _, body = self.get("/")
        self.assertIn("Unrecognised audio configuration", body)
        self.assertIn("dtparam=audio=off", body)
        self.assertNotIn("Audio lines outside the odioctl block", body)

    def test_stray_lines_next_to_managed_block_are_flagged(self):
        self.post("/dac", {"id": "hifiberry-dac"})
        with open(self.config, "a") as f:
            f.write("dtoverlay=hifiberry-dacplus\n")
        _, body = self.get("/")
        self.assertIn("Audio lines outside the odioctl block", body)
        self.assertIn("dtoverlay=hifiberry-dacplus", body)

    def test_static_assets(self):
        code, css = self.get("/static/style.css")
        self.assertEqual(code, 200)
        self.assertIn("--lime", css)
        self.assertEqual(self.get("/static/nope.css")[0], 404)
        self.assertEqual(self.get("/static/../server.py")[0], 404)
        self.assertEqual(self.post("/static/style.css", {})[0], 404)

    def test_unknown_paths_are_404(self):
        self.assertEqual(self.get("/api/status")[0], 404)
        self.assertEqual(self.post("/nope", {})[0], 404)

    def test_wrong_verb_on_known_path_is_405(self):
        for path in ("/components", "/dac", "/dac/unset"):
            with self.subTest(path=path):
                code, _ = self.get(path)
                self.assertEqual(code, 405)
        self.assertEqual(self.post("/", {})[0], 405)
        req = urllib.request.Request(self.base + "/dac", method="GET")
        with self.assertRaises(urllib.error.HTTPError) as cm:
            self.opener.open(req, timeout=5)
        self.assertEqual(cm.exception.headers.get("Allow"), "POST")


class ComponentFormTests(WebTestCase):
    def test_disable_and_enable_role(self):
        code, body = self.post("/components", {"kind": "role", "name": "mpd", "enabled": "0"})
        self.assertEqual(code, 200)
        self.assertIn('class="banner ok">MPD disabled', body)
        self.assertIn('class="chip excluded">Disabled<', body)
        self.assertIn(">Enable<", body)
        st = self.state()
        self.assertEqual(st["roles_excluded"], ["mpd"])
        self.assertNotIn("mpd", st["roles"])
        code, body = self.post("/components", {"kind": "role", "name": "mpd", "enabled": "1"})
        self.assertEqual(code, 200)
        self.assertEqual(self.state()["roles_excluded"], [])
        # mpd is now "default" (not in roles, not excluded) → installs on next upgrade
        self.assertIn("Will install on next upgrade", body)
        self.assertIn("MPD enabled — it will be installed by the next upgrade", body)
        with open(self.upgrades) as f:
            report = json.load(f)
        self.assertTrue(report["upgrade_available"])
        self.assertIn("role:mpd", report["pending_components"])
        self.assertIn("<li>install MPD</li>", body)
        self.assertIn(">Apply now<", body)
        self.assertIn('href="http://127.0.0.1:8018/ui"', body)

    def test_enable_opt_in_role_writes_an_explicit_yes(self):
        self.manifest = {
            "odios": "2026.5.0",
            "roles": {"mpd": "1", "common": "1", "qbzd": "2026.9.0b1"},
        }
        # qbzd is in neither state list: off, and the button offers to enable it.
        _, body = self.get()
        self.assertIn("Qobuz Connect", body)
        self.assertIn(
            'name="name" value="qbzd"><input type="hidden" name="enabled" value="1">'
            '<button class="small" type="submit">Enable</button>',
            body,
        )
        code, body = self.post("/components", {"kind": "role", "name": "qbzd", "enabled": "1"})
        self.assertEqual(code, 200)
        # roles carries the explicit Y (empty version until install.sh writes one)
        self.assertEqual(self.state()["roles"]["qbzd"], "")
        self.assertEqual(self.state()["roles_excluded"], [])
        self.assertIn("Will install on next upgrade", body)
        self.assertIn("<li>install Qobuz Connect</li>", body)
        with open(self.upgrades) as f:
            report = json.load(f)
        self.assertTrue(report["upgrade_available"])
        self.assertEqual(report["pending_components"], ["role:qbzd"])
        self.assertEqual(report["roles"], [])
        code, _ = self.post("/components", {"kind": "role", "name": "qbzd", "enabled": "0"})
        self.assertEqual(code, 200)
        self.assertNotIn("qbzd", self.state()["roles"])
        self.assertEqual(self.state()["roles_excluded"], ["qbzd"])

    def test_disable_feature(self):
        code, _ = self.post("/components", {"kind": "feature", "name": "tidal", "enabled": "0"})
        self.assertEqual(code, 200)
        self.assertEqual(self.state()["features_excluded"], ["tidal"])

    def test_infra_role_is_refused_with_error_banner(self):
        code, body = self.post("/components", {"kind": "role", "name": "common", "enabled": "0"})
        self.assertEqual(code, 200)
        self.assertIn('class="banner err">', body)
        self.assertIn("infrastructure", body)
        self.assertEqual(self.state()["roles_excluded"], [])

    def test_unknown_name_is_refused(self):
        code, body = self.post("/components", {"kind": "role", "name": "nope", "enabled": "0"})
        self.assertEqual(code, 200)
        self.assertIn("unknown role", body)

    def test_missing_token_is_403_and_changes_nothing(self):
        code, body = self.post(
            "/components", {"kind": "role", "name": "mpd", "enabled": "0"}, token=False
        )
        self.assertEqual(code, 403)
        self.assertIn("token", body)
        code, _ = self.post(
            "/components", {"kind": "role", "name": "mpd", "enabled": "0", "token": "wrong"}
        )
        self.assertEqual(code, 403)
        self.assertEqual(self.state()["roles_excluded"], [])

    def test_non_form_content_type_is_refused(self):
        req = urllib.request.Request(
            self.base + "/components",
            data=b'{"x":1}',
            method="POST",
            headers={"Content-Type": "application/json"},
        )
        with self.opener.open(req, timeout=5) as resp:
            body = resp.read().decode()
        self.assertIn("expected a form submission", body)
        self.assertEqual(self.state()["roles_excluded"], [])


# A catalog action of the test's own: the machinery must not depend on which
# components declare one today. `mpd` is installed in the base state, `spotifyd`
# is not — same action on both covers the installed/not-installed split.
TEST_ACTION = components.Action(
    id="login",
    label="Log in",
    description="Sign in to the service",
    argv=("acmed", "login", "--callback-host", "{host}"),
    link_label="Open the sign-in page",
    link_note="valid 5 minutes",
)


def with_test_actions() -> list[Any]:
    """Put the action under test on mpd and spotifyd, and take the catalog's own
    out of the way, so these tests count only what they installed themselves."""
    return [
        patch.dict(
            components.ROLE_CATALOG,
            {
                name: replace(components.ROLE_CATALOG[name], actions=(TEST_ACTION,))
                for name in ("mpd", "spotifyd")
            },
        ),
        patch.dict(
            components.FEATURE_CATALOG,
            {name: replace(info, actions=()) for name, info in components.FEATURE_CATALOG.items()},
        ),
    ]


class ComponentActionTests(WebTestCase):
    """The box runs the command for the operator — no shell on the box needed."""

    def setUp(self):
        super().setUp()
        for patcher in with_test_actions():
            patcher.start()
            self.addCleanup(patcher.stop)

    def post_login(self, **over: str) -> tuple[int, str]:
        form = {"kind": "role", "name": "mpd", "action": "login"} | over
        return self.post("/components/action", form)

    def test_button_needs_an_installed_component(self):
        _, body = self.get("/")
        # mpd is installed, spotifyd is not — one button, on the right row
        self.assertEqual(body.count('action="/components/action"'), 1)
        self.assertIn(
            'name="name" value="mpd"><input type="hidden" name="action" value="login">', body
        )
        self.assertEqual(self.spawns, [])  # rendering a button starts nothing

    def test_button_sits_under_the_description_not_in_the_status_pair(self):
        _, body = self.get("/")
        # its own line in the left column, right after the description
        self.assertIn(
            '<small>Music Player Daemon: local library, CDs, web radios</small><form class="run"',
            body,
        )
        # nothing wedged between the status chip and the enable/disable button
        self.assertIn(
            '<span class="chip installed">Installed</span>'
            '<form method="post" action="/components">',
            body,
        )

    def test_link_is_lifted_off_stdout_and_shown(self):
        code, body = self.post_login()
        self.assertEqual(code, 200)
        # the callback host is the name the browser reached the box by
        self.assertEqual(self.spawns, [["acmed", "login", "--callback-host", "127.0.0.1"]])
        self.assertIn('href="https://qobuz.test/oauth?id=1"', body)
        self.assertIn(">Open the sign-in page</a>", body)
        self.assertIn("Log in: open the link below to finish.", body)
        # the process keeps waiting for the callback, so a plain reload keeps it
        _, body = self.get("/")
        self.assertIn('href="https://qobuz.test/oauth?id=1"', body)
        self.assertIsNone(self.procs[0].poll())

    def test_second_click_reuses_the_running_command(self):
        self.post_login()
        code, body = self.post_login()
        self.assertEqual(code, 200)
        self.assertEqual(len(self.spawns), 1)
        self.assertIn("already running", body)
        self.assertIn('href="https://qobuz.test/oauth?id=1"', body)

    def test_finished_run_becomes_a_note_on_the_next_render(self):
        self.script = "echo https://qobuz.test/oauth?id=2; exit 0"
        # the banner is the same either way; the row races the exit, so only the
        # state after the process is reaped is asserted
        _, body = self.post_login()
        self.assertIn("Log in: open the link below to finish.", body)
        self.procs[0].wait(timeout=5)
        _, body = self.get("/")
        self.assertIn("Log in: Done.", body)
        self.assertNotIn("https://qobuz.test/oauth?id=2", body)

    def test_what_the_command_prints_after_the_link_is_never_recorded(self):
        # Past the link, a login helper prints the credential it just obtained
        # (upmpdcli's Tidal one dumps the access and refresh tokens). Draining
        # continues so the child never wedges on a full pipe; recording stops.
        self.script = (
            "echo 'paste this URL:'; echo 'https://qobuz.test/oauth?id=1'; "
            'echo \'"refresh_token": "s3cr3t"\'; exit 4'
        )
        _, body = self.post_login()
        self.assertNotIn("s3cr3t", body)
        self.procs[0].wait(timeout=5)
        _, body = self.get("/")
        self.assertIn("Log in: Failed (exit 4).", body)
        self.assertNotIn("s3cr3t", body)

    def test_failure_shows_the_output_in_the_modal(self):
        self.script = "echo 'no app id'; exit 3"
        code, body = self.post_login()
        self.assertEqual(code, 200)
        self.assertIn('class="banner err">Log in failed (exit 3)', body)
        self.assertIn('role="dialog"', body)
        self.assertIn("<pre>no app id</pre>", body)
        self.assertNotIn("qobuz.test", body)

    def test_the_modal_carries_the_output_and_is_gone_on_reload(self):
        _, body = self.post_login()
        self.assertIn('role="dialog"', body)
        # the command's own output, verbatim, plus the link as a button
        self.assertIn("paste this URL:", body)
        self.assertIn('<a class="btn primary" href="https://qobuz.test/oauth?id=1"', body)
        self.assertIn('<a class="btn" href="/">Close</a>', body)
        # Close is a plain link back to the page: the modal is shown once, the
        # pending link on the component row is what stays.
        _, body = self.get("/")
        self.assertNotIn('role="dialog"', body)
        self.assertIn('href="https://qobuz.test/oauth?id=1"', body)

    def test_unknown_action_or_component_never_spawns(self):
        for form in (
            {"action": "rm -rf /"},
            {"action": ""},
            {"name": "snapclient"},  # a component with no actions
            {"kind": "feature"},
            {"kind": "wat"},
        ):
            code, body = self.post_login(**form)
            self.assertEqual(code, 200)
            self.assertIn('class="banner err"', body)
        self.assertEqual(self.spawns, [])

    def test_component_not_installed_is_refused(self):
        code, body = self.post_login(name="spotifyd")
        self.assertEqual(code, 200)
        self.assertIn("Spotify Connect is not installed", body)
        self.assertEqual(self.spawns, [])

    def test_missing_token_is_403_and_never_spawns(self):
        code, _ = self.post(
            "/components/action",
            {"kind": "role", "name": "mpd", "action": "login"},
            token=False,
        )
        self.assertEqual(code, 403)
        self.assertEqual(self.spawns, [])


class QbzdLoginTests(WebTestCase):
    """The real catalog entry, end to end through the page."""

    def test_login_button_and_link_for_an_installed_qbzd(self):
        self.write_roles(qbzd="2026.9.0b1")
        _, body = self.get("/")
        self.assertIn(">Log in to Qobuz<", body)
        code, body = self.post(
            "/components/action", {"kind": "role", "name": "qbzd", "action": "login"}
        )
        self.assertEqual(code, 200)
        self.assertEqual(self.spawns, [["qbzd", "login", "--callback-host", "127.0.0.1"]])
        self.assertIn(">Open the Qobuz sign-in page</a>", body)


class TidalLoginTests(WebTestCase):
    """The real catalog entry, end to end through the page."""

    def test_login_button_spawns_the_helper_with_the_home_expanded(self):
        _, body = self.get("/")
        self.assertIn(">Log in to Tidal<", body)
        code, body = self.post(
            "/components/action", {"kind": "feature", "name": "tidal", "action": "login"}
        )
        self.assertEqual(code, 200)
        creds = os.path.expanduser("~/.cache/upmpdcli/tidal/oauth2.credentials.json")
        self.assertEqual(
            self.spawns,
            [
                [
                    "python3",
                    "-u",
                    "/usr/share/upmpdcli/cdplugins/tidal/get_credentials.py",
                    "-f",
                    creds,
                ]
            ],
        )
        self.assertNotIn("{home}", " ".join(self.spawns[0]))
        self.assertIn(">Open the Tidal sign-in page</a>", body)


class UpgradeTests(WebTestCase):
    def test_no_report_yet(self):
        _, body = self.get("/")
        self.assertIn("No upgrade check has run yet", body)
        self.assertNotIn(">Apply now<", body)
        code, body = self.post("/upgrade", {})
        self.assertEqual(code, 200)
        self.assertIn("nothing to apply", body)
        self.assertEqual(self.user_calls, [])

    def test_up_to_date_hides_the_button(self):
        # Disabling writes a report but is never pending → nothing to apply.
        self.post("/components", {"kind": "feature", "name": "tidal", "enabled": "0"})
        _, body = self.get("/")
        self.assertIn("Up to date — odio 2026.5.0", body)
        self.assertNotIn(">Apply now<", body)

    def test_apply_now_starts_the_user_unit(self):
        self.post("/components", {"kind": "role", "name": "mpd", "enabled": "0"})
        self.post("/components", {"kind": "role", "name": "mpd", "enabled": "1"})
        code, body = self.post("/upgrade", {})
        self.assertEqual(code, 200)
        self.assertIn("Upgrade started", body)
        self.assertEqual(
            self.user_calls,
            [["systemctl", "--user", "start", "--no-block", "odio-upgrade.service"]],
        )

    def test_toggle_works_offline_with_cached_manifest(self):
        self.post("/components", {"kind": "feature", "name": "mympd", "enabled": "0"})
        self.manifest = None  # network gone; upgrades.json now holds the manifest
        self.post("/components", {"kind": "feature", "name": "mympd", "enabled": "1"})
        with open(self.upgrades) as f:
            report = json.load(f)
        self.assertEqual(report["pending_components"], ["feature:mympd"])
        self.assertTrue(report["upgrade_available"])


class DacFormTests(WebTestCase):
    def test_set_runs_privileged_and_marks_reboot(self):
        code, body = self.post("/dac", {"id": "hifiberry-dacplus-std"})
        self.assertEqual(code, 200)
        self.assertIn("DAC set to hifiberry-dacplus-std", body)
        self.assertEqual(
            self.calls, [["dac", "set", "hifiberry-dacplus-std", "--config", self.config]]
        )
        with open(self.config) as f:
            self.assertEqual(dac.parse(f.read()).current, "hifiberry-dacplus-std")
        self.assertIn("A reboot is required", body)
        self.assertIn('<option value="hifiberry-dacplus-std" selected>', body)
        self.assertIn("managed by odioctl", body)
        self.assertIn(">Reset<", body)

    def test_unset_restores(self):
        self.post("/dac", {"id": "hifiberry-dacplus-std"})
        code, _ = self.post("/dac/unset", {})
        self.assertEqual(code, 200)
        self.assertEqual(self.calls[-1][:2], ["dac", "unset"])
        with open(self.config) as f:
            self.assertEqual(f.read(), CONFIG)

    def test_unknown_id_is_refused_and_never_escalates(self):
        code, body = self.post("/dac", {"id": "evil; rm -rf /"})
        self.assertEqual(code, 200)
        self.assertIn("unknown DAC id", body)
        self.assertEqual(self.calls, [])

    def test_empty_id_is_refused_not_treated_as_unset(self):
        code, body = self.post("/dac", {"id": ""})
        self.assertEqual(code, 200)
        self.assertIn("no DAC selected", body)
        self.assertEqual(self.calls, [])

    def test_privileged_failure_shows_error(self):
        def failing(args: list[str]) -> subprocess.CompletedProcess[str]:
            return subprocess.CompletedProcess(
                args, 1, stdout="", stderr="sudo: a password is required"
            )

        self.services.privileged_run = failing
        code, body = self.post("/dac", {"id": "hifiberry-dac"})
        self.assertEqual(code, 200)
        self.assertIn("password is required", body)

    def test_no_config_txt(self):
        os.unlink(self.config)
        _, body = self.get("/")
        self.assertIn("No config.txt found", body)


class SocketActivationTests(WebTestCase):
    """`odioctl web` under odioctl-web.socket: systemd binds, we inherit fd 3."""

    @contextlib.contextmanager
    def as_listen_fd(self, sock: socket.socket):
        """Put `sock` on fd 3, where sd_listen_fds(3) says the handover lands."""
        fd = server.SD_LISTEN_FDS_START
        try:
            saved = os.dup(fd)
        except OSError:
            saved = None
        os.dup2(sock.fileno(), fd)
        try:
            yield
        finally:
            if saved is None:
                os.close(fd)
            else:
                os.dup2(saved, fd)
                os.close(saved)

    def listener(self) -> socket.socket:
        lst = socket.socket()
        lst.bind(("127.0.0.1", 0))
        lst.listen(5)
        self.addCleanup(lst.close)
        return lst

    def test_no_handover_means_no_activation(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertIsNone(server.systemd_socket())

    def test_handover_addressed_to_a_parent_is_ignored(self):
        env = {"LISTEN_PID": str(os.getpid() + 1), "LISTEN_FDS": "1"}
        with patch.dict(os.environ, env, clear=True):
            self.assertIsNone(server.systemd_socket())
            # …and never forwarded to `sudo odioctl dac …`.
            self.assertNotIn("LISTEN_PID", os.environ)
            self.assertNotIn("LISTEN_FDS", os.environ)

    def test_zero_sockets_means_no_activation(self):
        env = {"LISTEN_PID": str(os.getpid()), "LISTEN_FDS": "0"}
        with patch.dict(os.environ, env, clear=True):
            self.assertIsNone(server.systemd_socket())

    def test_unexpected_handover_is_refused(self):
        for fds in ("2", "not-a-number"):
            with self.subTest(fds=fds):
                env = {"LISTEN_PID": str(os.getpid()), "LISTEN_FDS": fds}
                with patch.dict(os.environ, env, clear=True):  # noqa: SIM117
                    with self.assertRaises(server.ActivationError):
                        server.systemd_socket()

    def test_the_inherited_socket_is_the_one_systemd_opened(self):
        lst = self.listener()
        env = {"LISTEN_PID": str(os.getpid()), "LISTEN_FDS": "1"}
        with self.as_listen_fd(lst), patch.dict(os.environ, env, clear=True):
            sock = server.systemd_socket()
            self.assertIsNotNone(sock)
            assert sock is not None
            self.assertEqual(sock.getsockname(), lst.getsockname())
            self.assertNotIn("LISTEN_PID", os.environ)
            sock.detach()  # fd 3 goes back to as_listen_fd()

    def test_serves_on_a_socket_it_did_not_bind(self):
        lst = self.listener()
        srv = server.make_server(self.services.cfg, self.services, sock=lst)
        self.addCleanup(srv.server_close)
        self.assertEqual(srv.socket.fileno(), lst.fileno())
        self.assertEqual(srv.server_port, lst.getsockname()[1])
        threading.Thread(
            target=srv.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True
        ).start()
        self.addCleanup(srv.shutdown)
        with urllib.request.urlopen(f"http://127.0.0.1:{srv.server_port}/", timeout=5) as resp:
            self.assertEqual(resp.status, 200)
            self.assertIn("<h1>Settings</h1>", resp.read().decode())

    def test_serve_refuses_a_broken_handover(self):
        env = {"LISTEN_PID": str(os.getpid()), "LISTEN_FDS": "2"}
        err = io.StringIO()
        with patch.dict(os.environ, env, clear=True), contextlib.redirect_stderr(err):
            rc = server.serve(self.services.cfg)
        self.assertEqual(rc, 2)
        self.assertIn("got 2", err.getvalue())


class WebCliTests(unittest.TestCase):
    def test_web_from_args_builds_config(self):
        import argparse

        captured: dict[str, Any] = {}

        def fake_serve(cfg: WebConfig) -> int:
            captured["cfg"] = cfg
            return 0

        ns = argparse.Namespace(bind="127.0.0.1", port=9999, state="/s", config="/c", upgrades=None)
        with patch.object(server, "serve", fake_serve):
            self.assertEqual(server.web_from_args(ns), 0)
        cfg = captured["cfg"]
        self.assertEqual(
            (cfg.bind, cfg.port, cfg.state_path, cfg.config_txt), ("127.0.0.1", 9999, "/s", "/c")
        )
