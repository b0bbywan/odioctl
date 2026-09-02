package upgrade

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
)

func TestComputeRoleUpgrades(t *testing.T) {
	tests := []struct {
		name      string
		installed map[string]string
		roles     map[string]string
		want      []string
	}{
		{"newer is listed", map[string]string{"mpd": "2026.4.0"}, map[string]string{"mpd": "2026.5.0"}, []string{"mpd"}},
		{"unchanged is excluded", map[string]string{"mpd": "2026.5.0"}, map[string]string{"mpd": "2026.5.0"}, nil},
		// Downgrade is not an "upgrade" — keep it out of the report.
		{"older is excluded", map[string]string{"mpd": "2026.5.0"}, map[string]string{"mpd": "2026.4.0"}, nil},
		{"missing from manifest is excluded", map[string]string{"snapclient": "0.27.0"}, map[string]string{}, nil},
		// RequestedVersion: enabled in the UI, never installed — pending, not a role upgrade.
		{"awaiting first install is excluded", map[string]string{"qbzd": ""}, map[string]string{"qbzd": "2026.9.0b1"}, nil},
		{"sorted alphabetically", map[string]string{"zzz": "2026.4.0", "aaa": "2026.4.0"},
			map[string]string{"zzz": "2026.5.0", "aaa": "2026.5.0"}, []string{"aaa", "zzz"}},
	}
	for _, tc := range tests {
		st := makeState()
		st.Roles = tc.installed
		got := computeRoleUpgrades(st, man("2026.5.0", tc.roles))
		var names []string
		for _, u := range got {
			names = append(names, u.Name)
		}
		if !reflect.DeepEqual(names, tc.want) {
			t.Errorf("%s: names = %v, want %v", tc.name, names, tc.want)
		}
	}
}

func TestRoleUpgradeKeepsTheMotdShape(t *testing.T) {
	// odio-motd reads {name, installed, available} by name and breaks
	// silently if the field set drifts.
	b, _ := json.Marshal(RoleUpgrade{Name: "mpd", Installed: "1", Available: "2"})
	if string(b) != `{"name":"mpd","installed":"1","available":"2"}` {
		t.Errorf("shape drifted: %s", b)
	}
}

func TestUpgradeAvailableWhenARoleIsBumped(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.4.0"}
	r := buildReport(st, man("2026.5.0", map[string]string{"mpd": "2026.5.0"}), "")
	if !r.UpgradeAvailable || r.Current != "2026.5.0" || r.Latest != "2026.5.0" || len(r.Roles) != 1 {
		t.Errorf("report = %+v", r)
	}
}

func TestUpgradeAvailableWhenOnlyOdiosIsBumped(t *testing.T) {
	// Installer-only releases must still surface — that's the OR.
	st := makeState()
	st.Odios = "2026.4.0"
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	r := buildReport(st, man("2026.5.0", map[string]string{"mpd": "2026.5.0"}), "")
	if !r.UpgradeAvailable || len(r.Roles) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestUpToDate(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	r := buildReport(st, man("2026.5.0", map[string]string{"mpd": "2026.5.0"}), "")
	if r.UpgradeAvailable || len(r.Roles) != 0 || len(r.PendingComponents) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestPendingComponentAloneMakesUpgradeAvailable(t *testing.T) {
	// mympd not installed, not excluded, parent mpd installed.
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	r := buildReport(st, man("2026.5.0", map[string]string{"mpd": "2026.5.0"}), "")
	if !r.UpgradeAvailable || !reflect.DeepEqual(r.PendingComponents, []string{"feature:mympd"}) ||
		len(r.Roles) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestPendingRolesLimitedToWhatTheManifestShips(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	m := man("2026.5.0", map[string]string{"mpd": "2026.5.0", "spotifyd": "1"})
	if r := buildReport(st, m, ""); !reflect.DeepEqual(r.PendingComponents, []string{"role:spotifyd"}) {
		t.Errorf("pending = %v", r.PendingComponents)
	}
	st.RolesExcluded = []string{"spotifyd"}
	if r := buildReport(st, m, ""); len(r.PendingComponents) != 0 {
		t.Errorf("pending = %v", r.PendingComponents)
	}
}

func TestOptInRoleOnlyGoesPendingOnceEnabled(t *testing.T) {
	m := man("2026.9.0b1", map[string]string{"mpd": "2026.9.0b1", "qbzd": "2026.9.0b1"})
	off := makeState()
	off.Odios = "2026.9.0b1"
	off.Roles = map[string]string{"mpd": "2026.9.0b1"}
	off.Features = []string{"mympd"}
	if buildReport(off, m, "").UpgradeAvailable {
		t.Error("nothing enabled: no upgrade")
	}
	enabled := off
	enabled.Roles = map[string]string{"mpd": "2026.9.0b1", "qbzd": ""}
	r := buildReport(enabled, m, "")
	if !r.UpgradeAvailable || !reflect.DeepEqual(r.PendingComponents, []string{"role:qbzd"}) ||
		len(r.Roles) != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestFullManifestIsCachedAlongsideDelta(t *testing.T) {
	st := makeState()
	st.Odios = "2026.4.0"
	st.Roles = map[string]string{"mpd": "2026.4.0"}
	m := man("2026.5.0", map[string]string{"mpd": "2026.5.0", "spotifyd": "0.4.4"})
	r := buildReport(st, m, "")
	if !reflect.DeepEqual(r.Manifest, m) {
		t.Errorf("manifest = %+v", r.Manifest)
	}
	if len(r.Roles) != 1 || r.Roles[0].Name != "mpd" {
		t.Errorf("delta = %+v", r.Roles)
	}
}

// What `pr-84` publishes: it names itself by version, never by its tag.
var prereleaseMan = man("2026.7.0rc2-9-gcad916c", map[string]string{"qbzd": "2026.9.0b1"})

func TestTagDefaultsToTheVersionForANormalRelease(t *testing.T) {
	st := makeState()
	st.Features = []string{"mympd"}
	r := buildReport(st, man("2026.5.0", map[string]string{"mpd": "2026.5.0"}), "")
	if r.TargetTag != "2026.5.0" {
		t.Errorf("target_tag = %q", r.TargetTag)
	}
}

func TestRequestedTagIsRecorded(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	statePath := writeState(t, d, st)
	urls := swapFetch(t, fetchOf(prereleaseMan))
	r := Refresh(CheckOptions{State: statePath, Version: "pr-84", Output: filepath.Join(d, "upgrades.json")})
	if r == nil || r.TargetTag != "pr-84" || r.Latest != "2026.7.0rc2-9-gcad916c" {
		t.Fatalf("report = %+v", r)
	}
	want, _ := manifest.ManifestURL("pr-84")
	if !reflect.DeepEqual(*urls, []string{want}) {
		t.Errorf("fetched %v, want [%s]", *urls, want)
	}
}

func TestEnvSelectsTheReleaseForTheDailyCheck(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	statePath := writeState(t, d, st)
	out := filepath.Join(d, "upgrades.json")
	t.Setenv(manifest.OdiosVersionEnv, "pr-84")
	swapFetch(t, fetchOf(prereleaseMan))
	var stdout bytes.Buffer
	RunCheck(&stdout, os.Stderr, CheckOptions{State: statePath, Output: out})
	r := ReadReport(out)
	if r == nil || r.TargetTag != "pr-84" {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(stdout.String(), "Comparing against release pr-84") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestOfflineRefreshKeepsTheReleaseItCached(t *testing.T) {
	// Losing the network must not silently move the box back onto the
	// published latest: the cached manifest and its tag go together.
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	statePath := writeState(t, d, st)
	out := filepath.Join(d, "upgrades.json")
	swapFetch(t, fetchOf(prereleaseMan))
	Refresh(CheckOptions{State: statePath, Version: "pr-84", Output: out})
	swapFetch(t, fetchDown)
	second := Refresh(CheckOptions{State: statePath, Output: out})
	if second == nil || second.TargetTag != "pr-84" {
		t.Errorf("report = %+v", second)
	}
}

func TestUnusableVersionIsRefused(t *testing.T) {
	d := t.TempDir()
	statePath := writeState(t, d, makeState())
	var stderr bytes.Buffer
	rc := RunCheck(os.Stdout, &stderr,
		CheckOptions{State: statePath, Version: "../../evil/repo", Output: filepath.Join(d, "upgrades.json")})
	if rc != 2 || !strings.Contains(stderr.String(), "not a release tag") {
		t.Errorf("rc = %d, stderr = %q", rc, stderr.String())
	}
}

func TestReportWithoutATagReadsAsItsOwnVersion(t *testing.T) {
	out := filepath.Join(t.TempDir(), "upgrades.json")
	payload, _ := json.Marshal(map[string]any{"latest": "2026.5.0", "manifest": prereleaseMan})
	os.WriteFile(out, payload, 0o644)
	r := ReadReport(out)
	if r == nil || r.TargetTag != "2026.5.0" {
		t.Errorf("report = %+v", r)
	}
}

func TestOfflineRefreshReusesCachedManifest(t *testing.T) {
	d := t.TempDir()
	m := man("2026.5.0", map[string]string{"mpd": "2026.5.0"})
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	statePath := writeState(t, d, st)
	out := filepath.Join(d, "upgrades.json")
	opts := CheckOptions{State: statePath, Output: out}
	swapFetch(t, fetchOf(m))
	first := Refresh(opts)
	if first == nil || first.UpgradeAvailable {
		t.Fatalf("first = %+v", first)
	}
	// user removes mympd from features; network gone
	st.Features = []string{}
	writeState(t, d, st)
	swapFetch(t, fetchDown)
	second := Refresh(opts)
	if second == nil || !second.UpgradeAvailable ||
		!reflect.DeepEqual(second.PendingComponents, []string{"feature:mympd"}) ||
		!reflect.DeepEqual(second.Manifest, m) {
		t.Fatalf("second = %+v", second)
	}
	if got := ReadReport(out); !reflect.DeepEqual(got, second) {
		t.Errorf("read back = %+v", got)
	}
}

func TestOfflineWithoutCacheWritesNothing(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	statePath := writeState(t, d, st)
	out := filepath.Join(d, "upgrades.json")
	swapFetch(t, fetchDown)
	if r := Refresh(CheckOptions{State: statePath, Output: out}); r != nil {
		t.Errorf("report = %+v", r)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("upgrades.json was written")
	}
	if ReadReport(out) != nil {
		t.Error("ReadReport should be nil")
	}
}

func TestRunCheckWritesReportAndReturns1(t *testing.T) {
	d := t.TempDir()
	m := man("2026.6.0", map[string]string{"mpd": "2026.6.0"})
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	statePath := writeState(t, d, st)
	out := filepath.Join(d, "cache", "upgrades.json")
	swapFetch(t, fetchOf(m))
	var stdout bytes.Buffer
	if rc := RunCheck(&stdout, os.Stderr, CheckOptions{State: statePath, Output: out}); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	r := ReadReport(out)
	if r == nil || r.Latest != "2026.6.0" || len(r.Roles) != 1 || r.Roles[0].Name != "mpd" ||
		!reflect.DeepEqual(r.Manifest, m) {
		t.Errorf("report = %+v", r)
	}
}

func TestRunCheckReturns0WhenUpToDate(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.Features = []string{"mympd"}
	statePath := writeState(t, d, st)
	swapFetch(t, fetchOf(man("2026.5.0", map[string]string{"mpd": "2026.5.0"})))
	var stdout bytes.Buffer
	rc := RunCheck(&stdout, os.Stderr, CheckOptions{State: statePath, Output: filepath.Join(d, "upgrades.json")})
	if rc != 0 || !strings.Contains(stdout.String(), "Up to date (2026.5.0)") {
		t.Errorf("rc = %d, stdout = %q", rc, stdout.String())
	}
}

func TestRunCheckInvalidStateReturns2(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "state.json")
	os.WriteFile(path, []byte(`{"odios": "2026.5.0"}`), 0o644)
	var stderr bytes.Buffer
	rc := RunCheck(os.Stdout, &stderr, CheckOptions{State: path, Output: path + ".out"})
	if rc != 2 || !strings.Contains(stderr.String(), "Error reading state") {
		t.Errorf("rc = %d, stderr = %q", rc, stderr.String())
	}
}

func TestRunCheckFetchFailureReturns2(t *testing.T) {
	statePath := writeState(t, t.TempDir(), makeState())
	swapFetch(t, fetchDown)
	if rc := RunCheck(os.Stdout, os.Stderr, CheckOptions{State: statePath, Output: "/x"}); rc != 2 {
		t.Errorf("rc = %d", rc)
	}
}
