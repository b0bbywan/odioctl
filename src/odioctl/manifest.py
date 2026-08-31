"""Release manifests and install.sh URLs for odios on GitHub."""

from __future__ import annotations

import json
import os
import re
import sys
import urllib.request
from typing import TypedDict, cast

from odioctl import __version__

GITHUB_REPO = "b0bbywan/odios"
LATEST_MANIFEST_URL = "https://odio.love/manifest.json"

# Set to a release tag to make `check` compare against that release instead of
# the published latest one — a test box runs a pre-release (`pr-84`) that the
# latest manifest knows nothing about, so without this every role it ships
# reads as "not in this release" and nothing is ever pending.
ODIOS_VERSION_ENV = "ODIOCTL_ODIOS_VERSION"

# Only a *tag* is overridable, never a URL: the tag is interpolated into a
# github.com/b0bbywan/odios path, so whoever sets it can pick another odios
# release but can never point odioctl at a manifest of their own. That only
# holds while the tag cannot walk out of the path — curl normalises away
# `..`, so `../../someone/else/releases/download/x` would fetch (and pipe to
# bash, in `apply`) a foreign repository. Real tags are calver
# ("2026.7.0rc2"), git-described ("2026.7.0rc2-9-gcad916c") or PR
# pre-releases ("pr-84").
_TAG_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$")


def is_release_tag(tag: str) -> bool:
    """True when `tag` is safe to interpolate into a release URL."""
    return bool(_TAG_RE.match(tag)) and ".." not in tag


def _checked_tag(version: str) -> str:
    if not is_release_tag(version):
        raise ValueError(f"not a release tag: {version!r}")
    return version


def env_version() -> str | None:
    """The release tag from $ODIOCTL_ODIOS_VERSION, or None when unset.

    An unusable value is a warning, not a failure: falling back to the
    published manifest keeps the daily timer working on a box whose env file
    has a typo in it.
    """
    raw = os.environ.get(ODIOS_VERSION_ENV, "").strip()
    if not raw:
        return None
    if not is_release_tag(raw):
        print(
            f"  warning: ignoring ${ODIOS_VERSION_ENV}={raw!r}: not a release tag", file=sys.stderr
        )
        return None
    return raw


class Manifest(TypedDict):
    """Schema of release manifest.json (built by odios' scripts/build-manifest.py)."""

    odios: str
    roles: dict[str, str]


def install_url(version: str) -> str:
    if version == "latest":
        return f"https://github.com/{GITHUB_REPO}/releases/latest/download/install.sh"
    return f"https://github.com/{GITHUB_REPO}/releases/download/{_checked_tag(version)}/install.sh"


def manifest_url(version: str) -> str:
    if version == "latest":
        return f"https://github.com/{GITHUB_REPO}/releases/latest/download/manifest.json"
    return (
        f"https://github.com/{GITHUB_REPO}/releases/download/{_checked_tag(version)}/manifest.json"
    )


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


def check_source(version: str | None) -> tuple[str, str | None]:
    """(manifest url, release tag) for `check`.

    The requested tag — --version or $ODIOCTL_ODIOS_VERSION — else the
    published latest manifest. There is deliberately no way to name a URL:
    every manifest odioctl reads is built here from a validated tag under
    github.com/b0bbywan/odios, so the worst a hostile value can do is pick
    another odios release.

    The tag is what travels to `apply` through upgrades.json: a pre-release is
    reached by its tag ("pr-84") while the manifest inside it describes itself
    by version ("2026.7.0rc2-9-gcad916c"), so `apply` cannot rebuild the
    install.sh URL from the version alone.
    """
    tag = version or env_version()
    if tag is None:
        return LATEST_MANIFEST_URL, None
    return manifest_url(_checked_tag(tag)), tag


def _cached_tag(data: dict[str, object]) -> object:
    """The release tag a cached upgrades.json points at — `latest` for a report
    written before `check` recorded the tag it used."""
    return data.get("target_tag") or data.get("latest")


def _resolve_manifest(version: str, upgrades_path: str) -> Manifest | None:
    """Read the cached manifest from upgrades.json when it describes the
    requested release, else fall back to a network fetch. The cache is
    populated by `odioctl upgrade check` on the daily timer.
    """
    try:
        with open(upgrades_path) as f:
            data = json.load(f)
        if _cached_tag(data) == version and data.get("manifest"):
            return cast(Manifest, data["manifest"])
    except (OSError, json.JSONDecodeError):
        pass
    return fetch_manifest(manifest_url(version))


def resolve_version(explicit: str | None, upgrades_path: str) -> str:
    """The release tag `apply` targets: --version, else what `check` recorded."""
    if explicit:
        return explicit
    try:
        with open(upgrades_path) as f:
            tag = _cached_tag(json.load(f))
    except (OSError, json.JSONDecodeError):
        return "latest"
    return tag if isinstance(tag, str) and tag else "latest"


def upgrade_reported(upgrades_path: str) -> bool:
    # Returns True if upgrades.json reports an upgrade is available. If the
    # file is missing or unreadable, returns True so install.sh can decide.
    try:
        with open(upgrades_path) as f:
            return bool(json.load(f).get("upgrade_available"))
    except (OSError, json.JSONDecodeError):
        return True
