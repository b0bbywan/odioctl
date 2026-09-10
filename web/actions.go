package web

// Component actions (components.Action) between requests: the runs still
// going — `qbzd login` waits up to 300s for the user to follow its link —
// and the exited ones, kept for the row's note and the modal's end.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/components"
)

// actionLinkTimeout is how long a component action gets to print its link.
// `qbzd login` fetches an app id over the network first, so it is not instant.
var actionLinkTimeout = 15 * time.Second

// ActionResult is what an action just did, shown in the modal of the POST
// response — it never reaches another client or the next page load; what
// outlives the request is the row's own link.
type ActionResult struct {
	ID                            string // element id, so a later fragment replaces this modal
	Title, Output, URL, LinkLabel string
	Note                          actionNote // how it ended, once it has: a Done button where the link was
}

type actionKey struct {
	kind components.Kind
	name string
	id   string
}

// domID names the modal of this action in the page, so a fragment for its
// end replaces that one and no other.
func (k actionKey) domID() string { return fmt.Sprintf("modal-%s-%s-%s", k.kind, k.name, k.id) }

type Actions struct {
	mu       sync.Mutex
	runs     map[actionKey]*actionRun
	finished map[actionKey]*actionRun // reaped, for the row's note and the modal's end
	spawn    func(argv []string) (ActionProcess, error)
	home     string // the target user's, what an action's {home} becomes
	log      *log.Logger
	notify   func(names ...string)
}

func newActions(spawn func([]string) (ActionProcess, error), home string, log *log.Logger, notify func(...string)) *Actions {
	return &Actions{
		runs:     map[actionKey]*actionRun{},
		finished: map[actionKey]*actionRun{},
		spawn:    spawn,
		home:     home,
		log:      log,
		notify:   notify,
	}
}

// resolveAction is the catalog action to run, or a *UserError naming what is
// wrong. Offered for installed components only.
func (a *App) resolveAction(kind components.Kind, name, id string) (components.Action, error) {
	action, ok := components.FindAction(kind, name, id)
	if !ok {
		return action, userErrorf("unknown action %q for %s", id, name)
	}
	st, err := a.ReadState()
	if err != nil {
		return action, &UserError{Msg: stateErrorMsg(a.cfg.StatePath, err)}
	}
	for _, c := range components.List(st, nil) {
		if c.Kind == kind && c.Name == name && c.Status == components.Installed {
			return action, nil
		}
	}
	return action, userErrorf("%s is not installed", components.LabelOf(kind, name))
}

// RunAction starts a catalog action and returns (banner, modal).
func (a *App) RunAction(kind components.Kind, name, id, host string) (string, *ActionResult, error) {
	action, err := a.resolveAction(kind, name, id)
	if err != nil {
		return "", nil, err
	}
	return a.actions.run(action, actionKey{kind, name, id}, host)
}

// run starts the action and returns (banner, modal). The command is never
// waited on: `qbzd login` prints its URL and then holds a listener open
// until the browser comes back (300s), so stdout is read only until the
// link shows up and the process is left to it.
func (x *Actions) run(action components.Action, key actionKey, host string) (string, *ActionResult, error) {
	name, id := key.name, key.id
	x.mu.Lock()
	if run, ok := x.runs[key]; ok && run.alive() {
		x.mu.Unlock()
		x.log.Printf("action %s/%s: already running (pid %d), showing its link again", name, id, run.proc.Pid())
		return action.Label + ": already running — the link is below.", run.result(), nil
	}
	delete(x.finished, key)
	run, err := startAction(x.spawn, action, host, x.home)
	if err != nil {
		x.mu.Unlock()
		return "", nil, err
	}
	run.id = key.domID()
	x.runs[key] = run
	x.mu.Unlock()
	x.log.Printf("action %s/%s: spawned pid %d: %s", name, id, run.proc.Pid(), strings.Join(run.argv, " "))
	go func() { // the exit, when it happens: logged, and the open pages told
		code := run.proc.ExitCode()
		x.log.Printf("action %s/%s: pid %d exited %d after %s", name, id, run.proc.Pid(), code, run.elapsed())
		x.notify("components", run.id) // the row, and the modal of a page still showing it
	}()

	if url := run.awaitLink(actionLinkTimeout); url != "" {
		x.log.Printf("action %s/%s: link after %s: %s", name, id, run.elapsed(), url)
		x.notify("components") // the row shows the link while the process lives
		return action.Label + ": open the link below to finish.", run.result(), nil
	}
	x.log.Printf("action %s/%s: no link after %s, output so far: %q", name, id, run.elapsed(), run.text())

	// No link: either it died (reap it for the exit code — stdout can close a
	// moment before the process does) or it is stuck and we stop it. The
	// entry stays in runs meanwhile: it is what keeps a second click from
	// spawning again while this one is still being stopped, and what State
	// turns into the row's "Failed (exit N)" note on reload.
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

// State is the (pending link, note) of one action — ("", "") when it never
// ran. Reaps a finished run into the note the next render shows.
func (x *Actions) State(kind components.Kind, name, id string) (url string, note actionNote) {
	key := actionKey{kind, name, id}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.reapLocked()
	if run, ok := x.runs[key]; ok {
		return run.link(), actionNote{}
	}
	if done := x.finished[key]; done != nil {
		return "", done.note()
	}
	return "", actionNote{}
}

// reapLocked moves every exited run to finished; x.mu held. A run being
// stopped is still alive and stays where a second click finds it.
func (x *Actions) reapLocked() {
	for key, run := range x.runs {
		if !run.alive() {
			delete(x.runs, key)
			x.finished[key] = run
		}
	}
}

// Finished is the end of every exited action, for the modal that showed
// its link: the same output, a Done button where the link was.
func (x *Actions) Finished() []*ActionResult {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.reapLocked()
	var out []*ActionResult
	for _, run := range x.finished {
		out = append(out, run.result())
	}
	return out
}
