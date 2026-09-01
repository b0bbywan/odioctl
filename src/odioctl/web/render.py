"""The page, built out of web/templates/*.html — no HTTP, no state changes.

Templates are `string.Template` (`$name` placeholders): whole sections as
well as the one-element partials (banner, hint, option, …), so the only
markup left in Python is the bare <h1> of the 403/404/405 dead ends in
`server`. The stylesheet and logo in web/static/ mirror odio-ui's look
(go-odio-api). Every value substituted into a template is HTML-escaped by
`escape` unless it is itself rendered HTML.

`render_page` reads from `Services` and never writes: the one exception is
`pop_action_result`, which is a read that consumes — the modal shows an
action's output once and the next page load is clean.
"""

from __future__ import annotations

import functools
import html
import importlib.resources
import socket
import string
from collections.abc import Callable
from typing import Any, cast

from odioctl import __version__, components, dac, state
from odioctl.upgrade import check
from odioctl.web.config import ODIO_UI_PORT
from odioctl.web.services import ActionResult, Services

_RES = importlib.resources.files("odioctl.web")

STATIC_FILES = {"style.css": "text/css; charset=utf-8", "logo.png": "image/png"}


@functools.cache
def _template(name: str) -> string.Template:
    return string.Template((_RES / "templates" / name).read_text(encoding="utf-8"))


@functools.cache
def static_asset(name: str) -> tuple[bytes, str] | None:
    """(content, media type) for a file under web/static/, or None if unknown."""
    if name not in STATIC_FILES:
        return None
    return (_RES / "static" / name).read_bytes(), STATIC_FILES[name]


def _render(template: str, **values: object) -> str:
    return _template(template).substitute(values)


def escape(value: object) -> str:
    """HTML-escape a value on its way into a template."""
    return html.escape(str(value), quote=True)


# Nearly every substituted value goes through it, hence the short local name.
_e = escape


# The sections receive a `render` with the per-process form token already bound
# rather than the token itself: every template carrying a form uses the same
# `$token` hidden field, so it is escaped once, here. Templates without the
# placeholder ignore the extra value.
Render = Callable[..., str]


def _renderer(token: str) -> Render:
    return functools.partial(_render, token=_e(token))


def _banner(kind: str, text: str) -> str:
    return _render("banner.html", kind=kind, text=_e(text)) if text else ""


def _hint(text: str) -> str:
    return _render("hint.html", text=_e(text))


def _section(title: str, body: str) -> str:
    """A section whose whole content is one banner or hint (the empty/unavailable states)."""
    return _render("section_banner.html", title=_e(title), banner=body)


# (chip text, button label) per component status; the button performs the opposite action.
_STATUS_UI = {
    "installed": ("Installed", "Disable"),
    "excluded": ("Disabled", "Enable"),
    "default": ("Will install on next upgrade", "Skip"),
}


ActionState = Callable[[str, str, str], tuple[str, str]]


def _component_actions(
    c: components.Component, render: Render, state_of: ActionState
) -> tuple[str, str]:
    """(buttons, notes) for one component's catalog actions.

    Offered only once the component is installed — the command it runs ships
    with the package. Each pending link (and the outcome of the last run) is
    rendered under the row, next to what it belongs to.
    """
    if not c.actions or c.status != "installed":
        return "", ""
    buttons: list[str] = []
    notes: list[str] = []
    for a in c.actions:
        buttons.append(
            render(
                "component_action_run.html",
                kind=_e(c.kind),
                name=_e(c.name),
                action=_e(a.id),
                button=_e(a.label),
            )
        )
        url, note = state_of(c.kind, c.name, a.id)
        if url:
            notes.append(
                render(
                    "component_action_link.html",
                    url=_e(url),
                    label=_e(a.link_label),
                    note=_e(a.link_note or "started"),
                )
            )
        elif note:
            notes.append(render("component_action_note.html", note=_e(f"{a.label}: {note}")))
    return "".join(buttons), "".join(notes)


def _component_row(
    c: components.Component, render: Render, child: bool, state_of: ActionState
) -> str:
    chip, button = _STATUS_UI[c.status]
    action = render(
        "component_action.html",
        kind=_e(c.kind),
        name=_e(c.name),
        enabled="0" if c.enabled else "1",
        button=button,
    )
    runs, notes = _component_actions(c, render, state_of)
    return render(
        "component_row.html",
        child=" child" if child else "",
        label=_e(c.label),
        description=_e(c.description or c.name),
        status=_e(c.status),
        chip=chip,
        action=action,
        runs=runs,
        notes=notes,
    )


def _components_section(
    st: state.State | None, err: str | None, render: Render, state_of: ActionState
) -> str:
    if st is None:
        return _section("Components", _banner("err", f"state.json: {err}"))
    comps = components.list_components(st)
    by_parent: dict[str, list[components.Component]] = {}
    orphans: list[components.Component] = []
    for f in comps:
        if f.kind != "feature":
            continue
        if f.parent:
            by_parent.setdefault(f.parent, []).append(f)
        else:
            orphans.append(f)
    rows_by_group: dict[str, list[str]] = {g: [] for g in components.GROUPS}
    infra: list[str] = []
    for r in comps:
        if r.kind != "role":
            continue
        if not r.toggleable:
            infra.append(r.label)
            continue
        rows = rows_by_group.setdefault(r.group, [])
        rows.append(_component_row(r, render, False, state_of))
        rows.extend(_component_row(f, render, True, state_of) for f in by_parent.get(r.name, []))
    rows_by_group[components.GROUPS[-1]].extend(
        _component_row(f, render, False, state_of) for f in orphans
    )
    groups = "".join(
        render("component_group.html", title=_e(title), rows="".join(rows))
        for title, rows in rows_by_group.items()
        if rows
    )
    # The install mode and target user drive what an upgrade actually installs,
    # so they belong here rather than in the page header.
    return render(
        "components.html",
        user=_e(st["target_user"]),
        mode=_e(st["install_mode"]),
        note=_e(components.APPLY_NOTE),
        groups=groups,
        infra=_e("Always installed: " + ", ".join(infra)) if infra else "",
    )


def _dac_section(d: dict[str, Any], render: Render) -> str:
    if not d["supported"]:
        return _section(
            "DAC",
            _banner(
                "warn", "No config.txt found — DAC selection is only available on Raspberry Pi."
            ),
        )
    current = d["current"]
    opts = [
        _render(
            "option.html",
            id="",
            attrs=" disabled selected" if not current else " disabled",
            text="— not configured —",
        )
    ]
    opts += [
        _render(
            "option.html",
            id=_e(e.id),
            attrs=" selected" if e.id == current else "",
            text=f"{_e(e.label)} ({_e(e.id)})",
        )
        for e in dac.CATALOG
    ]
    if current:
        cur = f"Current: {current}" + (
            " (managed by odioctl)" if d["managed"] else " (from existing config.txt)"
        )
    elif d["stray_lines"] and not d["managed"]:
        cur = "Unrecognised audio configuration in config.txt: " + "; ".join(d["stray_lines"])
    else:
        cur = "No DAC configured"
    unset = render("dac_unset.html") if d["managed"] else ""
    stray = ""
    if d["stray_lines"] and d["managed"]:
        # Unmanaged lines are simply what defines `current`; once odioctl owns the
        # block, anything else left active is a conflict worth flagging.
        stray = _banner(
            "warn",
            "Audio lines outside the odioctl block (commented out on Apply): "
            + "; ".join(d["stray_lines"]),
        )
    return render(
        "dac.html",
        options="".join(opts),
        unset=unset,
        current=_e(cur),
        stray=stray,
    )


def _upgrade_section(report: check.UpgradeReport | None, render: Render, ui_url: str) -> str:
    if report is None:
        return _section(
            "Upgrade", _banner("warn", "No upgrade check has run yet (odio-check-upgrade.timer).")
        )
    if not report["upgrade_available"]:
        return _section(
            "Upgrade",
            _hint(f"Up to date — odio {report['current']} (checked {report['checked_at']})."),
        )
    items: list[str] = []
    if report["latest"] != report["current"]:
        items.append(f"odio {_e(report['current'])} → {_e(report['latest'])}")
    items.extend(
        f"{_e(r['name'])} {_e(r['installed'])} → {_e(r['available'])}" for r in report["roles"]
    )
    for ref in report["pending_components"]:
        kind, _, name = ref.partition(":")
        items.append(f"install {_e(components.label_of(cast(components.Kind, kind), name))}")
    return render(
        "upgrade.html",
        items="".join(_render("upgrade_item.html", text=i) for i in items),
        ui_url=_e(ui_url),
    )


def _modal(result: ActionResult | None, render: Render) -> str:
    """The action's output, over the page. Plain HTML: no JS anywhere, so it is
    the POST response that carries it and `Close` is a link back to the page."""
    if result is None:
        return ""
    link = (
        render("modal_link.html", url=_e(result.url), label=_e(result.link_label))
        if result.url
        else ""
    )
    return render(
        "modal.html",
        title=_e(result.title),
        output=_e(result.output.strip() or "(no output)"),
        link=link,
    )


def render_page(services: Services, *, message: str = "", error: str = "", host: str = "") -> str:
    st, err = services.read_state()
    d = services.dac_status()
    render = _renderer(services.token)
    # The Host header when the browser gave one (that name reaches the box), the
    # box's own hostname otherwise — same address for the odio-ui link and ssh.
    # The logo is that way home: this page is a settings annex of odio-ui.
    hostname = host or socket.gethostname()
    ui_url = f"http://{hostname}:{ODIO_UI_PORT}/ui"
    version_badge = ""
    if st is not None:
        version_badge = _render("version_badge.html", odios=_e(st["odios"]))
    banners = [_banner("ok", message), _banner("err", error)]
    if d["reboot_required"]:
        banners.append(_banner("warn", "A reboot is required to apply the DAC change."))
    return render(
        "page.html",
        version=_e(__version__),
        hostname=_e(socket.gethostname()),
        ui_url=_e(ui_url),
        version_badge=version_badge,
        banners="".join(banners),
        components=_components_section(st, err, render, services.action_state),
        upgrade=_upgrade_section(services.upgrade_report(), render, ui_url),
        dac=_dac_section(d, render),
        modal=_modal(services.pop_action_result(), render),
    )
