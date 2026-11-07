// Package web serves the odioctl settings UI: server-rendered HTML over htmx,
// every section following odio on /events. Runs as the odios target user;
// only config.txt writes escalate, through `sudo -n odioctl dac …`.
package web

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/b0bbywan/odioctl/state"
)

const (
	DefaultPort = 8021
	OdioAPIPort = 8018               // odio-api: its dashboard
	OdioAPIEnv  = "ODIOCTL_ODIO_API" // its base URL, set and empty for none
)

// Config is what `odioctl web` reads and writes, and the ports it speaks to.
// The services and pages take their paths from it rather than from module
// constants, so a test can point the whole server at a tmpdir.
type Config struct {
	Bind         string
	Port         int
	Socket       string // a Unix socket to serve on too, for a reverse proxy; "" → none
	SystemdOnly  bool   // serve only the sockets systemd passes, never bind: the units' mode
	StatePath    string
	ConfigTxt    string // "" → dac.ConfigTxt
	OdioctlBin   string
	UpgradesPath string // "" → sibling of a custom StatePath, else /var/cache
	Home         string // the target user's home, what an action's {home} becomes
	RuntimeDir   string // the user manager's $XDG_RUNTIME_DIR, where it exposes its units
	OdioAPI      string // odio-api's base URL; "" → none
}

func DefaultConfig() Config {
	bin := os.Getenv("ODIOCTL_BIN")
	if bin == "" {
		bin = "/usr/bin/odioctl"
	}
	// The web process runs as the target user, so its home is theirs. Left
	// empty when unknown: an action that needs it is refused, not pointed
	// at "/.cache".
	home, _ := os.UserHomeDir()
	// odio-api always listens on lo while its UI is on, unless odio says otherwise
	api, ok := os.LookupEnv(OdioAPIEnv)
	if !ok {
		api = fmt.Sprintf("http://127.0.0.1:%d", OdioAPIPort)
	}
	return Config{
		Bind:       "0.0.0.0",
		Port:       DefaultPort,
		StatePath:  state.SystemStatePath,
		OdioctlBin: bin,
		Home:       home,
		RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"),
		OdioAPI:    api,
	}
}

// UnitsDir is where the user manager keeps an invocation:<unit> symlink for
// as long as a unit runs (odio-api follows the same links).
func (c Config) UnitsDir() string {
	return filepath.Join(c.RuntimeDir, "systemd", "units")
}

// odioAPIBase is s as a base the client appends /upgrade to: http(s), a
// host, nothing after it but slashes (dropped). "" stays none.
func odioAPIBase(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("odio-api %q (--odio-api or $%s): want http://host[:port]", s, OdioAPIEnv)
	}
	return strings.TrimRight(s, "/"), nil
}

func (c Config) ResolvedUpgradesPath() string {
	if c.UpgradesPath != "" {
		return c.UpgradesPath
	}
	return state.UpgradesPathFor(c.StatePath)
}
