"""Components = odios roles (services) and features (plugins of a role).

state.json is the source of truth: `roles`/`features` list what the last
run installed, `roles_excluded`/`features_excluded` what the user opted out
of. A name in neither list is "default": install.sh's own Y defaults will
install it on the next upgrade — that's how a role added by a newer release
self-installs. Toggling here only edits those lists; nothing is installed
or removed until `odioctl upgrade apply` runs.

Opt-in roles (`RoleInfo.default_install=False`, e.g. qbzd) invert that:
install.sh asks `[y/N]`, so a name in neither list means *off*. Enabling one
records it in `roles` with an empty version — that is what makes
derive_install_env emit the explicit `INSTALL_X=Y`; the next write_state.yml
replaces the placeholder with the version actually installed.

The catalog below is advisory (labels, packages, feature→role parent, the
per-component actions the web UI offers). Any name present in state.json is
accepted even if unknown here, so a role that odios adds later never gets
blocked by a stale odioctl.
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict, dataclass
from typing import Literal

from odioctl import state
from odioctl.state import State

Kind = Literal["role", "feature"]
Status = Literal["installed", "excluded", "default"]


@dataclass(frozen=True)
class Action:
    """A one-off command the box runs for the user, so no shell is needed.

    `argv` is fixed here and never built from request input; `{host}` is
    substituted with the address the browser reached the box by, so a login
    callback comes back to this machine (`qbzd login --callback-host`).

    These commands print a URL and then keep running until the user has
    followed it (`qbzd login`: 300s deadline, one-shot listener on an ephemeral
    port), so the web UI starts them, lifts the URL off stdout and shows it as
    a link — it does not wait for them. Only for installed components.
    """

    id: str  # form value, unique per component
    label: str  # button text
    description: str  # one line: what the command does
    argv: tuple[str, ...]
    link_scheme: str = "https://"  # the stdout token to surface as a link
    link_label: str = "Open this link"  # anchor text for that token
    link_note: str = ""  # how long the operator has to follow it


@dataclass(frozen=True)
class RoleInfo:
    label: str  # product name the user knows
    description: str  # one line, what it does
    group: str
    package: str | None = None
    default_install: bool = True  # False = install.sh asks [y/N]; see module docstring
    actions: tuple[Action, ...] = ()  # commands offered next to an installed component


@dataclass(frozen=True)
class FeatureInfo:
    label: str
    description: str
    package: str
    parent: str
    actions: tuple[Action, ...] = ()


# Display order of the web UI / `components list`; unknown roles go to the last group.
GROUPS: tuple[str, ...] = ("Audio", "Playback", "Streaming", "System")

# Roles that install.sh always runs; never user-toggleable.
INFRA_ROLES = frozenset({"common", "upgrade"})

# Insertion order = display order within a group.
ROLE_CATALOG: dict[str, RoleInfo] = {
    # The odios `pipewire` role is experimental and not exposed by install.sh — not listed.
    "pulseaudio": RoleInfo(
        "PulseAudio",
        "Sound server, also a network audio sink for other machines",
        "Audio",
        "pulseaudio",
    ),
    "bluetooth": RoleInfo(
        "Bluetooth",
        "A2DP sink with automatic pairing, and output to Bluetooth speakers",
        "Audio",
        "bluez",
    ),
    "mpd": RoleInfo(
        "MPD", "Music Player Daemon: local library, CDs, web radios", "Playback", "mpd"
    ),
    "mpd_discplayer": RoleInfo(
        "CD player", "Audio CD playback through MPD, with metadata", "Playback", "mpd-discplayer"
    ),
    "shairport_sync": RoleInfo(
        "AirPlay", "AirPlay receiver (Shairport Sync)", "Streaming", "shairport-sync"
    ),
    "spotifyd": RoleInfo(
        "Spotify Connect", "Spotify Connect receiver (spotifyd)", "Streaming", "spotifyd"
    ),
    "qbzd": RoleInfo(
        "Qobuz Connect",
        "Qobuz Connect endpoint (qbzd, alpha)",
        "Streaming",
        "qbzd",
        default_install=False,
        actions=(
            Action(
                id="login",
                label="Log in to Qobuz",
                description="Sign in to Qobuz — opens a Qobuz link, the box catches the callback",
                argv=("qbzd", "login", "--callback-host", "{host}"),
                link_label="Open the Qobuz sign-in page",
                link_note="valid 5 minutes",
            ),
        ),
    ),
    "snapclient": RoleInfo("Snapcast", "Multi-room audio client", "Streaming", "snapclient"),
    "upmpdcli": RoleInfo(
        "UPnP / DLNA", "UPnP/OpenHome renderer (upmpdcli)", "Streaming", "upmpdcli"
    ),
    "odio_api": RoleInfo("odio-api", "Remote control API and web dashboard", "System", "odio-api"),
    "branding": RoleInfo("Branding", "odio login banner (MOTD)", "System"),
    "common": RoleInfo("Base system", "Core configuration shared by every component", "System"),
    "upgrade": RoleInfo("Upgrade", "odioctl and the upgrade check timer", "System"),
}

FEATURE_CATALOG: dict[str, FeatureInfo] = {
    "mympd": FeatureInfo("myMPD", "Web UI for MPD (port 8080)", "mympd", "mpd"),
    "tidal": FeatureInfo("Tidal", "Tidal streaming through upmpdcli", "upmpdcli-tidal", "upmpdcli"),
    "qobuz": FeatureInfo("Qobuz", "Qobuz streaming through upmpdcli", "upmpdcli-qobuz", "upmpdcli"),
    "upnpwebradios": FeatureInfo(
        "Web radios", "Internet radios through upmpdcli", "upmpdcli-radios", "upmpdcli"
    ),
}

_ROLE_ORDER = {name: i for i, name in enumerate(ROLE_CATALOG)}
_FEATURE_ORDER = {name: i for i, name in enumerate(FEATURE_CATALOG)}


class ComponentError(ValueError):
    """Invalid component operation (unknown name/kind, infra role)."""


@dataclass
class Component:
    kind: Kind
    name: str
    label: str
    description: str
    group: str
    status: Status
    installed_version: str | None
    parent: str | None
    toggleable: bool
    actions: tuple[Action, ...]

    @property
    def enabled(self) -> bool:
        return self.status != "excluded"

    def to_dict(self) -> dict[str, object]:
        d = asdict(self)
        d["enabled"] = self.enabled
        return d


# Version stored for an opt-in role enabled here but not installed yet: `roles`
# membership is what makes derive_install_env emit INSTALL_X=Y, and the empty
# string keeps it out of the version comparisons (`_role_up_to_date`,
# `_compute_role_upgrades`) until install.sh writes the real one.
REQUESTED_VERSION = ""


def _role_status(st: State, name: str) -> Status:
    if st["roles"].get(name):
        return "installed"
    if name in st["roles"]:
        return "default"  # opted in here, installs on the next apply
    if name in st["roles_excluded"]:
        return "excluded"
    info = ROLE_CATALOG.get(name)
    if info and not info.default_install:
        return "excluded"  # install.sh answers N: neither list means off, not default
    return "default"


def _feature_status(st: State, name: str) -> Status:
    if name in st["features"]:
        return "installed"
    if name in st["features_excluded"]:
        return "excluded"
    return "default"


def _order(known: dict[str, int], name: str) -> tuple[int, str]:
    return (known.get(name, len(known)), name)


def list_components(st: State) -> list[Component]:
    """Roles in catalog order (grouped), unknown roles last; then features."""
    roles = set(ROLE_CATALOG) | set(st["roles"]) | set(st["roles_excluded"])
    features = set(FEATURE_CATALOG) | set(st["features"]) | set(st["features_excluded"])
    out: list[Component] = []
    for name in sorted(roles, key=lambda n: _order(_ROLE_ORDER, n)):
        info = ROLE_CATALOG.get(name)
        out.append(
            Component(
                kind="role",
                name=name,
                label=info.label if info else name,
                description=info.description if info else "",
                group=info.group if info else GROUPS[-1],
                status=_role_status(st, name),
                installed_version=st["roles"].get(name) or None,
                parent=None,
                toggleable=name not in INFRA_ROLES,
                actions=info.actions if info else (),
            )
        )
    for name in sorted(features, key=lambda n: _order(_FEATURE_ORDER, n)):
        finfo = FEATURE_CATALOG.get(name)
        out.append(
            Component(
                kind="feature",
                name=name,
                label=finfo.label if finfo else name,
                description=finfo.description if finfo else "",
                group=GROUPS[-1],
                status=_feature_status(st, name),
                installed_version=None,
                parent=finfo.parent if finfo else None,
                toggleable=True,
                actions=finfo.actions if finfo else (),
            )
        )
    return out


def _known(st: State, kind: Kind, name: str) -> bool:
    if kind == "role":
        return name in ROLE_CATALOG or name in st["roles"] or name in st["roles_excluded"]
    return name in FEATURE_CATALOG or name in st["features"] or name in st["features_excluded"]


def set_component(st: State, kind: Kind, name: str, enabled: bool) -> State:
    """Return a copy of `st` with `name` opted in (enabled) or out.

    Disabling a role removes it from `roles` *and* adds it to
    `roles_excluded`: derive_install_env emits Y for `roles` after N for the
    excluded list, so leaving it in `roles` would win. Enabling only clears
    the exclusion — install.sh's default Y (and no RUN_X=N since the role is
    no longer in `roles`) makes the next apply install it in full.

    Enabling an opt-in role (install.sh asks [y/N]) also records it in `roles`
    with REQUESTED_VERSION, since clearing the exclusion would otherwise leave
    install.sh answering N for it.
    """
    if kind not in ("role", "feature"):
        raise ComponentError(f"unknown component kind {kind!r}")
    if kind == "role" and name in INFRA_ROLES:
        raise ComponentError(f"{name!r} is an infrastructure role and cannot be toggled")
    if not _known(st, kind, name):
        raise ComponentError(f"unknown {kind} {name!r}")

    new = State(
        odios=st["odios"],
        install_mode=st["install_mode"],
        target_user=st["target_user"],
        roles=dict(st["roles"]),
        roles_excluded=list(st["roles_excluded"]),
        features=list(st["features"]),
        features_excluded=list(st["features_excluded"]),
        release_history=list(st["release_history"]),
    )
    if kind == "role":
        excluded = set(new["roles_excluded"])
        if enabled:
            excluded.discard(name)
            info = ROLE_CATALOG.get(name)
            if info and not info.default_install and name not in new["roles"]:
                new["roles"][name] = REQUESTED_VERSION
        else:
            new["roles"].pop(name, None)
            excluded.add(name)
        new["roles_excluded"] = sorted(excluded)
    else:
        active = set(new["features"])
        excluded = set(new["features_excluded"])
        if enabled:
            excluded.discard(name)
        else:
            active.discard(name)
            excluded.add(name)
        new["features"] = sorted(active)
        new["features_excluded"] = sorted(excluded)
    return new


def find_action(kind: Kind, name: str, action_id: str) -> Action | None:
    """The catalog action `action_id` of a component, or None — the only way an
    argv is resolved, so a request can never name a command of its own."""
    info: RoleInfo | FeatureInfo | None = (
        ROLE_CATALOG.get(name) if kind == "role" else FEATURE_CATALOG.get(name)
    )
    if info is None:
        return None
    return next((a for a in info.actions if a.id == action_id), None)


def label_of(kind: Kind, name: str) -> str:
    info = ROLE_CATALOG.get(name) if kind == "role" else FEATURE_CATALOG.get(name)
    return info.label if info else name


def pending_components(st: State, available_roles: set[str] | None = None) -> list[str]:
    """Components that the next `upgrade apply` would install: toggleable roles in
    "default" status (not installed, not excluded) that the target release ships —
    `available_roles` is the manifest's role set, the catalog when unknown — plus
    "default" features whose parent role is installed or pending. An opt-in role
    only reaches "default" once it has been enabled here, so it is pending then
    and never before.

    Disabling is not a pending change: install.sh just skips the role (nothing is
    uninstalled), so there is nothing to apply.

    Returned as ["role:mpd", "feature:mympd", …] in catalog order.
    """
    shipped = available_roles if available_roles is not None else set(ROLE_CATALOG)
    pending: list[str] = []
    pending_roles: set[str] = set()
    for c in list_components(st):
        if c.kind == "role":
            if c.toggleable and c.status == "default" and c.name in shipped:
                pending.append(f"role:{c.name}")
                pending_roles.add(c.name)
        elif c.status == "default" and c.parent:
            parent_on = c.parent in st["roles"] or c.parent in pending_roles
            if parent_on:
                pending.append(f"feature:{c.name}")
    return pending


def load(path: str) -> State:
    return state.read_state(path)


def save(path: str, st: State) -> None:
    state.write_state_file(path, st)


APPLY_NOTE = (
    "Enabling installs on the next upgrade; disabling keeps the component installed "
    "but stops updating it."
)


# --- CLI -----------------------------------------------------------------


def add_components_arguments(p: argparse.ArgumentParser) -> None:
    p.description = "List or toggle odios roles and features recorded in state.json."
    p.add_argument("--state", default=state.SYSTEM_STATE_PATH, help="path to state.json")
    sub = p.add_subparsers(dest="components_cmd", metavar="COMMAND", required=True)
    ls = sub.add_parser("list", help="show every role/feature and its status")
    ls.add_argument("--json", action="store_true", help="machine-readable output")
    for verb, enabled in (("enable", True), ("disable", False)):
        sp = sub.add_parser(verb, help=f"{verb} a role or feature by name")
        sp.add_argument("name")
        sp.set_defaults(enabled=enabled)


def components_from_args(ns: argparse.Namespace) -> int:
    try:
        st = load(ns.state)
    except (OSError, json.JSONDecodeError, state.StateError) as e:
        print(f"Error reading {ns.state}: {e}", file=sys.stderr)
        return 2

    if ns.components_cmd == "list":
        comps = list_components(st)
        if ns.json:
            print(json.dumps([c.to_dict() for c in comps], indent=2))
        else:
            _print_table(comps)
        return 0

    kind: Kind = (
        "role"
        if ns.name in ROLE_CATALOG or ns.name in st["roles"] or ns.name in st["roles_excluded"]
        else "feature"
    )
    try:
        new = set_component(st, kind, ns.name, ns.enabled)
    except ComponentError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2
    try:
        save(ns.state, new)
    except OSError as e:
        print(f"Error writing {ns.state}: {e}", file=sys.stderr)
        return 2
    verb = "enabled" if ns.enabled else "disabled"
    print(f"{kind} {ns.name} {verb}. {APPLY_NOTE}")
    return 0


def _print_table(comps: list[Component]) -> None:
    for kind in ("role", "feature"):
        print(f"{kind}s:")
        for c in comps:
            if c.kind != kind:
                continue
            ver = f" ({c.installed_version})" if c.installed_version else ""
            lock = "" if c.toggleable else " [infra]"
            parent = f" ← {c.parent}" if c.parent else ""
            print(f"  {c.name:<16} {c.status:<10}{ver}{parent}{lock}")
            # Only offered by the web UI, and only once the binaries are there.
            for a in c.actions if c.status == "installed" else ():
                print(f"      action: {' '.join(a.argv)} — {a.description}")
