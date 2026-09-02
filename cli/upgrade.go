package cli

import (
	"fmt"
	"io"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

func runUpgrade(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: odioctl upgrade check|apply|verify")
		return 2
	}
	switch args[0] {
	case "check":
		return runUpgradeCheck(stdout, stderr, args[1:])
	case "apply":
		return runUpgradeApply(stdout, stderr, args[1:])
	case "verify":
		return runUpgradeVerify(stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "odioctl upgrade: unknown command %q\n", args[0])
		return 2
	}
}

func runUpgradeCheck(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("upgrade check", stderr)
	var opts upgrade.CheckOptions
	fs.StringVar(&opts.State, "state", state.SystemStatePath, "path to state.json")
	fs.StringVar(&opts.Version, "version", "", "release tag to compare against "+
		"(default: the published latest, or $"+manifest.OdiosVersionEnv+")")
	fs.StringVar(&opts.Output, "output", state.SystemUpgradesPath, "path to upgrades.json")
	if code, done := parse(fs, args); done {
		return code
	}
	return upgrade.RunCheck(stdout, stderr, opts)
}

func runUpgradeApply(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("upgrade apply", stderr)
	var opts upgrade.ApplyOptions
	var progress, noProgress bool
	fs.StringVar(&opts.Version, "version", "", "target version tag (default: latest from upgrades.json)")
	fs.StringVar(&opts.State, "state", "", "path to state.json (default: "+state.SystemStatePath+")")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the invocation without running")
	fs.BoolVar(&opts.Force, "force", false, "run even if no upgrade is reported")
	fs.BoolVar(&opts.Reinstall, "reinstall", false,
		"re-run every role in full: no smart-upgrade skips, all first-install scaffold re-applied")
	fs.BoolVar(&progress, "progress", false,
		"set ODIOS_PROGRESS=Y so install.sh emits ODIO_PROGRESS events "+
			"(default: auto-on when odio-api's upgrade socket is present)")
	fs.BoolVar(&noProgress, "no-progress", false, "never emit progress events, even on an instance")
	if code, done := parse(fs, args); done {
		return code
	}
	switch {
	case progress:
		opts.Progress = true
	case noProgress:
		opts.Progress = false
	default:
		opts.Progress = upgrade.OdioAPIListening()
	}
	return upgrade.RunApply(stdout, stderr, opts)
}

func runUpgradeVerify(stderr io.Writer, args []string) int {
	fs := newFlagSet("upgrade verify", stderr)
	statePath := fs.String("state", state.SystemStatePath, "path to state.json")
	expected := fs.String("expected-version", "", "also assert state.odios matches this tag")
	if code, done := parse(fs, args); done {
		return code
	}
	return upgrade.RunVerify(stderr, *statePath, *expected)
}
