package components

import (
	"maps"
	"slices"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

// Pending lists what the next `upgrade apply` would install, as ["role:mpd",
// "feature:mympd", …] in List's order, a switched audio server first.
// What it would remove is Removals'.
func Pending(st state.State, man *manifest.Manifest) []string {
	var refs []string
	for _, c := range pending(st, man) {
		refs = append(refs, string(c.Kind)+":"+c.Name)
	}
	return refs
}

// PendingRuns lists the roles that next apply must run for Pending to land: a
// feature is installed by its parent, odio_api templates its service list.
func PendingRuns(st state.State, man *manifest.Manifest) []string {
	var runs []string
	for _, c := range pending(st, man) {
		role := c.Name
		if c.Kind == Feature {
			role = c.Parent
		}
		runs = with(runs, role)
	}
	if runs != nil {
		runs = with(runs, "odio_api")
	}
	return runs
}

func pending(st state.State, man *manifest.Manifest) []Component {
	ships := func(name string) bool { return man != nil && man.Ships(name) }
	var pending []Component
	// A switch installs the picked server: apply then runs every role.
	if a := AudioserverOf(st, man); man != nil && a.Switching() {
		pending = append(pending, roleComponent(st, man, a.Picked))
	}
	pendingRoles := map[string]bool{}
	for _, c := range List(st, man) {
		switch {
		case c.Kind == Role:
			if c.Toggleable && c.Status == Default && ships(c.Name) {
				pending = append(pending, c)
				pendingRoles[c.Name] = true
			}
		case c.Status == Default && c.Parent != "":
			if roleOn(st, c.Parent) || pendingRoles[c.Parent] {
				pending = append(pending, c)
			}
		}
	}
	return pending
}

// Removals is what the next `upgrade apply` disables, as odios' disable.yml
// takes it: the roles, and the features of the roles that stay, each with its
// role. A feature the catalog does not place cannot be run, and is left out.
type Removals struct {
	Roles    []string
	Features map[string]string // feature → its role
	withRole []string          // the removed roles' features, whatever their status
}

// RemovalsOf relies on List's order: roles before features.
func RemovalsOf(st state.State, man *manifest.Manifest) Removals {
	r := Removals{Features: map[string]string{}}
	for _, c := range List(st, man) {
		switch {
		case c.Kind == Role && c.Status == Removing:
			r.Roles = append(r.Roles, c.Name)
		case c.Kind == Feature && slices.Contains(r.Roles, c.Parent):
			r.withRole = append(r.withRole, c.Name)
		case c.Kind == Feature && c.Status == Removing && c.Parent != "":
			r.Features[c.Name] = c.Parent
		}
	}
	return r
}

func (r Removals) Empty() bool { return len(r.Roles) == 0 && len(r.Features) == 0 }

// Refs is r as upgrades.json lists it: ["role:x", "feature:y"], roles first.
func (r Removals) Refs() []string {
	refs := []string{}
	for _, n := range r.Roles {
		refs = append(refs, string(Role)+":"+n)
	}
	for _, n := range slices.Sorted(maps.Keys(r.Features)) {
		refs = append(refs, string(Feature)+":"+n)
	}
	return refs
}

// Drop returns st once r is removed: out of Roles and Features, the
// exclusions kept. A removed role's features go with it, not excluded, so
// they come back with the role.
func Drop(st state.State, r Removals) state.State {
	out := st.Clone()
	for _, n := range r.Roles {
		delete(out.Roles, n)
	}
	out.Features = slices.DeleteFunc(out.Features, func(f string) bool {
		_, removed := r.Features[f]
		return removed || slices.Contains(r.withRole, f)
	})
	return out
}
