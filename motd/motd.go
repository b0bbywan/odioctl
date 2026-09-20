// Package motd prints odio's login banner, what odios shipped as a shell
// script until now: the release, what an upgrade would bring (upgrades.json,
// read here rather than by a python3 spawned at every login) and the PWA
// link. odios only hooks it into the target user's ~/.profile.
package motd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/b0bbywan/odioctl/netinfo"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

const banner = ` .----------------.   .----------------.   .----------------.
| .--------------. | | .--------------. | | .--------------. |
| |     ____     | | | |              | | | |     ____     | |
| |   .'    ` + "`" + `.   | | | |      ___     | | | |   .'    ` + "`" + `.   | |
| |  /  .--.  \  | | | |    _|  (_)   | | | |  /  .--.  \  | |
| |  | |    | |  | | | |   / _` + "`" + ` | |   | | | |  | |    | |  | |
| |  \  ` + "`" + `--'  /  | | | |  | (_| | |   | | | |  \  ` + "`" + `--'  /  | |
| |   ` + "`" + `.____.'   | | | |   \__,_|_|   | | | |   ` + "`" + `.____.'   | |
| '--------------' | | '--------------' | | '--------------' |
 '----------------'   '----------------'   '----------------'`

// uname is the kernel line, `uname -snrvm` as the shell script printed it.
// A seam: tests do not depend on the kernel they run on.
var uname = func() string {
	out, err := exec.Command("uname", "-snrvm").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RunMOTD is `odioctl motd`. Always 0: a login shell is not the place to
// fail, so whatever is missing is simply left out.
func RunMOTD(stdout io.Writer, statePath string) int {
	fmt.Fprintln(stdout, banner)
	if u := uname(); u != "" {
		fmt.Fprintln(stdout, u)
	}
	printRelease(stdout, statePath)
	if url := netinfo.PWAURL(); url != "" {
		fmt.Fprintf(stdout, "\n\033[1;32m📲 %s\033[0m\n", url)
	}
	fmt.Fprintf(stdout, "\nodio - %s comes with ABSOLUTELY NO WARRANTY, to the\n"+
		"extent permitted by applicable law.\n", osName())
	return 0
}

// printRelease is the installed version and, when `check` found one, the
// upgrade waiting with its per-role detail.
func printRelease(stdout io.Writer, statePath string) {
	report := upgrade.ReadReport(state.UpgradesPathFor(statePath))
	if report == nil {
		if st, err := state.Read(statePath); err == nil {
			fmt.Fprintf(stdout, "odio v%s\n", st.Odios)
		}
		return
	}
	fmt.Fprintf(stdout, "odio v%s\n", report.Current)
	if !report.UpgradeAvailable {
		return
	}
	fmt.Fprintf(stdout, "\033[1;33m⬆ Update available: %s → %s\033[0m\n", report.Current, report.Latest)
	for _, r := range report.Roles {
		fmt.Fprintf(stdout, "  %s: %s → %s\n", r.Name, r.Installed, r.Available)
	}
}

// osName is /etc/os-release's NAME, the distribution the warranty line names.
func osName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	defer func() { _ = f.Close() }()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if name, ok := strings.CutPrefix(scan.Text(), "NAME="); ok {
			if name = strings.Trim(name, `"'`); name != "" {
				return name
			}
		}
	}
	return "Linux"
}
