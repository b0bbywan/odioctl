// Package procutil is what the exec'd subprocesses share: reading an exit
// status out of the error os/exec hands back.
package procutil

import (
	"errors"
	"os/exec"
)

// ExitCode turns the error from cmd.Run/Wait into the child's exit status.
// A non-nil error means the child never ran or could not be waited for: the
// caller decides what that is worth (a code of its own, a log line, or the
// error itself).
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
