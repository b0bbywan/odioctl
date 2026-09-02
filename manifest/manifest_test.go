package manifest

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestManifestURLShapes(t *testing.T) {
	tests := []struct{ version, want string }{
		{"latest", "https://github.com/" + GitHubRepo + "/releases/latest/download/manifest.json"},
		{"2026.5.0", "https://github.com/" + GitHubRepo + "/releases/download/2026.5.0/manifest.json"},
		// PR pre-releases tag as `pr-<N>` — odioctl must hit that asset.
		{"pr-42", "https://github.com/" + GitHubRepo + "/releases/download/pr-42/manifest.json"},
	}
	for _, tc := range tests {
		got, err := ManifestURL(tc.version)
		if err != nil || got != tc.want {
			t.Errorf("ManifestURL(%q) = %q, %v; want %q", tc.version, got, err, tc.want)
		}
	}
}

func TestInstallURLMirrorsManifestURL(t *testing.T) {
	u, err := InstallURL("latest")
	if err != nil || !strings.HasSuffix(u, "/releases/latest/download/install.sh") {
		t.Errorf("InstallURL(latest) = %q, %v", u, err)
	}
	u, err = InstallURL("2026.5.0")
	if err != nil || !strings.HasSuffix(u, "/download/2026.5.0/install.sh") {
		t.Errorf("InstallURL(2026.5.0) = %q, %v", u, err)
	}
}

func TestAcceptsTheTagShapesOdiosPublishes(t *testing.T) {
	for _, tag := range []string{"latest", "2026.5.0", "2026.7.0rc2", "2026.7.0rc2-9-gcad916c", "pr-84"} {
		if !IsReleaseTag(tag) {
			t.Errorf("IsReleaseTag(%q) = false, want true", tag)
		}
	}
}

func TestRejectsAnythingThatLeavesTheReleasePath(t *testing.T) {
	// curl normalises `..` away, so a traversal in the tag would fetch —
	// and in `apply`, pipe into bash as root — a foreign repository.
	for _, tag := range []string{
		"../../someone/else/releases/download/x",
		"..",
		"2026.5.0/../../evil",
		"https://evil.invalid/install.sh",
		"2026.5.0?x=1",
		"",
		"-rf",
		strings.Repeat("a", 65),
	} {
		if IsReleaseTag(tag) {
			t.Errorf("IsReleaseTag(%q) = true, want false", tag)
		}
	}
}

func TestURLBuildersRefuseAnUnsafeTag(t *testing.T) {
	if _, err := ManifestURL("../../evil/repo/releases/download/x"); err == nil {
		t.Error("ManifestURL should refuse a traversal tag")
	}
	if _, err := InstallURL("../../evil/repo/releases/download/x"); err == nil {
		t.Error("InstallURL should refuse a traversal tag")
	}
}

func TestCheckSourceDefaultsToPublishedLatest(t *testing.T) {
	t.Setenv(OdiosVersionEnv, "")
	url, tag, err := CheckSource("")
	if err != nil || url != LatestManifestURL || tag != "" {
		t.Errorf("CheckSource(\"\") = %q, %q, %v", url, tag, err)
	}
}

func TestCheckSourceEnvSelectsAPrereleaseByTag(t *testing.T) {
	t.Setenv(OdiosVersionEnv, "pr-84")
	url, tag, err := CheckSource("")
	want, _ := ManifestURL("pr-84")
	if err != nil || tag != "pr-84" || url != want {
		t.Errorf("CheckSource = %q, %q, %v", url, tag, err)
	}
}

func TestCheckSourceExplicitVersionWinsOverEnv(t *testing.T) {
	t.Setenv(OdiosVersionEnv, "pr-84")
	_, tag, err := CheckSource("2026.6.0")
	if err != nil || tag != "2026.6.0" {
		t.Errorf("tag = %q, %v; want 2026.6.0", tag, err)
	}
}

func TestCheckSourceBlankEnvIsNoOverride(t *testing.T) {
	t.Setenv(OdiosVersionEnv, "   ")
	url, tag, err := CheckSource("")
	if err != nil || url != LatestManifestURL || tag != "" {
		t.Errorf("CheckSource = %q, %q, %v", url, tag, err)
	}
}

func TestCheckSourceUnusableEnvFallsBack(t *testing.T) {
	t.Setenv(OdiosVersionEnv, "../../evil/repo")
	url, tag, err := CheckSource("")
	if err != nil || url != LatestManifestURL || tag != "" {
		t.Errorf("CheckSource = %q, %q, %v", url, tag, err)
	}
}

func TestCheckSourceUnusableExplicitVersionErrors(t *testing.T) {
	// Typed on the command line, so it is an error rather than a warning.
	if _, _, err := CheckSource("../../evil/repo"); err == nil {
		t.Error("want error for an unsafe explicit version")
	}
}

func TestEveryManifestURLIsBuiltHere(t *testing.T) {
	// No URL can be named from anywhere: whatever the inputs, the fetch
	// target is either the published manifest or a github.com release path.
	t.Setenv(OdiosVersionEnv, "pr-84")
	for _, version := range []string{"", "2026.6.0"} {
		url, _, err := CheckSource(version)
		if err != nil {
			t.Fatal(err)
		}
		if url != LatestManifestURL &&
			!strings.HasPrefix(url, "https://github.com/"+GitHubRepo+"/releases/") {
			t.Errorf("CheckSource(%q) url = %q", version, url)
		}
	}
}

func TestFetchReturnsParsedManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}`))
	}))
	defer srv.Close()
	got, err := Fetch(srv.URL)
	want := &Manifest{Odios: "2026.5.0", Roles: map[string]string{"mpd": "2026.5.0"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Fetch = %+v, %v; want %+v", got, err, want)
	}
}

func TestFetchReturnsTheError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Fetch(srv.URL); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want HTTP 404", err)
	}
	srv.Close()
	if _, err := Fetch(srv.URL); err == nil {
		t.Error("want a connection error") // connection refused once closed
	}
}
