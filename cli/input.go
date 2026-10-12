package cli

import (
	"fmt"
	"io"

	"github.com/b0bbywan/odioctl/input"
)

func runInput(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: odioctl input status | ir set|unset ...")
		return 2
	}
	switch args[0] {
	case "status":
		fs := newFlagSet("input status", stderr)
		asJSON := fs.Bool("json", false, "machine-readable output")
		configPath := fs.String("config", "", configUsage)
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		return input.RunStatus(stdout, *configPath, *asJSON)
	case "ir":
		if len(args) < 2 || (args[1] != "set" && args[1] != "unset") {
			fmt.Fprintln(stderr, "usage: odioctl input ir set GPIO | unset")
			return 2
		}
		return runSetUnset(stderr, "input ir", args[1], "GPIO", args[2:],
			func(pin, configPath string, dryRun bool) int {
				return input.RunSetIR(stdout, stderr, pin, configPath, dryRun)
			},
			func(configPath string, dryRun bool) int {
				return input.RunUnsetIR(stdout, stderr, configPath, dryRun)
			})
	default:
		fmt.Fprintf(stderr, "odioctl input: unknown command %q\n", args[0])
		return 2
	}
}
