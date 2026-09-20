package motd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const upgradesJSON = `{
    "current": "2026.9.0b2",
    "latest": "2026.10.0",
    "target_tag": "2026.10.0",
    "upgrade_available": true,
    "roles": [{"name": "mpd", "installed": "2026.9.0b2", "available": "2026.10.0"}],
    "pending_components": [],
    "manifest": {"odios": "2026.10.0", "roles": {"mpd": "2026.10.0"}},
    "checked_at": "2026-09-20T10:00:00Z"
}`

const stateJSON = `{
    "odios": "2026.9.0b2",
    "install_mode": "live",
    "target_user": "odio",
    "roles": {"mpd": "2026.9.0b2"},
    "roles_excluded": [],
    "features": [],
    "features_excluded": [],
    "release_history": ["2026.9.0b2"]
}`

// fixture writes state.json, and upgrades.json beside it when given one.
func fixture(t *testing.T, upgrades string) string {
	t.Helper()
	old := uname
	uname = func() string { return "Linux odio 6.12.0 #1 SMP armv6l" }
	t.Cleanup(func() { uname = old })
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if upgrades != "" {
		if err := os.WriteFile(filepath.Join(dir, "upgrades.json"), []byte(upgrades), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return statePath
}

func run(t *testing.T, statePath string) string {
	t.Helper()
	var out bytes.Buffer
	if rc := RunMOTD(&out, statePath); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	return out.String()
}

func wants(t *testing.T, got string, substrings ...string) {
	t.Helper()
	for _, s := range substrings {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
}

func TestBannerShowsThePendingUpgrade(t *testing.T) {
	got := run(t, fixture(t, upgradesJSON))
	wants(t, got,
		"'----------------'", // the odio banner
		"Linux odio 6.12.0",
		"odio v2026.9.0b2",
		"Update available: 2026.9.0b2 → 2026.10.0",
		"  mpd: 2026.9.0b2 → 2026.10.0",
		"ABSOLUTELY NO WARRANTY")
}

func TestUpToDateSaysOnlyTheVersion(t *testing.T) {
	up := strings.Replace(upgradesJSON, `"upgrade_available": true`, `"upgrade_available": false`, 1)
	got := run(t, fixture(t, up))
	wants(t, got, "odio v2026.9.0b2")
	if strings.Contains(got, "Update available") || strings.Contains(got, "mpd:") {
		t.Errorf("got:\n%s", got)
	}
}

// A check has never run (a fresh image): state.json still knows the release.
func TestWithoutAReportTheStateGivesTheVersion(t *testing.T) {
	got := run(t, fixture(t, ""))
	wants(t, got, "odio v2026.9.0b2")
	if strings.Contains(got, "Update available") {
		t.Errorf("got:\n%s", got)
	}
}

// Nothing to read at all: the banner still prints, a login must not break.
func TestWithoutAnythingItStillGreets(t *testing.T) {
	old := uname
	uname = func() string { return "" }
	t.Cleanup(func() { uname = old })
	got := run(t, filepath.Join(t.TempDir(), "state.json"))
	wants(t, got, "'----------------'", "ABSOLUTELY NO WARRANTY")
	if strings.Contains(got, "odio v") {
		t.Errorf("got:\n%s", got)
	}
}
