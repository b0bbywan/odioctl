package input

// CLI runners for `odioctl input` — the flag parsing lives in the cli
// package, the behavior here.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/b0bbywan/odioctl/configtxt"
)

// RunStatus shows the configured inputs.
func RunStatus(stdout io.Writer, configPath string, asJSON bool) int {
	s := GetStatus(configPath)
	if asJSON {
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if !s.Supported {
		fmt.Fprintln(stdout, "no config.txt found — not a Raspberry Pi boot partition?")
		return 0
	}
	ir := "(none)"
	if s.IRPin != 0 {
		ir = fmt.Sprintf("GPIO %d", s.IRPin)
	}
	fmt.Fprintf(stdout, "config:   %s\n", s.Config)
	fmt.Fprintf(stdout, "ir:       %s\n", ir)
	managed := "no"
	if s.Managed {
		managed = "yes"
	}
	fmt.Fprintf(stdout, "managed:  %s\n", managed)
	if s.RebootRequired {
		fmt.Fprintln(stdout, "reboot required to apply the last change")
	}
	return 0
}

// RunSetIR puts the infrared receiver on a GPIO (root; reboot required).
func RunSetIR(stdout, stderr io.Writer, pin, configPath string, dryRun bool) int {
	// The sudoers fragment lists one frozen argv per pin; this check is the
	// interactive-use guard.
	n, ok := ParsePin(pin)
	if !ok {
		fmt.Fprintf(stderr, "Error: invalid GPIO %q (one of %s)\n", pin, pinList())
		return 2
	}
	return configtxt.RunEdit(stdout, stderr, configPath, dryRun, fmt.Sprintf("IR receiver set on GPIO %d", n),
		func(text string) string { return ApplyIR(text, n) })
}

// RunUnsetIR removes the receiver and restores the previous lines (root).
func RunUnsetIR(stdout, stderr io.Writer, configPath string, dryRun bool) int {
	return configtxt.RunEdit(stdout, stderr, configPath, dryRun, "IR receiver removed", UnapplyIR)
}

func pinList() string {
	var b strings.Builder
	for i, pin := range Pins {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprint(&b, pin)
	}
	return b.String()
}
