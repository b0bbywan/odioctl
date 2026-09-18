package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/b0bbywan/odioctl/state"
)

func runState(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("state", stderr)
	statePath := fs.String("state", state.SystemStatePath, "path to state.json")
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: odioctl state [--state PATH] record")
		return 2
	}
	switch rest[0] {
	case "record":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "usage: odioctl state record < run.json")
			return 2
		}
		return state.RunRecord(os.Stdin, stdout, stderr, *statePath)
	default:
		fmt.Fprintf(stderr, "odioctl state: unknown command %q\n", rest[0])
		return 2
	}
}
