// Package web serves the odioctl settings UI: server-rendered HTML over htmx,
// every section following odio on /events. Runs as the odios target user;
// only config.txt writes escalate, through `sudo -n odioctl dac …`.
package web

import (
	"fmt"
	"os"

	"github.com/b0bbywan/odioctl/state"
)

const (
	DefaultPort = 8021
	OdioUIPort  = 8018 // odio-api: its dashboard, and the upgrades it runs
)

// Config is what `odioctl web` reads and writes, and the ports it speaks to.
// The services and pages take their paths from it rather than from module
// constants, so a test can point the whole server at a tmpdir.
type Config struct {
	Bind         string
	Port         int
	Socket       string // a Unix socket to serve on too, for a reverse proxy; "" → none
	SystemdOnly  bool   // serve only the sockets systemd passes, never bind: the units' mode
	UIURL        string // where the logo leads; "" → odio-api on the host the browser used
	StatePath    string
	ConfigTxt    string // "" → dac.ConfigTxt
	OdioctlBin   string
	UpgradesPath string // "" → sibling of a custom StatePath, else /var/cache
	Home         string // the target user's home, what an action's {home} becomes
	OdioAPI      string // odio-api's base URL: it starts and tracks upgrades; "" → none
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
		UIURL:      os.Getenv("ODIOCTL_UI_URL"),
		StatePath:  state.SystemStatePath,
		OdioctlBin: bin,
		Home:       home,
		// odio-api always listens on lo while its UI is on
		OdioAPI: fmt.Sprintf("http://127.0.0.1:%d", OdioUIPort),
	}
}

func (c Config) ResolvedUpgradesPath() string {
	if c.UpgradesPath != "" {
		return c.UpgradesPath
	}
	return state.UpgradesPathFor(c.StatePath)
}
