// Package versions parses and orders odios version strings: calver plus an
// optional pre-release phase ("2026.4.2b2", "2026.7.0rc1"), optionally
// suffixed by a git-describe tail ("-20-g7c1f6c4") for PR pre-releases.
package versions

import (
	"regexp"
	"slices"
	"strconv"
)

var versionRE = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:(a|b|rc)(\d+))?(?:-(\d+)-g[0-9a-f]+)?$`)

var prePhases = map[string]int{"a": 0, "b": 1, "rc": 2}

// parse returns a sortable key; unparseable input (incl. "latest") maps to [0].
func parse(v string) []int {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return []int{0}
	}
	key := []int{atoi(m[1]), atoi(m[2]), atoi(m[3]), 3, 0, 0}
	if m[4] != "" {
		key[3] = prePhases[m[4]]
		key[4] = atoi(m[5])
	}
	if m[6] != "" {
		key[5] = atoi(m[6])
	}
	return key
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Compare orders two version strings by their parse keys.
func Compare(a, b string) int {
	return slices.Compare(parse(a), parse(b))
}

// IsValid reports whether v parses as an odios version — "latest" and
// arbitrary strings do not.
func IsValid(v string) bool {
	return len(parse(v)) > 1
}

// IsDowngrade reports whether both versions parse cleanly and target is below
// stateOdios. False for "latest" or anything unparseable — safer to let
// install.sh resolve and fail than to refuse on a parse miss.
func IsDowngrade(target, stateOdios string) bool {
	if stateOdios == "" {
		return false
	}
	tv, sv := parse(target), parse(stateOdios)
	if len(tv) == 1 || len(sv) == 1 {
		return false
	}
	return slices.Compare(tv, sv) < 0
}

// RoleUpToDate reports whether the installed role version covers target AND is
// trustworthy: a target ahead of stateOdios is past the last release certified
// on this box, so the marker for `installed` cannot be trusted — re-run.
func RoleUpToDate(installed, target, stateOdios string) bool {
	if installed == "" || target == "" {
		return false
	}
	if Compare(target, installed) > 0 {
		return false
	}
	return stateOdios == "" || Compare(target, stateOdios) <= 0
}
