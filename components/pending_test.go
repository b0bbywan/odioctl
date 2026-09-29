package components

import (
	"maps"
	"slices"
	"testing"

	"github.com/b0bbywan/odioctl/state"
)

// settled is a state where every role and feature of release is installed:
// nothing pending until a test takes something out.
func settled() state.State {
	st := makeState()
	for name, meta := range release().Catalog {
		st.Roles[name] = "1"
		for f := range meta.Features {
			st.Features = append(st.Features, f)
		}
	}
	return st
}

func TestPendingRunsNothingWhenSettled(t *testing.T) {
	st := settled()
	if p := Pending(st, release()); p != nil {
		t.Errorf("Pending = %v", p)
	}
	if r := PendingRuns(st, release()); r != nil {
		t.Errorf("PendingRuns = %v, want nil: no odio_api run for nothing", r)
	}
}

func TestPendingRunsAFeatureByItsParent(t *testing.T) {
	st := settled()
	st.Features = without(st.Features, "tidal")
	if p := Pending(st, release()); !slices.Equal(p, []string{"feature:tidal"}) {
		t.Errorf("Pending = %v", p)
	}
	if r := PendingRuns(st, release()); !slices.Equal(r, []string{"odio_api", "upmpdcli"}) {
		t.Errorf("PendingRuns = %v", r)
	}
}

func TestPendingRunsAParentOnceForItsFeatures(t *testing.T) {
	st := settled()
	delete(st.Roles, "upmpdcli")
	st.Features = without(without(st.Features, "tidal"), "qobuz")
	want := []string{"role:upmpdcli", "feature:qobuz", "feature:tidal"}
	if p := Pending(st, release()); !slices.Equal(p, want) {
		t.Errorf("Pending = %v, want %v", p, want)
	}
	if r := PendingRuns(st, release()); !slices.Equal(r, []string{"odio_api", "upmpdcli"}) {
		t.Errorf("PendingRuns = %v", r)
	}
}

func TestPendingSkipsTheFeatureOfARemovedParent(t *testing.T) {
	st := settled()
	st.RolesExcluded = []string{"upmpdcli"}
	st.Features = without(st.Features, "tidal")
	if p := Pending(st, release()); p != nil {
		t.Errorf("Pending = %v, want nil", p)
	}
}

func TestNothingToRemoveWhenSettled(t *testing.T) {
	if r := RemovalsOf(settled(), release()); !r.Empty() || len(r.Refs()) != 0 {
		t.Errorf("Removals = %+v", r)
	}
}

// A feature goes by its own removal unless its role goes too.
func TestRemovalsLeaveOutTheFeaturesOfARemovedRole(t *testing.T) {
	st := settled()
	st.RolesExcluded = []string{"spotifyd", "upmpdcli"}
	st.FeaturesExcluded = []string{"mympd", "tidal"}
	r := RemovalsOf(st, release())
	if !slices.Equal(r.Roles, []string{"spotifyd", "upmpdcli"}) ||
		!maps.Equal(r.Features, map[string]string{"mympd": "mpd"}) {
		t.Errorf("Removals = %+v", r)
	}
	want := []string{"role:spotifyd", "role:upmpdcli", "feature:mympd"}
	if refs := r.Refs(); !slices.Equal(refs, want) {
		t.Errorf("Refs = %v, want %v", refs, want)
	}
}

// Excluded and never installed is not a removal.
func TestAnExcludedRoleNotInstalledIsNotRemoved(t *testing.T) {
	st := settled()
	delete(st.Roles, "spotifyd")
	st.RolesExcluded = []string{"spotifyd"}
	if r := RemovalsOf(st, release()); !r.Empty() {
		t.Errorf("Removals = %+v", r)
	}
}

func TestDropKeepsTheExclusionsAndTakesARolesFeatures(t *testing.T) {
	st := settled()
	st.RolesExcluded = []string{"upmpdcli"}
	st.FeaturesExcluded = []string{"mympd", "qobuz"}
	got := Drop(st, release(), RemovalsOf(st, release()))
	if _, ok := got.Roles["upmpdcli"]; ok {
		t.Error("upmpdcli should leave Roles")
	}
	for _, f := range []string{"mympd", "qobuz", "tidal", "upnpwebradios"} {
		if slices.Contains(got.Features, f) {
			t.Errorf("Features = %v, %s should be gone", got.Features, f)
		}
	}
	if !slices.Equal(got.RolesExcluded, st.RolesExcluded) ||
		!slices.Equal(got.FeaturesExcluded, st.FeaturesExcluded) {
		t.Errorf("exclusions = %v / %v", got.RolesExcluded, got.FeaturesExcluded)
	}
	if r := RemovalsOf(got, release()); !r.Empty() {
		t.Errorf("Removals after Drop = %+v", r)
	}
	if st.Roles["upmpdcli"] == "" {
		t.Error("original state mutated")
	}
}

func TestPendingSkipsTheFeatureOfAnExcludedParent(t *testing.T) {
	st := settled()
	delete(st.Roles, "upmpdcli")
	st.RolesExcluded = []string{"upmpdcli"}
	st.Features = without(st.Features, "tidal")
	if r := PendingRuns(st, release()); r != nil {
		t.Errorf("PendingRuns = %v, want nil", r)
	}
}
