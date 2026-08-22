"""DAC selection: manage the `dtoverlay=` line in the Raspberry Pi config.txt.

odioctl owns one marked block at the end of config.txt:

    # BEGIN odioctl dac -- managed block, edit with `odioctl dac`
    [all]
    dtoverlay=
    dtparam=audio=off
    dtoverlay=hifiberry-dacplus-std
    # END odioctl dac

The `[all]` resets any `[pi4]`/`[cm5]`-style filter section that may be
open above so the block always applies, and the empty `dtoverlay=` resets
the target of `dtparam=` back to the base DTB (see OVERLAY_RESET) — the
block sits at the end of the file, so overlays are always loaded above it.
Pre-existing top-level audio lines
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
# An empty `dtoverlay=` "marks the end of a list of overlay parameters" (config.txt
# docs): a `dtparam=` is applied to the last overlay loaded above it, so without
# this reset our `dtparam=audio=` would land on whatever the file loaded last
# (`vc4-kms-v3d` in a stock config.txt) instead of the base DTB — snd_bcm2835
# never loads and ALSA is left with a dummy output.
OVERLAY_RESET = "dtoverlay="
DISABLED_PREFIX = "#odioctl-disabled: "

ONBOARD = "onboard"


@dataclass(frozen=True)
class DacEntry:
    id: str  # also the dtoverlay name: every board is known by its overlay
    label: str
    params: str = ""

    def dtoverlay_line(self) -> str | None:
        """The config.txt line, or None for onboard audio, which has no overlay."""
        if self.id == ONBOARD:
            return None
        return f"dtoverlay={self.id}" + (f",{self.params}" if self.params else "")


CATALOG: tuple[DacEntry, ...] = (
    DacEntry(ONBOARD, "Onboard audio (3.5mm jack / HDMI)"),
    # Generic I2S
    DacEntry("i2s-dac", "Generic passive I2S DAC (Pi as clock master)"),
    DacEntry("i2s-master-dac", "Generic I2S DAC acting as clock master"),
    # HiFiBerry
    DacEntry("hifiberry-dac", "HiFiBerry DAC / DAC+ Light / DAC+ Zero / MiniAmp / PCM5102"),
    DacEntry("hifiberry-dacplus", "HiFiBerry DAC+ (auto-detect std/pro)"),
    DacEntry("hifiberry-dacplus-std", "HiFiBerry DAC+ (standard)"),
    DacEntry("hifiberry-dacplus-pro", "HiFiBerry DAC+ Pro / DAC2 Pro"),
    DacEntry("hifiberry-dacplusadc", "HiFiBerry DAC+ ADC"),
    DacEntry("hifiberry-dacplusadcpro", "HiFiBerry DAC+ ADC Pro / DAC2 ADC Pro"),
    DacEntry("hifiberry-dacplushd", "HiFiBerry DAC+ HD / DAC2 HD"),
    DacEntry("hifiberry-dacplusdsp", "HiFiBerry DAC+ DSP"),
    DacEntry("hifiberry-dac8x", "HiFiBerry DAC8x (Pi 5 only)"),
    DacEntry("hifiberry-studio-dac8x", "HiFiBerry Studio DAC8x"),
    DacEntry("hifiberry-studio-dac8x-pro", "HiFiBerry Studio DAC8x Pro"),
    DacEntry("hifiberry-digi", "HiFiBerry Digi / Digi+"),
    DacEntry("hifiberry-digi-pro", "HiFiBerry Digi+ Pro / Digi2 Pro"),
    DacEntry("hifiberry-studio-digi", "HiFiBerry Studio Digi / AES"),
    DacEntry("hifiberry-amp", "HiFiBerry Amp / Amp+"),
    DacEntry("hifiberry-amp100", "HiFiBerry Amp100"),
    DacEntry("hifiberry-amp3", "HiFiBerry Amp3"),
    DacEntry("hifiberry-amp4pro", "HiFiBerry Amp4 Pro"),
    # IQaudIO
    DacEntry("iqaudio-dac", "IQaudIO Pi-DAC / Pi-DAC Zero"),
    DacEntry("iqaudio-dacplus", "IQaudIO Pi-DAC+ / Pi-DAC Pro / Pi-DigiAMP+"),
    DacEntry("iqaudio-digi-wm8804-audio", "IQaudIO Pi-Digi+"),
    DacEntry("iqaudio-codec", "IQaudIO Pi-Codec+ / Codec Zero"),
    DacEntry("akkordion-iqdacplus", "Digital Dreamtime Akkordion (IQaudIO DAC+ based)"),
    # Raspberry Pi branded
    DacEntry("rpi-dacplus", "Raspberry Pi DAC+"),
    DacEntry("rpi-dacpro", "Raspberry Pi DAC Pro"),
    DacEntry("rpi-digiampplus", "Raspberry Pi DigiAMP+"),
    DacEntry("rpi-codeczero", "Raspberry Pi Codec Zero"),
    # Allo
    DacEntry("allo-boss-dac-pcm512x-audio", "Allo Boss DAC"),
    DacEntry("allo-boss2-dac-audio", "Allo Boss2 DAC"),
    DacEntry("allo-piano-dac-pcm512x-audio", "Allo Piano DAC 2.0 (2.1 in stereo only)"),
    DacEntry("allo-piano-dac-plus-pcm512x-audio", "Allo Piano DAC 2.1"),
    DacEntry("allo-digione", "Allo DigiOne"),
    DacEntry("allo-katana-dac-audio", "Allo Katana DAC"),
    # JustBoom
    DacEntry("justboom-dac", "JustBoom DAC HAT / Amp HAT / DAC Zero / Amp Zero"),
    DacEntry("justboom-digi", "JustBoom Digi HAT / Digi Zero"),
    DacEntry("justboom-both", "JustBoom DAC + Digi (stacked)"),
    # PiFi
    DacEntry("pifi-dac-hd", "PiFi DAC HD"),
    DacEntry("pifi-dac-zero", "PiFi DAC Zero"),
    DacEntry("pifi-40", "PiFi 40W stereo amplifier"),
    DacEntry("pifi-mini-210", "PiFi Mini stereo amplifier"),
    # Dion Audio
    DacEntry("dionaudio-loco", "Dion Audio LOCO DAC-AMP"),
    DacEntry("dionaudio-loco-v2", "Dion Audio LOCO-V2 DAC-AMP"),
    DacEntry("dionaudio-kiwi", "Dion Audio KIWI Streamer"),
    # AudioInjector
    DacEntry("audioinjector-wm8731-audio", "AudioInjector Zero / Stereo"),
    DacEntry("audioinjector-addons", "AudioInjector Octo"),
    DacEntry("audioinjector-ultra", "AudioInjector Ultra"),
    DacEntry("audioinjector-isolated-soundcard", "AudioInjector Isolated"),
    DacEntry("audioinjector-bare-i2s", "AudioInjector bare I2S"),
    # Blokas
    DacEntry("pisound", "Blokas Pisound"),
    DacEntry("pisound-pi5", "Blokas Pisound (Pi 5)"),
    DacEntry("pisound-micro", "Blokas Pisound Micro"),
    # Others
    DacEntry("applepi-dac", "Orchard Audio ApplePi-DAC"),
    DacEntry("i-sabre-q2m", "Audiophonics I-Sabre Q2M"),
    DacEntry("rra-digidac1-wm8741-audio", "Red Rocks Audio DigiDAC1"),
    DacEntry("dacberry400", "DACBerry 400"),
    DacEntry("chipdip-dac", "Chip Dip DAC"),
    DacEntry("interludeaudio-analog", "Interlude Audio Analog HAT"),
    DacEntry("interludeaudio-digital", "Interlude Audio Digital HAT"),
    DacEntry("cirrus-wm5102", "Cirrus Logic Audio Card"),
    DacEntry("fe-pi-audio", "Fe-Pi Audio"),
    DacEntry("superaudioboard", "SuperAudioBoard"),
    DacEntry("proto-codec", "PROTO Audio Codec (WM8731)"),
    DacEntry("mbed-dac", "mbed AudioCODEC (TLV320AIC23B)"),
    DacEntry("max98357a", "Maxim MAX98357A I2S amplifier"),
    DacEntry("wm8960-soundcard", "Waveshare WM8960 Audio HAT"),
    DacEntry("googlevoicehat-soundcard", "Google AIY Voice HAT"),
    DacEntry("merus-amp", "Infineon MERUS Audio Amp"),
    DacEntry("ghost-amp", "Ghost amplifier"),
    DacEntry("audiosense-pi", "AudioSense-Pi"),
    DacEntry("pibell", "PiBell"),
    DacEntry("ezsound-6x8iso", "ezsound-6x8 Pi5 multichannel soundcard"),
)

BY_ID: dict[str, DacEntry] = {e.id: e for e in CATALOG}
# Same keys as BY_ID minus onboard, which is the one entry with no overlay.
BY_OVERLAY: dict[str, DacEntry] = {e.id: e for e in CATALOG if e.id != ONBOARD}
KNOWN_OVERLAYS = frozenset(BY_OVERLAY)

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
    """True for an overlay the catalog knows. Anything else is left alone: the
    catalog covers what trixie ships, and guessing from a name is not a claim
    we can make."""
    return name in KNOWN_OVERLAYS


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
    lines = [
        BEGIN,
        "[all]",
        OVERLAY_RESET,
        f"dtparam=audio={'on' if entry.id == ONBOARD else 'off'}",
    ]
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
