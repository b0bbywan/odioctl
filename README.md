<p align="center">
  <a href="https://odio.love"><img src="https://odio.love/logo.png" alt="odio" width="160" /></a>
</p>
<h1 align="center">odioctl</h1>
<p align="center"><em>System control for odio: upgrades, components, DAC overlay and a local web UI.</em></p>
<p align="center">
  <a href="https://github.com/b0bbywan/odioctl/releases"><img src="https://img.shields.io/github/v/release/b0bbywan/odioctl?include_prereleases" alt="Release" /></a>
  <a href="https://github.com/b0bbywan/odioctl/blob/main/LICENSE"><img src="https://img.shields.io/github/license/b0bbywan/odioctl" alt="License" /></a>
  <a href="https://github.com/b0bbywan/odioctl/actions/workflows/build.yml"><img src="https://github.com/b0bbywan/odioctl/actions/workflows/build.yml/badge.svg" alt="Build" /></a>
  <a href="https://golangci-lint.run/"><img src="https://img.shields.io/badge/lint-golangci--lint-00ADD8?logo=go&logoColor=white" alt="golangci-lint" /></a>
  <a href="https://github.com/sponsors/b0bbywan"><img src="https://img.shields.io/github/sponsors/b0bbywan?label=Sponsor&logo=GitHub" alt="GitHub Sponsors" /></a>
</p>
<p align="center">
  <a href="#upgrade"><img src="https://img.shields.io/badge/Upgrades-5AB81E" alt="Upgrades" /></a>
  <a href="#components"><img src="https://img.shields.io/badge/Components-0055AA" alt="Components" /></a>
  <a href="#dac"><img src="https://img.shields.io/badge/DAC-6B21A8" alt="DAC" /></a>
  <a href="#web"><img src="https://img.shields.io/badge/Web%20UI-F97316" alt="Web UI" /></a>
</p>
<p align="center">
  Part of the <a href="https://odio.love">odio</a> project — <a href="https://docs.odio.love/operations/settings/">documentation</a>.
</p>
<p align="center">
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white" alt="Go" /></a>
  <a href="https://systemd.io/"><img src="https://img.shields.io/badge/systemd-FF6B35" alt="systemd" /></a>
  <a href="https://www.debian.org/"><img src="https://img.shields.io/badge/Debian-A81D33?logo=debian&logoColor=white" alt="Debian" /></a>
</p>

`odioctl` is the CLI and web UI for configuring an [odio](https://odio.love)
node — upgrades, components and DAC selection today, and meant to grow with
the rest of the node's settings. It started as a rewrite of the `odio-upgrade`
script that used to ship inside [odios](https://github.com/b0bbywan/odios).
A single static Go binary, **stdlib only**, packaged as per-arch `.deb`s on
[apt.odio.love](https://apt.odio.love).

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

A stdlib `net/http` server on port 8021 serving one server-rendered page —
plain HTML forms, no JavaScript, no JSON API: a components table with
Enable/Disable buttons, and a DAC picker (select + Apply, Reset to drop the
odioctl block). Actions are `POST /components`, `POST /dac`, `POST /dac/unset`;
a POST re-renders the page with a message or error banner. Every form
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
make lint test
go run . web --bind 127.0.0.1 --state /tmp/state.json --config /tmp/config.txt
make deb   # cross-compiles amd64/armhf/arm64 and packages via nfpm
```

`data/sudoers/odioctl` is generated from the DAC catalog: `make sudoers`
(= `go generate ./dac`).

## License

BSD 2-Clause — see [LICENSE](LICENSE).
