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
	"github.com/b0bbywan/odioctl/state"
)

func TestDeriveInstallEnv(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "branding": "1"}
	st.RolesExcluded = []string{"spotifyd"}
	st.Features = []string{"tidal"}
	st.FeaturesExcluded = []string{"mympd"}
	env := DeriveInstallEnv(st)
	want := map[string]string{
		"INSTALL_MPD":      "Y",
		"INSTALL_BRANDING": "Y",
		"INSTALL_TIDAL":    "Y",
		"INSTALL_SPOTIFYD": "N",
		"INSTALL_MYMPD":    "N",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %v", env)
	}
	if len(DeriveInstallEnv(makeState())) != 0 {
		t.Error("empty state should emit nothing")
	}
}

func runManifest(roles map[string]string) *manifest.Manifest {
	m := man("2026.5.0", roles)
	return &m
}

func TestDeriveRunEnv(t *testing.T) {
	base := func(roles map[string]string) state.State {
		st := makeState()
		st.Roles = roles
		return st
	}
	t.Run("no manifest returns empty", func(t *testing.T) {
		if env := DeriveRunEnv(base(map[string]string{"mpd": "2026.5.0"}), nil, nil); len(env) != 0 {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("unchanged role emits RUN_N", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"mpd": "2026.5.0"}),
			runManifest(map[string]string{"mpd": "2026.5.0"}),
			map[string]string{"INSTALL_MPD": "Y"})
		if env["RUN_MPD"] != "N" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("bumped role is not emitted", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"mpd": "2026.4.0"}),
			runManifest(map[string]string{"mpd": "2026.5.0"}),
			map[string]string{"INSTALL_MPD": "Y"})
		if _, ok := env["RUN_MPD"]; ok {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("excluded role is skipped", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"spotifyd": "2026.5.0"}),
			runManifest(map[string]string{"spotifyd": "2026.5.0"}),
			map[string]string{"INSTALL_SPOTIFYD": "N"})
		if _, ok := env["RUN_SPOTIFYD"]; ok {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("role missing from manifest is not emitted", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"snapclient": "0.27"}),
			runManifest(map[string]string{}),
			map[string]string{"INSTALL_SNAPCLIENT": "Y"})
		if len(env) != 0 {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("common is emitted without install env", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"common": "2026.5.0"}),
			runManifest(map[string]string{"common": "2026.5.0"}), map[string]string{})
		if env["RUN_COMMON"] != "N" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("unparseable installed version re-runs", func(t *testing.T) {
		env := DeriveRunEnv(base(map[string]string{"mpd": "garbage"}),
			runManifest(map[string]string{"mpd": "2026.5.0"}),
			map[string]string{"INSTALL_MPD": "Y"})
		if _, ok := env["RUN_MPD"]; ok {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("role ahead of state odios re-runs", func(t *testing.T) {
		st := base(map[string]string{"bluetooth": "2026.5.0b1"})
		st.Odios = "2026.4.2b2-8-gabc1234"
		env := DeriveRunEnv(st, runManifest(map[string]string{"bluetooth": "2026.5.0b1"}),
			map[string]string{"INSTALL_BLUETOOTH": "Y"})
		if _, ok := env["RUN_BLUETOOTH"]; ok {
			t.Errorf("env = %v", env)
		}
	})
}

func TestLoadState(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "x"}
	st.RolesExcluded = []string{"spotifyd"}
	st.TargetUser = "alice"
	path := writeState(t, d, st)
	var stdout bytes.Buffer
	gotPath, got, ok := loadState(&stdout, os.Stderr, ApplyOptions{State: path})
	if !ok || gotPath != path || got.TargetUser != "alice" {
		t.Errorf("loadState = %q, %+v, %v", gotPath, got, ok)
	}
	text := stdout.String()
	if !strings.Contains(text, "state.json read from "+path+":") ||
		!strings.Contains(text, "roles_excluded:    spotifyd") {
		t.Errorf("stdout = %q", text)
	}
}

func TestLoadStateErrors(t *testing.T) {
	var stderr bytes.Buffer
	if _, _, ok := loadState(os.Stdout, &stderr, ApplyOptions{State: "/missing/state.json"}); ok {
		t.Error("want failure")
	}
	if !strings.Contains(stderr.String(), "Error reading /missing/state.json") {
		t.Errorf("stderr = %q", stderr.String())
	}
	// Legacy schemas are not supported: refuse loudly instead of guessing.
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte(`{"odios": "2026.4.0", "roles": {}}`), 0o644)
	stderr.Reset()
	if _, _, ok := loadState(os.Stdout, &stderr, ApplyOptions{State: path}); ok {
		t.Error("want failure")
	}
	if !strings.Contains(stderr.String(), "missing required fields") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func applyEnv(t *testing.T, st state.State, opts ApplyOptions, m *manifest.Manifest) (map[string]string, string) {
	t.Helper()
	var out bytes.Buffer
	env := buildApplyEnv(&out, st, "2026.5.0", "alice", m, opts)
	return env, out.String()
}

func TestTargetManifestReusesTheCacheOnlyForItsTag(t *testing.T) {
	cached := man("2026.6.0", map[string]string{"mpd": "2026.6.0"})
	report := &Report{TargetTag: "2026.6.0", Manifest: cached}
	noFetch(t)
	if got := targetManifest(report, "2026.6.0"); got == nil || !reflect.DeepEqual(*got, cached) {
		t.Errorf("cached tag: got %+v", got)
	}
	fetched := man("2026.7.0", nil)
	urls := swapFetch(t, fetchOf(fetched))
	if got := targetManifest(report, "2026.7.0"); got == nil || !reflect.DeepEqual(*got, fetched) {
		t.Errorf("other tag: got %+v", got)
	}
	if got := targetManifest(nil, "2026.7.0"); got == nil || !reflect.DeepEqual(*got, fetched) {
		t.Errorf("no report: got %+v", got)
	}
	if len(*urls) != 2 {
		t.Errorf("fetched %v", *urls)
	}
	swapFetch(t, fetchDown)
	if got := targetManifest(report, "2026.7.0"); got != nil {
		t.Errorf("network down: got %+v, want nil", got)
	}
}

func TestBuildApplyEnvSkipsUnchangedRoles(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	env, out := applyEnv(t, st, ApplyOptions{}, runManifest(map[string]string{"mpd": "2026.5.0"}))
	if env["TARGET_USER"] != "alice" || env["ODIOS_VERSION"] != "2026.5.0" || env["RUN_MPD"] != "N" {
		t.Errorf("env = %v", env)
	}
	if !strings.Contains(out, "skipping unchanged roles: mpd") {
		t.Errorf("out = %q", out)
	}
}

func TestBuildApplyEnvNoManifest(t *testing.T) {
	st := makeState()
	st.Odios = "2026.4.0"
	st.Roles = map[string]string{"mpd": "2026.4.0"}
	env, out := applyEnv(t, st, ApplyOptions{}, nil)
	if _, ok := env["RUN_MPD"]; ok {
		t.Errorf("env = %v", env)
	}
	if !strings.Contains(out, "manifest unavailable") {
		t.Errorf("out = %q", out)
	}
}

func TestBuildApplyEnvAllRolesBumped(t *testing.T) {
	st := makeState()
	st.Odios = "2026.4.0"
	st.Roles = map[string]string{"mpd": "2026.4.0"}
	env, out := applyEnv(t, st, ApplyOptions{}, runManifest(map[string]string{"mpd": "2026.5.0"}))
	if _, ok := env["RUN_MPD"]; ok {
		t.Errorf("env = %v", env)
	}
	if !strings.Contains(out, "all roles bumped") {
		t.Errorf("out = %q", out)
	}
}

func TestBuildApplyEnvReinstall(t *testing.T) {
	// Even with an up-to-date manifest, --reinstall suppresses the skip and
	// sets the scaffold force flag.
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	env, out := applyEnv(t, st, ApplyOptions{Reinstall: true}, runManifest(map[string]string{"mpd": "2026.5.0"}))
	if _, ok := env["RUN_MPD"]; ok {
		t.Errorf("env = %v", env)
	}
	if env["ODIOS_FORCE_SCAFFOLD"] != "Y" || !strings.Contains(out, "reinstall: running all roles") {
		t.Errorf("env = %v, out = %q", env, out)
	}
}

func TestBuildApplyEnvProgress(t *testing.T) {
	old := lookupUID
	lookupUID = func(name string) (string, error) {
		if name != "alice" {
			t.Errorf("lookupUID(%q), want alice", name)
		}
		return "1001", nil
	}
	t.Cleanup(func() { lookupUID = old })
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	m := runManifest(map[string]string{"mpd": "2026.5.0"})
	env, _ := applyEnv(t, st, ApplyOptions{Progress: true}, m)
	if env["ODIOS_PROGRESS"] != "Y" {
		t.Errorf("env = %v", env)
	}
	if env["XDG_RUNTIME_DIR"] != "/run/user/1001" {
		t.Errorf("env = %v", env)
	}
	env, _ = applyEnv(t, st, ApplyOptions{}, m)
	if _, ok := env["ODIOS_PROGRESS"]; ok {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["XDG_RUNTIME_DIR"]; ok {
		t.Errorf("env = %v", env)
	}
}

func TestBuildApplyEnvProgressWithoutAUID(t *testing.T) {
	old := lookupUID
	lookupUID = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookupUID = old })
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	env, out := applyEnv(t, st, ApplyOptions{Progress: true}, runManifest(map[string]string{"mpd": "2026.5.0"}))
	if _, ok := env["XDG_RUNTIME_DIR"]; ok || env["ODIOS_PROGRESS"] != "Y" {
		t.Errorf("env = %v", env)
	}
	if !strings.Contains(out, "cannot resolve alice's uid") {
		t.Errorf("out = %q", out)
	}
}

func runApply(t *testing.T, d string, opts ApplyOptions) (int, string) {
	t.Helper()
	var out bytes.Buffer
	opts.State = filepath.Join(d, "state.json")
	rc := RunApply(&out, &out, opts)
	return rc, out.String()
}

func writeUpgradesJSON(t *testing.T, d string, payload map[string]any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(d, "upgrades.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunPrintsEnvAndDoesNotInvoke(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	st.RolesExcluded = []string{"spotifyd"}
	writeState(t, d, st)
	swapFetch(t, fetchDown)
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{DryRun: true, Force: true, Version: "2026.6.0"})
	if rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, text)
	}
	for _, want := range []string{
		"Upgrading to 2026.6.0 via",
		"INSTALL_SPOTIFYD=N",
		"TARGET_USER=odio",
		"(dry-run, not invoking)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestNoUpgradeReportedReturns0WithoutForce(t *testing.T) {
	d := t.TempDir()
	writeState(t, d, makeState())
	writeUpgradesJSON(t, d, map[string]any{"upgrade_available": false, "latest": "2026.5.0"})
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !strings.Contains(text, "No upgrade reported") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestRefusesDowngrade(t *testing.T) {
	d := t.TempDir()
	writeState(t, d, makeState())
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{Version: "2026.4.0", DryRun: true})
	if rc != 2 || !strings.Contains(text, "Refusing to downgrade") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestUsesSiblingUpgradesJSONOfExplicitState(t *testing.T) {
	// --state /path/state.json → /path/upgrades.json is the cache; its
	// `target_tag` drives the target and its manifest is reused offline.
	d := t.TempDir()
	st := makeState()
	st.Roles = map[string]string{"mpd": "2026.5.0"}
	writeState(t, d, st)
	writeUpgradesJSON(t, d, map[string]any{
		"upgrade_available": true, "latest": "2026.6.0", "target_tag": "2026.6.0",
		"manifest": man("2026.6.0", map[string]string{"mpd": "2026.6.0"}),
	})
	noFetch(t)
	rc, text := runApply(t, d, ApplyOptions{DryRun: true})
	if rc != 0 || !strings.Contains(text, "ODIOS_VERSION=2026.6.0") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestRefusesATargetThatIsNotAReleaseTag(t *testing.T) {
	// upgrades.json is group-writable and this runs as root: a planted
	// `target_tag` must not become part of a curl | bash URL.
	d := t.TempDir()
	writeState(t, d, makeState())
	writeUpgradesJSON(t, d, map[string]any{
		"upgrade_available": true, "target_tag": "../../someone/else/releases/download/x",
		"manifest": man("2026.6.0", map[string]string{}),
	})
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 2 || !strings.Contains(text, "not a release tag") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestMissingReportIsNothingToApply(t *testing.T) {
	// The release is decided by `check`: with no report there is nothing to
	// follow, and "latest" is not picked on odio's behalf.
	d := t.TempDir()
	writeState(t, d, makeState())
	noFetch(t)
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{})
	if rc != 0 || !strings.Contains(text, "No upgrade reported") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestForceWithoutReportTargetsLatest(t *testing.T) {
	d := t.TempDir()
	writeState(t, d, makeState())
	swapFetch(t, fetchDown)
	noInstall(t)
	rc, text := runApply(t, d, ApplyOptions{Force: true, DryRun: true})
	if rc != 0 || !strings.Contains(text, "Upgrading to latest via") ||
		!strings.Contains(text, "manifest unavailable, running all roles") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestTargetsTheTagRecordedByCheck(t *testing.T) {
	d := t.TempDir()
	st := makeState()
	st.Odios = "2026.7.0rc2"
	st.ReleaseHistory = []string{"2026.7.0rc2"}
	writeState(t, d, st)
	writeUpgradesJSON(t, d, map[string]any{
		"upgrade_available": true, "latest": "2026.7.0rc2-9-gcad916c",
		"target_tag": "pr-84", "manifest": man("2026.7.0rc2-9-gcad916c", map[string]string{}),
	})
	rc, text := runApply(t, d, ApplyOptions{DryRun: true})
	if rc != 0 || !strings.Contains(text, "Upgrading to pr-84 via") ||
		!strings.Contains(text, "/releases/download/pr-84/install.sh") {
		t.Errorf("rc = %d, out = %q", rc, text)
	}
}

func TestMissingStateReturns2(t *testing.T) {
	rc, _ := runApply(t, t.TempDir(), ApplyOptions{Force: true})
	if rc != 2 {
		t.Errorf("rc = %d", rc)
	}
}

func TestOdioAPIListening(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", d)
	if OdioAPIListening() {
		t.Error("no socket yet")
	}
	os.MkdirAll(filepath.Join(d, "odio-api"), 0o755)
	os.WriteFile(filepath.Join(d, "odio-api", "upgrade.sock"), nil, 0o644)
	if !OdioAPIListening() {
		t.Error("socket exists")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if OdioAPIListening() {
		t.Error("unset runtime dir")
	}
}
