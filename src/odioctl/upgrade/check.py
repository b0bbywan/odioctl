"""`odioctl upgrade check` — compare state.json against the published manifest
and refresh /var/cache/odio/upgrades.json (wired to a daily systemd user timer)."""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import sys
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import TypedDict, cast

from odioctl import components, manifest, state
from odioctl.manifest import Manifest
from odioctl.state import SYSTEM_STATE_PATH, SYSTEM_UPGRADES_PATH, State
from odioctl.versions import parse_version


class RoleUpgrade(TypedDict):
    """One per-role entry in the upgrades.json `roles` list."""

    name: str
    installed: str
    available: str


class UpgradeReport(TypedDict):
    """Schema of upgrades.json (written by `check`, read by `apply` and odio-api).

    `roles` is a delta — only roles whose target > installed appear, in the
    {name, installed, available} shape consumed by odio-motd. `manifest` is
    the full target snapshot cached so `apply` can skip the network round-
    trip once `check` has run. `pending_components` lists what the user
    enabled but is not installed yet ("role:x" / "feature:y"); they make
    `upgrade_available` true on their own so odio-ui offers the upgrade and
    `apply` does not refuse it.

    `latest` is the version the target release calls itself; `target_tag` is
    the GitHub tag it is published under, which `apply` needs to build its
    install.sh URL. They are the same string for a normal release and differ
    for a pre-release ("2026.7.0rc2-9-gcad916c" published as "pr-84").
    """

    current: str
    latest: str
    target_tag: str
    upgrade_available: bool
    roles: list[RoleUpgrade]
    pending_components: list[str]
    manifest: Manifest
    checked_at: str


@dataclass
class CheckOptions:
    state: str = SYSTEM_STATE_PATH
    version: str | None = None
    output: str = SYSTEM_UPGRADES_PATH


def add_check_arguments(p: argparse.ArgumentParser) -> None:
    p.description = "Compare local state against the remote manifest and refresh upgrades.json."
    p.add_argument("--state", default=SYSTEM_STATE_PATH)
    p.add_argument(
        "--version",
        help="release tag to compare against, for a box running a pre-release "
        f"(default: the published latest, or ${manifest.ODIOS_VERSION_ENV})",
    )
    p.add_argument("--output", default=SYSTEM_UPGRADES_PATH)


def check_from_args(ns: argparse.Namespace) -> int:
    return run_check(CheckOptions(state=ns.state, version=ns.version, output=ns.output))


def _compute_role_upgrades(st: State, man: Manifest) -> list[RoleUpgrade]:
    upgrades: list[RoleUpgrade] = []
    for role, installed in st["roles"].items():
        # No version = enabled here, not installed yet: that's a pending
        # component, not a role upgrade (components.REQUESTED_VERSION).
        if not installed:
            continue
        available = man["roles"].get(role)
        if available and parse_version(available) > parse_version(installed):
            upgrades.append({"name": role, "installed": installed, "available": available})
    upgrades.sort(key=lambda r: r["name"])
    return upgrades


def _build_upgrades_report(
    st: State, man: Manifest, target_tag: str | None = None
) -> UpgradeReport:
    upgrades = _compute_role_upgrades(st, man)
    pending = components.pending_components(st, set(man["roles"]))
    current = st["odios"]
    latest = man["odios"]
    newer = parse_version(latest) > parse_version(current)
    return {
        "current": current,
        "latest": latest,
        "target_tag": target_tag or latest,
        "upgrade_available": bool(upgrades) or newer or bool(pending),
        "roles": upgrades,
        "pending_components": pending,
        "manifest": man,
        "checked_at": datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }


def _write_upgrades_report(report: UpgradeReport, output: str) -> None:
    os.makedirs(os.path.dirname(output), exist_ok=True)
    with open(output, "w") as f:
        json.dump(report, f, indent=2)
        f.write("\n")
    # Default umask gives 0644; explicit chmod so other `odio` group members
    # (target_user when the timer runs as them, ansible become_user, etc.)
    # can rewrite this file without needing root.
    with contextlib.suppress(OSError):
        os.chmod(output, 0o664)


def _print_check_summary(report: UpgradeReport) -> None:
    if report["target_tag"] != report["latest"]:
        print(f"Comparing against release {report['target_tag']} ({report['latest']})")
    if report["upgrade_available"]:
        print(f"Upgrades available: {report['current']} → {report['latest']}")
        for r in report["roles"]:
            print(f"  {r['name']}: {r['installed']} → {r['available']}")
        for c in report["pending_components"]:
            print(f"  {c}: pending install")
    else:
        print(f"Up to date ({report['current']})")


def read_report(upgrades_path: str) -> UpgradeReport | None:
    """The cached upgrades.json, or None when missing/unreadable."""
    try:
        with open(upgrades_path) as f:
            data = json.load(f)
    except (OSError, json.JSONDecodeError):
        return None
    if not isinstance(data, dict) or "manifest" not in data:
        return None
    data.setdefault("pending_components", [])
    data.setdefault("target_tag", data.get("latest") or "latest")
    return cast(UpgradeReport, data)


def refresh(opts: CheckOptions) -> UpgradeReport | None:
    """Refresh upgrades.json after a local change (component toggle).

    Fetches the manifest like `run_check`; when that fails (offline) the cached
    manifest in the existing upgrades.json is reused so `pending_components`
    and `upgrade_available` still reflect the new state. None when neither a
    manifest nor a cache is available (nothing was written).
    """
    try:
        st = state.read_state(opts.state)
    except (OSError, json.JSONDecodeError, state.StateError):
        return None
    try:
        url, tag = manifest.check_source(opts.version)
    except ValueError:
        return None
    man = manifest.fetch_manifest(url)
    if man is None:
        cached = read_report(opts.output)
        if cached is None:
            return None
        man = cached["manifest"]
        # Reusing that manifest means staying on the release it came from:
        # dropping its tag here would leave `apply` building an install.sh URL
        # from a version string that is not a tag.
        tag = tag or cached["target_tag"]
    report = _build_upgrades_report(st, man, tag)
    try:
        _write_upgrades_report(report, opts.output)
    except OSError:
        return None
    return report


def run_check(opts: CheckOptions) -> int:
    try:
        st = state.read_state(opts.state)
    except (OSError, json.JSONDecodeError, state.StateError) as e:
        print(f"Error reading state: {e}", file=sys.stderr)
        return 2

    try:
        url, tag = manifest.check_source(opts.version)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2

    man = manifest.fetch_manifest(url)
    if man is None:
        return 2

    report = _build_upgrades_report(st, man, tag)
    _write_upgrades_report(report, opts.output)
    _print_check_summary(report)
    return 1 if report["upgrade_available"] else 0
