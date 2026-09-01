"""`odioctl` command-line entry point."""

from __future__ import annotations

import argparse
from collections.abc import Sequence

from odioctl import __version__, components, dac, netinfo
from odioctl.upgrade import apply, check, verify


def _cmd_web(ns: argparse.Namespace) -> int:
    from odioctl.web import server  # lazy: keeps `odioctl upgrade` startup lean

    return server.web_from_args(ns)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="odioctl",
        description="odio system control: upgrades, components, DAC overlay and a local web UI.",
    )
    p.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    sub = p.add_subparsers(dest="command", metavar="COMMAND")

    up = sub.add_parser("upgrade", help="check for / apply / verify odios upgrades")
    upsub = up.add_subparsers(dest="upgrade_cmd", metavar="COMMAND", required=True)
    sp = upsub.add_parser("check", help="compare state.json with the published manifest")
    check.add_check_arguments(sp)
    sp.set_defaults(func=check.check_from_args)
    sp = upsub.add_parser("apply", help="run install.sh from the target release")
    apply.add_apply_arguments(sp)
    sp.set_defaults(func=apply.apply_from_args)
    sp = upsub.add_parser("verify", help="schema sanity checks on state.json")
    verify.add_verify_arguments(sp)
    sp.set_defaults(func=verify.verify_from_args)

    sp = sub.add_parser("pwa-url", help="print the PWA URL for this host")
    netinfo.add_pwa_url_arguments(sp)
    sp.set_defaults(func=netinfo.cmd_pwa_url)

    sp = sub.add_parser("components", help="list / enable / disable roles and features")
    components.add_components_arguments(sp)
    sp.set_defaults(func=components.components_from_args)

    sp = sub.add_parser("dac", help="select the DAC overlay in config.txt")
    dac.add_dac_arguments(sp)
    sp.set_defaults(func=dac.dac_from_args)

    sp = sub.add_parser("web", help="serve the local web UI")
    from odioctl.web import server  # argument definitions only; server is stdlib

    server.add_web_arguments(sp)
    sp.set_defaults(func=_cmd_web)
    return p


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    ns = parser.parse_args(argv)
    if not getattr(ns, "func", None):
        parser.print_help()
        return 2
    return int(ns.func(ns))
