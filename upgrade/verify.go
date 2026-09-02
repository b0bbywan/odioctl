package upgrade

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"sort"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/versions"
)

// A warning, not an error: a feature odios adds after this odioctl shipped
// is unknown here, and the box is fine.
func warnFeaturesUnknown(st state.State) string {
	var bad []string
	for _, f := range slices.Concat(st.Features, st.FeaturesExcluded) {
		if !components.KnownFeature(f) && !slices.Contains(bad, f) {
			bad = append(bad, f)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return fmt.Sprintf("features unknown to this odioctl: %v", bad)
}

func overlap(a []string, b []string) []string {
	var out []string
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func checkFeaturesNoOverlap(st state.State) string {
	if o := overlap(st.Features, st.FeaturesExcluded); len(o) > 0 {
		return fmt.Sprintf("features and features_excluded overlap: %v", o)
	}
	return ""
}

func checkRolesNoOverlap(st state.State) string {
	if o := overlap(slices.Sorted(maps.Keys(st.Roles)), st.RolesExcluded); len(o) > 0 {
		return fmt.Sprintf("roles and roles_excluded overlap: %v", o)
	}
	return ""
}

func checkHistoryMatchesOdios(st state.State) string {
	h := st.ReleaseHistory
	if len(h) > 0 && st.Odios != "" && h[len(h)-1] != st.Odios {
		return fmt.Sprintf("release_history[-1]=%q != state.odios=%q", h[len(h)-1], st.Odios)
	}
	return ""
}

func checkExpectedVersion(st state.State, expected string) string {
	// PR pre-releases tag as `pr-<N>`; the resolved odios string is a
	// git-describe (e.g. 2026.4.2b2-20-g7c1f6c4). Released tags match exactly.
	if len(expected) > 3 && expected[:3] == "pr-" {
		if !versions.IsValid(st.Odios) {
			return fmt.Sprintf("state.odios=%q not a valid version for %s", st.Odios, expected)
		}
		return ""
	}
	if st.Odios != expected {
		return fmt.Sprintf("state.odios=%q expected %s", st.Odios, expected)
	}
	return ""
}

// RunVerify runs schema sanity checks on state.json. Exit 0 valid, 1
// invalid, 2 missing.
func RunVerify(stderr io.Writer, statePath, expectedVersion string) int {
	st, err := state.Read(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stderr, "no state.json on disk")
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "  %v\n", err)
		return 1
	}

	if w := warnFeaturesUnknown(st); w != "" {
		fmt.Fprintf(stderr, "  warning: %s\n", w)
	}

	checks := []string{
		checkFeaturesNoOverlap(st),
		checkRolesNoOverlap(st),
		checkHistoryMatchesOdios(st),
	}
	if expectedVersion != "" {
		checks = append(checks, checkExpectedVersion(st, expectedVersion))
	}

	failed := false
	for _, c := range checks {
		if c != "" {
			failed = true
			fmt.Fprintf(stderr, "  %s\n", c)
		}
	}
	if failed {
		return 1
	}
	return 0
}
