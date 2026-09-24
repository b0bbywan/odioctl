package components

import (
	"slices"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
)

// withCatalog is a release shipping mpd, qbzd and newrole, described by meta.
func withCatalog(meta manifest.RoleMeta) *manifest.Manifest {
	man := shipping("mpd", "qbzd")
	meta.Version = "x"
	man.Catalog["newrole"] = meta
	return man
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

func TestTheCatalogListsARoleOdioctlHasNeverHeardOf(t *testing.T) {
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	man := withCatalog(manifest.RoleMeta{Description: "From the release", Group: "Playback", OptIn: true})
	c := listed(t, List(st, man), "newrole")
	if c.Description != "From the release" || c.Group != "Playback" || c.Status != Excluded {
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

func TestTheCatalogDescribesARoleOdioctlGivesItsActions(t *testing.T) {
	man := withCatalog(manifest.RoleMeta{})
	man.Catalog["qbzd"] = manifest.RoleMeta{Description: "Released", Group: "System"}
	info, ok := roleInfo(man, "qbzd")
	if !ok || info.Description != "Released" ||
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
	if g := listed(t, comps, "qbzd").Group; g != Groups[len(Groups)-1] {
		t.Errorf("qbzd group = %q", g)
	}
}

// What the release ships but holds out of its catalog (pipewire, until
// odioctl can offer it) is not offered here either.
func TestAShippedRoleOutOfTheCatalogIsNotListed(t *testing.T) {
	man := withCatalog(manifest.RoleMeta{})
	man.Roles = map[string]string{"pipewire": "x"} // for the odioctl before the catalog
	if lists(List(makeState(), man), "pipewire") {
		t.Error("pipewire listed")
	}
}

func TestAFeatureIsTheCatalogsUnderItsRole(t *testing.T) {
	man := withCatalog(manifest.RoleMeta{Features: map[string]manifest.FeatureMeta{"newplugin": {Description: "Plugged"}}})
	info, ok := featureInfo(man, "newplugin")
	if !ok || info.Parent != "newrole" || info.Description != "Plugged" {
		t.Errorf("newplugin = %+v, %v", info, ok)
	}
	if _, ok := featureInfo(nil, "newplugin"); ok {
		t.Error("a feature without a release")
	}
}

func onArch(t *testing.T, a string) {
	t.Helper()
	prev := arch
	arch = a
	t.Cleanup(func() { arch = prev })
}

func lists(comps []Component, name string) bool {
	return slices.ContainsFunc(comps, func(c Component) bool { return c.Kind == Role && c.Name == name })
}

func TestManifestArchsHideAnUnsupportedRole(t *testing.T) {
	onArch(t, "armhf")
	st := makeState()
	st.Roles = map[string]string{"mpd": "1"}
	st.RolesExcluded = []string{"newrole"} // what install.sh leaves on armhf
	man := withCatalog(manifest.RoleMeta{Group: "System", Archs: []string{"amd64", "arm64"}})
	if lists(List(st, man), "newrole") {
		t.Error("newrole listed on armhf")
	}
	if slices.Contains(Pending(st, man), "role:newrole") {
		t.Error("newrole pending on armhf")
	}
	_, err := Set(st, man, Role, "newrole", true)
	wantComponentError(t, err)
}

func TestManifestArchsKeepARoleInstalledHere(t *testing.T) {
	onArch(t, "armhf")
	st := makeState()
	st.Roles = map[string]string{"newrole": "1"}
	man := withCatalog(manifest.RoleMeta{Group: "System", Archs: []string{"amd64", "arm64"}})
	if !lists(List(st, man), "newrole") {
		t.Error("an installed newrole must stay listed, if only to be disabled")
	}
	if _, err := Set(st, man, Role, "newrole", false); err != nil {
		t.Errorf("disable = %v", err)
	}
}

func TestManifestArchsAllowThisArch(t *testing.T) {
	onArch(t, "arm64")
	st := makeState()
	man := withCatalog(manifest.RoleMeta{Group: "System", OptIn: true, Archs: []string{"amd64", "arm64"}})
	if !lists(List(st, man), "newrole") {
		t.Error("newrole hidden on arm64")
	}
	if _, err := Set(st, man, Role, "newrole", true); err != nil {
		t.Errorf("enable = %v", err)
	}
}

func TestDebArch(t *testing.T) {
	for goarch, want := range map[string]string{"arm": "armhf", "arm64": "arm64", "amd64": "amd64"} {
		if got := debArch(goarch); got != want {
			t.Errorf("debArch(%q) = %q, want %q", goarch, got, want)
		}
	}
}
