package components

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/state"
)

func writeState(t *testing.T, st state.State) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Write(path, st); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunListTable(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0", "qbzd": "1"}
	st.Features = []string{"tidal"}
	var out, errb bytes.Buffer
	if rc := RunList(&out, &errb, writeState(t, st), release(), false); rc != 0 {
		t.Fatalf("rc = %d, stderr %s", rc, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"roles:\n",
		"features:\n",
		"  mpd              installed  (2026.5.0) [infra]\n",
		"  snapclient       default   \n",
		"  tidal            installed  ← upmpdcli\n",
		"      action: qbzd login --callback-host {host} — Sign in to Qobuz\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "features:") < strings.Index(got, "snapclient") {
		t.Error("features listed before the roles")
	}
	// qobuz is not installed: its login is not offered.
	if strings.Contains(got, "qobuz-init-oauth") {
		t.Errorf("action of a component not installed:\n%s", got)
	}
}

func TestRunListJSON(t *testing.T) {
	st := makeState()
	st.RolesExcluded = []string{"spotifyd"}
	var out, errb bytes.Buffer
	if rc := RunList(&out, &errb, writeState(t, st), release(), true); rc != 0 {
		t.Fatalf("rc = %d, stderr %s", rc, errb.String())
	}
	var got []componentJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	named := map[string]componentJSON{}
	for _, c := range got {
		named[c.Name] = c
	}
	if c := named["spotifyd"]; c.Enabled || c.Status != Excluded {
		t.Errorf("spotifyd = %+v", c)
	}
	if c := named["qbzd"]; len(c.Actions) != 1 || c.Actions[0].ID != "login" {
		t.Errorf("qbzd actions = %+v", c.Actions)
	}
	if c := named["mpd"]; c.Actions == nil {
		t.Error("no actions must be [], not null")
	}
}

func TestRunListUnreadableState(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "state.json")
	if rc := RunList(&out, &errb, missing, nil, false); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(errb.String(), "Error reading") || out.Len() != 0 {
		t.Errorf("stdout %q, stderr %q", out.String(), errb.String())
	}
}

func TestRunSetWritesState(t *testing.T) {
	path := writeState(t, makeState())
	var out, errb bytes.Buffer
	if rc := RunSet(&out, &errb, path, release(), "tidal", false); rc != 0 {
		t.Fatalf("rc = %d, stderr %s", rc, errb.String())
	}
	if !strings.HasPrefix(out.String(), "feature tidal disabled. ") {
		t.Errorf("stdout = %q", out.String())
	}
	st, err := state.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.FeaturesExcluded[0] != "tidal" {
		t.Errorf("features_excluded = %v", st.FeaturesExcluded)
	}
}

func TestRunSetRefusalLeavesStateAlone(t *testing.T) {
	path := writeState(t, makeState())
	var out, errb bytes.Buffer
	if rc := RunSet(&out, &errb, path, release(), "mpd", false); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(errb.String(), "required by odio") {
		t.Errorf("stderr = %q", errb.String())
	}
	if st, _ := state.Read(path); len(st.RolesExcluded) != 0 {
		t.Errorf("roles_excluded = %v", st.RolesExcluded)
	}
}

func TestRunSetUnreadableState(t *testing.T) {
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "state.json")
	if rc := RunSet(&out, &errb, missing, nil, "tidal", true); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	if !strings.Contains(errb.String(), "Error reading") {
		t.Errorf("stderr = %q", errb.String())
	}
}
