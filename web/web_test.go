package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

const configFixture = "dtparam=audio=on\n[all]\nenable_uart=1\n"

// fixture boots a real HTTP server on tmp state/config with every subprocess
// seam replaced: privileged runs call the dac runners in-process, user runs
// are recorded, spawns run a shell stand-in for `qbzd login`.
type fixture struct {
	t          *testing.T
	dir        string
	statePath  string
	configPath string
	svc        *Services
	srv        *httptest.Server
	privileged [][]string
	userCalls  [][]string
	spawns     [][]string
	script     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir()}
	f.statePath = filepath.Join(f.dir, "state.json")
	f.writeRoles(map[string]string{"mpd": "1", "common": "1"})
	f.configPath = filepath.Join(f.dir, "config.txt")
	if err := os.WriteFile(f.configPath, []byte(configFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	oldFlag := dac.RebootFlag
	dac.RebootFlag = filepath.Join(f.dir, "reboot-required")
	t.Cleanup(func() { dac.RebootFlag = oldFlag })

	// No network: `upgrade.Refresh` after a toggle sees this manifest.
	oldFetch := manifest.Fetch
	manifest.Fetch = func(string) (*manifest.Manifest, error) {
		return &manifest.Manifest{Odios: "2026.5.0", Roles: map[string]string{"mpd": "1", "common": "1", "qbzd": "1"}}, nil
	}
	t.Cleanup(func() { manifest.Fetch = oldFetch })

	f.script = "echo 'paste this URL:'; echo '  https://qobuz.test/oauth?id=1'; sleep 30"
	cfg := Config{StatePath: f.statePath, ConfigTxt: f.configPath}
	f.svc = NewServices(cfg, Runners{
		// Stand-in for `sudo -n odioctl dac …`: the same code path, in-process.
		Privileged: func(args []string) (RunResult, error) {
			f.privileged = append(f.privileged, args)
			return f.runDacInProcess(args), nil
		},
		User: func(args []string) (RunResult, error) {
			f.userCalls = append(f.userCalls, args)
			return RunResult{}, nil
		},
		Spawn: func(argv []string) (ActionProcess, error) {
			f.spawns = append(f.spawns, argv)
			return defaultSpawn([]string{"sh", "-c", f.script})
		},
	})
	f.srv = httptest.NewServer(NewHandler(f.svc))
	t.Cleanup(f.srv.Close)
	t.Cleanup(f.stopRuns)
	return f
}

func (f *fixture) runDacInProcess(args []string) RunResult {
	var out, errb strings.Builder
	rc := 2
	if len(args) >= 2 && args[0] == "dac" {
		config := ""
		for i, a := range args {
			if a == "--config" && i+1 < len(args) {
				config = args[i+1]
			}
		}
		switch args[1] {
		case "set":
			rc = dac.RunSet(&out, &errb, args[2], config, false)
		case "unset":
			rc = dac.RunUnset(&out, &errb, config, false)
		}
	}
	return RunResult{Stdout: out.String(), Stderr: errb.String(), Code: rc}
}

// waitRunsGone waits for every started process to exit.
func (f *fixture) waitRunsGone() bool {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	for _, run := range f.svc.runs {
		if !run.proc.WaitFor(2 * time.Second) {
			return false
		}
	}
	return true
}

// stopRuns kills the shell stand-ins still sleeping.
func (f *fixture) stopRuns() {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	for _, run := range f.svc.runs {
		run.proc.Stop()
	}
}

func (f *fixture) writeRoles(roles map[string]string) {
	f.t.Helper()
	st := state.State{
		Odios: "2026.5.0", InstallMode: "image", TargetUser: "alice",
		Roles: roles, RolesExcluded: []string{},
		Features: []string{"tidal", "mympd"}, FeaturesExcluded: []string{},
		ReleaseHistory: []string{"2026.5.0"},
	}
	if err := state.Write(f.statePath, st); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) get(path string) (int, string) {
	f.t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// post submits a form; the response re-renders the page with a banner.
func (f *fixture) post(path string, form url.Values, withToken bool) (int, string) {
	f.t.Helper()
	if withToken && form.Get("token") == "" {
		form.Set("token", f.svc.Token())
	}
	resp, err := http.Post(f.srv.URL+path, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (f *fixture) state() state.State {
	f.t.Helper()
	st, err := state.Read(f.statePath)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func wants(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q in response", s)
		}
	}
}

func TestIndexRendersComponentsAndDac(t *testing.T) {
	f := newFixture(t)
	code, body := f.get("/")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	wants(t, body, "MPD", "Spotify Connect", "hifiberry-dacplus-std",
		"alice", "odio 2026.5.0", f.svc.Token())
	if strings.Contains(body, "State: installed") {
		t.Error("raw status leaked")
	}
}

func TestIndexSurvivesBrokenState(t *testing.T) {
	f := newFixture(t)
	os.WriteFile(f.statePath, []byte("{broken"), 0o644)
	code, body := f.get("/")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	wants(t, body, "state.json")
}

func TestStaticAssets(t *testing.T) {
	f := newFixture(t)
	if code, body := f.get("/static/style.css"); code != 200 || !strings.Contains(body, "--zinc-900") {
		t.Errorf("style.css: %d", code)
	}
	if code, _ := f.get("/static/nope.js"); code != 404 {
		t.Errorf("nope.js: %d", code)
	}
}

func TestUnknownPathsAre404(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.get("/api/things"); code != 404 {
		t.Error("want 404")
	}
}

func TestWrongVerbIs405(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.post("/", url.Values{}, true); code != 405 {
		t.Error("POST / should be 405")
	}
	if code, _ := f.get("/components"); code != 405 {
		t.Error("GET /components should be 405")
	}
}

func TestDisableAndEnableRole(t *testing.T) {
	f := newFixture(t)
	code, body := f.post("/components", url.Values{
		"kind": {"role"}, "name": {"mpd"}, "enabled": {"0"},
	}, true)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	wants(t, body, "MPD disabled")
	st := f.state()
	if _, ok := st.Roles["mpd"]; ok || len(st.RolesExcluded) != 1 {
		t.Errorf("state = %+v", st)
	}
	// upgrades.json refreshed: the pending disable never blocks, but the
	// re-enable goes pending so `apply` will not refuse.
	_, body = f.post("/components", url.Values{
		"kind": {"role"}, "name": {"mpd"}, "enabled": {"1"},
	}, true)
	wants(t, body, "MPD enabled — it will be installed by the next upgrade")
}

func TestEnableOptInRoleWritesAnExplicitYes(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/components", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "enabled": {"1"},
	}, true)
	wants(t, body, "Qobuz Connect enabled — it will be installed by the next upgrade")
	if v, ok := f.state().Roles["qbzd"]; !ok || v != "" {
		t.Errorf("roles = %v", f.state().Roles)
	}
}

func TestInfraRoleIsRefused(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/components", url.Values{
		"kind": {"role"}, "name": {"common"}, "enabled": {"0"},
	}, true)
	wants(t, body, "infrastructure role")
	if _, ok := f.state().Roles["common"]; !ok {
		t.Error("state changed")
	}
}

func TestMissingTokenIs403AndChangesNothing(t *testing.T) {
	f := newFixture(t)
	code, _ := f.post("/components", url.Values{
		"kind": {"role"}, "name": {"mpd"}, "enabled": {"0"},
	}, false)
	if code != 403 {
		t.Fatalf("code = %d", code)
	}
	if _, ok := f.state().Roles["mpd"]; !ok {
		t.Error("state changed without a token")
	}
}

func TestNonFormContentTypeIsRefused(t *testing.T) {
	f := newFixture(t)
	resp, err := http.Post(f.srv.URL+"/components", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	wants(t, string(body), "expected a form submission")
	if _, ok := f.state().Roles["mpd"]; !ok {
		t.Error("state changed")
	}
}

func (f *fixture) installQbzd() {
	f.writeRoles(map[string]string{"mpd": "1", "common": "1", "qbzd": "2026.9.0b1"})
}

func TestActionLinkIsLiftedOffStdoutAndShown(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	_, body := f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	wants(t, body, "https://qobuz.test/oauth?id=1", "open the link below to finish",
		"Open the Qobuz sign-in page")
	if len(f.spawns) != 1 || f.spawns[0][0] != "qbzd" {
		t.Errorf("spawns = %v", f.spawns)
	}
	// the row keeps the link on the next page load, while the process lives
	_, body = f.get("/")
	wants(t, body, "https://qobuz.test/oauth?id=1")
}

func TestActionHostReachesTheArgv(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	argv := f.spawns[0]
	// --callback-host is the name the browser reached the box by
	if argv[2] != "--callback-host" || !strings.HasPrefix(argv[3], "127.0.0.1") {
		t.Errorf("argv = %v", argv)
	}
}

func TestSecondClickReusesTheRunningCommand(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.post("/components/action", url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}}, true)
	_, body := f.post("/components/action", url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}}, true)
	wants(t, body, "already running", "https://qobuz.test/oauth?id=1")
	if len(f.spawns) != 1 { // the second click must not spawn again
		t.Errorf("spawns = %d", len(f.spawns))
	}
}

func TestFinishedRunBecomesANoteOnTheNextRender(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.script = "echo 'https://qobuz.test/oauth?id=1'" // prints its link, then exits 0
	_, body := f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	wants(t, body, "open the link below to finish")
	if !f.waitRunsGone() {
		t.Fatal("run still alive")
	}
	_, body = f.get("/")
	wants(t, body, "Log in to Qobuz: Done.")
	if strings.Contains(body, "qobuz.test/oauth") {
		t.Error("link survived the end of the run")
	}
}

func TestFailureShowsTheOutputInTheModal(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.script = "echo 'qbzd: cannot reach qobuz'; exit 3"
	_, body := f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	wants(t, body, "failed (exit 3)", "cannot reach qobuz")
	// the modal travels with that one response only
	_, body = f.get("/")
	if strings.Contains(body, "scrim") {
		t.Error("modal survived the reload")
	}
}

func TestUnknownActionOrComponentNeverSpawns(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	for _, form := range []url.Values{
		{"kind": {"role"}, "name": {"qbzd"}, "action": {"rm"}},
		{"kind": {"role"}, "name": {"nope"}, "action": {"login"}},
		{"kind": {"plugin"}, "name": {"qbzd"}, "action": {"login"}},
	} {
		f.post("/components/action", form, true)
	}
	if len(f.spawns) != 0 {
		t.Errorf("spawns = %v", f.spawns)
	}
}

func TestComponentNotInstalledIsRefused(t *testing.T) {
	f := newFixture(t) // qbzd not installed
	_, body := f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	wants(t, body, "not installed")
	if len(f.spawns) != 0 {
		t.Errorf("spawns = %v", f.spawns)
	}
}

func TestUpgradeSection(t *testing.T) {
	f := newFixture(t)
	_, body := f.get("/")
	wants(t, body, "No upgrade check has run yet")
	// a toggle refreshes upgrades.json; disabling mpd leaves nothing pending
	f.post("/components", url.Values{"kind": {"feature"}, "name": {"mympd"}, "enabled": {"0"}}, true)
	_, body = f.get("/")
	if !strings.Contains(body, "Up to date") && !strings.Contains(body, "Apply now") {
		t.Error("upgrade section missing")
	}
}

func TestApplyNowStartsTheUserUnit(t *testing.T) {
	f := newFixture(t)
	// make an upgrade pending: enable qbzd (opt-in, shipped by the manifest)
	f.post("/components", url.Values{"kind": {"role"}, "name": {"qbzd"}, "enabled": {"1"}}, true)
	_, body := f.post("/upgrade", url.Values{}, true)
	wants(t, body, "Upgrade started")
	if len(f.userCalls) != 1 || !strings.Contains(strings.Join(f.userCalls[0], " "),
		"systemctl --user start --no-block odio-upgrade.service") {
		t.Errorf("userCalls = %v", f.userCalls)
	}
}

func TestApplyWithNothingPendingIsRefused(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/upgrade", url.Values{}, true)
	wants(t, body, "nothing to apply")
	if len(f.userCalls) != 0 {
		t.Errorf("userCalls = %v", f.userCalls)
	}
}

func TestDacSetRunsPrivilegedAndMarksReboot(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/dac", url.Values{"id": {"hifiberry-dacplus-std"}}, true)
	wants(t, body, "DAC set to hifiberry-dacplus-std", "reboot required")
	if len(f.privileged) != 1 {
		t.Fatalf("privileged = %v", f.privileged)
	}
	text, _ := dac.ReadConfig(f.configPath)
	if dac.Parse(text).Current != "hifiberry-dacplus-std" {
		t.Error("config.txt not updated")
	}
	_, body = f.get("/")
	wants(t, body, "A reboot is required")
}

func TestDacUnsetRestores(t *testing.T) {
	f := newFixture(t)
	f.post("/dac", url.Values{"id": {"hifiberry-dacplus-std"}}, true)
	f.post("/dac/unset", url.Values{}, true)
	text, _ := dac.ReadConfig(f.configPath)
	if text != configFixture {
		t.Errorf("config.txt = %q", text)
	}
}

func TestDacUnknownIdNeverEscalates(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/dac", url.Values{"id": {"pwn; rm -rf /"}}, true)
	wants(t, body, "unknown DAC id")
	if len(f.privileged) != 0 {
		t.Errorf("privileged = %v", f.privileged)
	}
}

func TestDacEmptyIdIsNotAnUnset(t *testing.T) {
	f := newFixture(t)
	_, body := f.post("/dac", url.Values{"id": {""}}, true)
	wants(t, body, "no DAC selected")
	if len(f.privileged) != 0 {
		t.Errorf("privileged = %v", f.privileged)
	}
}

func TestHostHeaderDrivesTheOdioUILink(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/", nil)
	req.Host = "odio.local:8021"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	wants(t, string(body), "http://odio.local:8018/ui")
}
