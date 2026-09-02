package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/state"
)

func run(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rc := Run(&stdout, &stderr, argv)
	return rc, stdout.String(), stderr.String()
}

const stateJSON = `{
    "odios": "2026.5.0",
    "install_mode": "image",
    "target_user": "odio",
    "roles": {"mpd": "2026.5.0", "common": "2026.5.0"},
    "roles_excluded": [],
    "features": ["tidal"],
    "features_excluded": [],
    "release_history": ["2026.5.0"]
}`

func writeStateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNoCommandPrintsUsageAndReturns2(t *testing.T) {
	rc, out, _ := run(t)
	if rc != 2 || !strings.Contains(out, "usage: odioctl") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestVersionFlag(t *testing.T) {
	rc, out, _ := run(t, "--version")
	if rc != 0 || !strings.HasPrefix(out, "odioctl ") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestUnknownCommandReturns2(t *testing.T) {
	rc, _, err := run(t, "frobnicate")
	if rc != 2 || !strings.Contains(err, "unknown command") {
		t.Errorf("rc = %d, stderr = %q", rc, err)
	}
}

func TestSubcommandsWithoutArgsError(t *testing.T) {
	for _, argv := range [][]string{{"upgrade"}, {"dac"}, {"components"}} {
		if rc, _, _ := run(t, argv...); rc != 2 {
			t.Errorf("%v: rc = %d, want 2", argv, rc)
		}
	}
}

func TestEverySubcommandHasHelp(t *testing.T) {
	for _, argv := range [][]string{
		{"upgrade", "check", "--help"},
		{"upgrade", "apply", "--help"},
		{"upgrade", "verify", "--help"},
		{"pwa-url", "--help"},
		{"components", "--help"},
		{"dac", "list", "--help"},
		{"dac", "status", "--help"},
	} {
		if rc, _, _ := run(t, argv...); rc != 0 {
			t.Errorf("%v: rc = %d, want 0", argv, rc)
		}
	}
}

func TestPWAURLPrintsSomethingPrintable(t *testing.T) {
	rc, out, _ := run(t, "pwa-url")
	if rc != 0 || !strings.HasPrefix(out, "https://pwa.odio.love") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestComponentsListJSON(t *testing.T) {
	rc, out, _ := run(t, "components", "--state", writeStateFile(t), "list", "--json")
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	var comps []map[string]any
	if err := json.Unmarshal([]byte(out), &comps); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, c := range comps {
		byName[c["kind"].(string)+":"+c["name"].(string)] = c
	}
	if c := byName["role:mpd"]; c["status"] != "installed" || c["enabled"] != true {
		t.Errorf("mpd = %v", c)
	}
	if c := byName["feature:tidal"]; c["parent"] != "upmpdcli" {
		t.Errorf("tidal = %v", c)
	}
}

func TestComponentsDisableAndEnableRoundTrip(t *testing.T) {
	path := writeStateFile(t)
	rc, out, _ := run(t, "components", "--state", path, "disable", "mpd")
	if rc != 0 || !strings.Contains(out, "role mpd disabled") {
		t.Fatalf("rc = %d, out = %q", rc, out)
	}
	st, err := state.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Roles["mpd"]; ok || len(st.RolesExcluded) != 1 {
		t.Errorf("state = %+v", st)
	}
	if rc, _, _ = run(t, "components", "--state", path, "enable", "mpd"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	st, _ = state.Read(path)
	if len(st.RolesExcluded) != 0 {
		t.Errorf("state = %+v", st)
	}
}

func TestComponentsInfraRoleReturns2(t *testing.T) {
	rc, _, err := run(t, "components", "--state", writeStateFile(t), "disable", "common")
	if rc != 2 || !strings.Contains(err, "infrastructure role") {
		t.Errorf("rc = %d, stderr = %q", rc, err)
	}
}

func TestComponentsMissingStateReturns2(t *testing.T) {
	rc, _, err := run(t, "components", "--state", "/nonexistent/state.json", "list")
	if rc != 2 || !strings.Contains(err, "Error reading") {
		t.Errorf("rc = %d, stderr = %q", rc, err)
	}
}

const configFixture = `# comment
dtparam=i2c_arm=on
dtparam=audio=on

[pi4]
arm_boost=1
`

func writeConfig(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	cfg := filepath.Join(d, "config.txt")
	if err := os.WriteFile(cfg, []byte(configFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	old := dac.RebootFlag
	dac.RebootFlag = filepath.Join(d, "run", "reboot-required")
	t.Cleanup(func() { dac.RebootFlag = old })
	return cfg
}

func TestDacListContainsCatalog(t *testing.T) {
	rc, out, _ := run(t, "dac", "list")
	if rc != 0 || !strings.Contains(out, "hifiberry-dacplus") || !strings.Contains(out, dac.Onboard) {
		t.Errorf("rc = %d", rc)
	}
}

func TestDacStatusUnmanaged(t *testing.T) {
	cfg := writeConfig(t)
	rc, out, _ := run(t, "dac", "status", "--config", cfg)
	if rc != 0 || !strings.Contains(out, "current:  onboard") || !strings.Contains(out, "managed:  no") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestDacStatusNoConfig(t *testing.T) {
	rc, out, _ := run(t, "dac", "status", "--config", "/nonexistent")
	if rc != 0 || !strings.Contains(out, "no config.txt") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestDacSetWritesBackupAndFlag(t *testing.T) {
	cfg := writeConfig(t)
	rc, out, _ := run(t, "dac", "set", "hifiberry-dacplus-std", "--config", cfg)
	if rc != 0 || !strings.Contains(out, "reboot required") {
		t.Fatalf("rc = %d, out = %q", rc, out)
	}
	text, _ := dac.ReadConfig(cfg)
	if dac.Parse(text).Current != "hifiberry-dacplus-std" {
		t.Error("overlay not applied")
	}
	bak, err := os.ReadFile(cfg + ".odioctl.bak")
	if err != nil || string(bak) != configFixture {
		t.Errorf("backup = %q, %v", bak, err)
	}
	if !dac.GetStatus(cfg).RebootRequired {
		t.Error("reboot flag not set")
	}
}

func TestDacDryRunWritesNothing(t *testing.T) {
	cfg := writeConfig(t)
	rc, out, _ := run(t, "dac", "set", "hifiberry-dacplus-std", "--config", cfg, "--dry-run")
	if rc != 0 || !strings.Contains(out, dac.Begin) {
		t.Fatalf("rc = %d", rc)
	}
	text, _ := dac.ReadConfig(cfg)
	if text != configFixture {
		t.Error("config was written")
	}
	if dac.GetStatus(cfg).RebootRequired {
		t.Error("reboot flag set on dry-run")
	}
}

func TestDacUnknownIDReturns2(t *testing.T) {
	cfg := writeConfig(t)
	rc, _, err := run(t, "dac", "set", "not-a-dac", "--config", cfg)
	if rc != 2 || !strings.Contains(err, "invalid choice") {
		t.Errorf("rc = %d, stderr = %q", rc, err)
	}
}

func TestDacSetThenUnsetRestores(t *testing.T) {
	cfg := writeConfig(t)
	run(t, "dac", "set", "hifiberry-dacplus-std", "--config", cfg)
	rc, _, _ := run(t, "dac", "unset", "--config", cfg)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	text, _ := dac.ReadConfig(cfg)
	if text != configFixture {
		t.Errorf("config = %q", text)
	}
}

func TestDacNoChangeWhenAlreadySet(t *testing.T) {
	cfg := writeConfig(t)
	run(t, "dac", "set", "hifiberry-dacplus-std", "--config", cfg)
	rc, out, _ := run(t, "dac", "set", "hifiberry-dacplus-std", "--config", cfg)
	if rc != 0 || !strings.Contains(out, "no change") {
		t.Errorf("rc = %d, out = %q", rc, out)
	}
}

func TestDacMissingConfigReturns2(t *testing.T) {
	rc, _, err := run(t, "dac", "set", "hifiberry-dac", "--config", "/nonexistent")
	if rc != 2 || !strings.Contains(err, "no config.txt") {
		t.Errorf("rc = %d, stderr = %q", rc, err)
	}
}

func TestUpgradeVerifyThroughCLI(t *testing.T) {
	rc, _, _ := run(t, "upgrade", "verify", "--state", writeStateFile(t), "--expected-version", "2026.5.0")
	if rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	if rc, _, _ := run(t, "upgrade", "verify", "--state", "/nonexistent/state.json"); rc != 2 {
		t.Errorf("rc = %d", rc)
	}
}
