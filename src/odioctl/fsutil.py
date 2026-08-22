"""Small filesystem helpers shared by the writers (state.json, config.txt)."""

from __future__ import annotations

import contextlib
import json
import os
import pwd
import sys
import tempfile
from typing import Any


def _whoami() -> str:
    try:
        return pwd.getpwuid(os.geteuid()).pw_name
    except KeyError:
        return str(os.geteuid())


def _default_mode() -> int:
    umask = os.umask(0)
    os.umask(umask)
    return 0o666 & ~umask


def atomic_write_text(path: str, text: str) -> None:
    """Write `text` to `path` atomically (temp file + rename in the same dir).

    Mode (and owner/group, when running as root) are copied from the
    existing file so a rewrite never widens or narrows permissions; a new
    file gets the umask default (mkstemp's 0600 would be wrong for a config
    other users must read). chmod/chown are best-effort — vfat (the Pi boot
    partition) fakes modes. When the directory itself refuses new files
    (e.g. /var/lib/odio is 2750 and we are not root) we fall back to an
    in-place rewrite of the existing file, which is not atomic but keeps
    the tool usable; a file that is not writable either raises a
    PermissionError with an actionable message.
    """
    directory = os.path.dirname(os.path.abspath(path)) or "."
    st = None
    with contextlib.suppress(FileNotFoundError):
        st = os.stat(path)

    try:
        fd, tmp = tempfile.mkstemp(prefix=".odioctl-", dir=directory)
    except PermissionError:
        _write_in_place(path, text)
        return

    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        with contextlib.suppress(OSError):
            os.chmod(tmp, (st.st_mode & 0o7777) if st is not None else _default_mode())
        if st is not None and os.geteuid() == 0:
            with contextlib.suppress(OSError):
                os.chown(tmp, st.st_uid, st.st_gid)
        os.replace(tmp, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp)
        raise
    with contextlib.suppress(OSError):
        dfd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)


def _write_in_place(path: str, text: str) -> None:
    try:
        f = open(path, "r+", encoding="utf-8")  # noqa: SIM115 — closed below, error path matters
    except PermissionError as e:
        raise PermissionError(
            f"{path} is not writable by {_whoami()} and its directory refuses new files "
            "(expected /var/lib/odio 2770 root:odio and state.json 0660)"
        ) from e
    print(
        f"  warning: {os.path.dirname(path)} not writable, rewriting {path} in place",
        file=sys.stderr,
    )
    with f:
        f.seek(0)
        f.truncate()
        f.write(text)
        f.flush()
        os.fsync(f.fileno())


def atomic_write_json(path: str, data: Any, *, indent: int = 4, sort_keys: bool = True) -> None:
    atomic_write_text(path, json.dumps(data, indent=indent, sort_keys=sort_keys) + "\n")
