package cli

import (
	"io"

	"github.com/b0bbywan/odioctl/motd"
	"github.com/b0bbywan/odioctl/state"
)

func runMOTD(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("motd", stderr)
	statePath := fs.String("state", state.SystemStatePath, "path to state.json")
	if code, done := parse(fs, args); done {
		return code
	}
	return motd.RunMOTD(stdout, *statePath)
}
