package components

// CLI runners for `odioctl components` — the flag parsing lives in the cli
// package, the behavior here.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/b0bbywan/odioctl/state"
)

// componentJSON is one `components list --json` entry.
type componentJSON struct {
	Kind             Kind         `json:"kind"`
	Name             string       `json:"name"`
	Label            string       `json:"label"`
	Description      string       `json:"description"`
	Group            string       `json:"group"`
	Status           Status       `json:"status"`
	InstalledVersion string       `json:"installed_version"`
	Parent           string       `json:"parent"`
	Toggleable       bool         `json:"toggleable"`
	Actions          []actionJSON `json:"actions"`
	Enabled          bool         `json:"enabled"`
}

type actionJSON struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Argv        []string `json:"argv"`
	LinkScheme  string   `json:"link_scheme"`
	LinkLabel   string   `json:"link_label"`
	LinkNote    string   `json:"link_note"`
}

func toJSON(comps []Component) []componentJSON {
	out := make([]componentJSON, 0, len(comps))
	for _, c := range comps {
		actions := []actionJSON{}
		for _, a := range c.Actions {
			actions = append(actions, actionJSON(a))
		}
		out = append(out, componentJSON{
			Kind: c.Kind, Name: c.Name, Label: c.Label, Description: c.Description,
			Group: c.Group, Status: c.Status, InstalledVersion: c.InstalledVersion,
			Parent: c.Parent, Toggleable: c.Toggleable, Actions: actions, Enabled: c.Enabled(),
		})
	}
	return out
}

func printTable(w io.Writer, comps []Component) {
	for _, kind := range []Kind{Role, Feature} {
		fmt.Fprintf(w, "%ss:\n", kind)
		for _, c := range comps {
			if c.Kind != kind {
				continue
			}
			ver := ""
			if c.InstalledVersion != "" {
				ver = fmt.Sprintf(" (%s)", c.InstalledVersion)
			}
			lock := ""
			if !c.Toggleable {
				lock = " [infra]"
			}
			parent := ""
			if c.Parent != "" {
				parent = " ← " + c.Parent
			}
			fmt.Fprintf(w, "  %-16s %-10s%s%s%s\n", c.Name, c.Status, ver, parent, lock)
			// Only offered by the web UI, and only once the binaries are there.
			if c.Status == Installed {
				for _, a := range c.Actions {
					fmt.Fprintf(w, "      action: %s — %s\n", strings.Join(a.Argv, " "), a.Description)
				}
			}
		}
	}
}

// RunList shows every role/feature and its status.
func RunList(stdout, stderr io.Writer, statePath string, asJSON bool) int {
	st, err := state.Read(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", statePath, err)
		return 2
	}
	comps := List(st, nil)
	if !asJSON {
		printTable(stdout, comps)
		return 0
	}
	b, err := json.MarshalIndent(toJSON(comps), "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stdout, string(b))
	return 0
}

// RunSet enables or disables a role/feature by bare name.
func RunSet(stdout, stderr io.Writer, statePath, name string, enabled bool) int {
	st, err := state.Read(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", statePath, err)
		return 2
	}
	kind := kindOf(st, name)
	newState, err := Set(st, kind, name, enabled)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	if err := state.Write(statePath, newState); err != nil {
		fmt.Fprintf(stderr, "Error writing %s: %v\n", statePath, err)
		return 2
	}
	verb := "disabled"
	if enabled {
		verb = "enabled"
	}
	fmt.Fprintf(stdout, "%s %s %s. %s\n", kind, name, verb, ApplyNote)
	return 0
}
