"""`odioctl upgrade verify` — schema sanity checks on state.json (CI / inspection)."""

from __future__ import annotations

import argparse
import json
import sys

from odioctl import components, state
from odioctl.state import State
from odioctl.versions import _VERSION_RE


def add_verify_arguments(p: argparse.ArgumentParser) -> None:
    p.description = (
        "Read state.json from disk and run schema sanity checks. "
        "Exit 0 if valid, 1 if invalid, 2 if state.json is missing."
    )
    p.add_argument("--state", default=state.SYSTEM_STATE_PATH, help="path to state.json")
    p.add_argument(
        "--expected-version",
        help="also assert state.odios matches this tag (test harness use)",
    )


def verify_from_args(ns: argparse.Namespace) -> int:
    return run_verify(ns.state, ns.expected_version)


def _warn_features_unknown(st: State) -> str | None:
    # A warning, not an error: a feature odios adds after this odioctl shipped
    # is unknown here, and the box is fine.
    bad = (set(st["features"]) | set(st["features_excluded"])) - components.FEATURE_CATALOG.keys()
    return f"features unknown to this odioctl: {sorted(bad)}" if bad else None


def _check_features_no_overlap(st: State) -> str | None:
    overlap = set(st["features"]) & set(st["features_excluded"])
    return f"features and features_excluded overlap: {sorted(overlap)}" if overlap else None


def _check_roles_no_overlap(st: State) -> str | None:
    overlap = set(st["roles"]) & set(st["roles_excluded"])
    return f"roles and roles_excluded overlap: {sorted(overlap)}" if overlap else None


def _check_history_matches_odios(st: State) -> str | None:
    history = st["release_history"]
    odios = st["odios"]
    if history and odios and history[-1] != odios:
        return f"release_history[-1]={history[-1]!r} != state.odios={odios!r}"
    return None


def _check_expected_version(st: State, expected: str) -> str | None:
    ver = st["odios"]
    # PR pre-releases tag as `pr-<N>`; the resolved odios string is a
    # git-describe (e.g. 2026.4.2b2-20-g7c1f6c4). Released tags match exactly.
    if expected.startswith("pr-"):
        if not _VERSION_RE.match(ver):
            return f"state.odios={ver!r} not a valid version for {expected}"
    elif ver != expected:
        return f"state.odios={ver!r} expected {expected}"
    return None


def run_verify(state_path: str, expected_version: str | None) -> int:
    try:
        st = state.read_state(state_path)
    except FileNotFoundError:
        print("no state.json on disk", file=sys.stderr)
        return 2
    except (OSError, json.JSONDecodeError, state.StateError) as e:
        print(f"  {e}", file=sys.stderr)
        return 1

    for warning in [w for w in (_warn_features_unknown(st),) if w]:
        print(f"  warning: {warning}", file=sys.stderr)

    checks: list[str | None] = [
        _check_features_no_overlap(st),
        _check_roles_no_overlap(st),
        _check_history_matches_odios(st),
    ]
    if expected_version:
        checks.append(_check_expected_version(st, expected_version))

    errors = [c for c in checks if c]
    for err in errors:
        print(f"  {err}", file=sys.stderr)
    return 1 if errors else 0
