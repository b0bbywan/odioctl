// Package manifest builds release manifest and install.sh URLs for odios on
// GitHub, and reads the upgrades.json cache written by `odioctl upgrade check`.
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
	// the published latest one — a test box runs a pre-release ("pr-84")
	// that the latest manifest knows nothing about, so without this every
	// role it ships reads as "not in this release" and nothing is pending.
	OdiosVersionEnv = "ODIOCTL_ODIOS_VERSION"
)

// Only a *tag* is overridable, never a URL: the tag is interpolated into a
// github.com/b0bbywan/odios path, so whoever sets it can pick another odios
// release but can never point odioctl at a manifest of its own. That only
// holds while the tag cannot walk out of the path — curl normalises away
// `..`, so `../../someone/else/releases/download/x` would fetch (and pipe to
// bash, in `apply`) a foreign repository. Real tags are calver
// ("2026.7.0rc2"), git-described ("2026.7.0rc2-9-gcad916c") or PR
// pre-releases ("pr-84").
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
// unset. An unusable value is a warning, not a failure: falling back to the
// published manifest keeps the daily timer working on a box whose env file
// has a typo in it.
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

// InstallURL is the install.sh location for a release tag.
func InstallURL(version string) (string, error) {
	if version == "latest" {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download/install.sh",
			GitHubRepo), nil
	}
	tag, err := checkedTag(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/install.sh",
		GitHubRepo, tag), nil
}

// ManifestURL is the manifest.json location for a release tag.
func ManifestURL(version string) (string, error) {
	if version == "latest" {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download/manifest.json",
			GitHubRepo), nil
	}
	tag, err := checkedTag(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/manifest.json",
		GitHubRepo, tag), nil
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// Fetch returns the manifest at url. Each caller decides what a failure
// means: `check` makes it fatal, `apply` and the web refresh degrade
// (install.sh defaults, cached manifest). A var so tests can swap the
// network out.
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
// latest manifest and "". There is deliberately no way to name a URL. The tag
// is what travels to `apply` through upgrades.json: a pre-release is reached
// by its tag ("pr-84") while the manifest inside it describes itself by
// version ("2026.7.0rc2-9-gcad916c"), so `apply` cannot rebuild the
// install.sh URL from the version alone.
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

// upgradesCache is the slice of upgrades.json this package reads back.
type upgradesCache struct {
	Latest           string          `json:"latest"`
	TargetTag        string          `json:"target_tag"`
	UpgradeAvailable bool            `json:"upgrade_available"`
	Manifest         json.RawMessage `json:"manifest"`
}

func readCache(path string) (upgradesCache, error) {
	var c upgradesCache
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// tag is the release tag a cached upgrades.json points at — `latest` for a
// report written before `check` recorded the tag it used.
func (c upgradesCache) tag() string {
	if c.TargetTag != "" {
		return c.TargetTag
	}
	return c.Latest
}

// Resolve returns the cached manifest from upgrades.json when it describes
// the requested release, else falls back to a network fetch. The cache is
// populated by `odioctl upgrade check` on the daily timer.
func Resolve(version, upgradesPath string) (*Manifest, error) {
	if c, err := readCache(upgradesPath); err == nil && c.tag() == version {
		var m Manifest
		if json.Unmarshal(c.Manifest, &m) == nil && (m.Odios != "" || m.Roles != nil) {
			return &m, nil
		}
	}
	u, err := ManifestURL(version)
	if err != nil {
		return nil, err
	}
	return Fetch(u)
}

// ResolveVersion is the release tag `apply` targets: explicit, else what
// `check` recorded, else "latest".
func ResolveVersion(explicit, upgradesPath string) string {
	if explicit != "" {
		return explicit
	}
	c, err := readCache(upgradesPath)
	if err != nil || c.tag() == "" {
		return "latest"
	}
	return c.tag()
}

// UpgradeReported reads the upgrade_available flag from upgrades.json; a
// missing or unreadable file reports true so install.sh can decide.
func UpgradeReported(upgradesPath string) bool {
	c, err := readCache(upgradesPath)
	if err != nil {
		return true
	}
	return c.UpgradeAvailable
}
