package upgrade

// odio-upgrade.service, the unit that applies an upgrade on odio (`sudo
// odioctl upgrade apply --progress`). The web UI and odio-api start it;
// following it to its end is here.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const Unit = "odio-upgrade.service"

// UnitPoll is how often the unit is asked about when it cannot be watched
// (no units directory); UnitRecheck is the tick behind the watch when it can.
var (
	UnitPoll    = 2 * time.Second
	UnitRecheck = 15 * time.Second
)

// Systemctl runs `systemctl --user` with args and returns its stdout; a
// non-zero exit is an error carrying stderr. A var so tests stand in.
var Systemctl = func(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), detail)
	}
	return stdout.String(), nil
}

// StartUnit enqueues the unit and returns at once: it is not activating
// yet when this returns.
func StartUnit() error {
	_, err := Systemctl("start", "--no-block", Unit)
	return err
}

// UnitState is what `systemctl show` says of the unit.
type UnitState struct {
	Active string // activating while the oneshot runs, then inactive or failed
	Result string // success, exit-code, …
	Code   int    // ExecMainStatus
}

func (u UnitState) Running() bool { return u.Active == "activating" }
func (u UnitState) Failed() bool  { return u.Active == "failed" }

// ShowUnit asks systemd. `show` prints Key=Value lines in systemd's own
// order, not the -p order (Result and ExecMainStatus before ActiveState),
// hence keyed.
func ShowUnit() (UnitState, error) {
	out, err := Systemctl("show", "-p", "ActiveState", "-p", "Result", "-p", "ExecMainStatus", Unit)
	if err != nil {
		return UnitState{}, err
	}
	var u UnitState
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "ActiveState":
			u.Active = value
		case "Result":
			u.Result = value
		case "ExecMainStatus":
			u.Code, _ = strconv.Atoi(value)
		}
	}
	return u, nil
}

// WaitUnit blocks until the unit is over and returns how: the word comes from
// its invocation link leaving unitsDir, a slow tick behind it, and polling
// without that directory. No probe up front — StartUnit returns too early.
func WaitUnit(unitsDir string, logf func(string, ...any)) UnitState {
	interval := UnitRecheck
	gone, stop, err := removedFrom(unitsDir, "invocation:"+Unit, logf)
	if err != nil {
		logf("upgrade: cannot watch %s (%v), polling %s", unitsDir, err, Unit)
		interval = UnitPoll // gone is nil: never fires
	} else {
		defer stop()
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-gone:
		case <-tick.C:
		}
		u, err := ShowUnit()
		if err != nil {
			logf("upgrade: %v", err)
			continue
		}
		if !u.Running() {
			return u
		}
	}
}

// removedFrom signals each removal of name from dir, coalesced, until stop.
// Watch errors go to logf and the watch goes on.
func removedFrom(dir, name string, logf func(string, ...any)) (gone <-chan struct{}, stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) == name && ev.Has(fsnotify.Remove|fsnotify.Rename) {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				logf("upgrade: watching %s: %v", dir, err)
			}
		}
	}()
	return ch, func() { _ = w.Close() }, nil
}
