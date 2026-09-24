// Package components models odios roles (services) and features (plugins of a
// role), toggled through state.json; nothing is installed until `odioctl
// upgrade apply` runs. The target release's catalog says what exists; a name
// only state.json knows is still listed. Without a release, nothing is toggled.
package components

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

type Kind string

const (
	Role    Kind = "role"
	Feature Kind = "feature"
)

type Status string

const (
	Installed Status = "installed"
	Excluded  Status = "excluded"
	Default   Status = "default"
)

// Error reports an invalid component operation (unknown name/kind, infra role).
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func errorf(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

type Component struct {
	Kind             Kind
	Name             string
	Label            string // the catalog's, else the name capitalized
	Description      string
	Group            string
	Status           Status
	InstalledVersion string
	Parent           string
	Toggleable       bool
	Actions          []Action
}

func (c Component) Enabled() bool { return c.Status != Excluded }

// RequestedVersion marks an opt-in role enabled but not installed yet: in
// Roles (so INSTALL_X=Y is emitted) but out of the version comparisons.
const RequestedVersion = ""

func roleStatus(st state.State, man *manifest.Manifest, name string) Status {
	if v, ok := st.Roles[name]; ok {
		if v != "" {
			return Installed
		}
		return Default // opted in here, installs on the next apply
	}
	if slices.Contains(st.RolesExcluded, name) {
		return Excluded
	}
	if info, ok := roleInfo(man, name); ok && info.OptIn {
		return Excluded // install.sh answers N: neither list means off, not default
	}
	return Default
}

func featureStatus(st state.State, name string) Status {
	if slices.Contains(st.Features, name) {
		return Installed
	}
	if slices.Contains(st.FeaturesExcluded, name) {
		return Excluded
	}
	return Default
}

// List returns roles by group then label, then features. man is the
// target release (nil = unknown): its catalog lists roles and their features,
// state.json adds what it names.
func List(st state.State, man *manifest.Manifest) []Component {
	roles := roleNames(st, man)
	features := featureNames(st, man, roles)
	out := make([]Component, 0, len(roles)+len(features))
	for name := range roles {
		out = append(out, roleComponent(st, man, name))
	}
	for name := range features {
		out = append(out, featureComponent(st, man, name))
	}
	slices.SortFunc(out, func(a, b Component) int {
		return cmp.Or(
			cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)),
			cmp.Compare(slices.Index(Groups, a.Group), slices.Index(Groups, b.Group)),
			cmp.Compare(a.Label, b.Label),
			cmp.Compare(a.Name, b.Name),
		)
	})
	return out
}

func kindRank(k Kind) int {
	if k == Role {
		return 0
	}
	return 1
}

func roleNames(st state.State, man *manifest.Manifest) map[string]bool {
	names := map[string]bool{}
	if man != nil {
		for n := range man.Catalog {
			names[n] = true
		}
	}
	for n := range st.Roles {
		names[n] = true
	}
	for _, n := range st.RolesExcluded {
		names[n] = true
	}
	maps.DeleteFunc(names, func(n string, _ bool) bool { return !roleShown(st, man, n) })
	return names
}

// roleShown hides the audio servers (a choice, see AudioserverOf), a role not
// for this architecture unless installed or requested here (roles_excluded
// lists it on every odio install.sh skipped it on), and one the release does
// not ship unless state.json names it.
func roleShown(st state.State, man *manifest.Manifest, name string) bool {
	if isAudioserver(name) {
		return false
	}
	_, inRoles := st.Roles[name]
	if info, ok := roleInfo(man, name); ok && !info.supported() && !inRoles {
		return false
	}
	if man != nil {
		return man.Ships(name) || stateHasRole(st, name)
	}
	return true
}

// featureNames lists the catalog's features whose parent is among roles, and
// every feature state.json names.
func featureNames(st state.State, man *manifest.Manifest, roles map[string]bool) map[string]bool {
	names := map[string]bool{}
	if man != nil {
		for role, meta := range man.Catalog {
			if roles[role] {
				for n := range meta.Features {
					names[n] = true
				}
			}
		}
	}
	for _, n := range st.Features {
		names[n] = true
	}
	for _, n := range st.FeaturesExcluded {
		names[n] = true
	}
	return names
}

func roleComponent(st state.State, man *manifest.Manifest, name string) Component {
	info, known := roleInfo(man, name)
	status := roleStatus(st, man, name)
	c := Component{
		Kind:             Role,
		Name:             name,
		Label:            nameLabel(name),
		Group:            Groups[len(Groups)-1],
		Status:           status,
		InstalledVersion: st.Roles[name],
		// A required role has no toggle while it is on. One install.sh
		// answered N to is still offered, so nothing is lost by requiring it.
		// Without a release nothing is: it is what says which ones are required.
		Toggleable: man != nil && (!known || !info.Required || status == Excluded),
		Actions:    roleActions[name],
	}
	if known {
		c.Label, c.Description, c.Group = info.Label, info.Description, info.Group
	}
	return c
}

func featureComponent(st state.State, man *manifest.Manifest, name string) Component {
	c := Component{
		Kind:       Feature,
		Name:       name,
		Label:      nameLabel(name),
		Group:      Groups[len(Groups)-1],
		Status:     featureStatus(st, name),
		Toggleable: man != nil,
		Actions:    featureActions[name],
	}
	if info, known := featureInfo(man, name); known {
		c.Label, c.Description, c.Parent = info.Label, info.Description, info.Parent
	}
	return c
}

func stateHasRole(st state.State, name string) bool {
	_, ok := st.Roles[name]
	return ok || slices.Contains(st.RolesExcluded, name)
}

func stateHasFeature(st state.State, name string) bool {
	return slices.Contains(st.Features, name) || slices.Contains(st.FeaturesExcluded, name)
}

func known(st state.State, man *manifest.Manifest, kind Kind, name string) bool {
	if kind == Role {
		_, inCatalog := roleInfo(man, name)
		return inCatalog || stateHasRole(st, name)
	}
	_, inCatalog := featureInfo(man, name)
	return inCatalog || stateHasFeature(st, name)
}

// kindOf resolves a bare CLI name: a role if the catalog or state.json knows
// it as one, a feature otherwise.
func kindOf(st state.State, man *manifest.Manifest, name string) Kind {
	if _, ok := roleInfo(man, name); ok || stateHasRole(st, name) {
		return Role
	}
	return Feature
}

// Set returns a copy of st with name opted in or out. Disabling moves a role
// into RolesExcluded; enabling clears the exclusion, and records an opt-in
// role with RequestedVersion (install.sh would answer its [y/N] with N).
func Set(st state.State, man *manifest.Manifest, kind Kind, name string, enabled bool) (state.State, error) {
	if err := checkSet(st, man, kind, name, enabled); err != nil {
		return state.State{}, err
	}
	out := st.Clone()
	if kind == Role {
		setRole(&out, man, name, enabled)
	} else {
		setFeature(&out, name, enabled)
	}
	return out, nil
}

func checkSet(st state.State, man *manifest.Manifest, kind Kind, name string, enabled bool) error {
	if kind != Role && kind != Feature {
		return errorf("unknown component kind %q", kind)
	}
	if man == nil {
		return errorf("no release catalog yet: run `odioctl upgrade check` first")
	}
	if kind == Role && isAudioserver(name) {
		return errorf("the audio server is a choice, not a toggle: odioctl components set audioserver %s", name)
	}
	if kind == Role {
		info, ok := roleInfo(man, name)
		if ok && !enabled && info.Required {
			return errorf("%q is required by odio and cannot be disabled", name)
		}
		if ok && enabled && !info.supported() {
			return errorf("%q is not available on %s", name, arch)
		}
	}
	if !known(st, man, kind, name) {
		return errorf("unknown %s %q", kind, name)
	}
	return nil
}

func setRole(st *state.State, man *manifest.Manifest, name string, enabled bool) {
	if !enabled {
		delete(st.Roles, name)
		st.RolesExcluded = with(st.RolesExcluded, name)
		return
	}
	st.RolesExcluded = without(st.RolesExcluded, name)
	if info, ok := roleInfo(man, name); ok && info.OptIn {
		if _, present := st.Roles[name]; !present {
			st.Roles[name] = RequestedVersion
		}
	}
}

func setFeature(st *state.State, name string, enabled bool) {
	if !enabled {
		st.Features = without(st.Features, name)
		st.FeaturesExcluded = with(st.FeaturesExcluded, name)
		return
	}
	st.FeaturesExcluded = without(st.FeaturesExcluded, name)
}

func with(list []string, name string) []string {
	out := slices.Clone(list)
	if !slices.Contains(out, name) {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func without(list []string, name string) []string {
	out := slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == name })
	slices.Sort(out)
	return out
}

const ApplyNote = "Enabling installs on the next upgrade; disabling keeps the component " +
	"installed but stops updating it."
