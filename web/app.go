package web

// What the handlers hold: the config, the form token, the log, the
// subprocess seams, and the three things that live between requests — the
// open streams (Changes), the component actions (Actions) and the upgrade
// watcher (Upgrades), each with its own lock. Reads go straight to the
// domain packages; each write sits in the file of its section
// (components.go, dac.go, actions.go, upgrades.go) and ends in
// changes.Changed. Subprocesses go through Runners so tests drive the real
// code path against stand-ins.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

// UserError is a failure the page shows as an error banner; Modal carries the
// action's output when there is some to show alongside it.
type UserError struct {
	Msg   string
	Modal *ActionResult
}

func (e *UserError) Error() string { return e.Msg }

func userErrorf(format string, args ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

type RunResult struct {
	Stdout, Stderr string
	Code           int
}

type RunFn func(args []string) (RunResult, error)

// ActionProcess is one started component action, as Actions sees it.
type ActionProcess interface {
	Output() io.Reader // combined stdout+stderr
	Pid() int
	Alive() bool
	ExitCode() int                // valid once !Alive()
	WaitFor(d time.Duration) bool // true when the process exited within d
	Stop()                        // terminate, then kill — nothing polls it again
}

// Runners are the subprocess seams; NewApp fills zero fields with the real
// ones.
type Runners struct {
	Privileged RunFn // sudo -n odioctl …
	User       RunFn // same user, no sudo (systemctl reboot)
	Spawn      func(argv []string) (ActionProcess, error)
	Log        io.Writer // what the process did, one line each; nil → stderr
}

type App struct {
	cfg   Config
	run   Runners
	token string
	log   *log.Logger

	stateMu  sync.Mutex // state.json's read-modify-write
	changes  *Changes
	actions  *Actions
	upgrades *Upgrades
}

func NewApp(cfg Config, r Runners) *App {
	if r.Privileged == nil {
		r.Privileged = defaultPrivilegedRun(cfg)
	}
	if r.User == nil {
		r.User = defaultUserRun
	}
	if r.Spawn == nil {
		r.Spawn = defaultSpawn
	}
	if r.Log == nil {
		r.Log = os.Stderr
	}
	app := &App{
		cfg:   cfg,
		run:   r,
		token: newToken(),
		// journald stamps the lines itself; under `go run` the shell does.
		log:     log.New(r.Log, "", 0),
		changes: NewChanges(),
	}
	app.actions = newActions(r.Spawn, cfg.Home, app.log, app.changes.Changed)
	app.upgrades = newUpgrades(cfg.UnitsDir(), app.log, app.changes.Changed)
	return app
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token is the per-process form token every POST must echo.
func (a *App) Token() string { return a.token }

func (a *App) Config() Config { return a.cfg }

// -- reads --------------------------------------------------------------

func (a *App) ReadState() (state.State, error) {
	return state.Read(a.cfg.StatePath)
}

func (a *App) DacStatus() dac.Status {
	return dac.GetStatus(a.cfg.ConfigTxt)
}

func (a *App) UpgradeReport() *upgrade.Report {
	return upgrade.ReadReport(a.cfg.ResolvedUpgradesPath())
}

// AvailableRoles is the target release's role set, nil until a check has run.
func (a *App) AvailableRoles() map[string]string {
	if report := a.UpgradeReport(); report != nil {
		return report.Manifest.Roles
	}
	return nil
}

// runChecked turns any subprocess failure into a UserError banner.
func runChecked(run RunFn, args []string, what string) error {
	res, err := run(args)
	if err != nil {
		return userErrorf("cannot run %s: %v", what, err)
	}
	if res.Code != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(res.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("exit %d", res.Code)
		}
		return userErrorf("%s failed: %s", what, detail)
	}
	return nil
}

func stateErrorMsg(path string, err error) string {
	if os.IsNotExist(err) {
		return path + " not found"
	}
	return fmt.Sprintf("cannot read state.json: %v", err)
}
