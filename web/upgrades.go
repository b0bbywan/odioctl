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

// set takes the run odio-api reports and tells the open pages. A run's end
// is its note, and the rows it installed. Called by follow alone.
func (u *Upgrades) set(r upgrade.Run) {
	u.mu.Lock()
	same, last := u.run.Equal(r), u.last
	u.mu.Unlock()
	if same {
		return
	}
	names := []string{"upgrade"}
	var note *actionNote
	switch {
	case r.Running():
		note = &actionNote{}
	case r.State == upgrade.RunIdle && last == upgrade.RunRunning:
		note = &actionNote{Text: "Done."}
	case r.State == upgrade.RunFailed && last != upgrade.RunFailed:
		n := failureNote(u.log)
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
	if r.Known() {
		u.last = r.State
	}
	if note != nil {
		u.note = *note
	}
	u.mu.Unlock()
	u.notify(names...)
}

// failureNote is how the unit failed, as systemd still has it; after a
// reboot only that it did.
func failureNote(log *log.Logger) actionNote {
	s, err := upgrade.ShowUnit()
	if err != nil {
		log.Printf("upgrade: %v", err)
	}
	if err != nil || !s.Failed() {
		return actionNote{Text: "Failed.", Failed: true}
	}
	return actionNote{Text: fmt.Sprintf("Failed (%s, exit %d).", s.Result, s.Code), Failed: true}
}

// Start asks odio-api for a run (= `sudo odioctl upgrade apply --progress`).
// A refusal inside the unit (sudoers) fails it within a second, and the
// run says so like any other end.
func (u *Upgrades) Start(report *upgrade.Report) (string, error) {
	if report == nil || !report.UpgradeAvailable {
		return "", userErrorf("nothing to apply — no upgrade or pending component reported")
	}
	run := u.State()
	switch {
	case !run.Known:
		return "", userErrorf("odio-api is not answering: upgrades run through it")
	case run.Running:
		return "Upgrade already running.", nil
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
	u.log.Printf("upgrade: odio-api started %s", upgrade.Unit)
	return "Upgrade started.", nil
}

// upgradeRun is the card's view of the run: unknown without odio-api,
// running (with its progress, once it came), or the note of the last one.
type upgradeRun struct {
	Known    bool
	Running  bool
	Progress *upgrade.Progress
	Note     actionNote
}

func (u *Upgrades) State() upgradeRun {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.run.Running() {
		return upgradeRun{Known: true, Running: true, Progress: u.run.Progress}
	}
	return upgradeRun{Known: u.run.Known(), Note: u.note}
}
