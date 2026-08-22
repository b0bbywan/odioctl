"""`odioctl web` — a small stdlib HTTP server rendering the components and
DAC pages for the LAN. Plain HTML forms (POST re-renders the page), no JavaScript, no JSON API.
Markup lives in web/templates/, the stylesheet in web/static/ (styled after odio-ui).

Runs as the target user (systemd --user unit). state.json is edited
directly; config.txt goes through `sudo -n odioctl dac set <id>` (see the
sudoers file shipped with the package). No authentication: same LAN trust
model as odio-api. Every form carries a per-process token so a cross-site
HTML form cannot drive the box.
"""

from __future__ import annotations

import argparse
import functools
import html
import importlib.resources
import json
import os
import secrets
import signal
import socket
import string
import subprocess
import sys
import threading
import urllib.parse
from collections.abc import Callable
from dataclasses import dataclass
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, cast

from odioctl import __version__, components, dac, netinfo, state
from odioctl.upgrade import check

DEFAULT_PORT = 8021
MAX_BODY = 16 * 1024
ODIO_UI_PORT = 8018  # odio-api's built-in dashboard, where upgrade progress is shown
UPGRADE_UNIT = "odio-upgrade.service"  # systemd --user unit shipped by this package
SD_LISTEN_FDS_START = 3  # sd_listen_fds(3): systemd hands sockets over from fd 3 up

RunFn = Callable[[list[str]], "subprocess.CompletedProcess[str]"]


class ActivationError(Exception):
    """systemd handed over something other than the one socket odioctl-web.socket declares."""


class WebError(Exception):
    """User-facing failure of a form action; rendered as an error banner."""


class TokenError(WebError):
    """Missing or invalid form token; answered with a bare 403, not a re-render."""


@dataclass
class WebConfig:
    bind: str = "0.0.0.0"
    port: int = DEFAULT_PORT
    state_path: str = state.SYSTEM_STATE_PATH
    config_txt: str | None = None  # None → dac.find_config_txt()
    odioctl_bin: str = os.environ.get("ODIOCTL_BIN", "/usr/bin/odioctl")
    upgrades_path: str | None = None  # None → sibling of a custom --state, else /var/cache

    def resolved_upgrades_path(self) -> str:
        if self.upgrades_path:
            return self.upgrades_path
        if self.state_path != state.SYSTEM_STATE_PATH:
            return os.path.join(os.path.dirname(self.state_path), "upgrades.json")
        return state.SYSTEM_UPGRADES_PATH


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


class Services:
    """Business operations behind the pages (also unit-testable directly)."""

    def __init__(
        self,
        cfg: WebConfig,
        privileged_run: RunFn | None = None,
        user_run: RunFn | None = None,
    ) -> None:
        self.cfg = cfg
        self.privileged_run = privileged_run or _default_privileged_run(cfg)
        self.user_run = user_run or _default_user_run  # same user, no sudo (systemctl --user)
        self.token = secrets.token_urlsafe(24)
        self._lock = threading.Lock()

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


# --- rendering ------------------------------------------------------------
#
# Templates live in web/templates/*.html (string.Template, `$name` placeholders) —
# whole sections as well as the one-element partials (banner, hint, option, …), so
# the only markup left in this file is the bare <h1> of the 403/404/405 dead ends.
# The stylesheet and logo in web/static/ mirror odio-ui's look (go-odio-api).
# Every value substituted into a template is HTML-escaped by `_e` unless it is
# itself rendered HTML.

_RES = importlib.resources.files("odioctl.web")


@functools.cache
def _template(name: str) -> string.Template:
    return string.Template((_RES / "templates" / name).read_text(encoding="utf-8"))


@functools.cache
def static_asset(name: str) -> tuple[bytes, str] | None:
    """(content, media type) for a file under web/static/, or None if unknown."""
    if name not in STATIC_FILES:
        return None
    return (_RES / "static" / name).read_bytes(), STATIC_FILES[name]


STATIC_FILES = {"style.css": "text/css; charset=utf-8", "logo.png": "image/png"}


def _render(template: str, **values: object) -> str:
    return _template(template).substitute(values)


def _e(value: object) -> str:
    return html.escape(str(value), quote=True)


# The sections receive a `render` with the per-process form token already bound
# rather than the token itself: every template carrying a form uses the same
# `$token` hidden field, so it is escaped once, here. Templates without the
# placeholder ignore the extra value.
Render = Callable[..., str]


def _renderer(token: str) -> Render:
    return functools.partial(_render, token=_e(token))


def _banner(kind: str, text: str) -> str:
    return _render("banner.html", kind=kind, text=_e(text)) if text else ""


def _hint(text: str) -> str:
    return _render("hint.html", text=_e(text))


def _section(title: str, body: str) -> str:
    """A section whose whole content is one banner or hint (the empty/unavailable states)."""
    return _render("section_banner.html", title=_e(title), banner=body)


# (chip text, button label) per component status; the button performs the opposite action.
_STATUS_UI = {
    "installed": ("Installed", "Disable"),
    "excluded": ("Disabled", "Enable"),
    "default": ("Will install on next upgrade", "Skip"),
}


def _component_row(c: components.Component, render: Render, child: bool) -> str:
    chip, button = _STATUS_UI[c.status]
    action = render(
        "component_action.html",
        kind=_e(c.kind),
        name=_e(c.name),
        enabled="0" if c.enabled else "1",
        button=button,
    )
    return render(
        "component_row.html",
        child=" child" if child else "",
        label=_e(c.label),
        description=_e(c.description or c.name),
        status=_e(c.status),
        chip=chip,
        action=action,
    )


def _components_section(st: state.State | None, err: str | None, render: Render) -> str:
    if st is None:
        return _section("Components", _banner("err", f"state.json: {err}"))
    comps = components.list_components(st)
    by_parent: dict[str, list[components.Component]] = {}
    orphans: list[components.Component] = []
    for f in comps:
        if f.kind != "feature":
            continue
        if f.parent:
            by_parent.setdefault(f.parent, []).append(f)
        else:
            orphans.append(f)
    rows_by_group: dict[str, list[str]] = {g: [] for g in components.GROUPS}
    infra: list[str] = []
    for r in comps:
        if r.kind != "role":
            continue
        if not r.toggleable:
            infra.append(r.label)
            continue
        rows = rows_by_group.setdefault(r.group, [])
        rows.append(_component_row(r, render, False))
        rows.extend(_component_row(f, render, True) for f in by_parent.get(r.name, []))
    rows_by_group[components.GROUPS[-1]].extend(_component_row(f, render, False) for f in orphans)
    groups = "".join(
        render("component_group.html", title=_e(title), rows="".join(rows))
        for title, rows in rows_by_group.items()
        if rows
    )
    # The install mode and target user drive what an upgrade actually installs,
    # so they belong here rather than in the page header.
    return render(
        "components.html",
        user=_e(st["target_user"]),
        mode=_e(st["install_mode"]),
        note=_e(components.APPLY_NOTE),
        groups=groups,
        infra=_e("Always installed: " + ", ".join(infra)) if infra else "",
    )


def _dac_section(d: dict[str, Any], render: Render) -> str:
    if not d["supported"]:
        return _section(
            "DAC",
            _banner(
                "warn", "No config.txt found — DAC selection is only available on Raspberry Pi."
            ),
        )
    current = d["current"]
    opts = [
        _render(
            "option.html",
            id="",
            attrs=" disabled selected" if not current else " disabled",
            text="— not configured —",
        )
    ]
    opts += [
        _render(
            "option.html",
            id=_e(e.id),
            attrs=" selected" if e.id == current else "",
            text=f"{_e(e.label)} ({_e(e.id)})",
        )
        for e in dac.CATALOG
    ]
    if current:
        cur = f"Current: {current}" + (
            " (managed by odioctl)" if d["managed"] else " (from existing config.txt)"
        )
    elif d["stray_lines"] and not d["managed"]:
        cur = "Unrecognised audio configuration in config.txt: " + "; ".join(d["stray_lines"])
    else:
        cur = "No DAC configured"
    unset = render("dac_unset.html") if d["managed"] else ""
    stray = ""
    if d["stray_lines"] and d["managed"]:
        # Unmanaged lines are simply what defines `current`; once odioctl owns the
        # block, anything else left active is a conflict worth flagging.
        stray = _banner(
            "warn",
            "Audio lines outside the odioctl block (commented out on Apply): "
            + "; ".join(d["stray_lines"]),
        )
    return render(
        "dac.html",
        options="".join(opts),
        unset=unset,
        current=_e(cur),
        stray=stray,
    )


def _upgrade_section(report: check.UpgradeReport | None, render: Render, ui_url: str) -> str:
    if report is None:
        return _section(
            "Upgrade", _banner("warn", "No upgrade check has run yet (odio-check-upgrade.timer).")
        )
    if not report["upgrade_available"]:
        return _section(
            "Upgrade",
            _hint(f"Up to date — odio {report['current']} (checked {report['checked_at']})."),
        )
    items: list[str] = []
    if report["latest"] != report["current"]:
        items.append(f"odio {_e(report['current'])} → {_e(report['latest'])}")
    items.extend(
        f"{_e(r['name'])} {_e(r['installed'])} → {_e(r['available'])}" for r in report["roles"]
    )
    for ref in report["pending_components"]:
        kind, _, name = ref.partition(":")
        items.append(f"install {_e(components.label_of(cast(components.Kind, kind), name))}")
    return render(
        "upgrade.html",
        items="".join(_render("upgrade_item.html", text=i) for i in items),
        ui_url=_e(ui_url),
    )


def render_page(services: Services, *, message: str = "", error: str = "", host: str = "") -> str:
    st, err = services.read_state()
    d = services.dac_status()
    render = _renderer(services.token)
    ui_url = f"http://{host or socket.gethostname()}:{ODIO_UI_PORT}/ui"
    version_badge = ""
    if st is not None:
        version_badge = _render("version_badge.html", odios=_e(st["odios"]))
    banners = [_banner("ok", message), _banner("err", error)]
    if d["reboot_required"]:
        banners.append(_banner("warn", "A reboot is required to apply the DAC change."))
    return render(
        "page.html",
        version=_e(__version__),
        hostname=_e(socket.gethostname()),
        version_badge=version_badge,
        banners="".join(banners),
        components=_components_section(st, err, render),
        upgrade=_upgrade_section(services.upgrade_report(), render, ui_url),
        dac=_dac_section(d, render),
    )


# --- HTTP layer -----------------------------------------------------------

PAGE_PATHS = frozenset({"/", "/index.html"})
STATIC_PREFIX = "/static/"


def _action_components(services: Services, form: dict[str, str]) -> str:
    return services.set_component(
        form.get("kind", ""), form.get("name", ""), form.get("enabled") == "1"
    )


def _action_dac(services: Services, form: dict[str, str]) -> str:
    dac_id = form.get("id", "")
    if not dac_id:  # an empty select is not an unset — that is /dac/unset
        raise WebError("no DAC selected")
    return services.set_dac(dac_id)


# The POST routes; ACTION_PATHS is derived so routing and dispatch cannot drift.
ACTIONS: dict[str, Callable[[Services, dict[str, str]], str]] = {
    "/components": _action_components,
    "/dac": _action_dac,
    "/dac/unset": lambda services, _form: services.set_dac(None),
    "/upgrade": lambda services, _form: services.start_upgrade(),
}
ACTION_PATHS = frozenset(ACTIONS)


class Handler(BaseHTTPRequestHandler):
    server_version = f"odioctl/{__version__}"
    services: Services  # injected by make_handler()

    def log_message(self, fmt: str, *args: Any) -> None:
        sys.stderr.write(f"{self.address_string()} {fmt % args}\n")

    # -- helpers ------------------------------------------------------------------

    def _send(
        self, code: int, raw: bytes, ctype: str, cache: str, allow: str | None = None
    ) -> None:
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Cache-Control", cache)
        if allow:
            self.send_header("Allow", allow)
        self.end_headers()
        self.wfile.write(raw)

    def _send_html(self, code: int, body: str) -> None:
        self._send(code, body.encode("utf-8"), "text/html; charset=utf-8", "no-store")

    def _send_status(self, code: HTTPStatus, detail: str = "", allow: str | None = None) -> None:
        """Minimal <h1>-only page for the 403/404/405 dead ends."""
        raw = f"<h1>{code.value}</h1>{detail}".encode()
        self._send(code, raw, "text/html; charset=utf-8", "no-store", allow)

    def _reject_path(self, path: str, method: str) -> bool:
        """404 for unknown paths, 405 for a known path with the wrong verb; True when rejected."""
        wanted, other = (
            (PAGE_PATHS, ACTION_PATHS) if method == "GET" else (ACTION_PATHS, PAGE_PATHS)
        )
        if path in wanted:
            return False
        if path in other:
            allow = "POST" if method == "GET" else "GET"
            self._send_status(HTTPStatus.METHOD_NOT_ALLOWED, allow=allow)
            return True
        self._send_status(HTTPStatus.NOT_FOUND)
        return True

    def _read_form(self) -> dict[str, str]:
        ctype = self.headers.get("Content-Type", "")
        if not ctype.startswith("application/x-www-form-urlencoded"):
            raise WebError("expected a form submission")
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError as e:
            raise WebError("bad Content-Length") from e
        if length < 0 or length > MAX_BODY:
            raise WebError("form too large")
        raw = self.rfile.read(length).decode("utf-8", errors="replace") if length else ""
        form = {k: v[-1] for k, v in urllib.parse.parse_qs(raw, keep_blank_values=True).items()}
        if not secrets.compare_digest(form.get("token", ""), self.services.token):
            raise TokenError("invalid or missing form token — reload the page and retry")
        return form

    # -- routes ---------------------------------------------------------------

    def _send_static(self, name: str) -> None:
        asset = static_asset(name)
        if asset is None:
            self._send_status(HTTPStatus.NOT_FOUND)
            return
        raw, ctype = asset
        self._send(HTTPStatus.OK, raw, ctype, "public, max-age=86400")

    def do_GET(self) -> None:
        path = urllib.parse.urlsplit(self.path).path
        if path.startswith(STATIC_PREFIX):
            self._send_static(path[len(STATIC_PREFIX) :])
            return
        if self._reject_path(path, "GET"):
            return
        self._send_html(HTTPStatus.OK, render_page(self.services, host=self._host()))

    def _host(self) -> str:
        """Hostname the client used (for the odio-ui link), without the port."""
        host = self.headers.get("Host", "")
        if host.startswith("["):  # IPv6 literal
            return host.split("]")[0] + "]"
        return host.rsplit(":", 1)[0] if ":" in host else host

    def do_POST(self) -> None:
        path = urllib.parse.urlsplit(self.path).path
        if self._reject_path(path, "POST"):
            return
        try:
            msg = ACTIONS[path](self.services, self._read_form())
        except TokenError as e:
            self._send_status(HTTPStatus.FORBIDDEN, f"<p>{_e(e)}</p>")
            return
        except WebError as e:
            page = render_page(self.services, error=str(e), host=self._host())
            self._send_html(HTTPStatus.OK, page)
            return
        self._send_html(HTTPStatus.OK, render_page(self.services, message=msg, host=self._host()))


def make_handler(services: Services) -> type[Handler]:
    return type("BoundHandler", (Handler,), {"services": services})


# --- socket activation ----------------------------------------------------
#
# odioctl-web.socket is what gets enabled; systemd binds port 8021 and starts the
# service on the first connection, passing the listening socket as fd 3. Running
# `odioctl web` by hand (no LISTEN_FDS) still binds for itself, so the dev loop and
# --bind/--port are unaffected.


def systemd_socket() -> socket.socket | None:
    """The listening socket passed by systemd, or None when not socket-activated.

    Follows sd_listen_fds(3): LISTEN_PID must name this process and LISTEN_FDS counts
    the sockets from fd 3 up. The variables are removed from the environment so that
    nothing we exec later (`sudo odioctl dac …`) sees a handover meant for us.
    """
    listen_pid = os.environ.pop("LISTEN_PID", None)
    listen_fds = os.environ.pop("LISTEN_FDS", None)
    os.environ.pop("LISTEN_FDNAMES", None)
    if listen_pid is None or listen_fds is None:
        return None
    if listen_pid != str(os.getpid()):
        return None  # inherited from a parent; the handover was not addressed to us
    try:
        count = int(listen_fds)
    except ValueError:
        raise ActivationError(f"LISTEN_FDS is not a number: {listen_fds!r}") from None
    if count == 0:
        return None
    if count != 1:
        raise ActivationError(f"expected one socket from systemd, got {count}")
    sock = socket.socket(fileno=SD_LISTEN_FDS_START)
    if sock.type != socket.SOCK_STREAM:
        raise ActivationError("the activation socket is not a stream socket (ListenStream=)")
    if sock.family not in (socket.AF_INET, socket.AF_INET6):
        # The page links to odio-ui by host:port and reads the Host header; a Unix
        # socket would have no address to speak of.
        raise ActivationError("the activation socket is not TCP (use ListenStream=PORT)")
    return sock


class InheritedHTTPServer(ThreadingHTTPServer):
    """ThreadingHTTPServer on a socket systemd already bound and listened on.

    Binding is skipped; only the bookkeeping HTTPServer.server_bind() would have done
    is reproduced, so the address family follows whatever the unit asked for.
    """

    def __init__(self, sock: socket.socket, handler: type[BaseHTTPRequestHandler]) -> None:
        self.address_family = sock.family
        super().__init__(sock.getsockname()[:2], handler, bind_and_activate=False)
        self.socket.close()  # the unused socket TCPServer.__init__ just made
        self.socket = sock
        addr = sock.getsockname()
        self.server_address = addr
        self.server_name = socket.getfqdn(str(addr[0]))
        self.server_port = int(addr[1])


def make_server(
    cfg: WebConfig, services: Services | None = None, sock: socket.socket | None = None
) -> ThreadingHTTPServer:
    services = services or Services(cfg)
    handler = make_handler(services)
    srv: ThreadingHTTPServer = (
        InheritedHTTPServer(sock, handler)
        if sock is not None
        else ThreadingHTTPServer((cfg.bind, cfg.port), handler)
    )
    srv.daemon_threads = True
    return srv


# --- CLI -----------------------------------------------------------------


def add_web_arguments(p: argparse.ArgumentParser) -> None:
    p.description = "Serve the odioctl web UI (components, upgrade, DAC) on the LAN."
    p.add_argument(
        "--bind",
        default="0.0.0.0",
        help="address to listen on (default: all; ignored under socket activation)",
    )
    p.add_argument(
        "--port",
        type=int,
        default=DEFAULT_PORT,
        help=f"TCP port (default: {DEFAULT_PORT}; ignored under socket activation)",
    )
    p.add_argument("--state", default=state.SYSTEM_STATE_PATH, help="path to state.json")
    p.add_argument(
        "--config",
        help="path to config.txt (default: auto-detect). Dev/test only: the sudoers rule "
        "does not admit --config, so DAC changes through sudo will be refused",
    )
    p.add_argument(
        "--upgrades",
        help="path to upgrades.json (default: /var/cache/odio/upgrades.json, or next to "
        "a custom --state)",
    )


def web_from_args(ns: argparse.Namespace) -> int:
    cfg = WebConfig(
        bind=ns.bind,
        port=ns.port,
        state_path=ns.state,
        config_txt=ns.config,
        upgrades_path=ns.upgrades,
    )
    return serve(cfg)


def serve(cfg: WebConfig) -> int:
    try:
        sock = systemd_socket()
    except ActivationError as e:
        print(f"odioctl web: {e}", file=sys.stderr)
        return 2
    srv = make_server(cfg, sock=sock)
    if sock is not None:
        print(
            f"Serving odioctl web UI on the socket passed by systemd (port {srv.server_port})",
            flush=True,
        )
    else:
        ip = (
            cfg.bind
            if cfg.bind not in ("0.0.0.0", "::")
            else (netinfo.default_route_ip() or "127.0.0.1")
        )
        print(f"Serving odioctl web UI on http://{ip}:{cfg.port}", flush=True)

    def _stop(_signum: int, _frame: Any) -> None:
        threading.Thread(target=srv.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _stop)
    signal.signal(signal.SIGINT, _stop)
    try:
        srv.serve_forever()
    finally:
        srv.server_close()
    return 0
