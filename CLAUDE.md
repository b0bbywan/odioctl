# odioctl — notes for contributors (human or AI)

System control for odio: `odioctl upgrade|components|dac|web|pwa-url`.
Extracted from odios' `installer/ansible/roles/upgrade/files/odio_upgrade.py`.

## Rules of the house

- **Python >= 3.11, core is stdlib-only.** `dependencies = []` stays empty; the
  package ships as a `.deb` on apt.odio.love for a Raspberry Pi appliance.
  A future `odioctl api` (odio-api client) would live under `odioctl/api/`
  behind an optional extra, imported lazily — not started.
- **No legacy support.** state.json must be the current schema (`state.State`,
  every field required) — no backfill of rc1–rc3 shapes, no dpkg
  reconstruction, no `odio-upgrade` compat CLI. Refuse loudly (exit 2) instead.
- **Import modules, not names, across seams you may need to mock**:
  `from odioctl import manifest, state` then `manifest.fetch_manifest(...)`.
  Tests patch at the *defining* module (`patch.object(manifest, "fetch_manifest")`).
- **Keep stdout strings of `upgrade apply/check` stable** — odios CI greps
  them (`smart-upgrade: …`, `Upgrading to … via …`, `(dry-run, not invoking)`).
- **`data/sudoers/odioctl` is generated** from `dac.CATALOG` by
  `scripts/gen-sudoers.py` (one explicit line per DAC id, no wildcards).
  Re-run it after touching the catalog; `tests/test_sudoers.py` fails on drift.
- `odioctl web` is **socket-activated**: `odioctl-web.socket` is the unit that gets
  enabled, systemd holds port 8021 and passes it as fd 3 (`sd_listen_fds`, see
  `web.server.systemd_socket`). Without `LISTEN_FDS` it binds for itself, so the
  dev loop and `--bind`/`--port` are unchanged.
- Privilege model: `odioctl web` runs as the odios target user (systemd --user)
  and edits state.json directly; only `config.txt` writes escalate through
  `sudo -n odioctl dac set <id>` / `dac unset`. Upgrades are never run by the
  web process: "Apply now" does `systemctl --user start odio-upgrade.service`
  (the unit odio-api drives too), so odio-ui shows the progress.
- **Two groups, on purpose.** `odio` is odios' state group (read/write on
  `/var/lib/odio`), and odios puts the installing user in it too. `odioctl` is
  the one the sudoers fragment grants root to, created empty by the postinst
  and joined only by the target user. Never key a sudoers rule on `odio`:
  that would make every state reader a root user.
- **upgrades.json is the contract with odio-ui and `upgrade apply`.** `check`
  sets `upgrade_available` on a version bump *or* on `pending_components`
  (enabled-but-not-installed, see `components.pending_components`); the web UI
  calls `check.refresh()` after every toggle (offline → cached manifest) so the
  badge lights up and `apply` does not refuse. Disabling is never pending.
- **The web UI is server-rendered HTML forms only** — no JSON API, no JavaScript.
  Markup lives in `web/templates/*.html` (`string.Template`, `$name` placeholders,
  values escaped in `web/server.py`), styling in `web/static/style.css` which
  hand-mirrors odio-ui's look (go-odio-api: forest zinc palette, lime accent) so
  both pages on the box feel like one product — keep it in sync, no Tailwind/htmx.
  A POST re-renders the page (no redirects, no query-string state).
- **`components.Action` = a command the box runs for the user**, so nobody needs
  a shell on it. `argv` lives in the catalog and is never built from the request
  — only `{host}` is substituted, with the name the browser reached the box by,
  so an OAuth callback lands here. The web process runs it as the target user
  (no sudo) and does *not* wait for it: such a command prints a URL and then
  keeps running until the user has followed it, so odioctl reads stdout only
  until the `https://` link. The output comes back in a modal (`modal.html`) —
  still no JS: the POST response carries it and `Close` is a link to `/`, so it
  shows once. What persists is the row's own link while the process lives, then
  a note with the exit code. Offered for installed components only.

## Dev loop

```
uv sync
uv run ruff check src tests scripts && uv run ruff format --check src tests scripts
uv run mypy
uv run pytest
uv run odioctl web --bind 127.0.0.1 --state /tmp/state.json --config /tmp/config.txt
```

Local .deb build (needs a Debian toolchain — use `podman run debian:trixie`):
`make deb` (see `.github/workflows/build.yml` for the exact recipe).

## Layout

`src/odioctl/{versions,state,manifest,netinfo,fsutil,components,dac,cli}.py`,
`upgrade/{check,apply,verify}.py`, `web/{server.py,templates/,static/}`;
`data/` (systemd --user units, sudoers), `debian/`, `tests/` (unittest-style
classes run by pytest, `tests/_helpers.py` builds states, `tests/fixtures/`).
