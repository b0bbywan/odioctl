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

import contextlib
import json
import os
import secrets
import subprocess
import threading
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass, field
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
    """User-facing failure of a form action; rendered as an error banner.

    `modal` carries the action's output when there is one to show alongside it.
    """

    def __init__(self, message: str, modal: ActionResult | None = None) -> None:
        super().__init__(message)
        self.modal = modal


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


def _checked_kind(kind: str) -> components.Kind:
    """The request's `kind` field, narrowed to the catalog's two kinds."""
    if kind not in ("role", "feature"):
        raise WebError(f"unknown component kind {kind!r}")
    return cast(components.Kind, kind)


def _stop(proc: subprocess.Popen[str]) -> None:
    """Stop a stuck action and reap it — nothing polls this process again."""
    proc.terminate()
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        proc.kill()
        with contextlib.suppress(subprocess.TimeoutExpired):
            proc.wait(timeout=2)


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
            with run.lock:
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
    """What an action just did, shown in the modal of the POST response.

    Travels back with that one response, so it never reaches another client
    or the next page load: what outlives the request is the row's own link.
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
    lock: threading.Lock = field(default_factory=threading.Lock)

    def alive(self) -> bool:
        return self.proc.poll() is None

    def text(self) -> str:
        """Snapshot of the output: the drain thread outlives the process."""
        with self.lock:
            return "".join(self.output)


def _result_of(action: components.Action, run: ActionRun) -> ActionResult:
    return ActionResult(
        title=action.label, output=run.text(), url=run.url, link_label=action.link_label
    )


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
        k = _checked_kind(kind)
        with self._lock:
            st, err = self.read_state()
            if st is None:
                raise WebError(err or "state.json unavailable")
            try:
                new = components.set_component(st, k, name, enabled)
            except components.ComponentError as e:
                raise WebError(str(e)) from e
            try:
                components.save(self.cfg.state_path, new)
            except OSError as e:
                raise WebError(f"cannot write state.json: {e}") from e
        # Keep upgrades.json in step so odio-ui's badge and `upgrade apply` see
        # the pending install without waiting for the daily timer. Outside the
        # lock: it fetches the manifest, and every render takes that lock.
        report = check.refresh(
            check.CheckOptions(state=self.cfg.state_path, output=self.cfg.resolved_upgrades_path())
        )
        label = components.label_of(k, name)
        if not enabled:
            return f"{label} disabled — it stays installed but will no longer be updated."
        if report is not None and f"{kind}:{name}" in report["pending_components"]:
            return f"{label} enabled — it will be installed by the next upgrade (apply it below)."
        return f"{label} enabled."

    def _resolve_action(
        self, kind: components.Kind, name: str, action_id: str
    ) -> components.Action:
        """The catalog action to run, or a WebError naming what is wrong."""
        action = components.find_action(kind, name, action_id)
        if action is None:
            raise WebError(f"unknown action {action_id!r} for {name}")
        st, err = self.read_state()
        if st is None:
            raise WebError(err or "state.json unavailable")
        comp = next(
            (c for c in components.list_components(st) if c.kind == kind and c.name == name), None
        )
        if comp is None or comp.status != "installed":
            raise WebError(f"{components.label_of(kind, name)} is not installed")
        return action

    def run_action(
        self, kind: str, name: str, action_id: str, host: str
    ) -> tuple[str, ActionResult | None]:
        """Start a catalog action and return (banner text, modal) for the page.

        The command is never waited on: `qbzd login` prints its Qobuz URL and
        then holds a listener open until the browser comes back (300s), so we
        read stdout only until the link shows up and leave the process to it.
        The link is then rendered next to the component until it is gone.
        """
        action = self._resolve_action(_checked_kind(kind), name, action_id)
        key = (kind, name, action_id)
        with self._lock:
            run = self._runs.get(key)
            if run is not None and run.alive():
                banner = f"{action.label}: already running — the link is below."
                return banner, _result_of(action, run)
            self._notes.pop(key, None)
            argv = [part.format(host=host, home=os.path.expanduser("~")) for part in action.argv]
            try:
                proc = self.action_spawn(argv)
            except (OSError, subprocess.SubprocessError) as e:
                raise WebError(f"cannot run {' '.join(argv)}: {e}") from e
            run = ActionRun(proc=proc, output=deque(maxlen=20))
            self._runs[key] = run
        _read_until_link(run, action.link_scheme, ACTION_LINK_TIMEOUT)
        result = _result_of(action, run)
        if run.url:
            return f"{action.label}: open the link below to finish.", result
        with self._lock:
            del self._runs[key]
        # No link: either it died (reap it for the exit code — stdout can close
        # a moment before the process does) or it is stuck and we stop it.
        try:
            run.proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            _stop(run.proc)
            raise WebError(
                f"{action.label}: no link after {ACTION_LINK_TIMEOUT:.0f}s", result
            ) from None
        raise WebError(f"{action.label} failed (exit {run.proc.returncode})", result)

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
                lines = run.text().splitlines()
                detail = " ".join(line.strip() for line in lines if line.strip())[-200:]
                note = f"Failed (exit {run.proc.returncode}). {detail}".strip()
            self._notes[key] = note
            return "", note

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
