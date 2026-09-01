"""`odioctl web` — the HTTP end of the settings UI: routes and CLI.

Plain HTML forms (POST re-renders the page), no JavaScript, no JSON API. The
work behind each form is in web/services.py, the markup in web/render.py and
web/templates/; what is left here is the wire: five POST routes, the static
files, and `odioctl web` itself.

Runs as the target user (systemd --user unit). No authentication: same LAN
trust model as odio-api. Every form carries a per-process token so a
cross-site HTML form cannot drive the box.
"""

from __future__ import annotations

import argparse
import secrets
import signal
import sys
import threading
import urllib.parse
from collections.abc import Callable
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from odioctl import __version__, netinfo, state
from odioctl.web import render
from odioctl.web.config import DEFAULT_PORT, WebConfig
from odioctl.web.services import Services, TokenError, WebError

MAX_BODY = 16 * 1024

PAGE_PATHS = frozenset({"/", "/index.html"})
STATIC_PREFIX = "/static/"


# --- routes ---------------------------------------------------------------


def _action_components(svc: Services, form: dict[str, str], _host: str) -> str:
    return svc.set_component(form.get("kind", ""), form.get("name", ""), form.get("enabled") == "1")


def _action_component_action(svc: Services, form: dict[str, str], host: str) -> str:
    # `host` is the name the browser reached the box by: it becomes the OAuth
    # callback host, so the redirect lands here and not on the box's loopback.
    return svc.run_action(form.get("kind", ""), form.get("name", ""), form.get("action", ""), host)


def _action_dac(svc: Services, form: dict[str, str], _host: str) -> str:
    dac_id = form.get("id", "")
    if not dac_id:  # an empty select is not an unset — that is /dac/unset
        raise WebError("no DAC selected")
    return svc.set_dac(dac_id)


# The POST routes; ACTION_PATHS is derived so routing and dispatch cannot drift.
ACTIONS: dict[str, Callable[[Services, dict[str, str], str], str]] = {
    "/components": _action_components,
    "/components/action": _action_component_action,
    "/dac": _action_dac,
    "/dac/unset": lambda svc, _form, _host: svc.set_dac(None),
    "/upgrade": lambda svc, _form, _host: svc.start_upgrade(),
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
        asset = render.static_asset(name)
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
        self._send_html(HTTPStatus.OK, render.render_page(self.services, host=self._host()))

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
            msg = ACTIONS[path](self.services, self._read_form(), self._host())
        except TokenError as e:
            self._send_status(HTTPStatus.FORBIDDEN, f"<p>{render.escape(e)}</p>")
            return
        except WebError as e:
            page = render.render_page(self.services, error=str(e), host=self._host())
            self._send_html(HTTPStatus.OK, page)
            return
        self._send_html(
            HTTPStatus.OK, render.render_page(self.services, message=msg, host=self._host())
        )


def make_handler(services: Services) -> type[Handler]:
    return type("BoundHandler", (Handler,), {"services": services})


def make_server(cfg: WebConfig, services: Services | None = None) -> ThreadingHTTPServer:
    services = services or Services(cfg)
    srv = ThreadingHTTPServer((cfg.bind, cfg.port), make_handler(services))
    srv.daemon_threads = True
    return srv


# --- CLI -----------------------------------------------------------------


def add_web_arguments(p: argparse.ArgumentParser) -> None:
    p.description = "Serve the odioctl web UI (components, upgrade, DAC) on the LAN."
    p.add_argument(
        "--bind",
        default="0.0.0.0",
        help="address to listen on (default: all)",
    )
    p.add_argument(
        "--port",
        type=int,
        default=DEFAULT_PORT,
        help=f"TCP port (default: {DEFAULT_PORT})",
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
    srv = make_server(cfg)
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
