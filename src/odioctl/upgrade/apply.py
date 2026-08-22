"""`odioctl upgrade apply` — re-run install.sh from the target release.

INSTALL_* is derived from state.json (what the previous install opted in/out
of), RUN_* from the per-role manifest diff (skip roles whose version did not
move), then `curl install.sh | bash` runs with those in its environment.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from dataclasses import dataclass

from odioctl import manifest, state
from odioctl.manifest import Manifest
from odioctl.state import State
from odioctl.versions import _is_downgrade, _role_up_to_date


@dataclass
class ApplyOptions:
    version: str | None = None
    state: str | None = None
    dry_run: bool = False
    force: bool = False
    reinstall: bool = False
    progress: bool = False


def add_apply_arguments(p: argparse.ArgumentParser) -> None:
    p.description = (
        "Re-run install.sh from the target release with INSTALL_X derived "
        "from state.json and RUN_X derived from the per-role manifest diff."
    )
    p.add_argument("--version", help="target version tag (default: latest from upgrades.json)")
    p.add_argument("--state", help=f"path to state.json (default: {state.SYSTEM_STATE_PATH})")
    p.add_argument("--dry-run", action="store_true", help="print the invocation without running")
    p.add_argument("--force", action="store_true", help="run even if no upgrade is reported")
    p.add_argument(
        "--reinstall",
        action="store_true",
        help="re-run every role in full: no smart-upgrade skips, all "
        "first-install scaffold re-applied (implies --force)",
    )
    p.add_argument(
        "--progress",
        dest="progress",
        action="store_true",
        default=None,
        help="set ODIOS_PROGRESS=Y so install.sh emits ODIO_PROGRESS events "
        "(consumed by odio-api via its upgrade socket). Default: auto-on when "
        "odio-api's upgrade socket is present (a real instance, not CI)",
    )
    p.add_argument(
        "--no-progress",
        dest="progress",
        action="store_false",
        help="never emit progress events, even on an instance",
    )


def apply_from_args(ns: argparse.Namespace) -> int:
    progress = ns.progress if ns.progress is not None else _odio_api_listening()
    return run_apply(
        ApplyOptions(
            version=ns.version,
            state=ns.state,
            dry_run=ns.dry_run,
            force=ns.force,
            reinstall=ns.reinstall,
            progress=progress,
        )
    )


def _odio_api_listening() -> bool:
    """True when odio-api's upgrade socket exists — i.e. a real instance, not CI.

    Under sudo (uid 0) XDG_RUNTIME_DIR is not the target_user's, so this
    returns False and the service path keeps its explicit --progress.
    """
    runtime = os.environ.get("XDG_RUNTIME_DIR")
    if not runtime:
        return False
    return os.path.exists(os.path.join(runtime, "odio-api", "upgrade.sock"))


def derive_install_env(st: State) -> dict[str, str]:
    """Return INSTALL_* flags derived from state.json.

    Emits N for everything in the *_excluded lists and Y for everything in
    `roles`/`features`. Anything in neither list is left unset so install.sh's
    own defaults (Y for every optional in upgrade-era releases) take over —
    that's what lets a role added after this tool was written self-install.
    """
    env: dict[str, str] = {}
    for role in st["roles_excluded"]:
        env[f"INSTALL_{role.upper()}"] = "N"
    for feature in st["features_excluded"]:
        env[f"INSTALL_{feature.upper()}"] = "N"
    for role in st["roles"]:
        env[f"INSTALL_{role.upper()}"] = "Y"
    for feature in st["features"]:
        env[f"INSTALL_{feature.upper()}"] = "Y"
    return env


def derive_run_env(
    st: State,
    man: Manifest | None,
    install_env: dict[str, str],
) -> dict[str, str]:
    """Return RUN_<role>=N for roles whose target version matches installed.

    Asymmetric: only N is emitted. Anything else falls through to install.sh's
    `RUN_X=${RUN_X:-$INSTALL_X}` default — i.e. RUN matches INSTALL, today's
    behaviour. That keeps the user-facing API as INSTALL_X only; RUN_X is an
    internal optimisation channel.
    """
    if man is None:
        return {}

    target_roles = man["roles"]
    state_odios = st["odios"]

    env: dict[str, str] = {}
    for role, installed in st["roles"].items():
        # Skip roles the user explicitly excluded — install.sh's INSTALL_X=N
        # already gates them, so the run flag is irrelevant.
        if install_env.get(f"INSTALL_{role.upper()}") == "N":
            continue
        target = target_roles.get(role)
        if _role_up_to_date(installed, target, state_odios):
            env[f"RUN_{role.upper()}"] = "N"

    return env


def _load_state(opts: ApplyOptions) -> tuple[str, State] | None:
    """Resolve (state_path, state) from opts, or None on read/schema error."""
    state_path = opts.state or state.SYSTEM_STATE_PATH
    try:
        st = state.read_state(state_path)
    except (OSError, json.JSONDecodeError, state.StateError) as e:
        print(f"Error reading {state_path}: {e}", file=sys.stderr)
        return None
    print(f"state.json read from {state_path}:", flush=True)
    state.print_state_summary(st)
    return state_path, st


def _build_apply_env(
    st: State,
    version: str,
    target_user: str,
    upgrades_path: str,
    opts: ApplyOptions,
) -> dict[str, str]:
    install_env = derive_install_env(st)
    man = manifest._resolve_manifest(version, upgrades_path)
    # --reinstall bypasses both skip layers: no RUN_X=N (every role runs) and
    # ODIOS_FORCE_SCAFFOLD=Y (read_state.yml blanks odios_prior_* so first-
    # install scaffold re-applies). The RUN_X skip alone wouldn't be enough —
    # a re-run role still skips its scaffold without the force flag.
    run_env = {} if opts.reinstall else derive_run_env(st, man, install_env)
    env_overrides = {
        **install_env,
        **run_env,
        "ODIOS_VERSION": version,
        "TARGET_USER": target_user,
    }
    if opts.reinstall:
        env_overrides["ODIOS_FORCE_SCAFFOLD"] = "Y"
    if opts.progress:
        env_overrides["ODIOS_PROGRESS"] = "Y"

    skipped = sorted(k.removeprefix("RUN_").lower() for k in run_env)
    if opts.reinstall:
        print("  reinstall: running all roles with full scaffold", flush=True)
    elif skipped:
        print(f"  smart-upgrade: skipping unchanged roles: {', '.join(skipped)}", flush=True)
    elif man is None:
        print("  smart-upgrade: manifest unavailable, running all roles", flush=True)
    else:
        print("  smart-upgrade: all roles bumped, running everything", flush=True)

    return env_overrides


def run_apply(opts: ApplyOptions) -> int:
    loaded = _load_state(opts)
    if loaded is None:
        return 2
    state_path, st = loaded
    target_user = st["target_user"]

    # An explicit --state points at a test/dev tree: use its sibling
    # upgrades.json. The system state uses the canonical /var/cache path.
    upgrades_path = (
        os.path.join(os.path.dirname(state_path), "upgrades.json")
        if opts.state
        else state.SYSTEM_UPGRADES_PATH
    )

    if (
        not opts.force
        and not opts.reinstall
        and not opts.version
        and not manifest.upgrade_reported(upgrades_path)
    ):
        print("No upgrade reported in upgrades.json — use --force to override.", flush=True)
        return 0

    version = manifest.resolve_version(opts.version, upgrades_path)
    if _is_downgrade(version, st["odios"]):
        print(f"Refusing to downgrade: target {version} < installed {st['odios']}.", flush=True)
        return 2
    url = manifest.install_url(version)
    env_overrides = _build_apply_env(st, version, target_user, upgrades_path, opts)

    print(f"Upgrading to {version} via {url}", flush=True)
    print("  env passed to install.sh:", flush=True)
    for k in sorted(env_overrides):
        print(f"    {k}={env_overrides[k]}", flush=True)

    if opts.dry_run:
        print("(dry-run, not invoking)", flush=True)
        return 0

    env = {**os.environ, **env_overrides}
    cmd = ["bash", "-c", f"curl -fsSL {url} | bash"]
    return subprocess.run(cmd, env=env).returncode
