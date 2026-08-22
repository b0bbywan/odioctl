"""Shared builders for the odioctl test-suite."""

from __future__ import annotations

import json
import os

from odioctl.state import State


def make_state(
    *,
    roles: dict[str, str] | None = None,
    roles_excluded: list[str] | None = None,
    features: list[str] | None = None,
    features_excluded: list[str] | None = None,
    odios: str = "2026.5.0",
    install_mode: str = "image",
    target_user: str = "odio",
    release_history: list[str] | None = None,
) -> State:
    """Build a current-schema State (every field set) for unit tests."""
    return State(
        odios=odios,
        install_mode=install_mode,
        target_user=target_user,
        roles=dict(roles) if roles is not None else {},
        roles_excluded=list(roles_excluded) if roles_excluded is not None else [],
        features=list(features) if features is not None else [],
        features_excluded=list(features_excluded) if features_excluded is not None else [],
        release_history=list(release_history) if release_history is not None else [odios],
    )


def write_state(directory: str, state: State, name: str = "state.json") -> str:
    path = os.path.join(directory, name)
    with open(path, "w") as f:
        json.dump(state, f, indent=4, sort_keys=True)
        f.write("\n")
    return path
