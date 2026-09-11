// Package web serves the odioctl settings UI: server-rendered HTML over htmx,
// every section following odio on /events. Runs as the odios target user;
// only config.txt writes escalate, through `sudo -n odioctl dac …`.
package web

import (
	"os"
	"path/filepath"

	"github.com/b0bbywan/odioctl/state"
)

const (
	DefaultPort = 8021
	OdioUIPort  = 8018 // odio-api's built-in dashboard, where upgrade progress is shown
)

// Config is what `odioctl web` reads and writes, and the ports it speaks to.
// The services and pages take their paths from it rather than from module
// constants, so a test can point the whole server at a tmpdir.
type Config struct {
	Bind         string
	Port         int
	StatePath    string
	ConfigTxt    string // "" → dac.ConfigTxt
	OdioctlBin   string
	UpgradesPath string // "" → sibling of a custom StatePath, else /var/cache
	Home         string // the target user's home, what an action's {home} becomes
	RuntimeDir   string // the user manager's $XDG_RUNTIME_DIR, where it exposes its units
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
	return Config{
		Bind:       "0.0.0.0",
		Port:       DefaultPort,
		StatePath:  state.SystemStatePath,
		OdioctlBin: bin,
		Home:       home,
		RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"),
	}
}

// UnitsDir is where the user manager keeps an invocation:<unit> symlink for
// as long as a unit runs (odio-api follows the same links).
func (c Config) UnitsDir() string {
	return filepath.Join(c.RuntimeDir, "systemd", "units")
}

func (c Config) ResolvedUpgradesPath() string {
	if c.UpgradesPath != "" {
		return c.UpgradesPath
	}
	return state.UpgradesPathFor(c.StatePath)
}
