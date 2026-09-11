// Package procutil is what the exec'd subprocesses share: reading an exit
// status out of the error os/exec hands back.
package procutil

import (
	"errors"
	"os/exec"
)

// ExitCode turns the error from cmd.Run/Wait into the child's exit status. A
// non-nil error means the child never ran: the caller decides what that costs.
func ExitCode(err error) (int, error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil
	default:
		return 0, err
	}
}
