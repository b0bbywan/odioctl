package web

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
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
	logs       syncBuf
	unit       *fakeUnit // set by useUnit
}

// syncBuf collects the log: the exit line comes from a goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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
	// the user manager's runtime dir, where a fakeUnit leaves its invocation link
	if err := os.MkdirAll(filepath.Join(f.dir, "run", "systemd", "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StatePath: f.statePath, ConfigTxt: f.configPath, Home: f.dir, RuntimeDir: filepath.Join(f.dir, "run")}
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
		Log: &f.logs,
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

// post submits a form as htmx does: the answer is the notice (and an
// action's modal), the state follows on the stream.
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

// stream is an open /events connection, read line by line from its own
// goroutine so a test can wait for a batch without a body that never ends.
type stream struct {
	t     *testing.T
	lines chan string
	read  strings.Builder
}

func (f *fixture) openStream() *stream {
	f.t.Helper()
	resp, err := http.Get(f.srv.URL + "/events")
	if err != nil {
		f.t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		f.t.Errorf("Content-Type = %q", ct)
	}
	s := &stream{t: f.t, lines: make(chan string, 1024)}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	f.t.Cleanup(func() { resp.Body.Close() })
	return s
}

// until reads through the end of the event carrying marker and returns
// what this call read; fatal after 2s.
func (s *stream) until(marker string) string {
	s.t.Helper()
	var got strings.Builder
	seen := false
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				s.t.Fatalf("stream closed before %q; read:\n%s", marker, s.read.String())
			}
			s.read.WriteString(line + "\n")
			got.WriteString(line + "\n")
			if strings.Contains(line, marker) {
				seen = true
			}
			if seen && line == "" {
				return got.String()
			}
		case <-deadline:
			s.t.Fatalf("no %q on the stream within 2s; read:\n%s", marker, s.read.String())
		}
	}
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
		"odio 2026.5.0", f.svc.Token())
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
	if code, body := f.get("/static/htmx.min.js"); code != 200 || !strings.Contains(body, "htmx") {
		t.Errorf("htmx.min.js: %d", code)
	}
	if code, body := f.get("/static/htmx-sse.js"); code != 200 || !strings.Contains(body, "sse-swap") {
		t.Errorf("htmx-sse.js: %d", code)
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
		"Qobuz sign-in page")
	if len(f.spawns) != 1 || f.spawns[0][0] != "qbzd" {
		t.Errorf("spawns = %v", f.spawns)
	}
	// the row keeps the link on the next page load, while the process lives
	_, body = f.get("/")
	wants(t, body, "https://qobuz.test/oauth?id=1")
}

func TestEventsStreamCarriesTheSectionsAsTheActionExits(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.script = "echo 'https://qobuz.test/oauth?id=1'; sleep 0.5"
	f.post("/components/action", url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}}, true)

	// on connect, the state now: the row with its link, every section named
	s := f.openStream()
	first := s.until("event: dac")
	wants(t, first,
		"event: banners\ndata: <div id=\"banners\" sse-swap=\"banners\" hx-swap=\"outerHTML\">",
		"event: upgrade\ndata: <section id=\"upgrade\" sse-swap=\"upgrade\" hx-swap=\"outerHTML\">",
		"event: components\ndata: <section id=\"components\" sse-swap=\"components\" hx-swap=\"outerHTML\">",
		`href="https://qobuz.test/oauth?id=1"`,
		"event: dac\ndata: <section id=\"dac\" sse-swap=\"dac\" hx-swap=\"outerHTML\">")
	if strings.Contains(first, "event: modal-") {
		t.Error("a modal on the stream before its action ended")
	}
	// the run ends: the row shows Done, and so does the modal, on its own
	// event (only a page showing it listens) — and nothing else is sent
	batch := s.until(`disabled>Done</button>`)
	wants(t, batch,
		"event: components\n",
		"event: modal-role-qbzd-login\ndata: <div id=\"modal-role-qbzd-login\" class=\"scrim\" sse-swap=\"modal-role-qbzd-login\" hx-swap=\"outerHTML\">",
		`<span class="chip installed">Done</span>`)
	if strings.Contains(batch, `href="https://qobuz.test`) {
		t.Error("the link survived the end of the run")
	}
	for _, untouched := range []string{"event: upgrade\n", "event: dac\n", "event: banners\n", "event: end"} {
		if strings.Contains(batch, untouched) {
			t.Errorf("an action's end sent %q", untouched)
		}
	}
}

func TestPostAnswersTheNoticeAndTheStreamTheState(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	s := f.openStream()
	s.until("event: dac")
	// an action: the notice, with the modal out of band; the row's link on
	// the stream
	code, body := f.post("/components/action", url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}}, true)
	if code != 200 || strings.Contains(body, "<html") || strings.Contains(body, "<section") {
		t.Errorf("code = %d, body = %q", code, body)
	}
	wants(t, body, `<div class="banner ok">Log in to Qobuz: open the link below to finish.</div>`,
		`<div id="modal" hx-swap-oob="innerHTML"><div id="modal-role-qbzd-login" class="scrim" sse-swap="modal-role-qbzd-login" hx-swap="outerHTML">`,
		"https://qobuz.test/oauth?id=1")
	batch := s.until(`href="https://qobuz.test/oauth?id=1"`)
	if strings.Contains(batch, "open the link below") {
		t.Error("the notice is the POST's answer, never on the stream")
	}
	// a toggle: the notice alone, the section on the stream
	code, body = f.post("/components", url.Values{"kind": {"role"}, "name": {"qbzd"}, "enabled": {"0"}}, true)
	if code != 200 || strings.Contains(body, "<section") || strings.Contains(body, "id=\"modal\"") {
		t.Errorf("code = %d, body = %q", code, body)
	}
	wants(t, body, `<div class="banner ok">Qobuz Connect disabled`)
	batch = s.until(">Disabled</span>")
	wants(t, batch, "event: upgrade\n", "event: components\n", `<div id="row-role-qbzd" class="card">`)
	if strings.Contains(batch, "event: dac\n") || strings.Contains(batch, "event: banners\n") {
		t.Error("a toggle re-sent sections it does not touch")
	}
	// a bad token: 403, and still a notice
	code, body = f.post("/dac/unset", url.Values{}, false)
	if code != 403 || strings.Contains(body, "<html") {
		t.Errorf("code = %d, body = %q", code, body)
	}
	wants(t, body, `<div class="banner err">invalid or missing form token`)
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

// The Tidal helper writes its credentials under {home}; argv runs without a
// shell, so the server fills it in — from Config, never from the process
// environment, and never as "" (that would be a root-level path).
func TestActionHomeReachesTheArgvOrIsRefused(t *testing.T) {
	var argv []string
	spawn := func(a []string) (ActionProcess, error) {
		argv = a
		return defaultSpawn([]string{"true"})
	}
	action := components.Action{Label: "Log in to Tidal", Argv: []string{"helper", "-f", "{home}/.cache/creds"}}

	run, err := startAction(spawn, action, "box.local", "/home/alice")
	if err != nil || argv[2] != "/home/alice/.cache/creds" {
		t.Errorf("argv = %v, err = %v", argv, err)
	}
	run.proc.Stop()

	argv = nil
	if _, err := startAction(spawn, action, "box.local", ""); err == nil || argv != nil {
		t.Errorf("spawned %v, err = %v; want a refusal", argv, err)
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
	wants(t, body, `<small class="action"><span class="chip installed">Done</span></small>`)
	if strings.Contains(body, "qobuz.test/oauth") {
		t.Error("link survived the end of the run")
	}
	// the exit line is written by the reaper goroutine, give it a moment
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(f.logs.String(), "exited 0") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	wants(t, f.logs.String(),
		"POST /components/action from ",
		"action qbzd/login: spawned pid ",
		": qbzd login --callback-host ",
		"action qbzd/login: link after ",
		"https://qobuz.test/oauth?id=1",
		"exited 0 after ")
}

func TestFailureShowsTheOutputInTheModal(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.script = "echo 'qbzd: cannot reach qobuz'; exit 3"
	_, body := f.post("/components/action", url.Values{
		"kind": {"role"}, "name": {"qbzd"}, "action": {"login"},
	}, true)
	wants(t, body, "failed (exit 3)", "cannot reach qobuz")
	// the modal travels with that one response only; the row keeps a note
	_, body = f.get("/")
	if strings.Contains(body, "scrim") {
		t.Error("modal survived the reload")
	}
	wants(t, body, `<small class="action err">Log in to Qobuz: Failed (exit 3). qbzd: cannot reach qobuz`)
}

func TestStuckRunIsNotRespawnedWhileBeingStopped(t *testing.T) {
	f := newFixture(t)
	f.installQbzd()
	f.script = "sleep 30" // never prints a link
	old := actionLinkTimeout
	actionLinkTimeout = 50 * time.Millisecond
	t.Cleanup(func() { actionLinkTimeout = old })
	form := func() url.Values { return url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}} }
	first := make(chan string, 1)
	go func() { _, body := f.post("/components/action", form(), true); first <- body }()
	time.Sleep(500 * time.Millisecond) // inside the first click's WaitFor/Stop window
	_, body := f.post("/components/action", form(), true)
	wants(t, body, "already running")
	wants(t, <-first, "no link after 0s")
	if len(f.spawns) != 1 {
		t.Errorf("spawns = %d, want the stuck run left alone", len(f.spawns))
	}
	_, body = f.get("/")
	wants(t, body, "Log in to Qobuz: Failed (exit")
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
	wants(t, body, "No upgrade check yet")
	// a toggle refreshes upgrades.json; disabling mpd leaves nothing pending
	f.post("/components", url.Values{"kind": {"feature"}, "name": {"mympd"}, "enabled": {"0"}}, true)
	_, body = f.get("/")
	if !strings.Contains(body, "Up to date") && !strings.Contains(body, "Apply now") {
		t.Error("upgrade section missing")
	}
}

// makeUpgradePending enables qbzd (opt-in, shipped by the manifest).
func (f *fixture) makeUpgradePending() {
	f.post("/components", url.Values{"kind": {"role"}, "name": {"qbzd"}, "enabled": {"1"}}, true)
}

// fakeUnit stands in for systemd's view of odio-upgrade.service behind
// upgrade.Systemctl: `show` answers from it in systemd's order (Result and
// ExecMainStatus before ActiveState), `start` flips it to activating. Its
// invocation link under the units directory lives while it is activating,
// as systemd's does. Guarded: the watcher asks from its own goroutine.
type fakeUnit struct {
	mu     sync.Mutex
	active string // activating, inactive, failed
	result string
	code   int
	link   string
	calls  []string // every systemctl --user argv, joined
}

func (u *fakeUnit) set(active, result string, code int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active, u.result, u.code = active, result, code
	u.exportLink()
}

// exportLink is systemd's invocation link: there while activating, gone
// after — removed once the state is, so a `show` on the event sees the end.
func (u *fakeUnit) exportLink() {
	if u.link == "" {
		return
	}
	if u.active == "activating" {
		_ = os.Symlink("0123456789abcdef", u.link)
	} else {
		_ = os.Remove(u.link)
	}
}

// useUnit puts u behind upgrade.Systemctl. The tick behind inotify is
// pushed far off: only the link's removal may end the watcher within
// waitWatcherGone's patience; the poll stays quick for the no-directory case.
func (f *fixture) useUnit(u *fakeUnit) {
	f.unit = u
	u.mu.Lock()
	u.link = filepath.Join(f.svc.cfg.UnitsDir(), "invocation:"+upgrade.Unit)
	u.exportLink()
	u.mu.Unlock()
	oldSystemctl := upgrade.Systemctl
	upgrade.Systemctl = func(args ...string) (string, error) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.calls = append(u.calls, strings.Join(args, " "))
		switch args[0] {
		case "show":
			return fmt.Sprintf("Result=%s\nExecMainStatus=%d\nActiveState=%s\n", u.result, u.code, u.active), nil
		case "start":
			u.active = "activating"
			u.exportLink()
		}
		return "", nil
	}
	oldPoll, oldRecheck := upgrade.UnitPoll, upgrade.UnitRecheck
	upgrade.UnitPoll, upgrade.UnitRecheck = 20*time.Millisecond, time.Hour
	f.t.Cleanup(func() {
		upgrade.Systemctl = oldSystemctl
		upgrade.UnitPoll, upgrade.UnitRecheck = oldPoll, oldRecheck
	})
}

// starts is every `systemctl --user start …` the unit saw.
func (f *fixture) starts() (out []string) {
	f.unit.mu.Lock()
	defer f.unit.mu.Unlock()
	for _, call := range f.unit.calls {
		if strings.HasPrefix(call, "start ") {
			out = append(out, call)
		}
	}
	return out
}

func (f *fixture) waitWatcherGone() bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.svc.mu.Lock()
		watching := f.svc.watching
		f.svc.mu.Unlock()
		if !watching {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestApplyNowStartsAndWatchesTheUserUnit(t *testing.T) {
	f := newFixture(t)
	f.makeUpgradePending()
	unit := &fakeUnit{active: "inactive", result: "success"}
	f.useUnit(unit)
	// an action waiting for its link to be followed is not an upgrade
	f.installQbzd()
	f.post("/components/action", url.Values{"kind": {"role"}, "name": {"qbzd"}, "action": {"login"}}, true)
	_, body := f.post("/upgrade", url.Values{}, true)
	wants(t, body, "Upgrade started.")
	_, body = f.get("/")
	wants(t, body, "Upgrading…", `<button class="primary" type="button" disabled>`)
	if starts := f.starts(); len(starts) != 1 || starts[0] != "start --no-block odio-upgrade.service" {
		t.Errorf("starts = %v", starts)
	}
	// a second click is not a second start
	_, body = f.post("/upgrade", url.Values{}, true)
	wants(t, body, "Upgrade already running.")
	if len(f.starts()) != 1 || len(f.spawns) != 1 {
		t.Errorf("starts = %v, spawns = %v", f.starts(), f.spawns)
	}
	// the unit ends having installed qbzd: the watcher notes it, the stream
	// says so, the card and the row show it
	s := f.openStream()
	wants(t, s.until("event: dac"), "Upgrading…")
	f.installQbzd()
	unit.set("inactive", "success", 0)
	batch := s.until(">Installed<")
	wants(t, batch, "event: upgrade\ndata: <section id=\"upgrade\"", "Upgrade: Done.", "Apply now",
		"event: components\n", `<div id="row-role-qbzd" class="card">`)
	if strings.Contains(batch, "Upgrade started.") {
		t.Error("the POST's notice is not the stream's to carry")
	}
	if strings.Contains(batch, "event: dac\n") {
		t.Error("an upgrade's end re-sent the DAC section")
	}
	if !f.waitWatcherGone() {
		t.Fatal("watcher still running")
	}
	_, body = f.get("/")
	wants(t, body, `<p class="hint outcome">Upgrade: Done.</p>`, "Apply now")
	if strings.Contains(body, "Upgrading…") {
		t.Error("page still shows the run after its end")
	}
	wants(t, f.logs.String(), "upgrade: started odio-upgrade.service", "odio-upgrade.service inactive after")
}

func TestUpgradeFailureIsTheNote(t *testing.T) {
	f := newFixture(t)
	f.makeUpgradePending()
	unit := &fakeUnit{active: "inactive", result: "success"}
	f.useUnit(unit)
	f.post("/upgrade", url.Values{}, true)
	unit.set("failed", "exit-code", 1) // sudoers said no, or the playbook died
	if !f.waitWatcherGone() {
		t.Fatal("watcher still running")
	}
	_, body := f.get("/")
	wants(t, body, `<p class="hint outcome err">Upgrade: Failed (exit-code, exit 1).</p>`, "Apply now")
}

func TestUpgradeStartedElsewhereIsWatchedNeverStarted(t *testing.T) {
	f := newFixture(t)
	f.makeUpgradePending()
	// odio-api started the unit, or this process restarted under it
	unit := &fakeUnit{active: "activating"}
	f.useUnit(unit)
	_, body := f.get("/")
	wants(t, body, "Upgrading…")
	f.get("/") // watched once, not probed again
	if len(f.starts()) != 0 || len(f.spawns) != 0 {
		t.Errorf("a render started something: starts = %v, spawns = %v", f.starts(), f.spawns)
	}
	wants(t, f.logs.String(), "odio-upgrade.service is activating, watching it")
	unit.set("failed", "exit-code", 2)
	if !f.waitWatcherGone() {
		t.Fatal("watcher still running")
	}
	_, body = f.get("/")
	wants(t, body, "Upgrade: Failed (exit-code, exit 2).")
}

func TestWithoutUnitsDirTheUnitIsPolled(t *testing.T) {
	f := newFixture(t)
	f.makeUpgradePending()
	f.svc.cfg.RuntimeDir = filepath.Join(f.dir, "nowhere")
	unit := &fakeUnit{active: "inactive", result: "success"}
	f.useUnit(unit)
	f.post("/upgrade", url.Values{}, true)
	unit.set("failed", "exit-code", 1)
	if !f.waitWatcherGone() {
		t.Fatal("watcher still running")
	}
	wants(t, f.logs.String(), "cannot watch", "polling odio-upgrade.service", "odio-upgrade.service failed after")
	_, body := f.get("/")
	wants(t, body, "Upgrade: Failed (exit-code, exit 1).")
}

func TestUnitFailedBeforeThisProcessIsShown(t *testing.T) {
	f := newFixture(t)
	f.makeUpgradePending()
	f.useUnit(&fakeUnit{active: "failed", result: "exit-code", code: 1})
	_, body := f.get("/")
	wants(t, body, "Upgrade: Failed (exit-code, exit 1).", "Apply now")
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
	wants(t, body, "A reboot is required", `<form hx-post="/reboot"`, "Reboot now")
	// the button asks logind as the user (odios' polkit rule), no sudo
	_, body = f.post("/reboot", url.Values{}, true)
	wants(t, body, `<div class="banner ok">Rebooting`)
	last := f.userCalls[len(f.userCalls)-1]
	if strings.Join(last, " ") != "systemctl reboot" || len(f.privileged) != 1 {
		t.Errorf("userCalls = %v, privileged = %v", f.userCalls, f.privileged)
	}
	wants(t, f.logs.String(), "reboot requested")
}

func TestRebootIsNotOfferedWithoutTheFlag(t *testing.T) {
	f := newFixture(t)
	_, body := f.get("/")
	if strings.Contains(body, "/reboot") {
		t.Error("reboot button without a pending change")
	}
}

func TestDacReapplyingTheCurrentSelectionIsANoOp(t *testing.T) {
	f := newFixture(t)
	f.post("/dac", url.Values{"id": {"hifiberry-dacplus-std"}}, true)
	calls := len(f.privileged)
	_, body := f.post("/dac", url.Values{"id": {"hifiberry-dacplus-std"}}, true)
	wants(t, body, "nothing to apply")
	if strings.Contains(body, "DAC set to") {
		t.Error("a no-op must not claim a change")
	}
	if len(f.privileged) != calls {
		t.Errorf("no-op escalated: %v", f.privileged[calls:])
	}
}

func TestDacSameIdOverUnmanagedConfigStillApplies(t *testing.T) {
	// config.txt says audio=on (current: onboard) but odioctl owns nothing
	// yet: applying "onboard" must take ownership of the block.
	f := newFixture(t)
	_, body := f.post("/dac", url.Values{"id": {"onboard"}}, true)
	wants(t, body, "DAC set to onboard", "reboot required")
	text, _ := dac.ReadConfig(f.configPath)
	if !dac.Parse(text).Managed {
		t.Error("block not taken over")
	}
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
