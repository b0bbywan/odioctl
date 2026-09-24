package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

func runComponents(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("components", stderr)
	statePath := fs.String("state", state.SystemStatePath, "path to state.json")
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: odioctl components [--state PATH] list|enable|disable|set ...")
		return 2
	}
	// The target manifest `check` cached, so its catalog describes new roles.
	man := upgrade.CachedManifest(state.UpgradesPathFor(*statePath))
	switch rest[0] {
	case "list":
		lfs := newFlagSet("components list", stderr)
		asJSON := lfs.Bool("json", false, "machine-readable output")
		if code, done := parse(lfs, rest[1:]); done {
			return code
		}
		return components.RunList(stdout, stderr, *statePath, man, *asJSON)
	case "enable", "disable":
		if len(rest) != 2 {
			fmt.Fprintf(stderr, "usage: odioctl components %s NAME\n", rest[0])
			return 2
		}
		return components.RunSet(stdout, stderr, *statePath, man, rest[1], rest[0] == "enable")
	case "set":
		if len(rest) != 3 || rest[1] != "audioserver" {
			fmt.Fprintf(stderr, "usage: odioctl components set audioserver %s\n",
				strings.Join(components.Audioservers, "|"))
			return 2
		}
		return components.RunSetAudioserver(stdout, stderr, *statePath, man, rest[2])
	default:
		fmt.Fprintf(stderr, "odioctl components: unknown command %q\n", rest[0])
		return 2
	}
}
