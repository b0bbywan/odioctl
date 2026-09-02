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
		configPath := fs.String("config", "", "path to config.txt (default: "+dac.ConfigTxt+")")
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		return dac.RunStatus(stdout, *configPath, *asJSON)
	case "set", "unset":
		fs := newFlagSet("dac "+args[0], stderr)
		configPath := fs.String("config", "", "path to config.txt (default: "+dac.ConfigTxt+")")
		dryRun := fs.Bool("dry-run", false, "print the resulting file, don't write")
		if code, done := parse(fs, args[1:]); done {
			return code
		}
		rest := fs.Args()
		if args[0] == "unset" {
			if len(rest) != 0 {
				fmt.Fprintln(stderr, "usage: odioctl dac unset [--config PATH] [--dry-run]")
				return 2
			}
			return dac.RunUnset(stdout, stderr, *configPath, *dryRun)
		}
		// The ID may sit before the flags (the sudoers lines, the web's
		// `dac set <id> --config …`) or after them: flag stops at the first
		// positional, so parse once more past it.
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "usage: odioctl dac set ID [--config PATH] [--dry-run]")
			return 2
		}
		id := rest[0]
		if code, done := parse(fs, rest[1:]); done {
			return code
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: odioctl dac set ID [--config PATH] [--dry-run]")
			return 2
		}
		return dac.RunSet(stdout, stderr, id, *configPath, *dryRun)
	default:
		fmt.Fprintf(stderr, "odioctl dac: unknown command %q\n", args[0])
		return 2
	}
}
