package dac

// CLI runners for `odioctl dac` — the flag parsing lives in the cli package,
// the behavior here.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RunList prints the catalog.
func RunList(stdout io.Writer, asJSON bool) int {
	if asJSON {
		b, _ := json.MarshalIndent(Catalog, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	for _, e := range Catalog {
		fmt.Fprintf(stdout, "  %-36s %s\n", e.ID, e.Label)
	}
	return 0
}

// RunStatus shows the currently configured DAC.
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
	current := s.Current
	label := "(unknown)"
	if e, ok := ByID(current); ok {
		label = e.Label
	}
	if current == "" {
		current, label = "(none)", ""
	}
	fmt.Fprintf(stdout, "config:   %s\n", s.Config)
	fmt.Fprintln(stdout, strings.TrimRight(fmt.Sprintf("current:  %s %s", current, label), " "))
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

// RunSet selects a DAC (root; reboot required).
func RunSet(stdout, stderr io.Writer, id, configPath string, dryRun bool) int {
	// The sudoers fragment lists one frozen argv per id; this check is the
	// interactive-use guard.
	entry, ok := ByID(id)
	if !ok {
		fmt.Fprintf(stderr, "Error: invalid choice %q (see `odioctl dac list`)\n", id)
		return 2
	}
	return runWrite(stdout, stderr, &entry, configPath, dryRun)
}

// RunUnset removes the odioctl block and restores the previous lines (root).
func RunUnset(stdout, stderr io.Writer, configPath string, dryRun bool) int {
	return runWrite(stdout, stderr, nil, configPath, dryRun)
}

// runWrite rewrites config.txt for entry (nil = unset).
func runWrite(stdout, stderr io.Writer, entry *Entry, configPath string, dryRun bool) int {
	path := configPath
	if path == "" {
		path = FindConfigTxt()
	}
	if path == "" || !isFile(path) {
		fmt.Fprintf(stderr, "Error: no config.txt found (tried %s)\n",
			strings.Join(ConfigCandidates, ", "))
		return 2
	}
	text, err := ReadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", path, err)
		return 2
	}
	updated := Unapply(text)
	what := "odioctl DAC block removed"
	if entry != nil {
		updated = Apply(text, *entry)
		what = "DAC set to " + entry.ID
	}

	if dryRun {
		fmt.Fprint(stdout, updated)
		return 0
	}
	if updated == text {
		fmt.Fprintln(stdout, "no change")
		return 0
	}
	if err := WriteConfig(path, updated); err != nil {
		fmt.Fprintf(stderr, "Error writing %s: %v\n", path, err)
		return 2
	}
	MarkRebootRequired()
	fmt.Fprintf(stdout, "%s in %s — reboot required\n", what, path)
	return 0
}
