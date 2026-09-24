package components

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/b0bbywan/odioctl/manifest"
)

// Action is a one-off command odio runs for the user. Argv is fixed here,
// never built from the request; the server only fills in {host} (the address
// the browser reached odio by) and {home} (the target user's home).
type Action struct {
	ID          string // form value, unique per component
	Label       string // button text
	Description string // one line: what the command does
	Argv        []string
	LinkScheme  string // the stdout token to surface as a link
	LinkSkip    string // a link containing this is not the one
	LinkLabel   string // anchor text for that token
	LinkNote    string // what to know about the link
	// the command prints the link and exits: the link stays on the row
	LinkOutlivesRun bool
}

type RoleInfo struct {
	Label       string // the name the user knows it by
	Description string // one line, what it does
	Group       string
	OptIn       bool     // install.sh asks [y/N]; see the package comment
	Required    bool     // odio needs it: shown, never offered for disabling
	Archs       []string // dpkg architectures it installs on; empty = all
	Actions     []Action
}

type FeatureInfo struct {
	Label       string
	Description string
	Parent      string
	Actions     []Action
}

// Groups is the display order of the web UI / `components list`; unknown
// roles go to the last group.
var Groups = []string{"Audio", "Playback", "Streaming", "System"}

// The actions are odioctl's own: the release's catalog describes a component,
// an argv never comes from a downloaded file.
var roleActions = map[string][]Action{
	"qbzd": {{
		ID:          "login",
		Label:       "Log in to Qobuz",
		Description: "Sign in to Qobuz",
		Argv:        []string{"qbzd", "login", "--callback-host", "{host}"},
		LinkScheme:  "https://",
		LinkLabel:   "Qobuz sign-in page",
		LinkNote:    "valid 5 minutes",
	}},
}

var featureActions = map[string][]Action{
	"tidal": {{
		ID:          "login",
		Label:       "Log in to Tidal",
		Description: "Sign in to Tidal",
		Argv: []string{
			"python3", "-u",
			"/usr/share/upmpdcli/cdplugins/tidal/get_credentials.py",
			"-f", "{home}/.cache/upmpdcli/tidal/oauth2.credentials.json",
		},
		LinkScheme: "https://",
		LinkLabel:  "Tidal sign-in page",
		LinkNote:   "valid 5 minutes",
	}},
	"qobuz": {{
		ID:          "login",
		Label:       "Log in to Qobuz",
		Description: "Sign in to Qobuz",
		Argv: []string{
			"python3", "-u",
			"/usr/share/upmpdcli/cdplugins/qobuz/qobuz-init-oauth.py",
		},
		LinkScheme:      "https://",
		LinkSkip:        "localhost", // it prints that one for a local browser
		LinkLabel:       "Qobuz sign-in page",
		LinkOutlivesRun: true, // upmpdcli answers the redirect, not the script
	}},
}

// roleInfo is the role as the target release's catalog describes it; false
// when there is no release (nil) or it does not list the role.
func roleInfo(man *manifest.Manifest, name string) (RoleInfo, bool) {
	if man == nil {
		return RoleInfo{}, false
	}
	meta, ok := man.Catalog[name]
	if !ok {
		return RoleInfo{}, false
	}
	info := RoleInfo{
		Label:       cmp.Or(meta.Label, nameLabel(name)),
		Description: meta.Description,
		Group:       Groups[len(Groups)-1],
		OptIn:       meta.OptIn,
		Required:    meta.Required,
		Archs:       meta.Archs,
		Actions:     roleActions[name],
	}
	if slices.Contains(Groups, meta.Group) {
		info.Group = meta.Group
	}
	return info, true
}

// featureInfo finds a feature under the catalog role that publishes it.
func featureInfo(man *manifest.Manifest, name string) (FeatureInfo, bool) {
	if man == nil {
		return FeatureInfo{}, false
	}
	for _, role := range slices.Sorted(maps.Keys(man.Catalog)) {
		if meta, ok := man.Catalog[role].Features[name]; ok {
			return FeatureInfo{Label: cmp.Or(meta.Label, nameLabel(name)), Description: meta.Description,
				Parent: role, Actions: featureActions[name]}, true
		}
	}
	return FeatureInfo{}, false
}

func actionsOf(kind Kind, name string) []Action {
	if kind == Role {
		return roleActions[name]
	}
	return featureActions[name]
}

// FindAction resolves the action actionID of a component — the only way an
// argv is resolved, so a request can never name a command of its own.
func FindAction(kind Kind, name, actionID string) (Action, bool) {
	for _, a := range actionsOf(kind, name) {
		if a.ID == actionID {
			return a, true
		}
	}
	return Action{}, false
}

// nameLabel is the label of what the catalog does not name: shairport_sync
// reads Shairport-sync.
func nameLabel(name string) string {
	label := strings.ReplaceAll(name, "_", "-")
	if label == "" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

// LabelOf names a component to the user: its catalog label, else nameLabel.
func LabelOf(man *manifest.Manifest, kind Kind, name string) string {
	if kind == Role {
		if info, ok := roleInfo(man, name); ok {
			return info.Label
		}
	} else if info, ok := featureInfo(man, name); ok {
		return info.Label
	}
	return nameLabel(name)
}
