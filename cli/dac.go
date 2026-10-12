package cli

import (
	"fmt"
	"io"

	"github.com/b0bbywan/odioctl/dac"
)

func runDAC(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: odioctl dac list|status|set|unset ...")
		return 2
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("dac list", stderr)
		asJSON := fs.Bool("json", false, "machine-readable output")
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		return dac.RunList(stdout, *asJSON)
	case "status":
		fs := newFlagSet("dac status", stderr)
		asJSON := fs.Bool("json", false, "machine-readable output")
		configPath := fs.String("config", "", configUsage)
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		return dac.RunStatus(stdout, *configPath, *asJSON)
	case "set", "unset":
		return runSetUnset(stderr, "dac", args[0], "ID", args[1:],
			func(id, configPath string, dryRun bool) int {
				return dac.RunSet(stdout, stderr, id, configPath, dryRun)
			},
			func(configPath string, dryRun bool) int {
				return dac.RunUnset(stdout, stderr, configPath, dryRun)
			})
	default:
		fmt.Fprintf(stderr, "odioctl dac: unknown command %q\n", args[0])
		return 2
	}
}
