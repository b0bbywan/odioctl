"""Release manifests and install.sh URLs for odios on GitHub."""

from __future__ import annotations

import json
import sys
import urllib.request
from typing import TypedDict, cast

from odioctl import __version__

GITHUB_REPO = "b0bbywan/odios"
LATEST_MANIFEST_URL = "https://odio.love/manifest.json"


class Manifest(TypedDict):
    """Schema of release manifest.json (built by odios' scripts/build-manifest.py)."""

    odios: str
    roles: dict[str, str]


def install_url(version: str) -> str:
    if version == "latest":
        return f"https://github.com/{GITHUB_REPO}/releases/latest/download/install.sh"
    return f"https://github.com/{GITHUB_REPO}/releases/download/{version}/install.sh"


def manifest_url(version: str) -> str:
    if version == "latest":
        return f"https://github.com/{GITHUB_REPO}/releases/latest/download/manifest.json"
    return f"https://github.com/{GITHUB_REPO}/releases/download/{version}/manifest.json"


def fetch_manifest(url: str) -> Manifest | None:
    """Fetch a manifest.json from `url`.

    Returns None on any error — `apply` callers fall back to skipping the
    per-role diff (install.sh defaults take over, run = install); `check`
    treats None as a hard error and exits 2.
    """
    try:
        req = urllib.request.Request(url, headers={"User-Agent": f"odioctl/{__version__}"})
        with urllib.request.urlopen(req, timeout=10) as resp:
            return cast(Manifest, json.loads(resp.read()))
    except Exception as e:
        print(f"  warning: could not fetch manifest at {url}: {e}", file=sys.stderr)
        return None


def _resolve_manifest(version: str, upgrades_path: str) -> Manifest | None:
    """Read the cached manifest from upgrades.json when its `latest` matches
    the requested version, else fall back to a network fetch. The cache is
    populated by `odioctl upgrade check` on the daily timer.
    """
    try:
        with open(upgrades_path) as f:
            data = json.load(f)
        if data.get("latest") == version and data.get("manifest"):
            return cast(Manifest, data["manifest"])
    except (OSError, json.JSONDecodeError):
        pass
    return fetch_manifest(manifest_url(version))


def resolve_version(explicit: str | None, upgrades_path: str) -> str:
    if explicit:
        return explicit
    try:
        with open(upgrades_path) as f:
            return json.load(f).get("latest") or "latest"
    except (OSError, json.JSONDecodeError):
        return "latest"


def upgrade_reported(upgrades_path: str) -> bool:
    # Returns True if upgrades.json reports an upgrade is available. If the
    # file is missing or unreadable, returns True so install.sh can decide.
    try:
        with open(upgrades_path) as f:
            return bool(json.load(f).get("upgrade_available"))
    except (OSError, json.JSONDecodeError):
        return True
