// Package upgrade implements `odioctl upgrade check|apply|verify`: the
// upgrades.json contract with odio-ui, and the install.sh re-run.
package upgrade

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/fsutil"
	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/versions"
)

// RoleUpgrade is one per-role entry of the upgrades.json delta, in the exact
// {name, installed, available} shape odio-motd reads.
type RoleUpgrade struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
}

// Report is the schema of upgrades.json (written by `check`, read by `apply`
// and odio-api). Roles is a delta, Manifest the snapshot that spares `apply`
// the network, Latest the version and TargetTag the release it comes from.
type Report struct {
	Current           string            `json:"current"`
	Latest            string            `json:"latest"`
	TargetTag         string            `json:"target_tag"`
	UpgradeAvailable  bool              `json:"upgrade_available"`
	Roles             []RoleUpgrade     `json:"roles"`
	PendingComponents []string          `json:"pending_components"`
	Manifest          manifest.Manifest `json:"manifest"`
	CheckedAt         string            `json:"checked_at"`
}

type CheckOptions struct {
	State   string // default state.SystemStatePath
	Version string // release tag override, "" = published latest / env
	Output  string // default state.UpgradesPathFor(State)
}

func (o CheckOptions) withDefaults() CheckOptions {
	if o.State == "" {
		o.State = state.SystemStatePath
	}
	if o.Output == "" {
		o.Output = state.UpgradesPathFor(o.State)
	}
	return o
}

func computeRoleUpgrades(st state.State, man manifest.Manifest) []RoleUpgrade {
	upgrades := []RoleUpgrade{}
	for role, installed := range st.Roles {
		// No version = enabled here, not installed yet: that's a pending
		// component, not a role upgrade (components.RequestedVersion).
		if installed == "" {
			continue
		}
		available := man.Roles[role]
		if available != "" && versions.Compare(available, installed) > 0 {
			upgrades = append(upgrades, RoleUpgrade{Name: role, Installed: installed, Available: available})
		}
	}
	slices.SortFunc(upgrades, func(a, b RoleUpgrade) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return upgrades
}

func buildReport(st state.State, man manifest.Manifest, targetTag string) Report {
	upgrades := computeRoleUpgrades(st, man)
	pending := components.Pending(st, man.Roles)
	if pending == nil {
		pending = []string{}
	}
	if targetTag == "" {
		targetTag = man.Odios
	}
	return Report{
		Current:           st.Odios,
		Latest:            man.Odios,
		TargetTag:         targetTag,
		UpgradeAvailable:  len(upgrades) > 0 || versions.Compare(man.Odios, st.Odios) > 0 || len(pending) > 0,
		Roles:             upgrades,
		PendingComponents: pending,
		Manifest:          man,
		CheckedAt:         time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func writeReport(report Report, output string) error {
	if dir := filepath.Dir(output); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return err
	}
	// Atomic: the timer and every web toggle rewrite this while `apply` reads
	// it, and a torn read silently falls back to "latest".
	if err := fsutil.AtomicWriteText(output, buf.String()); err != nil {
		return err
	}
	// 0664 so other `odio` group members (target_user under the timer,
	// ansible become_user) can rewrite it without root.
	_ = os.Chmod(output, 0o664)
	return nil
}

func printCheckSummary(w io.Writer, r Report) {
	if r.TargetTag != r.Latest {
		fmt.Fprintf(w, "Comparing against release %s (%s)\n", r.TargetTag, r.Latest)
	}
	if !r.UpgradeAvailable {
		fmt.Fprintf(w, "Up to date (%s)\n", r.Current)
		return
	}
	fmt.Fprintf(w, "Upgrades available: %s → %s\n", r.Current, r.Latest)
	for _, ru := range r.Roles {
		fmt.Fprintf(w, "  %s: %s → %s\n", ru.Name, ru.Installed, ru.Available)
	}
	for _, c := range r.PendingComponents {
		fmt.Fprintf(w, "  %s: pending install\n", c)
	}
}

// HasPending reports whether ref ("role:x" / "feature:y") is pending.
func (r *Report) HasPending(ref string) bool {
	return slices.Contains(r.PendingComponents, ref)
}

// ReadReport returns the cached upgrades.json, nil when missing, unreadable
// or not a report `check` wrote (no manifest snapshot, no target tag).
func ReadReport(path string) *Report {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var probe struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if json.Unmarshal(b, &probe) != nil || probe.Manifest == nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil || r.TargetTag == "" {
		return nil
	}
	if r.PendingComponents == nil {
		r.PendingComponents = []string{}
	}
	return &r
}

// check is the pipeline behind RunCheck and Refresh: state.json against the
// target manifest, written to upgrades.json. RunCheck always fetches; Refresh
// reuses the cached manifest, never under another tag than the one it was
// written for — `apply` trusts target_tag to name the manifest next to it.
func check(opts CheckOptions, useCache bool) (*Report, error) {
	opts = opts.withDefaults()
	st, err := state.Read(opts.State)
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	url, tag, err := manifest.CheckSource(opts.Version)
	if err != nil {
		return nil, err
	}
	var man *manifest.Manifest
	if useCache {
		if cached := ReadReport(opts.Output); cached != nil && (tag == "" || cached.TargetTag == tag) {
			man = &cached.Manifest
			tag = cached.TargetTag
		}
	}
	if man == nil {
		if man, err = manifest.Fetch(url); err != nil {
			return nil, fmt.Errorf("could not fetch manifest at %s: %w", url, err)
		}
	}
	report := buildReport(st, *man, tag)
	if err := writeReport(report, opts.Output); err != nil {
		return nil, fmt.Errorf("writing %s: %w", opts.Output, err)
	}
	return &report, nil
}

// Refresh rewrites upgrades.json after a local change (component toggle),
// from the cached manifest, fetching only when none is cached for the
// target tag. Nil when nothing was written.
func Refresh(opts CheckOptions) *Report {
	report, err := check(opts, true)
	if err != nil {
		slog.Warn("upgrades.json not refreshed", "err", err)
		return nil
	}
	return report
}

// RunCheck compares state.json against the target manifest and rewrites
// upgrades.json. Exit 0 up to date, 1 upgrade available, 2 error.
func RunCheck(stdout, stderr io.Writer, opts CheckOptions) int {
	report, err := check(opts, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	printCheckSummary(stdout, *report)
	if report.UpgradeAvailable {
		return 1
	}
	return 0
}
