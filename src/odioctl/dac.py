"""DAC selection: manage the `dtoverlay=` line in the Raspberry Pi config.txt.

odioctl owns one marked block at the end of config.txt:

    # BEGIN odioctl dac -- managed block, edit with `odioctl dac`
    [all]
    dtparam=audio=off
    dtoverlay=hifiberry-dacplus-std
    # END odioctl dac

The `[all]` resets any `[pi4]`/`[cm5]`-style filter section that may be
open above so the block always applies. Pre-existing top-level audio lines
(`dtparam=audio=…`, `dtoverlay=<known DAC overlay>`) are commented out with
a recognisable prefix so `dac unset` can restore them verbatim. Text
manipulation is pure (`parse`/`apply`/`unapply`) and unit-tested; only
`read_config`/`write_config` touch the disk. Writing needs root — the web
UI and unprivileged callers go through `sudo odioctl dac set <id>`, and the
sudoers wildcard is safe because argparse `choices` re-validates the id.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import re
import shutil
import sys
from dataclasses import asdict, dataclass

from odioctl import fsutil

CONFIG_CANDIDATES = ("/boot/firmware/config.txt", "/boot/config.txt")
REBOOT_FLAG = "/run/odioctl/reboot-required"

BEGIN = "# BEGIN odioctl dac -- managed block, edit with `odioctl dac`"
END = "# END odioctl dac"
DISABLED_PREFIX = "#odioctl-disabled: "

ONBOARD = "onboard"


@dataclass(frozen=True)
class DacEntry:
    id: str
    label: str
    overlay: str | None
    params: str = ""

    def dtoverlay_line(self) -> str | None:
        if self.overlay is None:
            return None
        return f"dtoverlay={self.overlay}" + (f",{self.params}" if self.params else "")


CATALOG: tuple[DacEntry, ...] = (
    DacEntry(ONBOARD, "Onboard audio (3.5mm jack / HDMI)", None),
    # HiFiBerry
    DacEntry(
        "hifiberry-dac",
        "HiFiBerry DAC / DAC+ Light / DAC Zero / MiniAmp / PCM5102",
        "hifiberry-dac",
    ),
    DacEntry("hifiberry-dacplus-std", "HiFiBerry DAC+ (standard)", "hifiberry-dacplus-std"),
    DacEntry("hifiberry-dacplus-pro", "HiFiBerry DAC+ Pro / DAC2 Pro", "hifiberry-dacplus-pro"),
    DacEntry("hifiberry-dacplusadc", "HiFiBerry DAC+ ADC", "hifiberry-dacplusadc"),
    DacEntry(
        "hifiberry-dacplusadcpro",
        "HiFiBerry DAC+ ADC Pro / DAC2 ADC Pro",
        "hifiberry-dacplusadcpro",
    ),
    DacEntry("hifiberry-dacplushd", "HiFiBerry DAC+ HD / DAC2 HD", "hifiberry-dacplushd"),
    DacEntry("hifiberry-digi", "HiFiBerry Digi / Digi+", "hifiberry-digi"),
    DacEntry("hifiberry-digi-pro", "HiFiBerry Digi+ Pro / Digi2 Pro", "hifiberry-digi-pro"),
    DacEntry("hifiberry-amp", "HiFiBerry Amp / Amp+", "hifiberry-amp"),
    DacEntry("hifiberry-amp100", "HiFiBerry Amp100", "hifiberry-amp100"),
    DacEntry("hifiberry-amp3", "HiFiBerry Amp3", "hifiberry-amp3"),
    # IQaudIO / Raspberry Pi
    DacEntry("iqaudio-dac", "IQaudIO Pi-DAC / Pi-DAC Zero", "iqaudio-dac"),
    DacEntry(
        "iqaudio-dacplus",
        "IQaudIO Pi-DAC+ / Pi-DAC Pro / Pi-DigiAMP+ / Raspberry Pi DAC Pro",
        "iqaudio-dacplus",
    ),
    DacEntry(
        "iqaudio-digi-wm8804-audio",
        "IQaudIO Pi-Digi+ / Raspberry Pi DigiAMP+",
        "iqaudio-digi-wm8804-audio",
    ),
    DacEntry("iqaudio-codec", "IQaudIO Pi-Codec+ / Raspberry Pi Codec Zero", "iqaudio-codec"),
    DacEntry("rpi-codeczero", "Raspberry Pi Codec Zero (legacy overlay)", "rpi-codeczero"),
    # Allo
    DacEntry("allo-boss-dac-pcm512x-audio", "Allo Boss DAC", "allo-boss-dac-pcm512x-audio"),
    DacEntry("allo-boss2-dac-audio", "Allo Boss2 DAC", "allo-boss2-dac-audio"),
    DacEntry("allo-piano-dac-pcm512x-audio", "Allo Piano DAC", "allo-piano-dac-pcm512x-audio"),
    DacEntry(
        "allo-piano-dac-plus-pcm512x-audio",
        "Allo Piano DAC 2.1",
        "allo-piano-dac-plus-pcm512x-audio",
    ),
    DacEntry("allo-digione", "Allo DigiOne", "allo-digione"),
    DacEntry("allo-katana-dac-audio", "Allo Katana DAC", "allo-katana-dac-audio"),
    # JustBoom
    DacEntry("justboom-dac", "JustBoom DAC / Amp", "justboom-dac"),
    DacEntry("justboom-digi", "JustBoom Digi", "justboom-digi"),
    DacEntry("justboom-both", "JustBoom DAC + Digi", "justboom-both"),
    # Others
    DacEntry(
        "audioinjector-wm8731-audio", "AudioInjector Zero / Stereo", "audioinjector-wm8731-audio"
    ),
    DacEntry("pisound", "Blokas pisound", "pisound"),
    DacEntry("googlevoicehat-soundcard", "Google AIY Voice HAT", "googlevoicehat-soundcard"),
)

BY_ID: dict[str, DacEntry] = {e.id: e for e in CATALOG}
BY_OVERLAY: dict[str, DacEntry] = {e.overlay: e for e in CATALOG if e.overlay}
KNOWN_OVERLAYS = frozenset(BY_OVERLAY)
# Any overlay from these families counts as an audio line even when it is not in the
# catalog (legacy names such as `hifiberry-dacplus`, vendor variants): it is commented
# out by apply() and reported as stray instead of being left to fight the managed block.
AUDIO_OVERLAY_PREFIXES = (
    "hifiberry-",
    "iqaudio-",
    "rpi-codeczero",
    "allo-",
    "justboom-",
    "audioinjector-",
    "pisound",
    "googlevoicehat-",
    "i-sabre-",
    "dionaudio-",
    "fe-pi-",
    "rpi-dac",
    "rpi-proto",
    "udrc",
    "adau",
)

_OVERLAY_RE = re.compile(r"^\s*dtoverlay\s*=\s*([^,\s]+)(?:,(.*))?\s*$")
_AUDIO_RE = re.compile(r"^\s*dtparam\s*=\s*audio\s*=\s*(on|off)\s*$", re.IGNORECASE)


@dataclass
class DacStatus:
    current: str | None  # DacEntry.id, or None when nothing recognisable is configured
    managed: bool  # True when the odioctl block is present
    stray_lines: list[str]  # active audio lines outside the managed block


def _split_block(lines: list[str]) -> tuple[list[str], list[str] | None, list[str]]:
    """Return (before, block_lines_or_None, after). A BEGIN without END swallows to EOF."""
    try:
        start = lines.index(BEGIN)
    except ValueError:
        return lines, None, []
    try:
        end = lines.index(END, start + 1)
    except ValueError:
        end = len(lines) - 1
    return lines[:start], lines[start : end + 1], lines[end + 1 :]


def _entry_for_lines(lines: list[str]) -> str | None:
    audio_on = False
    for line in lines:
        m = _OVERLAY_RE.match(line)
        if m and m.group(1) in BY_OVERLAY:
            return BY_OVERLAY[m.group(1)].id
        a = _AUDIO_RE.match(line)
        if a and a.group(1).lower() == "on":
            audio_on = True
    return ONBOARD if audio_on else None


def is_audio_overlay(name: str) -> bool:
    return name in KNOWN_OVERLAYS or name.startswith(AUDIO_OVERLAY_PREFIXES)


def _is_audio_line(line: str) -> bool:
    m = _OVERLAY_RE.match(line)
    if m and is_audio_overlay(m.group(1)):
        return True
    return bool(_AUDIO_RE.match(line))


def parse(text: str) -> DacStatus:
    lines = text.splitlines()
    before, block, after = _split_block(lines)
    outside = before + after
    stray = [ln for ln in outside if _is_audio_line(ln)]
    if block is not None:
        return DacStatus(current=_entry_for_lines(block), managed=True, stray_lines=stray)
    return DacStatus(current=_entry_for_lines(outside), managed=False, stray_lines=stray)


def render_block(entry: DacEntry) -> list[str]:
    lines = [BEGIN, "[all]", f"dtparam=audio={'on' if entry.overlay is None else 'off'}"]
    dto = entry.dtoverlay_line()
    if dto:
        lines.append(dto)
    lines.append(END)
    return lines


def _strip_trailing_blank(lines: list[str]) -> list[str]:
    while lines and not lines[-1].strip():
        lines.pop()
    return lines


def apply(text: str, entry: DacEntry) -> str:
    """Return config.txt text with `entry` selected. Idempotent."""
    before, _block, after = _split_block(text.splitlines())
    body: list[str] = []
    for line in before + after:
        if _is_audio_line(line):
            body.append(DISABLED_PREFIX + line)
        else:
            body.append(line)
    body = _strip_trailing_blank(body)
    if body:
        body.append("")
    body.extend(render_block(entry))
    return "\n".join(body) + "\n"


def unapply(text: str) -> str:
    """Return config.txt text with the managed block removed and disabled lines restored."""
    before, _block, after = _split_block(text.splitlines())
    body: list[str] = []
    for line in before + after:
        if line.startswith(DISABLED_PREFIX):
            body.append(line[len(DISABLED_PREFIX) :])
        else:
            body.append(line)
    body = _strip_trailing_blank(body)
    return ("\n".join(body) + "\n") if body else ""


# --- I/O -------------------------------------------------------------------


def find_config_txt() -> str | None:
    for p in CONFIG_CANDIDATES:
        if os.path.isfile(p):
            return p
    return None


def read_config(path: str) -> str:
    with open(path, encoding="utf-8", errors="replace") as f:
        return f.read()


def write_config(path: str, text: str) -> None:
    backup = path + ".odioctl.bak"
    if not os.path.exists(backup):
        with contextlib.suppress(OSError):
            shutil.copy2(path, backup)
    fsutil.atomic_write_text(path, text)


def mark_reboot_required() -> None:
    with contextlib.suppress(OSError):
        os.makedirs(os.path.dirname(REBOOT_FLAG), exist_ok=True)
        with open(REBOOT_FLAG, "w") as f:
            f.write("dac\n")


def reboot_required() -> bool:
    return os.path.exists(REBOOT_FLAG)


def status(config_path: str | None = None) -> dict[str, object]:
    """Status dict shared by the CLI and the web API."""
    path = config_path or find_config_txt()
    if path is None or not os.path.isfile(path):
        return {
            "supported": False,
            "config": path,
            "current": None,
            "managed": False,
            "stray_lines": [],
            "reboot_required": reboot_required(),
        }
    st = parse(read_config(path))
    return {
        "supported": True,
        "config": path,
        "current": st.current,
        "managed": st.managed,
        "stray_lines": st.stray_lines,
        "reboot_required": reboot_required(),
    }


# --- CLI -----------------------------------------------------------------


def add_dac_arguments(p: argparse.ArgumentParser) -> None:
    p.description = "Select the DAC (dtoverlay) in the Raspberry Pi config.txt."
    sub = p.add_subparsers(dest="dac_cmd", metavar="COMMAND", required=True)
    ls = sub.add_parser("list", help="list supported DACs")
    ls.add_argument("--json", action="store_true")
    stt = sub.add_parser("status", help="show the currently configured DAC")
    stt.add_argument("--json", action="store_true")
    stt.add_argument("--config", help="path to config.txt (default: auto-detect)")
    st = sub.add_parser("set", help="select a DAC (root; reboot required)")
    st.add_argument("id", choices=sorted(BY_ID), metavar="ID", help="DAC id from `dac list`")
    st.add_argument("--config", help="path to config.txt (default: auto-detect)")
    st.add_argument("--dry-run", action="store_true", help="print the resulting file, don't write")
    un = sub.add_parser("unset", help="remove the odioctl block, restore previous lines (root)")
    un.add_argument("--config", help="path to config.txt (default: auto-detect)")
    un.add_argument("--dry-run", action="store_true")


def dac_from_args(ns: argparse.Namespace) -> int:
    if ns.dac_cmd == "list":
        if ns.json:
            print(json.dumps([asdict(e) for e in CATALOG], indent=2))
        else:
            for e in CATALOG:
                print(f"  {e.id:<36} {e.label}")
        return 0

    if ns.dac_cmd == "status":
        s = status(ns.config)
        if ns.json:
            print(json.dumps(s, indent=2))
        elif not s["supported"]:
            print("no config.txt found — not a Raspberry Pi boot partition?")
        else:
            cur = s["current"]
            label = BY_ID[str(cur)].label if isinstance(cur, str) and cur in BY_ID else "(unknown)"
            print(f"config:   {s['config']}")
            print(f"current:  {cur or '(none)'} {label if cur else ''}".rstrip())
            print(f"managed:  {'yes' if s['managed'] else 'no'}")
            if s["reboot_required"]:
                print("reboot required to apply the last change")
        return 0

    path = ns.config or find_config_txt()
    if path is None or not os.path.isfile(path):
        print(
            "Error: no config.txt found (tried " + ", ".join(CONFIG_CANDIDATES) + ")",
            file=sys.stderr,
        )
        return 2
    text = read_config(path)
    new = apply(text, BY_ID[ns.id]) if ns.dac_cmd == "set" else unapply(text)

    if ns.dry_run:
        sys.stdout.write(new)
        return 0
    if new == text:
        print("no change")
        return 0
    try:
        write_config(path, new)
    except OSError as e:
        print(f"Error writing {path}: {e}", file=sys.stderr)
        return 2
    mark_reboot_required()
    what = f"DAC set to {ns.id}" if ns.dac_cmd == "set" else "odioctl DAC block removed"
    print(f"{what} in {path} — reboot required")
    return 0
