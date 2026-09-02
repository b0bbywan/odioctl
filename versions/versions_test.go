package versions

import "testing"

func TestPhaseOrdering(t *testing.T) {
	// 2026.5.0 > 2026.5.0rc1 > 2026.5.0b1 > 2026.5.0a1 — the phase axis is
	// what lets odioctl tell "ship-ready" from "iterating".
	for _, tc := range [][2]string{
		{"2026.5.0", "2026.5.0rc1"},
		{"2026.5.0rc1", "2026.5.0b1"},
		{"2026.5.0b1", "2026.5.0a1"},
	} {
		if Compare(tc[0], tc[1]) <= 0 {
			t.Errorf("Compare(%q, %q) = %d, want > 0", tc[0], tc[1], Compare(tc[0], tc[1]))
		}
	}
}

func TestDevCommitsBreakTiesWithinAPhase(t *testing.T) {
	// build-manifest stamps `<base>-<N>-g<sha>` on commits past the tag —
	// smart-upgrade must treat those as newer than the bare tag.
	if Compare("2026.5.0b1-3-gabc1234", "2026.5.0b1") <= 0 {
		t.Error("dev-commit suffix should compare above the bare tag")
	}
}

func TestUnparseableIsLowest(t *testing.T) {
	for _, v := range []string{"garbage", "latest"} {
		if IsValid(v) {
			t.Errorf("IsValid(%q) = true, want false", v)
		}
	}
	if Compare("garbage", "2026.4.0a1") >= 0 {
		t.Error("unparseable should compare below any real version")
	}
}

func TestEqualVersionsAreEqual(t *testing.T) {
	if Compare("2026.5.0", "2026.5.0") != 0 {
		t.Error("equal versions should compare equal")
	}
}

func TestIsDowngrade(t *testing.T) {
	tests := []struct {
		target, stateOdios string
		want               bool
	}{
		{"2026.4.0", "2026.5.0", true},
		{"2026.5.0", "2026.4.0", false},
		{"2026.5.0", "2026.5.0", false},
		{"2026.5.0", "", false},
		// "latest" parses to [0]; refuse-on-parse-miss would be wrong here.
		{"latest", "2026.5.0", false},
		{"2026.5.0", "garbage", false},
		// a target on the bare tag is older than the dev-commit suffix.
		{"2026.5.0b1", "2026.5.0b1-3-gabc1234", true},
	}
	for _, tc := range tests {
		if got := IsDowngrade(tc.target, tc.stateOdios); got != tc.want {
			t.Errorf("IsDowngrade(%q, %q) = %v, want %v", tc.target, tc.stateOdios, got, tc.want)
		}
	}
}

func TestRoleUpToDate(t *testing.T) {
	tests := []struct {
		installed, target, stateOdios string
		want                          bool
	}{
		{"", "2026.5.0", "2026.5.0", false},
		{"2026.5.0", "", "2026.5.0", false},
		{"2026.4.0", "2026.5.0", "2026.5.0", false},
		// target ahead of state.odios is not trusted.
		{"2026.5.0b1", "2026.5.0b1", "2026.4.2b2-8-gabc", false},
		{"2026.5.0", "2026.5.0", "2026.5.0", true},
	}
	for _, tc := range tests {
		if got := RoleUpToDate(tc.installed, tc.target, tc.stateOdios); got != tc.want {
			t.Errorf("RoleUpToDate(%q, %q, %q) = %v, want %v",
				tc.installed, tc.target, tc.stateOdios, got, tc.want)
		}
	}
}
