# odioctl — notes for contributors (human or AI)

System control for odio: `odioctl upgrade|components|dac|web|pwa-url`.
Extracted from odios' `installer/ansible/roles/upgrade/files/odio_upgrade.py`,
since rewritten in Go.

## Rules of the house

- **Go, stdlib plus fsnotify.** `go.mod` requires `github.com/fsnotify/fsnotify`
  (the units-directory watch below, the same one odio-api uses) and nothing
  else; a new dependency is a decision, not a convenience. The package ships
  as per-arch `.deb`s on apt.odio.love for a Raspberry Pi appliance (armhf is
  GOARM=6 so one binary runs from the Zero up). Subprocesses (`curl | bash`,
  `sudo -n`, `systemctl --user`) stay exec'd.
- **No legacy support.** state.json must be the current schema (`state.State`,
  every field required — `state.Parse` refuses the rest as `*SchemaError`), no
  backfill of rc1–rc3 shapes, no dpkg reconstruction, no `odio-upgrade` compat
  CLI. Refuse loudly (exit 2) instead.
- **Test seams are explicit**: swappable package vars (`manifest.Fetch`,
  `upgrade.runInstall`, `dac.RebootFlag`) and injected funcs (`web.Runners`).
  Tests live in the package they test and swap the seam with `t.Cleanup`;
  never reach around a seam to mock deeper.
- **The `cli` package only parses argv.** Command behavior lives in the owning
  package as `RunX(stdout, stderr, …) int` funcs (`upgrade.RunCheck`,
  `dac.RunSet`, `components.RunList`).
- **Keep stdout strings of `upgrade apply/check` stable** — odios CI greps
  them (`smart-upgrade: …`, `Upgrading to … via …`, `(dry-run, not invoking)`).
- **`data/sudoers/odioctl` is generated** from `dac.Catalog` by
  `go generate ./dac` (one explicit line per DAC id, no wildcards).
  Re-run it after touching the catalog; `dac/sudoers_test.go` fails on drift.
- `odioctl web` is **socket-activated**: `odioctl-web.socket` is the unit that
  gets enabled, systemd holds port 8021 and passes it as fd 3 (`sd_listen_fds`,
  see `web.SystemdListener` — hand-rolled, no go-systemd for fifteen lines).
  Without `LISTEN_FDS` it binds for itself, so the dev loop and
  `--bind`/`--port` are unchanged.
- Privilege model: `odioctl web` runs as the odios target user (systemd --user)
  and edits state.json directly; only `config.txt` writes escalate through
  `sudo -n odioctl dac set <id>` / `dac unset`. The reboot the DAC change
  waits for ("Reboot now" on its banner, `POST /reboot`) is `systemctl
  reboot` as that user: odios' polkit rule (`10-allow-shutdown.rules`) lets
  the target user reboot through logind, no sudo, no D-Bus library — the
  same grant odio-api's power buttons use. Upgrades are never run by the
  web process: "Apply now" does `systemctl --user start --no-block
  odio-upgrade.service` (the unit odio-api drives too) and that token-checked
  POST is the only place the unit is ever started — a render observes. What
  the card shows comes from systemd, not from a held child: a watcher
  (`Services.watchUpgrade`) waits on `$XDG_RUNTIME_DIR/systemd/units`
  (fsnotify, `Config.RuntimeDir`) for the `invocation:odio-upgrade.service`
  link systemd removes as the oneshot leaves `activating`, then reads
  `systemctl show` once — by key: `show` prints properties in systemd's
  order, never the `-p` one — and keeps `Result`/`ExecMainStatus` as the
  note. No probe right after `start --no-block`: it returns before the unit
  is activating. A slow tick backs the events; without the directory the
  unit is polled. A unit found `activating` at render time (odio-api started
  it, or the web restarted under it) gets a watcher; one found `failed` is
  shown as such.
  The playbook's last step re-runs `check`, so upgrades.json settles by itself.
- **Two groups, on purpose.** `odio` is odios' state group (read/write on
  `/var/lib/odio`), and odios puts the installing user in it too. `odioctl` is
  the one the sudoers fragment grants root to, created empty by the postinst
  and joined only by the target user. Never key a sudoers rule on `odio`:
  that would make every state reader a root user. Rules say `%:odioctl`
  (`group_plugin=system_group.so`): matched against `/etc/group`, not the
  caller's group vector, which a `--user` manager started before the
  membership landed never has. `apply --progress` sets `XDG_RUNTIME_DIR` to
  the target user's itself, sudo's `env_keep` is not relied on.
- **upgrades.json is the contract with odio-ui and `upgrade apply`.** `check`
  sets `upgrade_available` on a version bump *or* on `pending_components`
  (enabled-but-not-installed, see `components.Pending`); the web UI calls
  `upgrade.Refresh` after every toggle (from the cached manifest, fetching
  only when nothing is cached for the target tag — a toggle is not a network
  check, and a cache is never re-stamped with another tag) so the badge
  lights up and `apply` does not refuse. Disabling is never pending.
  The `Report` struct's field order and json tags are the wire format.
- **The target release is decided in one place: `check`.** `apply` never picks
  a release of its own on the box (`odio-upgrade.service` is a frozen sudoers
  argv, no `--version`), it follows upgrades.json — read once through
  `upgrade.ReadReport`, the only decoder of that file, and no report means
  nothing to apply (only `--force`/`--version` run without one). Where that
  file lives is `state.UpgradesPathFor(statePath)` for `check`, `apply` and
  `web` alike, so a custom `--state` never splits them. A test box is steered by
  pointing `check` at a pre-release — `--version pr-84` or
  `ODIOCTL_ODIOS_VERSION` (`/etc/default/odioctl`, wired into both `--user`
  units: the web process refreshes upgrades.json on every toggle). Never add a
  *URL* override reachable from the environment — the tag is interpolated into
  a `b0bbywan/odios` release path, which is what keeps a rogue value to picking
  another odios release instead of a manifest of its own; `manifest.IsReleaseTag`
  is that boundary and `apply` re-checks it, because upgrades.json is
  group-writable and its tag ends up in a `curl … | bash` run as root. The tag
  and the version are two strings: `pr-84` publishes `2026.7.0rc2-9-gcad916c`,
  hence `target_tag` next to `latest` in upgrades.json.
- **The web UI is server-rendered HTML over htmx, the way odio-api's is** —
  no JSON API, no script of our own. `web/static/htmx.min.js` and
  `htmx-sse.js` are odio-api's copies (same version, keep them so). The
  body opens `GET /events` (`sse-connect`) on load and keeps it; the server
  sends every section as a named event — `banners`, `upgrade`, `components`,
  `dac`, and `modal-<key>` for each finished action — on connect and on every
  `Services.changed()`, rendered by the page's own templates
  (`web.RenderSections`). Each section's root carries `sse-swap="<name>"
  hx-swap="outerHTML"` and replaces itself; a modal listens to its own id, so
  only a page showing it takes its end. Forms are `hx-post` with
  `hx-target="#notice"`, nothing else (no script, no page): the answer to
  a POST is the notice alone (`web.RenderNotice`: the ok/err banner, and an
  action's modal out of band into `#modal`), the state follows on the
  stream — so every mutation ends in `changed()`. `GET /` is the only page
  render. Never put `hx-target`/`hx-swap` on an ancestor of a
  section: htmx inherits them and the stream's swaps would follow. Markup
  lives in `web/templates/*.html` (`html/template`: composition via
  `{{range}}`/`{{if}}`/`{{template}}` stays in the templates, Go builds view
  models only, escaping is the engine's), styling in `web/static/style.css`
  which hand-mirrors odio-ui's look (go-odio-api: forest zinc palette, lime
  accent) so both pages on the box feel like one product — keep it in sync,
  no Tailwind. No redirects, no query-string state.
- **`components.Action` = a command the box runs for the user**, so nobody
  needs a shell on it. `Argv` lives in the catalog and is never built from the
  request — only `{host}` (the name the browser reached the box by, so an
  OAuth callback lands here) and `{home}` (the target user's home: argv runs
  without a shell, so a `~` would stay literal) are substituted. The web
  process runs it as the target user (no sudo) and does *not* wait for it:
  such a command prints a URL and then keeps running until the user has
  followed it, so odioctl reads stdout only until the `https://` link. The
  output comes back in a modal: the POST response carries it and `Close` is a
  link to `/`, so it shows once. What persists is the row's own link while
  the process lives, then a Done badge or the failure with its exit code —
  the stream swaps the row and the modal in on the exit. Offered for
  installed components only. qbzd's `login` is the first one: it prints its
  Qobuz URL, then holds a one-shot listener for 300s waiting for the browser
  to come back to `{host}`. Tidal's runs upmpdcli's own `get_credentials.py`,
  whose link is followed on any device — no callback, hence `{home}` for the
  credentials path and no `{host}`.

## Dev loop

```
make lint    # gofmt + go vet
make test    # go test ./...
go run . web --bind 127.0.0.1 --state /tmp/state.json --config /tmp/config.txt
make deb     # cross-compiles amd64/armhf/arm64 and packages via nfpm
```

## Layout

`versions`, `state`, `manifest`, `netinfo`, `fsutil`, `procutil`, `components`,
`dac` (+ `dac/gen`, the sudoers generator), `upgrade` (check/apply/verify), `web`
(config, services, action, render, server, socket + `templates/`, `static/`),
`cli`, `main.go` — that is also the import order, no cycles. `config/` holds
the ldflags-injected version. `data/` (systemd --user units, sudoers),
`debian/` (postinst, copyright — nfpm.yaml is the package recipe).
