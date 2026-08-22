"""Host network helpers: LAN-reachable IP and the PWA deep link."""

from __future__ import annotations

import argparse
import re
import subprocess

PWA_URL = "https://pwa.odio.love"


def default_route_ip() -> str | None:
    """Source IP of the default route, or None when it can't be determined
    (no default route, `ip` missing, unparseable output)."""
    try:
        out = subprocess.run(
            ["ip", "route", "get", "1.1.1.1"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None
    m = re.search(r"\bsrc\s+(\S+)", out)
    return m.group(1) if m else None


def pwa_url() -> str:
    """https://pwa.odio.love/#/i/<ip>, or the bare PWA URL when no IP is
    detectable so callers (motd, post-install summary) always get something
    printable."""
    ip = default_route_ip()
    return f"{PWA_URL}/#/i/{ip}" if ip else PWA_URL


def add_pwa_url_arguments(p: argparse.ArgumentParser) -> None:
    p.description = (
        "Print the PWA URL pointing at this host's LAN-reachable IP "
        "(the source IP of the default route)."
    )


def cmd_pwa_url(_ns: argparse.Namespace) -> int:
    print(pwa_url())
    return 0
