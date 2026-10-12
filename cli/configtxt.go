package cli

import (
	"fmt"
	"io"

	"github.com/b0bbywan/odioctl/configtxt"
)

const configUsage = "path to config.txt (default: " + configtxt.Path + ")"

// runSetUnset parses `NAME set ARG` and `NAME unset`, the two writes a
// command has on config.txt. ARG may sit before the flags (the sudoers
// lines, the web's `dac set <id> --config …`) or after them: flag stops at
// the first positional, so parse once more past it.
func runSetUnset(stderr io.Writer, name, verb, arg string, args []string,
	set func(value, configPath string, dryRun bool) int, unset func(configPath string, dryRun bool) int) int {
	fs := newFlagSet(name+" "+verb, stderr)
	configPath := fs.String("config", "", configUsage)
	dryRun := fs.Bool("dry-run", false, "print the resulting file, don't write")
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args()
	if verb == "unset" {
		if len(rest) != 0 {
			fmt.Fprintf(stderr, "usage: odioctl %s unset [--config PATH] [--dry-run]\n", name)
			return 2
		}
		return unset(*configPath, *dryRun)
	}
	usage := fmt.Sprintf("usage: odioctl %s set %s [--config PATH] [--dry-run]", name, arg)
	if len(rest) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if code, done := parse(fs, rest[1:]); done {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return set(rest[0], *configPath, *dryRun)
}
