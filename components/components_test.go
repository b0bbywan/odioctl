package components

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

func makeState() state.State {
	return state.State{
		Odios:            "2026.5.0",
		InstallMode:      "image",
		TargetUser:       "odio",
		Roles:            map[string]string{},
		RolesExcluded:    []string{},
		Features:         []string{},
		FeaturesExcluded: []string{},
		ReleaseHistory:   []string{"2026.5.0"},
	}
}

func byName(comps []Component) map[[2]string]Component {
	out := map[[2]string]Component{}
	for _, c := range comps {
		out[[2]string{string(c.Kind), c.Name}] = c
	}
	return out
}

func role(t *testing.T, st state.State, name string) Component {
	t.Helper()
	for _, c := range List(st, release()) {
		if c.Kind == Role && c.Name == name {
			return c
		}
	}
	t.Fatalf("role %q not listed", name)
	return Component{}
}

func wantComponentError(t *testing.T, err error) {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *components.Error", err)
	}
}

func TestStatuses(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "common": "1"}
	st.RolesExcluded = []string{"spotifyd"}
	st.Features = []string{"tidal"}
	st.FeaturesExcluded = []string{"mympd"}
	m := byName(List(st, release()))
	if c := m[[2]string{"role", "mpd"}]; c.Status != Installed || c.InstalledVersion != "1" {
		t.Errorf("mpd = %+v", c)
	}
	if c := m[[2]string{"role", "spotifyd"}]; c.Status != Excluded || c.Enabled() {
		t.Errorf("spotifyd = %+v", c)
	}
	if c := m[[2]string{"role", "snapclient"}]; c.Status != Default || !c.Enabled() {
		t.Errorf("snapclient = %+v", c)
	}
	if m[[2]string{"role", "common"}].Toggleable {
		t.Error("common should not be toggleable")
	}
	if c := m[[2]string{"feature", "tidal"}]; c.Status != Installed || c.Parent != "upmpdcli" {
		t.Errorf("tidal = %+v", c)
	}
	if m[[2]string{"feature", "mympd"}].Status != Excluded {
		t.Error("mympd should be excluded")
	}
	if m[[2]string{"feature", "qobuz"}].Status != Default {
		t.Error("qobuz should be default")
	}
}

func TestUnknownNamesFromStateAreListed(t *testing.T) {
	// A role the release does not describe must show up if state.json names it.
	st := makeState()
	st.Roles = map[string]string{"newthing": "1"}
	st.FeaturesExcluded = []string{"newplugin"}
	m := byName(List(st, release()))
	if c := m[[2]string{"role", "newthing"}]; c.Label != "Newthing" || c.Description != "" {
		t.Errorf("newthing = %+v", c)
	}
	if m[[2]string{"feature", "newplugin"}].Status != Excluded {
		t.Error("newplugin should be excluded")
	}
}

// Roles by group then label, then the features.
func TestListOrder(t *testing.T) {
	var got []string
	for _, c := range List(makeState(), shipping("pulseaudio", "bluetooth", "mpd", "spotifyd", "shairport_sync")) {
		got = append(got, c.Name)
	}
	want := []string{"bluetooth", "mpd", "shairport_sync", "spotifyd", "mympd"}
	if !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

// Without a release nothing says what exists nor what odio requires: what
// state.json names is shown, nothing is toggled.
func TestWithoutAReleaseStateIsShownAndNothingToggles(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "spotifyd": "1"}
	st.Features = []string{"tidal"}
	comps := List(st, nil)
	if n := names(comps); len(n) != 3 || !n[[2]string{"role", "spotifyd"}] || !n[[2]string{"feature", "tidal"}] {
		t.Errorf("List = %v", n)
	}
	for _, c := range comps {
		if c.Toggleable {
			t.Errorf("%s toggleable", c.Name)
		}
	}
	_, err := Set(st, nil, Role, "spotifyd", false)
	wantComponentError(t, err)
	if p := Pending(st, nil); p != nil {
		t.Errorf("Pending = %v", p)
	}
}

func names(comps []Component) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, c := range comps {
		out[[2]string{string(c.Kind), c.Name}] = true
	}
	return out
}

func TestShippedHidesWhatTheReleaseLacks(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "upmpdcli": "1"}
	n := names(List(st, shipping("mpd", "upmpdcli")))
	for _, absent := range [][2]string{{"role", "qbzd"}, {"role", "spotifyd"}} {
		if n[absent] {
			t.Errorf("%v should be hidden", absent)
		}
	}
	for _, present := range [][2]string{{"role", "mpd"}, {"feature", "tidal"}} {
		if !n[present] {
			t.Errorf("%v should be listed", present)
		}
	}
}

func TestShippedKeepsWhatStateCarries(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"qbzd": ""}
	st.RolesExcluded = []string{"spotifyd"}
	st.Features = []string{"mympd"}
	n := names(List(st, shipping("mpd")))
	for _, present := range [][2]string{{"role", "qbzd"}, {"role", "spotifyd"}, {"feature", "mympd"}} {
		if !n[present] {
			t.Errorf("%v should be listed", present)
		}
	}
}

func TestShippedDropsFeaturesOfADroppedParent(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	n := names(List(st, shipping("mpd")))
	if n[[2]string{"feature", "tidal"}] {
		t.Error("tidal should follow upmpdcli out")
	}
	if !n[[2]string{"feature", "mympd"}] {
		t.Error("mympd should stay with mpd")
	}
}

func TestDisableRoleMovesItOutOfRolesAndIntoExcluded(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "spotifyd": "1"}
	got, err := Set(st, release(), Role, "spotifyd", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Roles["spotifyd"]; ok {
		t.Error("spotifyd should leave Roles")
	}
	if !slices.Equal(got.RolesExcluded, []string{"spotifyd"}) {
		t.Errorf("RolesExcluded = %v", got.RolesExcluded)
	}
	if _, ok := st.Roles["spotifyd"]; !ok {
		t.Error("original state mutated")
	}
}

func TestEnableRoleOnlyClearsExclusion(t *testing.T) {
	st := makeState()
	st.RolesExcluded = []string{"snapclient", "spotifyd"}
	got, err := Set(st, release(), Role, "spotifyd", true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.RolesExcluded, []string{"snapclient"}) {
		t.Errorf("RolesExcluded = %v", got.RolesExcluded)
	}
	if _, ok := got.Roles["spotifyd"]; ok {
		t.Error("a default-Y role must not be recorded in Roles")
	}
}

func TestDisableFeature(t *testing.T) {
	st := makeState()
	st.Features = []string{"qobuz", "tidal"}
	got, err := Set(st, release(), Feature, "tidal", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Features, []string{"qobuz"}) ||
		!slices.Equal(got.FeaturesExcluded, []string{"tidal"}) {
		t.Errorf("features = %v / %v", got.Features, got.FeaturesExcluded)
	}
}

func TestEnableFeatureClearsExclusion(t *testing.T) {
	st := makeState()
	st.FeaturesExcluded = []string{"mympd"}
	got, err := Set(st, release(), Feature, "mympd", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FeaturesExcluded) != 0 || len(got.Features) != 0 {
		t.Errorf("features = %v / %v", got.Features, got.FeaturesExcluded)
	}
}

func TestSetIdempotent(t *testing.T) {
	st := makeState()
	st.RolesExcluded = []string{"spotifyd"}
	got, err := Set(st, release(), Role, "spotifyd", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.RolesExcluded, []string{"spotifyd"}) {
		t.Errorf("RolesExcluded = %v", got.RolesExcluded)
	}
}

func TestInfraRoleRejected(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"common": "1"}
	_, err := Set(st, release(), Role, "common", false)
	wantComponentError(t, err)
}

// Required is shown, not hidden: a row with its version and no toggle. One
// install.sh answered N to can still be brought back.
func TestRequiredRolesAreListedWithoutAToggle(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"common": "1", "upgrade": "1", "mpd": "1", "odio_api": "1", "pulseaudio": "1"}
	for k, c := range byName(List(st, release())) {
		if k[0] == "role" && slices.Contains([]string{"common", "upgrade", "mpd", "odio_api", "pulseaudio"}, k[1]) {
			if c.Toggleable || c.Status != Installed {
				t.Errorf("%s = %+v", k[1], c)
			}
		}
	}
	st.Roles = map[string]string{}
	st.RolesExcluded = []string{"odio_api"}
	if c := byName(List(st, release()))[[2]string{"role", "odio_api"}]; !c.Toggleable {
		t.Errorf("declined odio_api = %+v", c)
	}
}

func TestUnknownKindAndNameRejected(t *testing.T) {
	if _, err := Set(makeState(), release(), "plugin", "mpd", true); err == nil {
		t.Error("want error for unknown kind")
	}
	_, err := Set(makeState(), release(), Role, "nope", false)
	wantComponentError(t, err)
	_, err = Set(makeState(), release(), Feature, "nope", true)
	wantComponentError(t, err)
}

func TestUnknownNamePresentInStateAccepted(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"newthing": "1"}
	got, err := Set(st, release(), Role, "newthing", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.RolesExcluded, []string{"newthing"}) {
		t.Errorf("RolesExcluded = %v", got.RolesExcluded)
	}
}

// withAction swaps an action onto mpd for one test: the mechanism must not
// depend on which components declare an action today.
func withAction(t *testing.T, action Action) {
	t.Helper()
	old, had := roleActions["mpd"]
	roleActions["mpd"] = []Action{action}
	t.Cleanup(func() {
		if had {
			roleActions["mpd"] = old
		} else {
			delete(roleActions, "mpd")
		}
	})
}

var testAction = Action{
	ID:          "login",
	Label:       "Log in",
	Description: "Sign in to the service",
	Argv:        []string{"acmed", "login", "--callback-host", "{host}"},
	LinkScheme:  "https://",
	LinkLabel:   "Open the sign-in page",
}

func fillArgv(argv []string, key, value string) []string {
	out := make([]string, len(argv))
	for i, p := range argv {
		out[i] = strings.ReplaceAll(p, "{"+key+"}", value)
	}
	return out
}

func TestHostIsTheOnlyThingARequestFillsIn(t *testing.T) {
	got := fillArgv(testAction.Argv, "host", "odio.local")
	if !slices.Equal(got, []string{"acmed", "login", "--callback-host", "odio.local"}) {
		t.Errorf("argv = %v", got)
	}
}

func TestComponentsWithoutActionsHaveNone(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1", "newthing": "1"}
	if len(role(t, st, "mpd").Actions) != 0 || len(role(t, st, "newthing").Actions) != 0 {
		t.Error("want no actions")
	}
}

func TestTheActionReachesTheComponent(t *testing.T) {
	withAction(t, testAction)
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	got := role(t, st, "mpd").Actions
	if len(got) != 1 || got[0].ID != "login" {
		t.Errorf("actions = %v", got)
	}
}

func TestFindActionOnlyResolvesOdioctlsActions(t *testing.T) {
	withAction(t, testAction)
	if a, ok := FindAction(Role, "mpd", "login"); !ok || a.ID != "login" {
		t.Errorf("FindAction = %v, %v", a, ok)
	}
	for _, tc := range []struct {
		kind           Kind
		name, actionID string
	}{
		{Role, "mpd", "rm"},
		{Role, "spotifyd", "login"},
		{Feature, "mpd", "login"},
		{Role, "nope", "login"},
	} {
		if _, ok := FindAction(tc.kind, tc.name, tc.actionID); ok {
			t.Errorf("FindAction(%v, %q, %q) resolved", tc.kind, tc.name, tc.actionID)
		}
	}
}

func TestTidalLoginRunsUpmpdcliHelperAgainstTheUserHome(t *testing.T) {
	login, ok := FindAction(Feature, "tidal", "login")
	if !ok {
		t.Fatal("tidal login not found")
	}
	// argv runs without a shell, so the home comes from {home}, not from ~
	got := fillArgv(login.Argv, "home", "/home/alice")
	want := []string{
		"python3", "-u",
		"/usr/share/upmpdcli/cdplugins/tidal/get_credentials.py",
		"-f", "/home/alice/.cache/upmpdcli/tidal/oauth2.credentials.json",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %v", got)
	}
}

func TestEveryPythonActionIsUnbuffered(t *testing.T) {
	// The server lifts the login URL off a stdout pipe while the child keeps
	// running; a buffered python child would never deliver the line.
	check := func(kind Kind, name string, actions []Action) {
		for _, a := range actions {
			base := a.Argv[0]
			if i := strings.LastIndexByte(base, '/'); i >= 0 {
				base = base[i+1:]
			}
			if (base == "python" || base == "python3") && (len(a.Argv) < 2 || a.Argv[1] != "-u") {
				t.Errorf("%s:%s:%s runs python without -u", kind, name, a.ID)
			}
		}
	}
	for name, actions := range roleActions {
		check(Role, name, actions)
	}
	for name, actions := range featureActions {
		check(Feature, name, actions)
	}
}

func TestQbzdLoginTakesTheCallbackHost(t *testing.T) {
	login, ok := FindAction(Role, "qbzd", "login")
	if !ok {
		t.Fatal("qbzd login not found")
	}
	// --callback-host is what sends the OAuth redirect back to odio
	got := fillArgv(login.Argv, "host", "odio.local")
	if !slices.Equal(got, []string{"qbzd", "login", "--callback-host", "odio.local"}) {
		t.Errorf("argv = %v", got)
	}
	if !strings.Contains(login.Label, "Qobuz") || !strings.Contains(login.LinkLabel, "Qobuz") {
		t.Errorf("labels = %q / %q", login.Label, login.LinkLabel)
	}
}

func TestOptInAbsentFromBothListsReadsAsOff(t *testing.T) {
	// An odio installed before qbzd existed has it in neither list. install.sh
	// would answer N, so the row must not promise an install (nor go pending).
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	st.Features = []string{"mympd"}
	c := role(t, st, "qbzd")
	if c.Status != Excluded || c.Enabled() {
		t.Errorf("qbzd = %+v", c)
	}
	if p := Pending(st, shipping("mpd", "qbzd")); len(p) != 0 {
		t.Errorf("Pending = %v", p)
	}
}

func TestOptInEnableRecordsAnExplicitInstall(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	st.RolesExcluded = []string{"qbzd"}
	got, err := Set(st, release(), Role, "qbzd", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RolesExcluded) != 0 || got.Roles["qbzd"] != RequestedVersion {
		t.Errorf("got = %+v", got)
	}
	c := role(t, got, "qbzd")
	if c.Status != Default || !c.Enabled() || c.InstalledVersion != "" {
		t.Errorf("qbzd = %+v", c) // placeholder version never shown
	}
	if !slices.Contains(Pending(got, shipping("mpd", "qbzd")), "role:qbzd") {
		t.Error("role:qbzd should be pending")
	}
	if _, ok := st.Roles["qbzd"]; ok {
		t.Error("original state mutated")
	}
}

func TestOptInEnableIsIdempotentAndNeverClobbersARealVersion(t *testing.T) {
	once, _ := Set(makeState(), release(), Role, "qbzd", true)
	twice, _ := Set(once, release(), Role, "qbzd", true)
	if twice.Roles["qbzd"] != "" || len(twice.Roles) != 1 {
		t.Errorf("Roles = %v", twice.Roles)
	}
	installed := makeState()
	installed.Roles = map[string]string{"qbzd": "2026.9.0b1"}
	again, _ := Set(installed, release(), Role, "qbzd", true)
	if again.Roles["qbzd"] != "2026.9.0b1" {
		t.Errorf("Roles = %v", again.Roles)
	}
}

func TestOptInDisableAfterEnableRoundTrips(t *testing.T) {
	enabled, _ := Set(makeState(), release(), Role, "qbzd", true)
	back, err := Set(enabled, release(), Role, "qbzd", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Roles["qbzd"]; ok {
		t.Error("qbzd should leave Roles")
	}
	if !slices.Equal(back.RolesExcluded, []string{"qbzd"}) {
		t.Errorf("RolesExcluded = %v", back.RolesExcluded)
	}
	if role(t, back, "qbzd").Status != Excluded {
		t.Error("status should be excluded")
	}
}

func TestInstalledQbzdLooksLikeAnyOtherRole(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"qbzd": "2026.9.0b1"}
	c := role(t, st, "qbzd")
	if c.Status != Installed || c.InstalledVersion != "2026.9.0b1" {
		t.Errorf("qbzd = %+v", c)
	}
	if p := Pending(st, shipping("qbzd")); len(p) != 0 {
		t.Errorf("Pending = %v", p)
	}
}

func TestLabelOf(t *testing.T) {
	if got := LabelOf(release(), Feature, "tidal"); got != "Tidal" {
		t.Errorf("LabelOf = %q", got)
	}
	// a catalog entry without a label, and no release at all: the name
	man := &manifest.Manifest{Catalog: map[string]manifest.RoleMeta{"shairport_sync": {Description: "AirPlay"}}}
	if got := LabelOf(man, Role, "shairport_sync"); got != "Shairport-sync" {
		t.Errorf("LabelOf = %q", got)
	}
	if got := LabelOf(nil, Role, "mpd"); got != "Mpd" {
		t.Errorf("LabelOf = %q", got)
	}
}

// A bare name is a role when the catalog or state.json says so, a feature
// otherwise: an unknown name then fails as an unknown feature.
func TestKindOf(t *testing.T) {
	st := makeState()
	st.Roles["localrole"] = "1"
	st.RolesExcluded = []string{"skippedrole"}
	for name, want := range map[string]Kind{
		"mpd":         Role,
		"localrole":   Role,
		"skippedrole": Role,
		"tidal":       Feature,
		"newthing":    Feature,
	} {
		if got := kindOf(st, release(), name); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", name, got, want)
		}
	}
}
