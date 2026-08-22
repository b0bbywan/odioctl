"""odios version strings: parsing and ordering.

Versions look like ``2026.4.2b2`` or ``2026.7.0rc1`` (calver + optional
pre-release phase), optionally suffixed by a git-describe tail
(``-20-g7c1f6c4``) for PR pre-releases.
"""

from __future__ import annotations

import re

_PRE_PHASES = {"a": 0, "b": 1, "rc": 2}
_VERSION_RE = re.compile(r"^(\d+)\.(\d+)\.(\d+)(?:(a|b|rc)(\d+))?(?:-(\d+)-g[0-9a-f]+)?$")


def parse_version(v: str) -> tuple[int, ...]:
    """Return a sortable tuple; unparseable input (incl. "latest") maps to (0,)."""
    m = _VERSION_RE.match(v)
    if not m:
        return (0,)
    year, month, patch = int(m.group(1)), int(m.group(2)), int(m.group(3))
    if m.group(4):
        phase = _PRE_PHASES[m.group(4)]
        pre_num = int(m.group(5))
    else:
        phase = 3
        pre_num = 0
    dev_commits = int(m.group(6)) if m.group(6) else 0
    return (year, month, patch, phase, pre_num, dev_commits)


def _is_downgrade(target: str, state_odios: str | None) -> bool:
    """True when both versions parse cleanly and target < state_odios.

    Returns False for "latest" (parses to (0,)) or any unparseable string —
    safer to let install.sh resolve and fail than to refuse on a parse miss.
    """
    if not state_odios:
        return False
    target_v = parse_version(target)
    state_v = parse_version(state_odios)
    if target_v == (0,) or state_v == (0,):
        return False
    return target_v < state_v


def _role_up_to_date(
    installed: str | None,
    target: str | None,
    state_odios: str | None,
) -> bool:
    """True when the installed role version covers target AND is trustworthy.

    "Trustworthy" = target is at or below state.odios. A target ahead of
    state.odios means the manifest is past the last release certified on this
    box, so the dpkg marker for `installed` was set under conditions we can't
    verify — re-run. At release time target <= state.odios for every role,
    so the guard is a no-op there.
    """
    if not installed or not target:
        return False
    if parse_version(target) > parse_version(installed):
        return False
    return state_odios is None or parse_version(target) <= parse_version(state_odios)
