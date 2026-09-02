package upgrade

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWarnFeaturesUnknown(t *testing.T) {
	st := makeState()
	st.Features = []string{"tidal"}
	if w := warnFeaturesUnknown(st); w != "" {
		t.Errorf("warning = %q", w)
	}
	st.Features = []string{"tidal", "newthing"}
	if w := warnFeaturesUnknown(st); !strings.Contains(w, "newthing") {
		t.Errorf("warning = %q", w)
	}
}

func TestFeaturesNoOverlap(t *testing.T) {
	st := makeState()
	st.Features = []string{"tidal"}
	st.FeaturesExcluded = []string{"mympd"}
	if c := checkFeaturesNoOverlap(st); c != "" {
		t.Errorf("check = %q", c)
	}
	st.FeaturesExcluded = []string{"tidal"}
	if c := checkFeaturesNoOverlap(st); !strings.Contains(c, "tidal") {
		t.Errorf("check = %q", c)
	}
}

func TestRolesNoOverlap(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	st.RolesExcluded = []string{"mpd"}
	if c := checkRolesNoOverlap(st); !strings.Contains(c, "mpd") {
		t.Errorf("check = %q", c)
	}
}

func TestHistoryMatchesOdios(t *testing.T) {
	st := makeState() // history ends with st.Odios
	if c := checkHistoryMatchesOdios(st); c != "" {
		t.Errorf("check = %q", c)
	}
	st.ReleaseHistory = []string{}
	if c := checkHistoryMatchesOdios(st); c != "" {
		t.Errorf("empty history: %q", c)
	}
	st.ReleaseHistory = []string{"2026.4.0"}
	if c := checkHistoryMatchesOdios(st); !strings.Contains(c, "2026.4.0") {
		t.Errorf("check = %q", c)
	}
}

func TestExpectedVersion(t *testing.T) {
	st := makeState() // odios 2026.5.0
	if c := checkExpectedVersion(st, "2026.5.0"); c != "" {
		t.Errorf("exact match: %q", c)
	}
	if c := checkExpectedVersion(st, "2026.6.0"); c == "" {
		t.Error("mismatch should fail")
	}
	// pr tags accept any git-describe, reject garbage.
	st.Odios = "2026.4.2b2-20-g7c1f6c4"
	if c := checkExpectedVersion(st, "pr-84"); c != "" {
		t.Errorf("git-describe for pr: %q", c)
	}
	st.Odios = "garbage"
	if c := checkExpectedVersion(st, "pr-84"); c == "" {
		t.Error("garbage for pr should fail")
	}
}

func TestRunVerify(t *testing.T) {
	d := t.TempDir()
	path := writeState(t, d, makeState())
	if rc := RunVerify(os.Stderr, path, "2026.5.0"); rc != 0 {
		t.Errorf("valid state: rc = %d", rc)
	}

	st := makeState()
	st.Features = []string{"newthing"}
	p := writeState(t, t.TempDir(), st)
	var stderr bytes.Buffer
	if rc := RunVerify(&stderr, p, ""); rc != 0 {
		t.Errorf("unknown feature: rc = %d", rc)
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Errorf("stderr = %q", stderr.String())
	}

	stderr.Reset()
	if rc := RunVerify(&stderr, path, "2026.6.0"); rc != 1 {
		t.Errorf("check failure: rc = %d", rc)
	}

	if rc := RunVerify(os.Stderr, "/nonexistent/state.json", ""); rc != 2 {
		t.Errorf("missing state: rc = %d", rc)
	}

	bad := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(bad, []byte(`{"odios": "x"}`), 0o644)
	if rc := RunVerify(&stderr, bad, ""); rc != 1 {
		t.Errorf("invalid schema: rc = %d", rc)
	}
}
