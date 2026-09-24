package components

import (
	"maps"
	"slices"

	"github.com/b0bbywan/odioctl/manifest"
)

// release is the target manifest the tests run against: the catalog as odios
// publishes it, pipewire included, every role shipped at its version.
func release() *manifest.Manifest {
	catalog := map[string]manifest.RoleMeta{
		"pulseaudio": {Label: "PulseAudio", Description: "Sound server, with network streaming", Group: "Audio",
			Required: true},
		"pipewire": {Label: "PipeWire", Description: "Sound server, with network streaming (experimental)",
			Group: "Audio", OptIn: true, Required: true},
		"bluetooth": {Label: "Bluetooth", Description: "Bluetooth audio, in and out", Group: "Audio"},
		"mpd": {Label: "MPD", Description: "Music library on USB drives, CDs and network shares", Group: "Playback",
			Required: true, Features: map[string]manifest.FeatureMeta{
				"mympd": {Label: "myMPD", Description: "Web player for your music library and web radios"},
			}},
		"mpd_discplayer": {Label: "CD & USB player", Description: "Plays a CD or USB drive as soon as it is inserted",
			Group: "Playback"},
		"shairport_sync": {Label: "AirPlay", Description: "Play from Apple devices", Group: "Streaming"},
		"spotifyd":       {Label: "Spotify Connect", Description: "Play from the Spotify app", Group: "Streaming"},
		"qbzd": {Label: "Qobuz Connect", Description: "Play from the Qobuz app (experimental)", Group: "Streaming",
			OptIn: true},
		"snapclient": {Label: "Snapcast", Description: "Synchronized multi-room playback", Group: "Streaming"},
		"upmpdcli": {Label: "UPnP / DLNA", Description: "Play from UPnP and OpenHome control apps", Group: "Streaming",
			Features: map[string]manifest.FeatureMeta{
				"tidal":         {Label: "Tidal", Description: "Tidal in UPnP control apps"},
				"qobuz":         {Label: "Qobuz", Description: "Qobuz in UPnP control apps"},
				"upnpwebradios": {Label: "Web radios", Description: "Web radios in UPnP control apps"},
			}},
		"odio_api": {Label: "odio-api", Description: "Remote control API and web dashboard", Group: "System",
			Required: true},
		"branding": {Label: "Branding", Description: "odio login banner", Group: "System"},
		"common":   {Label: "Base system", Description: "Core system configuration", Group: "System", Required: true},
		"upgrade":  {Label: "Upgrade", Description: "Keeps odio up to date", Group: "System", Required: true},
	}
	for name, meta := range catalog {
		meta.Version = "2026.9.0"
		catalog[name] = meta
	}
	// no roles: the catalog's versions say what the release ships
	return &manifest.Manifest{Odios: "2026.9.0", Catalog: catalog}
}

// shipping is release with only these roles in it.
func shipping(names ...string) *manifest.Manifest {
	m := release()
	maps.DeleteFunc(m.Roles, func(n, _ string) bool { return !slices.Contains(names, n) })
	maps.DeleteFunc(m.Catalog, func(n string, _ manifest.RoleMeta) bool { return !slices.Contains(names, n) })
	return m
}
