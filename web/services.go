package web

// What the pages can ask the box to do — no HTTP, no HTML. One operation per
// form. state.json is edited in this process; only config.txt writes escalate
// (`sudo -n odioctl dac …`). Upgrades are never run here: the web process
// starts odio-upgrade.service, the unit odio-api drives too, and follows it
// to its end. Subprocesses go through Runners so tests drive the real code
// path against stand-ins.

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

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

// actionLinkTimeout is how long a component action gets to print its link.
// `qbzd login` fetches an app id over the network first, so it is not instant.
var actionLinkTimeout = 15 * time.Second

// upgradeStartGrace is how long "Apply now" watches the follower for an
// immediate refusal before calling the upgrade started.
var upgradeStartGrace = 2 * time.Second

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

// ActionProcess is one started component action, as the services see it.
type ActionProcess interface {
	Output() io.Reader // combined stdout+stderr
	Pid() int
	Alive() bool
	ExitCode() int                // valid once !Alive()
	WaitFor(d time.Duration) bool // true when the process exited within d
	Stop()                        // terminate, then kill — nothing polls it again
}

// Runners are the subprocess seams; NewServices fills zero fields with the
// real ones.
type Runners struct {
	Privileged RunFn // sudo -n odioctl …
	User       RunFn // same user, no sudo (systemctl --user)
	Spawn      func(argv []string) (ActionProcess, error)
	Log        io.Writer // what the process did, one line each; nil → stderr
}

// ActionResult is what an action just did, shown in the modal of the POST
// response — it never reaches another client or the next page load; what
// outlives the request is the row's own link.
type ActionResult struct {
	Title, Output, URL, LinkLabel string
}

type actionKey struct {
	kind components.Kind
	name string
	id   string
}

// Services holds the business operations behind the pages (also
// unit-testable directly).
type Services struct {
	cfg   Config
	run   Runners
	token string
	log   *log.Logger

	mu sync.Mutex
	// Started actions outlive their request: `qbzd login` waits up to 300s
	// for the user to follow its link.
	runs  map[actionKey]*actionRun
	notes map[actionKey]actionNote
	// The `systemctl --user start` following odio-upgrade.service, and how
	// the last one ended.
	upgrade     *actionRun
	upgradeNote actionNote
	// Open /events streams, each told once when something changed.
	subs map[chan struct{}]struct{}
}

func NewServices(cfg Config, r Runners) *Services {
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
	return &Services{
		cfg:   cfg,
		run:   r,
		token: newToken(),
		// journald stamps the lines itself; under `go run` the shell does.
		log:   log.New(r.Log, "", 0),
		runs:  map[actionKey]*actionRun{},
		notes: map[actionKey]actionNote{},
		subs:  map[chan struct{}]struct{}{},
	}
}

// Subscribe hands out a channel that gets one value when something the page
// shows has changed; cancel drops it.
func (s *Services) Subscribe() (ch <-chan struct{}, cancel func()) {
	c := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[c] = struct{}{}
	s.mu.Unlock()
	return c, func() {
		s.mu.Lock()
		delete(s.subs, c)
		s.mu.Unlock()
	}
}

// changed wakes every subscriber; a buffered channel coalesces bursts.
func (s *Services) changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Token is the per-process form token every POST must echo.
func (s *Services) Token() string { return s.token }

func (s *Services) Config() Config { return s.cfg }

// -- reads --------------------------------------------------------------

func (s *Services) ReadState() (state.State, error) {
	return state.Read(s.cfg.StatePath)
}

func (s *Services) DacStatus() dac.Status {
	return dac.GetStatus(s.cfg.ConfigTxt)
}

func (s *Services) UpgradeReport() *upgrade.Report {
	return upgrade.ReadReport(s.cfg.ResolvedUpgradesPath())
}

// AvailableRoles is the target release's role set, nil until a check has run.
func (s *Services) AvailableRoles() map[string]string {
	if report := s.UpgradeReport(); report != nil {
		return report.Manifest.Roles
	}
	return nil
}

// -- writes -------------------------------------------------------------

func (s *Services) SetComponent(kind components.Kind, name string, enabled bool) (string, error) {
	if err := s.writeComponent(kind, name, enabled); err != nil {
		return "", err
	}
	// Keep upgrades.json in step so odio-ui's badge and `upgrade apply` see
	// the pending install without waiting for the daily timer. Outside the
	// lock: it fetches the manifest, and every render takes that lock.
	report := upgrade.Refresh(upgrade.CheckOptions{
		State:  s.cfg.StatePath,
		Output: s.cfg.ResolvedUpgradesPath(),
	})
	label := components.LabelOf(kind, name)
	switch {
	case !enabled:
		return label + " disabled — it stays installed but will no longer be updated.", nil
	case report != nil && report.HasPending(string(kind)+":"+name):
		return label + " enabled — it will be installed by the next upgrade (apply it below).", nil
	default:
		return label + " enabled.", nil
	}
}

func (s *Services) writeComponent(kind components.Kind, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ReadState()
	if err != nil {
		return &UserError{Msg: stateErrorMsg(s.cfg.StatePath, err)}
	}
	next, err := components.Set(st, kind, name, enabled)
	if err != nil {
		return &UserError{Msg: err.Error()}
	}
	if err := state.Write(s.cfg.StatePath, next); err != nil {
		return userErrorf("cannot write state.json: %v", err)
	}
	return nil
}

// resolveAction is the catalog action to run, or a *UserError naming what is
// wrong. Offered for installed components only.
func (s *Services) resolveAction(kind components.Kind, name, id string) (components.Action, error) {
	action, ok := components.FindAction(kind, name, id)
	if !ok {
		return action, userErrorf("unknown action %q for %s", id, name)
	}
	st, err := s.ReadState()
	if err != nil {
		return action, &UserError{Msg: stateErrorMsg(s.cfg.StatePath, err)}
	}
	for _, c := range components.List(st, nil) {
		if c.Kind == kind && c.Name == name && c.Status == components.Installed {
			return action, nil
		}
	}
	return action, userErrorf("%s is not installed", components.LabelOf(kind, name))
}

// RunAction starts a catalog action and returns (banner, modal). The command
// is never waited on: `qbzd login` prints its URL and then holds a listener
// open until the browser comes back (300s), so stdout is read only until the
// link shows up and the process is left to it.
func (s *Services) RunAction(kind components.Kind, name, id, host string) (string, *ActionResult, error) {
	action, err := s.resolveAction(kind, name, id)
	if err != nil {
		return "", nil, err
	}
	key := actionKey{kind, name, id}

	s.mu.Lock()
	if run, ok := s.runs[key]; ok && run.alive() {
		s.mu.Unlock()
		s.log.Printf("action %s/%s: already running (pid %d), showing its link again", name, id, run.proc.Pid())
		return action.Label + ": already running — the link is below.", run.result(), nil
	}
	delete(s.notes, key)
	run, err := startAction(s.run.Spawn, action, host, s.cfg.Home)
	if err != nil {
		s.mu.Unlock()
		return "", nil, err
	}
	s.runs[key] = run
	s.mu.Unlock()
	s.log.Printf("action %s/%s: spawned pid %d: %s", name, id, run.proc.Pid(), strings.Join(run.argv, " "))
	go func() { // the exit, when it happens: logged, and the open pages told
		code := run.proc.ExitCode()
		s.log.Printf("action %s/%s: pid %d exited %d after %s", name, id, run.proc.Pid(), code, run.elapsed())
		s.changed()
	}()

	if url := run.awaitLink(actionLinkTimeout); url != "" {
		s.log.Printf("action %s/%s: link after %s: %s", name, id, run.elapsed(), url)
		return action.Label + ": open the link below to finish.", run.result(), nil
	}
	s.log.Printf("action %s/%s: no link after %s, output so far: %q", name, id, run.elapsed(), run.text())

	// No link: either it died (reap it for the exit code — stdout can close a
	// moment before the process does) or it is stuck and we stop it. The
	// entry stays in s.runs meanwhile: it is what keeps a second click from
	// spawning again while this one is still being stopped, and what
	// ActionState turns into the row's "Failed (exit N)" note on reload.
	if !run.proc.WaitFor(2 * time.Second) {
		run.proc.Stop()
		return "", nil, &UserError{
			Msg:   fmt.Sprintf("%s: no link after %.0fs", action.Label, actionLinkTimeout.Seconds()),
			Modal: run.result(),
		}
	}
	return "", nil, &UserError{
		Msg:   fmt.Sprintf("%s failed (exit %d)", action.Label, run.proc.ExitCode()),
		Modal: run.result(),
	}
}

// ActionState is the (pending link, note) of one action — ("", "") when it
// never ran. Reaps a finished run into the note the next render shows: no
// JavaScript here, the operator reloads to see the end.
func (s *Services) ActionState(kind components.Kind, name, id string) (url string, note actionNote) {
	key := actionKey{kind, name, id}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[key]
	if !ok {
		return "", s.notes[key]
	}
	if run.alive() {
		return run.link(), actionNote{}
	}
	delete(s.runs, key)
	s.notes[key] = run.note()
	return "", s.notes[key]
}

// Busy reports whether anything followed — an action, the upgrade — is still
// alive: the page then loads app.js and waits on /events.
func (s *Services) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upgrade != nil && s.upgrade.alive() {
		return true
	}
	for _, run := range s.runs {
		if run.alive() {
			return true
		}
	}
	return false
}

// StartUpgrade starts odio-upgrade.service (= `sudo odioctl upgrade apply
// --progress`) and follows it to its end. A refusal (sudoers, unit missing)
// shows within a moment and becomes the error; a real run does not.
func (s *Services) StartUpgrade() (string, error) {
	report := s.UpgradeReport()
	if report == nil || !report.UpgradeAvailable {
		return "", userErrorf("nothing to apply — no upgrade or pending component reported")
	}
	run, joined, err := s.followUpgrade()
	if err != nil {
		return "", err
	}
	if joined {
		return "Upgrade already running.", nil
	}
	if run.proc.WaitFor(upgradeStartGrace) && run.proc.ExitCode() != 0 {
		return "", &UserError{
			Msg:   fmt.Sprintf("upgrade failed to start (exit %d)", run.proc.ExitCode()),
			Modal: run.result(),
		}
	}
	return "Upgrade started.", nil
}

// followUpgrade spawns `systemctl --user start` without --no-block: it
// returns only when the oneshot is over, with its result, and on a unit
// already activating (odio-api started it) it waits on that same job. The
// unit runs in its own cgroup, so what this process follows is systemctl.
// joined is true when a follower was already alive.
func (s *Services) followUpgrade() (run *actionRun, joined bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upgrade != nil && s.upgrade.alive() {
		return s.upgrade, true, nil
	}
	argv := []string{"systemctl", "--user", "start", UpgradeUnit}
	proc, err := s.run.Spawn(argv)
	if err != nil {
		return nil, false, userErrorf("cannot run %s: %v", strings.Join(argv, " "), err)
	}
	run = trackRun(proc, argv, "Upgrade", "", "")
	s.upgrade = run
	s.upgradeNote = actionNote{}
	s.log.Printf("upgrade: spawned pid %d: %s", proc.Pid(), strings.Join(argv, " "))
	go func() {
		code := proc.ExitCode()
		s.log.Printf("upgrade: pid %d exited %d after %s", proc.Pid(), code, run.elapsed())
		s.mu.Lock()
		s.upgradeNote = run.note()
		s.mu.Unlock()
		s.changed()
	}()
	return run, false, nil
}

// UpgradeState is (running, note of the last run). Without a follower of its
// own, and only while the report says there is something to apply, it asks
// systemd: a unit activating was started by odio-api or before this process,
// and gets followed from here. The wait between show and start can land on
// an inactive unit and start it again, which `apply` then refuses: the
// playbook's last step re-ran `check`, so nothing is available any more.
func (s *Services) UpgradeState(report *upgrade.Report) (running bool, note actionNote) {
	s.mu.Lock()
	run, note := s.upgrade, s.upgradeNote
	s.mu.Unlock()
	if run != nil && run.alive() {
		return true, actionNote{}
	}
	if report == nil || !report.UpgradeAvailable {
		return false, note
	}
	res, err := s.run.User([]string{"systemctl", "--user", "show", "-p", "ActiveState", "--value", UpgradeUnit})
	if err != nil || strings.TrimSpace(res.Stdout) != "activating" {
		return false, note
	}
	s.log.Printf("upgrade: %s is activating, following it", UpgradeUnit)
	if _, _, err := s.followUpgrade(); err != nil {
		s.log.Printf("upgrade: %v", err)
		return true, note
	}
	return true, actionNote{}
}

// SetDAC escalates through `sudo -n odioctl dac set <id>`.
func (s *Services) SetDAC(id string) (string, error) {
	if _, ok := dac.ByID(id); !ok {
		return "", userErrorf("unknown DAC id %q", id)
	}
	// Plain HTML cannot grey the Apply button out, so re-applying the current
	// selection is one click away: recognise the no-op here rather than
	// escalate through sudo and claim a reboot that nothing needs. Only when
	// odioctl owns the block, though — same id over an unmanaged config.txt
	// does change the file (takes ownership, comments the stray lines out).
	if d := s.DacStatus(); d.Managed && d.Current == id {
		return "DAC already set to " + id + " — nothing to apply.", nil
	}
	if err := s.runDac("dac", "set", id); err != nil {
		return "", err
	}
	return "DAC set to " + id + " — reboot required.", nil
}

// UnsetDAC removes the odioctl block from config.txt, through sudo.
func (s *Services) UnsetDAC() (string, error) {
	if err := s.runDac("dac", "unset"); err != nil {
		return "", err
	}
	return "DAC block removed — reboot required.", nil
}

func (s *Services) runDac(args ...string) error {
	if s.cfg.ConfigTxt != "" {
		args = append(args, "--config", s.cfg.ConfigTxt)
	}
	return runChecked(s.run.Privileged, args, "odioctl dac")
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
