// Package components models odios roles (services) and features (plugins of
// a role), toggled through state.json; nothing is installed or removed until
// `odioctl upgrade apply` runs. The catalog is advisory: any name present in
// state.json is accepted even if unknown here.
package components

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

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

// Action is a one-off command the box runs for the user. Argv is fixed here,
// never built from the request; the server only fills in {host} (the address
// the browser reached the box by) and {home} (the target user's home).
type Action struct {
	ID          string // form value, unique per component
	Label       string // button text
	Description string // one line: what the command does
	Argv        []string
	LinkScheme  string // the stdout token to surface as a link
	LinkLabel   string // anchor text for that token
	LinkNote    string // how long the operator has to follow it
}

type RoleInfo struct {
	Label       string // product name the user knows
	Description string // one line, what it does
	Group       string
	Package     string
	OptIn       bool // install.sh asks [y/N]; see the package comment
	Actions     []Action
}

type FeatureInfo struct {
	Label       string
	Description string
	Package     string
	Parent      string
	Actions     []Action
}

// Groups is the display order of the web UI / `components list`; unknown
// roles go to the last group.
var Groups = []string{"Audio", "Playback", "Streaming", "System"}

// Roles that install.sh always runs; never user-toggleable.
var infraRoles = map[string]bool{"common": true, "upgrade": true}

type catalogRole struct {
	name string
	info RoleInfo
}

type catalogFeature struct {
	name string
	info FeatureInfo
}

// Slice order = display order within a group.
var roleCatalog = []catalogRole{
	// The odios `pipewire` role is experimental and not exposed by install.sh — not listed.
	{"pulseaudio", RoleInfo{
		Label:       "PulseAudio",
		Description: "Sound server, also a network audio sink for other machines",
		Group:       "Audio",
		Package:     "pulseaudio",
	}},
	{"bluetooth", RoleInfo{
		Label:       "Bluetooth",
		Description: "A2DP sink with automatic pairing, and output to Bluetooth speakers",
		Group:       "Audio",
		Package:     "bluez",
	}},
	{"mpd", RoleInfo{
		Label:       "MPD",
		Description: "Music Player Daemon: local library, CDs, web radios",
		Group:       "Playback",
		Package:     "mpd",
	}},
	{"mpd_discplayer", RoleInfo{
		Label:       "CD player",
		Description: "Audio CD playback through MPD, with metadata",
		Group:       "Playback",
		Package:     "mpd-discplayer",
	}},
	{"shairport_sync", RoleInfo{
		Label:       "AirPlay",
		Description: "AirPlay receiver (Shairport Sync)",
		Group:       "Streaming",
		Package:     "shairport-sync",
	}},
	{"spotifyd", RoleInfo{
		Label:       "Spotify Connect",
		Description: "Spotify Connect receiver (spotifyd)",
		Group:       "Streaming",
		Package:     "spotifyd",
	}},
	{"qbzd", RoleInfo{
		Label:       "Qobuz Connect",
		Description: "Qobuz Connect endpoint (qbzd, alpha)",
		Group:       "Streaming",
		Package:     "qbzd",
		OptIn:       true,
		Actions: []Action{{
			ID:          "login",
			Label:       "Log in to Qobuz",
			Description: "Sign in to Qobuz — opens a Qobuz link, the box catches the callback",
			Argv:        []string{"qbzd", "login", "--callback-host", "{host}"},
			LinkScheme:  "https://",
			LinkLabel:   "Open the Qobuz sign-in page",
			LinkNote:    "valid 5 minutes",
		}},
	}},
	{"snapclient", RoleInfo{
		Label:       "Snapcast",
		Description: "Multi-room audio client",
		Group:       "Streaming",
		Package:     "snapclient",
	}},
	{"upmpdcli", RoleInfo{
		Label:       "UPnP / DLNA",
		Description: "UPnP/OpenHome renderer (upmpdcli)",
		Group:       "Streaming",
		Package:     "upmpdcli",
	}},
	{"odio_api", RoleInfo{
		Label:       "odio-api",
		Description: "Remote control API and web dashboard",
		Group:       "System",
		Package:     "odio-api",
	}},
	{"branding", RoleInfo{
		Label:       "Branding",
		Description: "odio login banner (MOTD)",
		Group:       "System",
	}},
	{"common", RoleInfo{
		Label:       "Base system",
		Description: "Core configuration shared by every component",
		Group:       "System",
	}},
	{"upgrade", RoleInfo{
		Label:       "Upgrade",
		Description: "odioctl and the upgrade check timer",
		Group:       "System",
	}},
}

var featureCatalog = []catalogFeature{
	{"mympd", FeatureInfo{
		Label:       "myMPD",
		Description: "Web UI for MPD (port 8080)",
		Package:     "mympd",
		Parent:      "mpd",
	}},
	{"tidal", FeatureInfo{
		Label:       "Tidal",
		Description: "Tidal streaming through upmpdcli",
		Package:     "upmpdcli-tidal",
		Parent:      "upmpdcli",
		Actions: []Action{{
			ID:          "login",
			Label:       "Log in to Tidal",
			Description: "Sign in to Tidal",
			Argv: []string{
				"python3", "-u",
				"/usr/share/upmpdcli/cdplugins/tidal/get_credentials.py",
				"-f", "{home}/.cache/upmpdcli/tidal/oauth2.credentials.json",
			},
			LinkScheme: "https://",
			LinkLabel:  "Open the Tidal sign-in page",
			LinkNote:   "valid 5 minutes",
		}},
	}},
	{"qobuz", FeatureInfo{
		Label:       "Qobuz",
		Description: "Qobuz streaming through upmpdcli",
		Package:     "upmpdcli-qobuz",
		Parent:      "upmpdcli",
	}},
	{"upnpwebradios", FeatureInfo{
		Label:       "Web radios",
		Description: "Internet radios through upmpdcli",
		Package:     "upmpdcli-radios",
		Parent:      "upmpdcli",
	}},
}

func roleInfo(name string) (RoleInfo, bool) {
	for _, e := range roleCatalog {
		if e.name == name {
			return e.info, true
		}
	}
	return RoleInfo{}, false
}

func featureInfo(name string) (FeatureInfo, bool) {
	for _, e := range featureCatalog {
		if e.name == name {
			return e.info, true
		}
	}
	return FeatureInfo{}, false
}

func roleIndex(name string) int {
	for i, e := range roleCatalog {
		if e.name == name {
			return i
		}
	}
	return len(roleCatalog)
}

func featureIndex(name string) int {
	for i, e := range featureCatalog {
		if e.name == name {
			return i
		}
	}
	return len(featureCatalog)
}

// Error reports an invalid component operation (unknown name/kind, infra role).
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func errorf(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

type Component struct {
	Kind             Kind
	Name             string
	Label            string
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

func roleStatus(st state.State, name string) Status {
	if v, ok := st.Roles[name]; ok {
		if v != "" {
			return Installed
		}
		return Default // opted in here, installs on the next apply
	}
	if slices.Contains(st.RolesExcluded, name) {
		return Excluded
	}
	if info, ok := roleInfo(name); ok && info.OptIn {
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

// List returns roles in catalog order (grouped), unknown roles last, then
// features. shipped is the target release's role set (keys only, nil =
// unknown): roles it lacks are dropped, names state.json carries are kept
// anyway, features follow their parent out.
func List(st state.State, shipped map[string]string) []Component {
	roles := map[string]bool{}
	for _, e := range roleCatalog {
		roles[e.name] = true
	}
	for n := range st.Roles {
		roles[n] = true
	}
	for _, n := range st.RolesExcluded {
		roles[n] = true
	}
	features := map[string]bool{}
	for _, e := range featureCatalog {
		features[e.name] = true
	}
	for _, n := range st.Features {
		features[n] = true
	}
	for _, n := range st.FeaturesExcluded {
		features[n] = true
	}
	if shipped != nil {
		for n := range roles {
			_, ships := shipped[n]
			if !ships && !stateHasRole(st, n) {
				delete(roles, n)
			}
		}
		for n := range features {
			if stateHasFeature(st, n) {
				continue
			}
			if info, ok := featureInfo(n); ok && !roles[info.Parent] {
				delete(features, n)
			}
		}
	}

	roleNames := slices.SortedFunc(maps.Keys(roles), func(a, b string) int {
		if c := roleIndex(a) - roleIndex(b); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	featureNames := slices.SortedFunc(maps.Keys(features), func(a, b string) int {
		if c := featureIndex(a) - featureIndex(b); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})

	out := make([]Component, 0, len(roleNames)+len(featureNames))
	for _, name := range roleNames {
		info, known := roleInfo(name)
		c := Component{
			Kind:             Role,
			Name:             name,
			Label:            name,
			Group:            Groups[len(Groups)-1],
			Status:           roleStatus(st, name),
			InstalledVersion: st.Roles[name],
			Toggleable:       !infraRoles[name],
		}
		if known {
			c.Label = info.Label
			c.Description = info.Description
			c.Group = info.Group
			c.Actions = info.Actions
		}
		out = append(out, c)
	}
	for _, name := range featureNames {
		info, known := featureInfo(name)
		c := Component{
			Kind:       Feature,
			Name:       name,
			Label:      name,
			Group:      Groups[len(Groups)-1],
			Status:     featureStatus(st, name),
			Toggleable: true,
		}
		if known {
			c.Label = info.Label
			c.Description = info.Description
			c.Parent = info.Parent
			c.Actions = info.Actions
		}
		out = append(out, c)
	}
	return out
}

func stateHasRole(st state.State, name string) bool {
	_, ok := st.Roles[name]
	return ok || slices.Contains(st.RolesExcluded, name)
}

func stateHasFeature(st state.State, name string) bool {
	return slices.Contains(st.Features, name) || slices.Contains(st.FeaturesExcluded, name)
}

func known(st state.State, kind Kind, name string) bool {
	if kind == Role {
		_, inCatalog := roleInfo(name)
		return inCatalog || stateHasRole(st, name)
	}
	_, inCatalog := featureInfo(name)
	return inCatalog || stateHasFeature(st, name)
}

// Set returns a copy of st with name opted in or out. Disabling a role moves
// it from Roles into RolesExcluded; enabling only clears the exclusion,
// except an opt-in role, recorded in Roles with RequestedVersion (install.sh
// would otherwise answer its [y/N] with N).
func Set(st state.State, kind Kind, name string, enabled bool) (state.State, error) {
	if kind != Role && kind != Feature {
		return state.State{}, errorf("unknown component kind %q", kind)
	}
	if kind == Role && infraRoles[name] {
		return state.State{}, errorf("%q is an infrastructure role and cannot be toggled", name)
	}
	if !known(st, kind, name) {
		return state.State{}, errorf("unknown %s %q", kind, name)
	}

	out := st
	out.Roles = maps.Clone(st.Roles)
	out.RolesExcluded = slices.Clone(st.RolesExcluded)
	out.Features = slices.Clone(st.Features)
	out.FeaturesExcluded = slices.Clone(st.FeaturesExcluded)
	out.ReleaseHistory = slices.Clone(st.ReleaseHistory)

	if kind == Role {
		if enabled {
			out.RolesExcluded = without(out.RolesExcluded, name)
			if info, ok := roleInfo(name); ok && info.OptIn {
				if _, present := out.Roles[name]; !present {
					out.Roles[name] = RequestedVersion
				}
			}
		} else {
			delete(out.Roles, name)
			out.RolesExcluded = with(out.RolesExcluded, name)
		}
	} else {
		if enabled {
			out.FeaturesExcluded = without(out.FeaturesExcluded, name)
		} else {
			out.Features = without(out.Features, name)
			out.FeaturesExcluded = with(out.FeaturesExcluded, name)
		}
	}
	return out, nil
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

// FindAction resolves the catalog action actionID of a component — the only
// way an argv is resolved, so a request can never name a command of its own.
func FindAction(kind Kind, name, actionID string) (Action, bool) {
	var actions []Action
	if kind == Role {
		if info, ok := roleInfo(name); ok {
			actions = info.Actions
		}
	} else if info, ok := featureInfo(name); ok {
		actions = info.Actions
	}
	for _, a := range actions {
		if a.ID == actionID {
			return a, true
		}
	}
	return Action{}, false
}

// kindOf resolves a bare CLI name: a role if the catalog or state.json knows
// it as one, a feature otherwise.
func kindOf(st state.State, name string) Kind {
	if _, ok := roleInfo(name); ok || stateHasRole(st, name) {
		return Role
	}
	return Feature
}

// KnownFeature reports whether the catalog lists this feature.
func KnownFeature(name string) bool {
	_, ok := featureInfo(name)
	return ok
}

// LabelOf is the catalog label of a component, its name when unknown.
func LabelOf(kind Kind, name string) string {
	if kind == Role {
		if info, ok := roleInfo(name); ok {
			return info.Label
		}
	} else if info, ok := featureInfo(name); ok {
		return info.Label
	}
	return name
}

// Pending lists what the next `upgrade apply` would install, as
// ["role:mpd", "feature:mympd", …] in catalog order: Default roles the
// release ships (shipped nil = the catalog) plus Default features whose
// parent is installed or pending. Disabling is never pending.
func Pending(st state.State, shipped map[string]string) []string {
	ships := func(name string) bool {
		if shipped == nil {
			_, ok := roleInfo(name)
			return ok
		}
		_, ok := shipped[name]
		return ok
	}
	var pending []string
	pendingRoles := map[string]bool{}
	for _, c := range List(st, nil) {
		switch {
		case c.Kind == Role:
			if c.Toggleable && c.Status == Default && ships(c.Name) {
				pending = append(pending, "role:"+c.Name)
				pendingRoles[c.Name] = true
			}
		case c.Status == Default && c.Parent != "":
			_, parentOn := st.Roles[c.Parent]
			if parentOn || pendingRoles[c.Parent] {
				pending = append(pending, "feature:"+c.Name)
			}
		}
	}
	return pending
}

const ApplyNote = "Enabling installs on the next upgrade; disabling keeps the component " +
	"installed but stops updating it."
