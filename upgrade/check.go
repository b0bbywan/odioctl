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
// and odio-api). Roles is a delta — only roles whose target > installed.
// Manifest caches the full target snapshot so `apply` skips the network.
// Latest is the version the target release calls itself; TargetTag the
// GitHub tag it is published under ("2026.7.0rc2-9-gcad916c" vs "pr-84").
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
	Output  string // default state.SystemUpgradesPath
}

func (o CheckOptions) withDefaults() CheckOptions {
	if o.State == "" {
		o.State = state.SystemStatePath
	}
	if o.Output == "" {
		o.Output = state.SystemUpgradesPath
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

// ReadReport returns the cached upgrades.json, nil when missing/unreadable.
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
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	if r.PendingComponents == nil {
		r.PendingComponents = []string{}
	}
	if r.TargetTag == "" {
		r.TargetTag = r.Latest
		if r.TargetTag == "" {
			r.TargetTag = "latest"
		}
	}
	return &r
}

// Refresh rewrites upgrades.json after a local change (component toggle).
// Offline, the cached manifest is reused — and staying on the release it
// came from, since dropping its tag would leave `apply` building an
// install.sh URL from a version string that is not a tag. Nil when neither
// a manifest nor a cache is available (nothing was written).
func Refresh(opts CheckOptions) *Report {
	opts = opts.withDefaults()
	st, err := state.Read(opts.State)
	if err != nil {
		return nil
	}
	url, tag, err := manifest.CheckSource(opts.Version)
	if err != nil {
		return nil
	}
	man, fetchErr := manifest.Fetch(url)
	if fetchErr != nil {
		cached := ReadReport(opts.Output)
		if cached == nil {
			return nil
		}
		slog.Warn("could not fetch manifest, reusing the cached one", "url", url, "err", fetchErr)
		man = &cached.Manifest
		if tag == "" {
			tag = cached.TargetTag
		}
	}
	report := buildReport(st, *man, tag)
	if writeReport(report, opts.Output) != nil {
		return nil
	}
	return &report
}

// RunCheck compares state.json against the target manifest and rewrites
// upgrades.json. Exit 0 up to date, 1 upgrade available, 2 error.
func RunCheck(stdout, stderr io.Writer, opts CheckOptions) int {
	opts = opts.withDefaults()
	st, err := state.Read(opts.State)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading state: %v\n", err)
		return 2
	}
	url, tag, err := manifest.CheckSource(opts.Version)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	man, err := manifest.Fetch(url)
	if err != nil {
		fmt.Fprintf(stderr, "Error: could not fetch manifest at %s: %v\n", url, err)
		return 2
	}
	report := buildReport(st, *man, tag)
	if err := writeReport(report, opts.Output); err != nil {
		fmt.Fprintf(stderr, "Error writing %s: %v\n", opts.Output, err)
		return 2
	}
	printCheckSummary(stdout, report)
	if report.UpgradeAvailable {
		return 1
	}
	return 0
}
