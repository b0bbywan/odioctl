package components

import (
	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

// Pending lists what the next `upgrade apply` would install, as ["role:mpd",
// "feature:mympd", …] in List's order, a switched audio server first.
// Disabling is never pending.
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
			_, parentOn := st.Roles[c.Parent]
			if parentOn || pendingRoles[c.Parent] {
				pending = append(pending, c)
			}
		}
	}
	return pending
}
