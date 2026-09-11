package web

// The upgrade card's side of odio-upgrade.service: one watcher at a time, and
// how the last run ended. Only Start ever starts the unit; a render observes.

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/upgrade"
)

type Upgrades struct {
	mu       sync.Mutex
	watching bool
	note     actionNote // how the last run seen ended
	unitsDir string
	log      *log.Logger
	notify   func(names ...string)
}

func newUpgrades(unitsDir string, log *log.Logger, notify func(...string)) *Upgrades {
	return &Upgrades{unitsDir: unitsDir, log: log, notify: notify}
}

// Start starts the unit (= `sudo odioctl upgrade apply --progress`) and
// watches it. A refusal inside the unit (sudoers) fails it within a
// second, and the watcher reports that like any other end.
func (u *Upgrades) Start(report *upgrade.Report) (string, error) {
	if report == nil || !report.UpgradeAvailable {
		return "", userErrorf("nothing to apply — no upgrade or pending component reported")
	}
	if u.running() {
		return "Upgrade already running.", nil
	}
	if err := upgrade.StartUnit(); err != nil {
		return "", userErrorf("cannot start %s: %v", upgrade.Unit, err)
	}
	u.log.Printf("upgrade: started %s", upgrade.Unit)
	u.watch()
	u.notify("upgrade")
	return "Upgrade started.", nil
}

func (u *Upgrades) running() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.watching
}

// unitNote is the end of a run, as the card shows it; a unit never run or
// run to success reads the same (inactive, success).
func unitNote(s upgrade.UnitState) actionNote {
	if s.Failed() {
		return actionNote{Text: fmt.Sprintf("Failed (%s, exit %d).", s.Result, s.Code), Failed: true}
	}
	return actionNote{Text: "Done."}
}

// watch follows the unit to its end, then keeps that as the note and tells
// the open pages. One watcher at a time.
func (u *Upgrades) watch() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.watching {
		return
	}
	u.watching = true
	u.note = actionNote{}
	started := time.Now()
	go func() {
		s := upgrade.WaitUnit(u.unitsDir, u.log.Printf)
		note := unitNote(s)
		u.log.Printf("upgrade: %s %s after %s (%s)", upgrade.Unit, s.Active, time.Since(started).Round(time.Second), note.Text)
		u.mu.Lock()
		u.watching, u.note = false, note
		u.mu.Unlock()
		u.notify("upgrade", "components") // the run installs rows
	}()
}

// State is (running, note of the last run). With no watcher of its own it
// asks systemd: a unit activating (odio-api's, or ours from before a restart)
// gets watched from here, one failed is shown as such.
func (u *Upgrades) State(report *upgrade.Report) (running bool, note actionNote) {
	u.mu.Lock()
	watching, note := u.watching, u.note
	u.mu.Unlock()
	if watching {
		return true, actionNote{}
	}
	if report == nil || !report.UpgradeAvailable {
		return false, note
	}
	s, err := upgrade.ShowUnit()
	switch {
	case err != nil:
		u.log.Printf("upgrade: %v", err)
		return false, note
	case s.Running():
		u.log.Printf("upgrade: %s is activating, watching it", upgrade.Unit)
		u.watch()
		return true, actionNote{}
	case s.Failed():
		return false, unitNote(s)
	}
	return false, note
}
