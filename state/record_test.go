package state

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// What write_state.yml pipes in: its `_odios_state` without the history.
const runJSON = `{
    "odios": "2026.10.0b1",
    "install_mode": "live",
    "target_user": "odio",
    "audioserver": "pipewire",
    "roles": {"common": "2026.10.0b1", "pipewire": "2026.10.0b1", "mpd": "2026.9.0b2"},
    "roles_excluded": ["pulseaudio", "qbzd"],
    "features": ["mympd"],
    "features_excluded": ["tidal"]
}`

func record(t *testing.T, path, run string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rc := RunRecord(strings.NewReader(run), &stdout, &stderr, path)
	return rc, stdout.String(), stderr.String()
}

func TestRecordOnAFreshInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if rc, _, stderr := record(t, path, runJSON); rc != 0 {
		t.Fatalf("rc = %d, stderr = %q", rc, stderr)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Odios: "2026.10.0b1", InstallMode: "live", TargetUser: "odio", Audioserver: PipeWire,
		Roles:         map[string]string{"common": "2026.10.0b1", "pipewire": "2026.10.0b1", "mpd": "2026.9.0b2"},
		RolesExcluded: []string{"qbzd"}, // the server not picked was not declined
		Features:      []string{"mympd"}, FeaturesExcluded: []string{"tidal"},
		ReleaseHistory: []string{"2026.10.0b1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != FileMode {
		t.Errorf("mode = %o, want %o", fi.Mode().Perm(), FileMode)
	}
}

func TestRecordKeepsTheHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(validJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	record(t, path, runJSON)
	record(t, path, runJSON) // the same release twice is one entry
	got, _ := Read(path)
	if want := []string{"2026.5.0", "2026.10.0b1"}; !reflect.DeepEqual(got.ReleaseHistory, want) {
		t.Errorf("history = %v, want %v", got.ReleaseHistory, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != FileMode {
		t.Errorf("mode = %o, want %o", fi.Mode().Perm(), FileMode)
	}
}

// The picked server excluded is a refusal of any audio server, kept as such.
func TestRecordKeepsThePickedServerDeclined(t *testing.T) {
	run := strings.Replace(runJSON, `["pulseaudio", "qbzd"]`, `["pipewire", "pulseaudio"]`, 1)
	path := filepath.Join(t.TempDir(), "state.json")
	record(t, path, run)
	got, _ := Read(path)
	if !reflect.DeepEqual(got.RolesExcluded, []string{"pipewire"}) {
		t.Errorf("roles_excluded = %v", got.RolesExcluded)
	}
}

func TestRecordReplacesAStateItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte(`{"odios": "rc3"}`), 0o660)
	rc, _, stderr := record(t, path, runJSON)
	if rc != 0 || !strings.Contains(stderr, "history restarts") {
		t.Fatalf("rc = %d, stderr = %q", rc, stderr)
	}
	if got, err := Read(path); err != nil || len(got.ReleaseHistory) != 1 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestRecordRefusesARunItDoesNotUnderstand(t *testing.T) {
	for name, tc := range map[string]struct{ run, says string }{
		"not an object":   {`[]`, "JSON object"},
		"missing field":   {strings.Replace(runJSON, `"audioserver": "pipewire",`, "", 1), "audioserver"},
		"null list":       {strings.Replace(runJSON, `["mympd"]`, `null`, 1), "features"},
		"unknown field":   {strings.Replace(runJSON, `"odios"`, `"network": {}, "odios"`, 1), "network"},
		"history given":   {strings.Replace(runJSON, `"odios"`, `"release_history": [], "odios"`, 1), "release_history"},
		"bad audioserver": {strings.Replace(runJSON, `"pipewire",`, `"alsa",`, 1), "alsa"},
		"empty user":      {strings.Replace(runJSON, `"target_user": "odio"`, `"target_user": ""`, 1), "target_user"},
		"wrong shape":     {strings.Replace(runJSON, `"mpd": "2026.9.0b2"`, `"mpd": 1`, 1), "roles"},
	} {
		path := filepath.Join(t.TempDir(), "state.json")
		rc, _, stderr := record(t, path, tc.run)
		if rc != 2 || !strings.Contains(stderr, tc.says) {
			t.Errorf("%s: rc = %d, stderr = %q", name, rc, stderr)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s: state.json written", name)
		}
	}
}

// read_state.yml's view: what an earlier odios left out is filled in.
func TestShowNormalizesAnEarlierState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte(strings.Replace(validJSON, `"audioserver": "pipewire",`, "", 1)), 0o660)
	var stdout, stderr bytes.Buffer
	if rc := RunShow(&stdout, &stderr, path); rc != 0 {
		t.Fatalf("rc = %d, stderr = %q", rc, stderr.String())
	}
	st, err := Parse(stdout.Bytes())
	if err != nil || st.Audioserver != PulseAudio || !strings.Contains(stdout.String(), `"audioserver": "pulseaudio"`) {
		t.Errorf("got %q, %v", stdout.String(), err)
	}
}

func TestShowExitCodes(t *testing.T) {
	dir := t.TempDir()
	refused := filepath.Join(dir, "refused.json")
	os.WriteFile(refused, []byte(`{"odios": "rc3"}`), 0o660)
	for path, want := range map[string]int{refused: 1, filepath.Join(dir, "none.json"): 3} {
		var stdout, stderr bytes.Buffer
		if rc := RunShow(&stdout, &stderr, path); rc != want || stdout.Len() != 0 {
			t.Errorf("%s: rc = %d, want %d, stdout = %q", filepath.Base(path), rc, want, stdout.String())
		}
	}
}

func TestRecordFailsWhenStateCannotBeRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state.json") // a directory: reading it fails
	os.Mkdir(dir, 0o700)
	if rc, _, _ := record(t, dir, runJSON); rc != 1 {
		t.Errorf("rc = %d", rc)
	}
}
