package upgrade

// odio-upgrade.service, the unit that applies an upgrade on odio (`sudo
// odioctl upgrade apply --progress`). odio-api starts and follows it
// (odioapi.go); odioctl only asks systemd how a failed one failed.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const Unit = "odio-upgrade.service"

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
