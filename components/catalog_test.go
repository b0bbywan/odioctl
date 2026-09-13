package components

import (
	"reflect"
	"slices"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
)

// withCatalog is a release shipping mpd and newrole, a role odioctl's own
// catalog does not know, described by the manifest alone.
func withCatalog(meta manifest.RoleMeta) *manifest.Manifest {
	return &manifest.Manifest{
		Roles:   map[string]string{"mpd": "x", "qbzd": "x", "newrole": "x"},
		Catalog: map[string]manifest.RoleMeta{"newrole": meta},
	}
}

func listed(t *testing.T, comps []Component, name string) Component {
	t.Helper()
	for _, c := range comps {
		if c.Kind == Role && c.Name == name {
			return c
		}
	}
	t.Fatalf("role %q not listed", name)
	return Component{}
}

func TestManifestCatalogListsARoleTheLocalCatalogLacks(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	man := withCatalog(manifest.RoleMeta{Description: "From the release", Group: "Playback", OptIn: true})
	c := listed(t, List(st, man), "newrole")
	if c.Label != "newrole" || c.Description != "From the release" || c.Group != "Playback" || c.Status != Excluded {
		t.Errorf("newrole = %+v", c)
	}
	if p := Pending(st, man); slices.Contains(p, "role:newrole") {
		t.Errorf("Pending = %v, an opt-in role is off until enabled", p)
	}
}

func TestManifestOptInRoleEnablesAsAnExplicitInstall(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	man := withCatalog(manifest.RoleMeta{Group: "System", OptIn: true})
	if k := kindOf(st, man, "newrole"); k != Role {
		t.Fatalf("kindOf = %q", k)
	}
	got, err := Set(st, man, Role, "newrole", true)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.Roles["newrole"]; !ok || v != RequestedVersion {
		t.Errorf("Roles = %v", got.Roles)
	}
	if !slices.Contains(Pending(got, man), "role:newrole") {
		t.Errorf("Pending = %v", Pending(got, man))
	}
}

func TestManifestRoleWithoutOptInGoesPending(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	man := withCatalog(manifest.RoleMeta{Group: "System"})
	if !slices.Contains(Pending(st, man), "role:newrole") {
		t.Errorf("Pending = %v", Pending(st, man))
	}
}

func TestManifestOverridesDescriptionGroupOptInButNotLabelOrActions(t *testing.T) {
	man := withCatalog(manifest.RoleMeta{})
	man.Catalog["qbzd"] = manifest.RoleMeta{Description: "Released", Group: "System"}
	info, ok := roleInfo(man, "qbzd")
	if !ok || info.Label != "Qobuz Connect" || info.Description != "Released" ||
		info.Group != "System" || info.OptIn || len(info.Actions) != 1 {
		t.Errorf("qbzd = %+v", info)
	}
}

func TestManifestUnknownGroupIsIgnored(t *testing.T) {
	man := withCatalog(manifest.RoleMeta{Group: "Nope"})
	man.Catalog["qbzd"] = manifest.RoleMeta{Group: "Nope"}
	comps := List(makeState(), man)
	if g := listed(t, comps, "newrole").Group; g != Groups[len(Groups)-1] {
		t.Errorf("newrole group = %q", g)
	}
	if g := listed(t, comps, "qbzd").Group; g != "Streaming" {
		t.Errorf("qbzd group = %q", g)
	}
}

func TestManifestWithoutCatalogIsTheLocalCatalog(t *testing.T) {
	man := &manifest.Manifest{Roles: map[string]string{"qbzd": "x"}}
	remote, _ := roleInfo(man, "qbzd")
	local, _ := roleInfo(nil, "qbzd")
	if !reflect.DeepEqual(remote, local) {
		t.Errorf("roleInfo = %+v, want %+v", remote, local)
	}
}
