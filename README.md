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
A single static Go binary (stdlib plus fsnotify), packaged as per-arch `.deb`s on
[apt.odio.love](https://apt.odio.love).

## Install

```bash
# odios installs and wires it for you. By hand, on a system with the odio apt repo:
sudo apt install odioctl
```

The package ships (not auto-enabled — odios' installer enables them per user):

| File | Purpose |
|---|---|
| `/usr/lib/systemd/user/odio-check-upgrade.{service,timer}` | daily `odioctl upgrade check` |
| `/usr/lib/systemd/user/odio-upgrade.service` | `sudo odioctl upgrade apply --progress` (started by odio-api) |
| `/usr/lib/systemd/user/odioctl-web.socket` | port 8021, the LAN door |
| `/usr/lib/systemd/user/odioctl-web-proxy.socket` | `$XDG_RUNTIME_DIR/odioctl-web.sock`, the door odio-api's reverse proxy uses (see [Behind odio-api](#behind-odio-api)) |
| `/usr/lib/systemd/user/odioctl-web.service` | `odioctl web --systemd-only`, started on the first connection to a socket; never enabled itself |

Enable one socket or both: the service serves those that run and never binds
anything itself, so a door odios leaves closed stays closed.
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
odioctl web [--bind 0.0.0.0] [--port 8021] [--socket PATH] [--systemd-only] [--ui-url URL] [--state PATH] [--config PATH]
odioctl state [--state PATH] record < run.json | show
```

Exit codes: `check` 0 up to date / 1 upgrades available / 2 error · `apply` 0
upgraded (or nothing to do) / 1 install.sh failed / 2 error · `verify` 0 valid /
1 invalid / 2 state.json missing · `state show` 0 shown / 1 refused / 3
state.json missing · `state record` 0 recorded / 1 write failed / 2 run refused.

### `upgrade`

Reads `/var/lib/odio/state.json` (written by odios after every run), compares it
with the published manifest (`https://odio.love/manifest.json` or the release
asset for `--version`), caches the result in `/var/cache/odio/upgrades.json`
(read by odio-api and odio-motd), and re-runs `install.sh` from the target
release with `INSTALL_*` derived from the state (opt-outs) and `RUN_*=N` for
roles whose version did not move (smart upgrade). A re-run never prompts, so
what install.sh asked at install comes back from the state too: `AUDIOSERVER`,
`MPD_MUSIC_DIRECTORY`, `MPD_CONF_PATH` (the last two only when recorded, else
install.sh's defaults). `--reinstall` re-runs every
role in full. `apply` follows the report `check` wrote: with none on disk there
is nothing to apply, unless `--force` or `--version` says otherwise. A custom
`--state` keeps upgrades.json next to it for every subcommand and for `web`.
Only the current state.json schema is accepted — pre-2026.5 installs are not
supported.

**Targeting a pre-release.** An odio installed from a PR build runs a release the
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

### `state`

state.json's schema belongs to odioctl; odios reports to it rather than
writing the file itself.

- **`record`** is what `write_state.yml` calls, as root, with what the run
  installed on stdin: `odios`, `install_mode`, `target_user`, `audioserver`
  (`pulseaudio` | `pipewire`), `mpd_music_directory`, `mpd_conf_path` (the
  effective paths, `""` when install.sh's default or detection applies; absolute,
  no quotes, backslashes or control characters, since install.sh splices them
  into its extra-vars run as root), `roles` (name → version), `roles_excluded`,
  `features`, `features_excluded` — every one of them and nothing else, so an
  odios newer than this odioctl is refused rather than half-recorded. odioctl
  appends the release to `release_history`, drops the audio server that was
  not picked from `roles_excluded` (not picking it is not declining it), and
  writes the file atomically, mode 0660 (the group comes from `/var/lib/odio`,
  2770 root:odio). A state.json it cannot read is replaced, history restarting
  from this run.
  ```yaml
  - name: Record state.json
    ansible.builtin.command: odioctl state record
    args:
      stdin: "{{ _odios_run | to_json }}"
    become: true
  ```
- **`show`** prints state.json as odioctl reads it, for `read_state.yml`: what
  an earlier odios left out is filled in (no `audioserver` is `pulseaudio`).
  3 means no state.json (a fresh install), 1 one odioctl refuses; 2 is not
  one of its answers but an odioctl that predates `state show` — the case of
  every odio upgrading from before it, since `read_state.yml` runs before the
  `upgrade` role installs the new odioctl.

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
HTML forms over htmx (the same htmx and SSE extension as odio-api's
dashboard), no JSON API: a components table with Enable/Disable buttons,
and a DAC picker (select + Apply, Reset to drop the odioctl block, then a
Reboot button on the banner the change leaves). Actions are
`POST /components`, `POST /dac`, `POST /dac/unset`, `POST /reboot`; the page answers with
a message or error banner and every section follows odio live over
`GET /events`. Every form carries a per-process token, so a cross-site page
cannot drive odio. There
is no authentication (same LAN trust model as odio-api); use `--bind 127.0.0.1`
to keep it local. Runs as the odios target user; state.json is edited
directly (needs `/var/lib/odio` group-writable by `odio`, see below),
config.txt through `sudo -n odioctl dac …` (needs that user in the `odioctl`
group).

The logo links to odio-ui at `http://<host>:8018/ui`, `<host>` being the name
the browser used; `ODIOCTL_UI_URL` in `/etc/default/odioctl` (or `--ui-url`)
replaces that guess, with an absolute URL or one from the root (`/ui`) — a
relative one would land under the page's `<base>`.

#### Behind odio-api

odioctl can be served by a reverse proxy, odio-api's `/admin/` being the one it
is meant for: beside port 8021 while odios moves over, instead of it once done.

- **The door.** `odioctl-web-proxy.socket` listens on
  `$XDG_RUNTIME_DIR/odioctl-web.sock`, mode 0600: only the target user, whom
  odio-api runs as, can connect. Without systemd, `--socket PATH` does the same.
- **What odioctl reads from the proxy**, on that socket only (never on 8021,
  where anyone could forge them): `X-Forwarded-Prefix` becomes the page's
  `<base href>` (every URL of the page is relative to it), `X-Forwarded-Host`
  the host of the odio-ui link and of an action's OAuth callback,
  `X-Forwarded-For` the address in the log.
- **What the proxy must do**: strip the prefix from the path, `SetXForwarded()`
  (Go's `httputil.ReverseProxy`), *set* — not add — `X-Forwarded-Prefix`,
  since `Rewrite` does not drop a client's own, and pass `/events` through
  unbuffered (SSE; `ReverseProxy` flushes `text/event-stream` by itself).

**Switching an odio over** (odios' `upgrade` role). Two rules from systemd
drive the order in live mode: it refuses to start a socket under a service
that is already running, and a running service keeps the fd of a socket
stopped under it (the port stays open). So the service is stopped first, and
the next connection starts it again with exactly the doors that run — on the
new binary too, which makes the stop a replacement for the "Restart
odioctl-web" handler. Image mode needs none of it: sockets bind at boot,
before the service.

1. **Both doors**, while odio-api and odio-ui catch up. Require the odioctl
   release that ships `odioctl-web-proxy.socket` (`upgrade_odioctl_apt_version`),
   enable it like `odioctl-web.socket` (`wanted_by: sockets.target`), then:
   ```sh
   systemctl --user stop odioctl-web.service
   systemctl --user start odioctl-web-proxy.socket
   ```
   Point odio-api's proxy at the socket, and odio-ui's admin link
   (`links.admin` in odio-api's `config.yaml.j2`) at `/admin/`. odio-api can
   show that link only when the socket file exists: it does from the moment the
   unit runs, without waking `odioctl web`.
2. **The proxy only.** Swap `odioctl-web.socket` for
   `odioctl-web-proxy.socket` in `upgrade_services` and the enable task, and on
   odios already switched:
   ```sh
   systemctl --user stop odioctl-web.service
   systemctl --user disable --now odioctl-web.socket
   systemctl --user enable --now odioctl-web-proxy.socket
   ```
   odioctl is then reachable through odio-api only; `ssh` +
   `odioctl upgrade apply` stays the way back if odio-api is what broke.

## Development

```bash
make lint test
go run . web --bind 127.0.0.1 --state /tmp/state.json --config /tmp/config.txt
# the proxy door: --socket, then speak to it as odio-api would
go run . web --bind 127.0.0.1 --socket /tmp/odioctl-web.sock --state /tmp/state.json
curl --unix-socket /tmp/odioctl-web.sock -H 'X-Forwarded-Prefix: /admin' http://odio/
make deb   # cross-compiles amd64/armhf/arm64 and packages via nfpm
```

`data/sudoers/odioctl` is generated from the DAC catalog: `make sudoers`
(= `go generate ./dac`).

## License

BSD 2-Clause — see [LICENSE](LICENSE).
