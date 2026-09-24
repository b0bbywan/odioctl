package components

import (
	"cmp"
	"slices"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

// Audioservers are the sound servers odio runs one of: state.json's
// audioserver picks it, so they are a choice, never two roles to toggle.
var Audioservers = []string{state.PulseAudio, state.PipeWire}

func isAudioserver(name string) bool { return slices.Contains(Audioservers, name) }

type AudioserverOption struct {
	Name, Label, Description string
}

type Audioserver struct {
	Picked    string // state.json's audioserver
	Installed string // the server whose role is installed, "" = none
	Options   []AudioserverOption
}

// Switching: another server than the picked one is installed, and the next
// apply replaces it.
func (a Audioserver) Switching() bool {
	return a.Installed != "" && a.Installed != a.Picked
}

// AudioserverOf is the choice as the release offers it: PulseAudio always,
// another server once the catalog lists it (odios holds pipewire back with
// pipewire_catalog: false), and whichever is picked or installed here.
func AudioserverOf(st state.State, man *manifest.Manifest) Audioserver {
	// an earlier odios wrote none: pulseaudio, the only server there was
	a := Audioserver{Picked: cmp.Or(st.Audioserver, state.PulseAudio)}
	for _, s := range Audioservers {
		if st.Roles[s] != "" && a.Installed != a.Picked {
			a.Installed = s
		}
	}
	for _, s := range Audioservers {
		info, listed := roleInfo(man, s)
		if s != state.PulseAudio && s != a.Picked && s != a.Installed && (!listed || !info.supported()) {
			continue
		}
		a.Options = append(a.Options, AudioserverOption{Name: s, Label: LabelOf(man, Role, s), Description: info.Description})
	}
	return a
}

// SetAudioserver returns a copy of st with name picked. Nothing is installed
// until apply, which then runs every role: they all follow the server.
func SetAudioserver(st state.State, man *manifest.Manifest, name string) (state.State, error) {
	if man == nil {
		return state.State{}, errorf("no release catalog yet: run `odioctl upgrade check` first")
	}
	if !isAudioserver(name) {
		return state.State{}, errorf("unknown audio server %q (one of %v)", name, Audioservers)
	}
	if !slices.ContainsFunc(AudioserverOf(st, man).Options, func(o AudioserverOption) bool { return o.Name == name }) {
		return state.State{}, errorf("%s is not offered by this release", LabelOf(man, Role, name))
	}
	out := st.Clone()
	out.Audioserver = name
	return out, nil
}
