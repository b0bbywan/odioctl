package components

import (
	"slices"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

// heldBack is a release that ships pipewire but keeps it out of its catalog,
// as odios does until odioctl offers the choice.
func heldBack() *manifest.Manifest {
	m := release()
	delete(m.Catalog, state.PipeWire)
	return m
}

func optionNames(a Audioserver) []string {
	var out []string
	for _, o := range a.Options {
		out = append(out, o.Name)
	}
	return out
}

func onPulseAudio() state.State {
	st := makeState()
	st.Audioserver = state.PulseAudio
	st.Roles = map[string]string{"pulseaudio": "1", "mpd": "1"}
	return st
}

// Neither server is a role row, nor a toggle: the choice is their only door.
func TestAudioserversAreNotRoles(t *testing.T) {
	st := onPulseAudio()
	n := names(List(st, release()))
	if n[[2]string{"role", "pulseaudio"}] || n[[2]string{"role", "pipewire"}] {
		t.Errorf("List = %v", n)
	}
	for _, enabled := range []bool{true, false} {
		for _, s := range Audioservers {
			_, err := Set(st, release(), Role, s, enabled)
			wantComponentError(t, err)
		}
	}
}

func TestPipeWireIsOfferedOnceTheCatalogListsIt(t *testing.T) {
	st := onPulseAudio()
	if got := optionNames(AudioserverOf(st, heldBack())); !slices.Equal(got, []string{"pulseaudio"}) {
		t.Errorf("held back: options = %v", got)
	}
	_, err := SetAudioserver(st, heldBack(), state.PipeWire)
	wantComponentError(t, err)

	a := AudioserverOf(st, release())
	if got := optionNames(a); !slices.Equal(got, []string{"pulseaudio", "pipewire"}) {
		t.Errorf("options = %v", got)
	}
	if a.Picked != "pulseaudio" || a.Installed != "pulseaudio" || a.Switching() {
		t.Errorf("audioserver = %+v", a)
	}
	if o := a.Options[1]; o.Label != "PipeWire" || o.Description == "" {
		t.Errorf("pipewire option = %+v", o)
	}
}

// Picking the other server is a switch: pending until apply runs every role.
func TestSwitchingIsPending(t *testing.T) {
	st, err := SetAudioserver(onPulseAudio(), release(), state.PipeWire)
	if err != nil {
		t.Fatal(err)
	}
	if st.Audioserver != state.PipeWire || st.Roles["pulseaudio"] != "1" {
		t.Errorf("state = %+v", st)
	}
	if a := AudioserverOf(st, release()); !a.Switching() {
		t.Errorf("audioserver = %+v", a)
	}
	if p := Pending(st, release()); len(p) == 0 || p[0] != "role:pipewire" {
		t.Errorf("Pending = %v", p)
	}
	back, err := SetAudioserver(st, release(), state.PulseAudio)
	if err != nil {
		t.Fatal(err)
	}
	if p := Pending(back, release()); slices.Contains(p, "role:pulseaudio") || slices.Contains(p, "role:pipewire") {
		t.Errorf("Pending = %v, picking the installed one back is no switch", p)
	}
}

// An odio already on PipeWire keeps it offered, and can always go back.
func TestAnOdioOnPipeWireCanGoBack(t *testing.T) {
	st := makeState()
	st.Audioserver = state.PipeWire
	st.Roles = map[string]string{"pipewire": "1"}
	if got := optionNames(AudioserverOf(st, heldBack())); !slices.Equal(got, []string{"pulseaudio", "pipewire"}) {
		t.Errorf("options = %v", got)
	}
	back, err := SetAudioserver(st, heldBack(), state.PulseAudio)
	if err != nil {
		t.Fatal(err)
	}
	if p := Pending(back, heldBack()); len(p) == 0 || p[0] != "role:pulseaudio" {
		t.Errorf("Pending = %v", p)
	}
}

// Both installed (a switch half done): the picked one is the one running.
func TestBothInstalledIsNoSwitch(t *testing.T) {
	st := onPulseAudio()
	st.Roles["pipewire"] = "1"
	for _, picked := range Audioservers {
		st.Audioserver = picked
		if a := AudioserverOf(st, release()); a.Installed != picked || a.Switching() {
			t.Errorf("picked %s: %+v", picked, a)
		}
	}
}

func TestSetAudioserverRefusals(t *testing.T) {
	st := onPulseAudio()
	_, err := SetAudioserver(st, nil, state.PulseAudio)
	wantComponentError(t, err)
	_, err = SetAudioserver(st, release(), "jack")
	wantComponentError(t, err)
}

// An earlier odios wrote no audioserver: PulseAudio, the only one there was.
func TestNoAudioserverIsPulseAudio(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"pulseaudio": "1"}
	if a := AudioserverOf(st, release()); a.Picked != "pulseaudio" || a.Switching() {
		t.Errorf("audioserver = %+v", a)
	}
}
