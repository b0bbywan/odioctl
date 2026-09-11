// Package manifest builds release manifest and install.sh URLs for odios on
// GitHub, and fetches the manifest itself.
package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/b0bbywan/odioctl/config"
)

const (
	GitHubRepo        = "b0bbywan/odios"
	LatestManifestURL = "https://odio.love/manifest.json"

	// OdiosVersionEnv makes `check` compare against that release instead of
	// the published latest: a test odio's pre-release ("pr-84") ships roles
	// the latest manifest ignores, and nothing would ever read as pending.
	OdiosVersionEnv = "ODIOCTL_ODIOS_VERSION"
)

// Only a tag is overridable, never a URL, and it must not walk out of the
// b0bbywan/odios path: `../..` would pipe a foreign repository into bash.
// Real tags: "2026.7.0rc2", "2026.7.0rc2-9-gcad916c", "pr-84".
var tagRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// IsReleaseTag reports whether tag is safe to interpolate into a release URL.
func IsReleaseTag(tag string) bool {
	return tagRE.MatchString(tag) && !strings.Contains(tag, "..")
}

func checkedTag(version string) (string, error) {
	if !IsReleaseTag(version) {
		return "", fmt.Errorf("not a release tag: %q", version)
	}
	return version, nil
}

// EnvVersion returns the release tag from $ODIOCTL_ODIOS_VERSION, "" when
// unset. An unusable value only warns: falling back to the published manifest
// keeps the daily timer working on an odio whose env file has a typo.
func EnvVersion() string {
	raw := strings.TrimSpace(os.Getenv(OdiosVersionEnv))
	if raw == "" {
		return ""
	}
	if !IsReleaseTag(raw) {
		slog.Warn("ignoring $"+OdiosVersionEnv+": not a release tag", "value", raw)
		return ""
	}
	return raw
}

// Manifest is the schema of a release manifest.json (built by odios'
// scripts/build-manifest.py).
type Manifest struct {
	Odios string            `json:"odios"`
	Roles map[string]string `json:"roles"`
}

// releaseAssetURL is where GitHub serves asset for a release tag — the one
// place a tag is interpolated into a URL, so IsReleaseTag guards it once.
func releaseAssetURL(version, asset string) (string, error) {
	if version == "latest" {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download/%s",
			GitHubRepo, asset), nil
	}
	tag, err := checkedTag(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		GitHubRepo, tag, asset), nil
}

// InstallURL is the install.sh location for a release tag.
func InstallURL(version string) (string, error) {
	return releaseAssetURL(version, "install.sh")
}

// ManifestURL is the manifest.json location for a release tag.
func ManifestURL(version string) (string, error) {
	return releaseAssetURL(version, "manifest.json")
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Fetch returns the manifest at url; each caller decides what a failure means
// (`check` fatal, `apply` and the web refresh degrade). A var, for tests.
var Fetch = func(url string) (*Manifest, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", config.AppName+"/"+config.AppVersion)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// CheckSource returns the (manifest url, release tag) pair for `check`: the
// requested tag — version or $ODIOCTL_ODIOS_VERSION — else the published
// latest and "". The tag travels to `apply` through upgrades.json — the
// version a pre-release calls itself cannot rebuild it.
func CheckSource(version string) (url, tag string, err error) {
	if version == "" {
		version = EnvVersion()
	}
	if version == "" {
		return LatestManifestURL, "", nil
	}
	u, err := ManifestURL(version)
	if err != nil {
		return "", "", err
	}
	return u, version, nil
}
