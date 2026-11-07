package web

// The upgrade card's side of odio-upgrade.service, which odio-api starts and
// tracks: its run, followed for the life of the process, and how the last
// one ended. Only Start ever asks for a run; a render observes.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/upgrade"
)

type Upgrades struct {
	mu      sync.Mutex
	run     upgrade.Run // odio-api's, unknown until it is heard
	heard   bool        // the follower reported once: unknown means not answering
	last    string      // the last state heard, kept through an outage
	note    actionNote  // how the last run seen ended
	odioAPI string      // "" → none: no run is known, none is started
	log     *log.Logger
	notify  func(names ...string)
}

func newUpgrades(odioAPI string, log *log.Logger, notify func(...string)) *Upgrades {
	return &Upgrades{odioAPI: odioAPI, log: log, notify: notify}
}

// follow keeps the run odio-api's until ctx is done.
func (u *Upgrades) follow(ctx context.Context) {
	if u.odioAPI == "" {
		return
	}
	upgrade.FollowRun(ctx, u.odioAPI, u.set, u.log.Printf)
}

// How odio-api stands when no run is known, for the card's hint.
const (
	reachNone       = "none"       // no odio-api configured: nothing to start or follow
	reachConnecting = "connecting" // the follower's first try is on
	reachOff        = "off"        // odio-api answers, without its upgrade backend
)

// set takes the run odio-api reports and tells the open pages. A run's end
// is its note, and the rows it installed. Called by follow alone.
func (u *Upgrades) set(r upgrade.Run) {
	u.mu.Lock()
	same, last, heard := u.run.Equal(r), u.last, u.heard
	u.mu.Unlock()
	if same && heard { // the first report is news even when unknown
		return
	}
	names := []string{"upgrade"}
	var note *actionNote
	switch {
	case r.Running():
		note = &actionNote{}
	case r.State == upgrade.RunIdle && last == upgrade.RunRunning:
		note = &actionNote{Text: "Done."}
	case r.State == upgrade.RunFailed && (last != upgrade.RunFailed || r.Failure != nil):
		// a reconnect's snapshot has no end line: the note stays
		n := failureNote(r.Failure)
		note = &n
	}
	if last == upgrade.RunRunning && r.Known() && !r.Running() {
		names = append(names, "components") // the run installs rows
	}
	if r.State != last && r.Known() {
		u.log.Printf("upgrade: odio-api says the run is %s", r.State)
	}
	u.mu.Lock()
	u.run = r
	u.heard = true
	if r.Known() {
		u.last = r.State
	}
	if note != nil {
		u.note = *note
	}
	u.mu.Unlock()
	u.notify(names...)
}

// failureNote is what the playbook said failed; only that it did when it
// died outside one (sudoers, the download) or before odioctl listened.
func failureNote(f *upgrade.Failure) actionNote {
	switch {
	case f == nil:
		return actionNote{Text: "Failed.", Failed: true}
	case f.Step == "":
		return actionNote{Text: "Failed: " + f.Error, Failed: true}
	}
	return actionNote{Text: fmt.Sprintf("Failed in %s: %s", f.Step, f.Error), Failed: true}
}

// Start asks odio-api for a run (= `sudo odioctl upgrade apply --progress`).
// A refusal inside the unit (sudoers) fails it within a second, and the
// run says so like any other end.
func (u *Upgrades) Start(report *upgrade.Report) (string, error) {
	if report == nil || !report.UpgradeAvailable {
		return "", userErrorf("nothing to apply — no upgrade or pending component reported")
	}
	run, _, reach := u.State()
	switch {
	case reach == reachNone:
		return "", userErrorf("no odio-api configured: upgrades run through it")
	case reach == reachConnecting:
		return "", userErrorf("odio-api not heard from yet: try again in a moment")
	case reach == reachOff:
		return "", userErrorf("odio-api's upgrade backend is off: upgrades run through it")
	case !run.Known():
		return "", userErrorf("odio-api is not answering: upgrades run through it")
	case run.Running():
		return "Upgrade already running.", nil
	case !run.CanStart:
		return "", userErrorf("odio-api cannot start an upgrade here: it has no upgrade unit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := upgrade.StartRun(ctx, u.odioAPI)
	switch {
	case errors.Is(err, upgrade.ErrRunning):
		return "Upgrade already running.", nil
	case err != nil:
		return "", userErrorf("cannot start the upgrade: %v", err)
	}
	u.log.Printf("upgrade: odio-api started a run")
	return "Upgrade started.", nil
}

// State is odio-api's run, how the last one seen ended, and where odio-api
// stands when that run is unknown: reachNone, reachConnecting, reachOff, or "".
func (u *Upgrades) State() (upgrade.Run, actionNote, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	reach := ""
	switch {
	case u.odioAPI == "":
		reach = reachNone
	case !u.heard:
		reach = reachConnecting
	case !u.run.Known() && u.run.Off:
		reach = reachOff
	}
	return u.run, u.note, reach
}
