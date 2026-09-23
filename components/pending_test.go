package components

import (
	"slices"
	"testing"

	"github.com/b0bbywan/odioctl/state"
)

// settled is a state where every catalog role and feature is installed:
// nothing pending until a test takes something out.
func settled() state.State {
	st := makeState()
	for _, e := range roleCatalog {
		st.Roles[e.name] = "1"
	}
	for _, e := range featureCatalog {
		st.Features = append(st.Features, e.name)
	}
	return st
}

func TestPendingRunsNothingWhenSettled(t *testing.T) {
	st := settled()
	if p := Pending(st, nil); p != nil {
		t.Errorf("Pending = %v", p)
	}
	if r := PendingRuns(st, nil); r != nil {
		t.Errorf("PendingRuns = %v, want nil: no odio_api run for nothing", r)
	}
}

func TestPendingRunsAFeatureByItsParent(t *testing.T) {
	st := settled()
	st.Features = without(st.Features, "tidal")
	if p := Pending(st, nil); !slices.Equal(p, []string{"feature:tidal"}) {
		t.Errorf("Pending = %v", p)
	}
	if r := PendingRuns(st, nil); !slices.Equal(r, []string{"odio_api", "upmpdcli"}) {
		t.Errorf("PendingRuns = %v", r)
	}
}

func TestPendingRunsAParentOnceForItsFeatures(t *testing.T) {
	st := settled()
	delete(st.Roles, "upmpdcli")
	st.Features = without(without(st.Features, "tidal"), "qobuz")
	want := []string{"role:upmpdcli", "feature:tidal", "feature:qobuz"}
	if p := Pending(st, nil); !slices.Equal(p, want) {
		t.Errorf("Pending = %v, want %v", p, want)
	}
	if r := PendingRuns(st, nil); !slices.Equal(r, []string{"odio_api", "upmpdcli"}) {
		t.Errorf("PendingRuns = %v", r)
	}
}

func TestPendingSkipsTheFeatureOfAnExcludedParent(t *testing.T) {
	st := settled()
	delete(st.Roles, "upmpdcli")
	st.RolesExcluded = []string{"upmpdcli"}
	st.Features = without(st.Features, "tidal")
	if r := PendingRuns(st, nil); r != nil {
		t.Errorf("PendingRuns = %v, want nil", r)
	}
}
