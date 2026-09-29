package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/state"
)

// swapRelease points ReleaseDir at a temp dir, with disable.yml or without.
func swapRelease(t *testing.T, withPlaybook bool) string {
	t.Helper()
	old := ReleaseDir
	ReleaseDir = t.TempDir()
	t.Cleanup(func() { ReleaseDir = old })
	if withPlaybook {
		addPlaybook(t)
	}
	return ReleaseDir
}

func addPlaybook(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(disablePlaybook()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disablePlaybook(), []byte("---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

type disableRun struct {
	Vars map[string]any
	Env  map[string]string
}

// recordRuns swaps runDisable and runInstall for recorders of what ran, in
// order; each returns rc.
func recordRuns(t *testing.T, disableRC, installRC int) (*[]string, *[]disableRun) {
	t.Helper()
	var order []string
	var runs []disableRun
	oldDisable, oldInstall := runDisable, runInstall
	runDisable = func(vars []byte, env map[string]string) int {
		var v map[string]any
		if err := json.Unmarshal(vars, &v); err != nil {
			t.Errorf("vars = %s: %v", vars, err)
		}
		order = append(order, "disable")
		runs = append(runs, disableRun{v, env})
		return disableRC
	}
	runInstall = func(url string, env map[string]string) int {
		order = append(order, "install")
		return installRC
	}
	t.Cleanup(func() { runDisable, runInstall = oldDisable, oldInstall })
	return &order, &runs
}

// removingState has spotifyd and mympd on their way out, every role at 2026.5.0.
func removingState(t *testing.T, d string) {
	t.Helper()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0", "spotifyd": "2026.5.0", "upmpdcli": "2026.5.0"}
	st.RolesExcluded = []string{"spotifyd"}
	st.Features = []string{"mympd", "tidal"}
	st.FeaturesExcluded = []string{"mympd"}
	writeState(t, d, st)
}

// checkFor writes the upgrades.json `check` would, against a release at latest.
func checkFor(t *testing.T, d, latest string) {
	t.Helper()
	m := man(latest, map[string]string{"mpd": latest, "spotifyd": latest, "upmpdcli": latest})
	swapFetch(t, fetchOf(m))
	if rc := RunCheck(&strings.Builder{}, &strings.Builder{}, CheckOptions{
		State: filepath.Join(d, "state.json"), Version: latest,
	}); rc != 1 {
		t.Fatalf("check rc = %d, want 1", rc)
	}
}

func readState(t *testing.T, d string) state.State {
	t.Helper()
	st, err := state.Read(filepath.Join(d, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestARemovalAloneRunsDisableAndNotInstall(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.5.0")
	swapRelease(t, true)
	order, runs := recordRuns(t, 0, 0)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !slices.Equal(*order, []string{"disable"}) {
		t.Fatalf("rc = %d, ran %v\n%s", rc, *order, text)
	}
	want := map[string]any{
		"target_user":            "odio",
		"odios_disable_roles":    []any{"spotifyd"},
		"odios_disable_features": map[string]any{"mympd": "mpd"},
	}
	if got := (*runs)[0].Vars; !reflect.DeepEqual(got, want) {
		t.Errorf("vars = %v, want %v", got, want)
	}
	for _, s := range []string{"Disabling role:spotifyd, feature:mympd via " + disablePlaybook(),
		"Nothing else to upgrade."} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in:\n%s", s, text)
		}
	}
	st := readState(t, d)
	if _, ok := st.Roles["spotifyd"]; ok || !slices.Equal(st.Features, []string{"tidal"}) ||
		!slices.Equal(st.RolesExcluded, []string{"spotifyd"}) ||
		!slices.Equal(st.FeaturesExcluded, []string{"mympd"}) {
		t.Errorf("state = %+v", st)
	}
	if r := ReadReport(filepath.Join(d, "upgrades.json")); r == nil || r.UpgradeAvailable ||
		len(r.PendingRemovals) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestTheInstalledReleaseDisablesBeforeTheUpgrade(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.6.0")
	swapRelease(t, true)
	order, _ := recordRuns(t, 0, 0)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !slices.Equal(*order, []string{"disable", "install"}) {
		t.Fatalf("rc = %d, ran %v\n%s", rc, *order, text)
	}
	if !strings.Contains(text, "INSTALL_SPOTIFYD=N") || strings.Contains(text, "Then disabling") {
		t.Errorf("out:\n%s", text)
	}
}

// A failed disable leaves the removal pending, and nothing else runs.
func TestAFailedDisableStopsTheApply(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.6.0")
	swapRelease(t, true)
	order, _ := recordRuns(t, 4, 0)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 4 || !slices.Equal(*order, []string{"disable"}) ||
		!strings.Contains(text, "disable.yml failed (exit 4): role:spotifyd, feature:mympd stay pending.") {
		t.Fatalf("rc = %d, ran %v\n%s", rc, *order, text)
	}
	if st := readState(t, d); st.Roles["spotifyd"] == "" {
		t.Errorf("state = %+v", st)
	}
}

// A release kept before disable.yml existed: the one install.sh brings disables.
func TestWithoutDisableTheUpgradeGoesFirst(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.5.0")
	swapRelease(t, false)
	order, runs := recordRuns(t, 0, 0)
	old := runInstall
	runInstall = func(url string, env map[string]string) int {
		addPlaybook(t)
		// install.sh's state record: INSTALL_SPOTIFYD=N took it out of roles
		st := readState(t, d)
		delete(st.Roles, "spotifyd")
		writeState(t, d, st)
		return old(url, env)
	}
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !slices.Equal(*order, []string{"install", "disable"}) {
		t.Fatalf("rc = %d, ran %v\n%s", rc, *order, text)
	}
	if !strings.Contains(text, "Then disabling role:spotifyd, feature:mympd from the release installed") {
		t.Errorf("out:\n%s", text)
	}
	if got := (*runs)[0].Vars["odios_disable_roles"]; !reflect.DeepEqual(got, []any{"spotifyd"}) {
		t.Errorf("roles = %v, gathered before install.sh", got)
	}
	if st := readState(t, d); slices.Contains(st.Features, "mympd") {
		t.Errorf("state = %+v", st)
	}
}

func TestAReleaseWithoutDisableLeavesItRunning(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.5.0")
	swapRelease(t, false)
	order, _ := recordRuns(t, 0, 0)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !slices.Equal(*order, []string{"install"}) ||
		!strings.Contains(text, "The release installed has no disable.yml: role:spotifyd, feature:mympd left running.") {
		t.Errorf("rc = %d, ran %v\n%s", rc, *order, text)
	}
}

func TestDryRunShowsTheRemovalsAndRunsNothing(t *testing.T) {
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.5.0")
	swapRelease(t, true)
	order, _ := recordRuns(t, 0, 0)
	rc, text := runApply(t, d, ApplyOptions{DryRun: true})
	if rc != 0 || len(*order) != 0 {
		t.Fatalf("rc = %d, ran %v", rc, *order)
	}
	for _, s := range []string{`vars passed to disable.yml: {"target_user":"odio"`, "(dry-run, not invoking)"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in:\n%s", s, text)
		}
	}
	if st := readState(t, d); st.Roles["spotifyd"] == "" {
		t.Errorf("state = %+v", st)
	}
}

func TestDisableProgressNamesTheCallbackAndTheTargetsRuntime(t *testing.T) {
	old := lookupUID
	lookupUID = func(string) (string, error) { return "1001", nil }
	t.Cleanup(func() { lookupUID = old })
	d := t.TempDir()
	removingState(t, d)
	checkFor(t, d, "2026.5.0")
	dir := swapRelease(t, true)
	_, runs := recordRuns(t, 0, 0)
	if rc, text := runApply(t, d, ApplyOptions{Progress: true}); rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, text)
	}
	want := map[string]string{
		"ANSIBLE_CALLBACK_PLUGINS":  filepath.Join(dir, "ansible", "callback_plugins"),
		"ANSIBLE_CALLBACKS_ENABLED": "odio_progress",
		"XDG_RUNTIME_DIR":           "/run/user/1001",
	}
	if got := (*runs)[0].Env; !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v", got)
	}
}

// The names end up in a path odios reads as root.
func TestDisableVarsRefuseWhatIsNotAName(t *testing.T) {
	for _, r := range []components.Removals{
		{Roles: []string{"../../etc"}},
		{Features: map[string]string{"tidal": "up/mpdcli"}},
		{Features: map[string]string{"Tidal": "upmpdcli"}},
	} {
		if _, err := disableVars("odio", r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	b, err := disableVars("odio", components.Removals{})
	if err != nil || string(b) != `{"target_user":"odio","odios_disable_roles":[],"odios_disable_features":{}}` {
		t.Errorf("vars = %s, %v", b, err)
	}
}
