// Package cli is the odioctl command-line entry point.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/b0bbywan/odioctl/config"
	"github.com/b0bbywan/odioctl/netinfo"
)

const usageText = `usage: odioctl [--version] COMMAND

odio system control: upgrades, components, DAC overlay and a local web UI.

commands:
  upgrade      check for / apply / verify odios upgrades
  pwa-url      print the PWA URL for this host
  components   list / enable / disable roles and features
  dac          select the DAC overlay in config.txt
`

// Run dispatches argv (without the program name) and returns the exit code.
func Run(stdout, stderr io.Writer, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprint(stdout, usageText)
		return 2
	}
	switch argv[0] {
	case "--version", "-version":
		fmt.Fprintf(stdout, "%s %s\n", config.AppName, config.AppVersion)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "upgrade":
		return runUpgrade(stdout, stderr, argv[1:])
	case "pwa-url":
		return runPWAURL(stdout, stderr, argv[1:])
	case "components":
		return runComponents(stdout, stderr, argv[1:])
	case "dac":
		return runDAC(stdout, stderr, argv[1:])
	default:
		fmt.Fprintf(stderr, "odioctl: unknown command %q\n%s", argv[0], usageText)
		return 2
	}
}

// newFlagSet builds a ContinueOnError set whose usage and errors go to stderr.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("odioctl "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parse runs the flag set; done means the command is over (help shown → 0,
// bad usage → 2) and code is what to return.
func parse(fs *flag.FlagSet, args []string) (code int, done bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return 0, false
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	default:
		return 2, true
	}
}

func runPWAURL(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("pwa-url", stderr)
	if code, done := parse(fs, args); done {
		return code
	}
	fmt.Fprintln(stdout, netinfo.PWAURL())
	return 0
}
