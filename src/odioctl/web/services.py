"""What the pages can ask the box to do — no HTTP, no HTML.

One operation per form: toggle a component, run a component's own command,
start an upgrade, select a DAC. state.json is edited in this process (it runs
as the odios target user); only config.txt writes escalate, through
`sudo -n odioctl dac set <id>`. Upgrades are never run here — the web process
starts `odio-upgrade.service`, the unit odio-api drives too.

Every subprocess goes through an injected callable (`privileged_run`,
`user_run`, `action_spawn`), so the tests drive the real code path against
stand-ins instead of the box.
"""

from __future__ import annotations

import json
import os
import secrets
import subprocess
import threading
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any, cast

from odioctl import components, dac, state
from odioctl.upgrade import check
from odioctl.web.config import UPGRADE_UNIT, WebConfig

RunFn = Callable[[list[str]], "subprocess.CompletedProcess[str]"]
SpawnFn = Callable[[list[str]], "subprocess.Popen[str]"]

# How long a component action gets to print its link before we give up on it.
# `qbzd login` fetches an app id over the network first, so it is not instant.
ACTION_LINK_TIMEOUT = 15.0


class WebError(Exception):
    """User-facing failure of a form action; rendered as an error banner."""


class TokenError(WebError):
    """Missing or invalid form token; answered with a bare 403, not a re-render."""


def _default_privileged_run(cfg: WebConfig) -> RunFn:
    def run(args: list[str]) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["sudo", "-n", cfg.odioctl_bin, *args],
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )

    return run


def _default_user_run(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, capture_output=True, text=True, timeout=30, check=False)


def _run_checked(run: RunFn, args: list[str], what: str) -> None:
    """Run a subprocess through `run`, turning any failure into a WebError banner."""
    try:
        proc = run(args)
    except (OSError, subprocess.SubprocessError) as e:
        raise WebError(f"cannot run {what}: {e}") from e
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "").strip() or f"exit {proc.returncode}"
        raise WebError(f"{what} failed: {detail}")


def _default_action_spawn(argv: list[str]) -> subprocess.Popen[str]:
    # Same user as this process (the odios target user), no sudo: a component
    # action is exactly what the operator would type on the box.
    return subprocess.Popen(
        argv,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        stdin=subprocess.DEVNULL,
        text=True,
        bufsize=1,
    )


def _read_until_link(run: ActionRun, scheme: str, timeout: float) -> None:
    """Drain the process output until it prints a link, EOF, or `timeout`.

    Keeps draining in the background afterwards: the child writes a few more
    lines when the user follows the link, and a full pipe would wedge it. It
    stops *recording* there, though — what a login helper prints once the user
    is through is the credential it just obtained (upmpdcli's Tidal helper
    dumps the access and refresh tokens), and nothing that lands in `output`
    is worth painting into a browser.
    """
    found = threading.Event()

    def drain() -> None:
        assert run.proc.stdout is not None
        for line in run.proc.stdout:
            if run.url:
                continue
            run.output.append(line)
            token = next((w for w in line.split() if w.startswith(scheme)), "")
            if token:
                run.url = token
                found.set()
        found.set()  # EOF: nothing more is coming

    threading.Thread(target=drain, daemon=True).start()
    found.wait(timeout)


@dataclass
class ActionResult:
    """What an action just did, shown once in the modal of the POST response.

    Popped by `render_page`, so it never survives into the next page load:
    what outlives the request is the pending link on the component row.
    """

    title: str
    output: str
    url: str = ""
    link_label: str = ""


@dataclass
class ActionRun:
    """One started action: its process, its output, and the link it printed."""

    proc: subprocess.Popen[str]
    output: deque[str]
    url: str = ""

    def alive(self) -> bool:
        return self.proc.poll() is None


class Services:
    """Business operations behind the pages (also unit-testable directly)."""

    def __init__(
        self,
        cfg: WebConfig,
        privileged_run: RunFn | None = None,
        user_run: RunFn | None = None,
        action_spawn: SpawnFn | None = None,
    ) -> None:
        self.cfg = cfg
        self.privileged_run = privileged_run or _default_privileged_run(cfg)
        self.user_run = user_run or _default_user_run  # same user, no sudo (systemctl --user)
        self.action_spawn = action_spawn or _default_action_spawn
        self.token = secrets.token_urlsafe(24)
        self._lock = threading.Lock()
        # Started actions outlive their request: `qbzd login` waits up to 300s
        # for the user to follow its link. Keyed by (kind, name, action id).
        self._runs: dict[tuple[str, str, str], ActionRun] = {}
        self._notes: dict[tuple[str, str, str], str] = {}
        self._result: ActionResult | None = None

    # -- reads ----------------------------------------------------------------

    def read_state(self) -> tuple[state.State | None, str | None]:
        """(state, None) or (None, error message)."""
        try:
            return state.read_state(self.cfg.state_path), None
        except FileNotFoundError:
            return None, f"{self.cfg.state_path} not found"
        except (OSError, json.JSONDecodeError, state.StateError) as e:
            return None, f"cannot read state.json: {e}"

    def dac_status(self) -> dict[str, Any]:
        return dac.status(self.cfg.config_txt)

    def upgrade_report(self) -> check.UpgradeReport | None:
        return check.read_report(self.cfg.resolved_upgrades_path())

    # -- writes ---------------------------------------------------------------

    def set_component(self, kind: str, name: str, enabled: bool) -> str:
        if kind not in ("role", "feature"):
            raise WebError(f"unknown component kind {kind!r}")
        with self._lock:
            st, err = self.read_state()
            if st is None:
                raise WebError(err or "state.json unavailable")
            try:
                new = components.set_component(st, cast(components.Kind, kind), name, enabled)
            except components.ComponentError as e:
                raise WebError(str(e)) from e
            try:
                components.save(self.cfg.state_path, new)
            except OSError as e:
                raise WebError(f"cannot write state.json: {e}") from e
            # Keep upgrades.json in step so odio-ui's badge and `upgrade apply`
            # see the pending install without waiting for the daily timer.
            report = check.refresh(
                check.CheckOptions(
                    state=self.cfg.state_path, output=self.cfg.resolved_upgrades_path()
                )
            )
        label = components.label_of(cast(components.Kind, kind), name)
        if not enabled:
            return f"{label} disabled — it stays installed but will no longer be updated."
        if report is not None and f"{kind}:{name}" in report["pending_components"]:
            return f"{label} enabled — it will be installed by the next upgrade (apply it below)."
        return f"{label} enabled."

    def run_action(self, kind: str, name: str, action_id: str, host: str) -> str:
        """Start a catalog action and return the banner text for the page.

        The command is never waited on: `qbzd login` prints its Qobuz URL and
        then holds a listener open until the browser comes back (300s), so we
        read stdout only until the link shows up and leave the process to it.
        The link is then rendered next to the component until it is gone.
        """
        if kind not in ("role", "feature"):
            raise WebError(f"unknown component kind {kind!r}")
        action = components.find_action(cast(components.Kind, kind), name, action_id)
        if action is None:
            raise WebError(f"unknown action {action_id!r} for {name}")
        st, err = self.read_state()
        if st is None:
            raise WebError(err or "state.json unavailable")
        comp = next(
            (c for c in components.list_components(st) if c.kind == kind and c.name == name), None
        )
        if comp is None or comp.status != "installed":
            raise WebError(
                f"{components.label_of(cast(components.Kind, kind), name)} is not installed"
            )

        key = (kind, name, action_id)
        with self._lock:
            run = self._runs.get(key)
            if run is not None and run.alive():
                self._result = ActionResult(
                    title=action.label,
                    output="".join(run.output),
                    url=run.url,
                    link_label=action.link_label,
                )
                return f"{action.label}: already running — the link is below."
            self._notes.pop(key, None)
            argv = [part.format(host=host, home=os.path.expanduser("~")) for part in action.argv]
            try:
                proc = self.action_spawn(argv)
            except (OSError, subprocess.SubprocessError) as e:
                raise WebError(f"cannot run {' '.join(argv)}: {e}") from e
            run = ActionRun(proc=proc, output=deque(maxlen=20))
            self._runs[key] = run
        _read_until_link(run, action.link_scheme, ACTION_LINK_TIMEOUT)
        output = "".join(run.output)
        with self._lock:
            self._result = ActionResult(
                title=action.label, output=output, url=run.url, link_label=action.link_label
            )
        if run.url:
            return f"{action.label}: open the link below to finish."
        with self._lock:
            del self._runs[key]
        # No link: either it died (reap it for the exit code — stdout can close
        # a moment before the process does) or it is stuck and we stop it.
        try:
            run.proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            run.proc.terminate()
        if run.proc.returncode is None:
            raise WebError(f"{action.label}: no link after {ACTION_LINK_TIMEOUT:.0f}s")
        raise WebError(f"{action.label} failed (exit {run.proc.returncode})")

    def action_state(self, kind: str, name: str, action_id: str) -> tuple[str, str]:
        """(pending link, note) for one action — ("", "") when it never ran.

        Reaps a finished run, turning it into the note the page shows on the
        next render: no JavaScript here, the operator reloads to see the end.
        """
        key = (kind, name, action_id)
        with self._lock:
            run = self._runs.get(key)
            if run is None:
                return "", self._notes.get(key, "")
            if run.alive():
                return run.url, ""
            del self._runs[key]
            if run.proc.returncode == 0:
                note = "Done."
            else:
                detail = " ".join(line.strip() for line in run.output if line.strip())[-200:]
                note = f"Failed (exit {run.proc.returncode}). {detail}".strip()
            self._notes[key] = note
            return "", note

    def pop_action_result(self) -> ActionResult | None:
        """The last action's output, once: the modal shows it and it is gone."""
        with self._lock:
            result, self._result = self._result, None
            return result

    def start_upgrade(self) -> str:
        """Start the odio-upgrade user unit (= `sudo odioctl upgrade apply --progress`)."""
        report = self.upgrade_report()
        if report is None or not report["upgrade_available"]:
            raise WebError("nothing to apply — no upgrade or pending component reported")
        _run_checked(
            self.user_run,
            ["systemctl", "--user", "start", "--no-block", UPGRADE_UNIT],
            f"systemctl --user start {UPGRADE_UNIT}",
        )
        return "Upgrade started — follow its progress in odio-ui."

    def set_dac(self, dac_id: str | None) -> str:
        if dac_id is not None and dac_id not in dac.BY_ID:
            raise WebError(f"unknown DAC id {dac_id!r}")
        args = ["dac", "unset"] if dac_id is None else ["dac", "set", dac_id]
        if self.cfg.config_txt:
            args += ["--config", self.cfg.config_txt]
        _run_checked(self.privileged_run, args, "odioctl dac")
        what = "DAC block removed" if dac_id is None else f"DAC set to {dac_id}"
        return f"{what} — reboot required."
