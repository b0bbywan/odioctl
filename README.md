<p align="center">
  <a href="https://odio.love"><img src="https://odio.love/logo.png" alt="odio" width="160" /></a>
</p>
<h1 align="center">odioctl</h1>
<p align="center"><em>System control for odio: upgrades, components, DAC overlay and a local web UI.</em></p>

`odioctl` is the tool an [odio](https://odio.love) box uses to look after
itself. It replaces the single-file `odio-upgrade` script that used to ship
inside [odios](https://github.com/b0bbywan/odios) and adds component and DAC
management plus a small server-rendered LAN web UI. Pure Python 3.11+, **no runtime
dependency**, packaged as a `.deb` on [apt.odio.love](https://apt.odio.love).

## Install

```bash
# odios installs and wires it for you. By hand, on a box with the odio apt repo:
sudo apt install odioctl
```

The package ships (not auto-enabled — odios' installer enables them per user):

| File | Purpose |
|---|---|
| `/usr/lib/systemd/user/odio-check-upgrade.{service,timer}` | daily `odioctl upgrade check` |
| `/usr/lib/systemd/user/odio-upgrade.service` | `sudo odioctl upgrade apply --progress` (started by odio-api) |
| `/usr/lib/systemd/user/odioctl-web.socket` | port 8021 — **this is the unit to enable** |
| `/usr/lib/systemd/user/odioctl-web.service` | `odioctl web`, started on the first connection |
| `/etc/sudoers.d/odioctl` | NOPASSWD for the `odioctl` group: `upgrade apply --progress`, `dac set <id>` (one line per id), `dac unset` |

The postinst creates the `odioctl` system group and leaves it empty; odios adds
its target user. It is deliberately not the `odio` group, which carries
state.json access and holds the installing user too: a group that grants reads
must not also grant passwordless root.

## CLI

```
odioctl upgrade check  [--version TAG] [--state PATH] [--output PATH]
odioctl upgrade apply  [--version V] [--state PATH] [--dry-run] [--force] [--reinstall] [--progress|--no-progress]
odioctl upgrade verify [--state PATH] [--expected-version TAG]
odioctl pwa-url
odioctl components [--state PATH] list [--json] | enable NAME | disable NAME
odioctl dac list [--json] | status [--json] | set ID [--dry-run] | unset
odioctl web [--bind 0.0.0.0] [--port 8021] [--state PATH] [--config PATH]
```

Exit codes: `check` 0 up to date / 1 upgrades available / 2 error · `apply` 0
upgraded (or nothing to do) / 1 install.sh failed / 2 error · `verify` 0 valid /
1 invalid / 2 state.json missing.

### `upgrade`

Reads `/var/lib/odio/state.json` (written by odios after every run), compares it
with the published manifest (`https://odio.love/manifest.json` or the release
asset for `--version`), caches the result in `/var/cache/odio/upgrades.json`
(read by odio-api and odio-motd), and re-runs `install.sh` from the target
release with `INSTALL_*` derived from the state (opt-outs) and `RUN_*=N` for
roles whose version did not move (smart upgrade). `--reinstall` re-runs every
role in full. Only the current state.json schema is accepted — pre-2026.5
installs are not supported.

**Targeting a pre-release.** A box installed from a PR build runs a release the
published manifest knows nothing about, so every role only that build ships
reads as "not in this release" and never goes pending. `check --version pr-84`,
or `ODIOCTL_ODIOS_VERSION=pr-84` in `/etc/default/odioctl` (read by the daily
timer *and* by `odioctl web`, which refreshes upgrades.json on every toggle),
compares against that release instead. Only a *tag* is overridable, never a
URL: it is interpolated into a `github.com/b0bbywan/odios` release path, and
anything that could walk out of it is refused — including a tag read back from
upgrades.json, which is group-writable while `apply` curls that URL into bash
as root. `check` records the tag under `target_tag` because a pre-release names
itself by version (`2026.7.0rc2-9-gcad916c`) and is published under a tag
(`pr-84`); `apply` needs the latter.

### `components`

Roles (services) and features (plugins of a role) as recorded in state.json.
Disabling adds the name to `roles_excluded`/`features_excluded` (and drops it
from `roles`/`features`); enabling clears the exclusion so install.sh's own
default installs it on the next run. Nothing is installed or removed until
`odioctl upgrade apply` runs (`--force` to run it right away). `common` and
`upgrade` are infrastructure roles and cannot be toggled. Names present in
state.json are always accepted, even if newer than this odioctl.

### `dac`

Owns one marked block at the end of `/boot/firmware/config.txt`:

```
# BEGIN odioctl dac -- managed block, edit with `odioctl dac`
[all]
dtparam=audio=off
dtoverlay=hifiberry-dacplus-std
# END odioctl dac
```

Pre-existing top-level audio lines — a `dtparam=audio=` or an overlay the
catalog lists — are commented out with an `#odioctl-disabled: ` prefix and
restored by `dac unset`. An overlay the catalog does not list is left alone:
whether a name is audio is not something to guess at. A one-time backup is
kept as `config.txt.odioctl.bak`; a reboot is required (`/run/odioctl/reboot-required`
flags it until then). `set`/`unset` need root — the web UI calls them through
`sudo -n`, and the sudoers file lists every catalog id explicitly, so no other
argument (in particular `--config`) can go through sudo.

### `web`

A stdlib `http.server` on port 8021 serving one server-rendered page — plain
HTML forms, no JavaScript, no JSON API: a components table with
Enable/Disable buttons, and a DAC picker (select + Apply, Reset to drop the
odioctl block). Actions are `POST /components`, `POST /dac`, `POST /dac/unset`
followed by a redirect back to `/` with a message or error banner. Every form
carries a per-process token, so a cross-site page cannot drive the box. There
is no authentication (same LAN trust model as odio-api); use `--bind 127.0.0.1`
to keep it local. Runs as the odios target user; state.json is edited
directly (needs `/var/lib/odio` group-writable by `odio`, see below),
config.txt through `sudo -n odioctl dac …` (needs that user in the `odioctl`
group).

## What odios has to do (follow-up, other repos)

- **odio-apt-repo**: add `odioctl` (release workflow already dispatches
  `release-published`).
- **odios `roles/upgrade`**: `apt install odioctl` instead of copying
  `odio_upgrade.py`; drop its own units/sudoers templates (the package ships
  them) and `/usr/local/bin/odio-upgrade`; enable
  `odio-check-upgrade.timer` and `odioctl-web.socket` for the target user;
  add that user to the `odioctl` group (after the apt install, which creates
  it) so the sudoers fragment applies. On an existing box the membership only
  reaches the running `systemd --user` session on the next login.
- **odios `write_state.yml`**: `/var/lib/odio` `2770 root:odio` and
  `state.json` `0660` so `odioctl components`/web can write it as `odio`;
  call `/usr/bin/odioctl upgrade check`.
- **odios `odio-motd`**: `odioctl pwa-url`. **odios CI**: stop publishing
  `odio_upgrade.py`, use the installed `odioctl upgrade verify`.
- odio-api config is unchanged (`checkUnit`/`upgradeUnit`).

## Development

```bash
uv sync
uv run ruff check src tests scripts && uv run mypy && uv run pytest
uv run odioctl web --bind 127.0.0.1 --state tests/fixtures/state.json --config /tmp/config.txt
make deb   # in a debian:trixie container
```

`data/sudoers/odioctl` is generated from the DAC catalog: `make sudoers`.

## License

BSD 2-Clause — see [LICENSE](LICENSE).
