package components

import (
	"cmp"
	"maps"
	"slices"

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
	Label       string // product name the user knows
	Description string // one line, what it does
	Group       string
	Package     string
	OptIn       bool     // install.sh asks [y/N]; see the package comment
	Required    bool     // odio needs it: shown, never offered for disabling
	Archs       []string // dpkg architectures it installs on; empty = all
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

type catalogEntry[I any] struct {
	name string
	info I
}

// Slice order = display order within a group.
var roleCatalog = []catalogEntry[RoleInfo]{
	// The audio server odio does not run is hidden: state.json's audioserver
	// picks one of the two, and neither is a toggle.
	{"pulseaudio", RoleInfo{
		Label:       "PulseAudio",
		Description: "Sound server and network audio sink",
		Group:       "Audio",
		Package:     "pulseaudio",
		Required:    true,
	}},
	{"pipewire", RoleInfo{
		Label:       "PipeWire",
		Description: "Sound server and network audio sink (experimental)",
		Group:       "Audio",
		Package:     "pipewire",
		Required:    true,
	}},
	{"bluetooth", RoleInfo{
		Label:       "Bluetooth",
		Description: "Bluetooth audio, in and out",
		Group:       "Audio",
		Package:     "bluez",
	}},
	{"mpd", RoleInfo{
		Label:       "MPD",
		Description: "Local library, CDs, web radios",
		Group:       "Playback",
		Package:     "mpd",
		Required:    true, // upmpdcli and the disc player play through it
	}},
	{"mpd_discplayer", RoleInfo{
		Label:       "CD player",
		Description: "Audio CD playback",
		Group:       "Playback",
		Package:     "mpd-discplayer",
	}},
	{"shairport_sync", RoleInfo{
		Label:       "AirPlay",
		Description: "AirPlay receiver",
		Group:       "Streaming",
		Package:     "shairport-sync",
	}},
	{"spotifyd", RoleInfo{
		Label:       "Spotify Connect",
		Description: "Spotify Connect receiver",
		Group:       "Streaming",
		Package:     "spotifyd",
	}},
	{"qbzd", RoleInfo{
		Label:       "Qobuz Connect",
		Description: "Qobuz Connect endpoint (experimental)",
		Group:       "Streaming",
		Package:     "qbzd",
		OptIn:       true,
		Actions: []Action{{
			ID:          "login",
			Label:       "Log in to Qobuz",
			Description: "Sign in to Qobuz",
			Argv:        []string{"qbzd", "login", "--callback-host", "{host}"},
			LinkScheme:  "https://",
			LinkLabel:   "Qobuz sign-in page",
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
		Description: "UPnP / OpenHome renderer",
		Group:       "Streaming",
		Package:     "upmpdcli",
	}},
	{"odio_api", RoleInfo{
		Label:       "odio-api",
		Description: "Remote control API and web dashboard",
		Group:       "System",
		Package:     "odio-api",
		Required:    true,
	}},
	{"branding", RoleInfo{
		Label:       "Branding",
		Description: "Login banner",
		Group:       "System",
	}},
	{"common", RoleInfo{
		Label:       "Base system",
		Description: "Core configuration shared by every component",
		Group:       "System",
		Required:    true,
	}},
	{"upgrade", RoleInfo{
		Label:       "Upgrade",
		Description: "odioctl and the upgrade check timer",
		Group:       "System",
		Required:    true,
	}},
}

var featureCatalog = []catalogEntry[FeatureInfo]{
	{"mympd", FeatureInfo{
		Label:       "myMPD",
		Description: "Web UI for MPD",
		Package:     "mympd",
		Parent:      "mpd",
	}},
	{"tidal", FeatureInfo{
		Label:       "Tidal",
		Description: "Tidal streaming",
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
			LinkLabel:  "Tidal sign-in page",
			LinkNote:   "valid 5 minutes",
		}},
	}},
	{"qobuz", FeatureInfo{
		Label:       "Qobuz",
		Description: "Qobuz streaming",
		Package:     "upmpdcli-qobuz",
		Parent:      "upmpdcli",
		Actions: []Action{{
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
	}},
	{"upnpwebradios", FeatureInfo{
		Label:       "Web radios",
		Description: "Internet radios",
		Package:     "upmpdcli-radios",
		Parent:      "upmpdcli",
	}},
}

// lookup finds name in a catalog: its position, len(catalog) when unknown.
func lookup[I any](catalog []catalogEntry[I], name string) (int, I, bool) {
	for i, e := range catalog {
		if e.name == name {
			return i, e.info, true
		}
	}
	var zero I
	return len(catalog), zero, false
}

// catalogOrder sorts names by catalog position, unknown ones last by name.
func catalogOrder[I any](catalog []catalogEntry[I], names map[string]bool) []string {
	index := func(name string) int {
		i, _, _ := lookup(catalog, name)
		return i
	}
	return slices.SortedFunc(maps.Keys(names), func(a, b string) int {
		return cmp.Or(index(a)-index(b), cmp.Compare(a, b))
	})
}

// roleInfo is the local entry overlaid with the target manifest's catalog:
// description, group and opt-in come from the release, label and actions never do.
func roleInfo(man *manifest.Manifest, name string) (RoleInfo, bool) {
	_, info, found := lookup(roleCatalog, name)
	if man == nil {
		return info, found
	}
	meta, ok := man.Catalog[name]
	if !ok {
		return info, found
	}
	info.Description = cmp.Or(meta.Description, info.Description)
	if slices.Contains(Groups, meta.Group) {
		info.Group = meta.Group
	}
	info.OptIn = meta.OptIn
	// A release that predates the field must not unlock what odioctl knows
	// odio needs.
	info.Required = info.Required || meta.Required
	if meta.Archs != nil {
		info.Archs = meta.Archs
	}
	return info, true
}

func featureInfo(name string) (FeatureInfo, bool) {
	_, info, ok := lookup(featureCatalog, name)
	return info, ok
}

// entry is what roles and features share: label and actions, which the
// manifest never overlays.
func entry(kind Kind, name string) (label string, actions []Action, ok bool) {
	if kind == Role {
		info, ok := roleInfo(nil, name)
		return info.Label, info.Actions, ok
	}
	info, ok := featureInfo(name)
	return info.Label, info.Actions, ok
}

// FindAction resolves the catalog action actionID of a component — the only
// way an argv is resolved, so a request can never name a command of its own.
func FindAction(kind Kind, name, actionID string) (Action, bool) {
	_, actions, _ := entry(kind, name)
	for _, a := range actions {
		if a.ID == actionID {
			return a, true
		}
	}
	return Action{}, false
}

// LabelOf is the catalog label of a component, its name when unknown.
func LabelOf(kind Kind, name string) string {
	if label, _, ok := entry(kind, name); ok {
		return label
	}
	return name
}

// KnownFeature reports whether the catalog lists this feature.
func KnownFeature(name string) bool {
	_, ok := featureInfo(name)
	return ok
}
