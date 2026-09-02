package cli

import (
	"fmt"
	"io"
	"strings"

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
		configPath := fs.String("config", "", "path to config.txt (default: auto-detect)")
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		return dac.RunStatus(stdout, *configPath, *asJSON)
	case "set", "unset":
		id, rest := "", args[1:]
		if args[0] == "set" {
			if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
				fmt.Fprintln(stderr, "usage: odioctl dac set ID [--config PATH] [--dry-run]")
				return 2
			}
			id, rest = rest[0], rest[1:]
		}
		fs := newFlagSet("dac "+args[0], stderr)
		configPath := fs.String("config", "", "path to config.txt (default: auto-detect)")
		dryRun := fs.Bool("dry-run", false, "print the resulting file, don't write")
		if code, done := parse(fs, rest); done {
			return code
		}
		if args[0] == "set" {
			return dac.RunSet(stdout, stderr, id, *configPath, *dryRun)
		}
		return dac.RunUnset(stdout, stderr, *configPath, *dryRun)
	default:
		fmt.Fprintf(stderr, "odioctl dac: unknown command %q\n", args[0])
		return 2
	}
}
