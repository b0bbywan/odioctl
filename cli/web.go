package cli

import (
	"io"

	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/web"
)

func runWeb(stdout, stderr io.Writer, args []string) int {
	fs := newFlagSet("web", stderr)
	cfg := web.DefaultConfig()
	fs.StringVar(&cfg.Bind, "bind", cfg.Bind, "address to listen on (default: all)")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "TCP port")
	fs.StringVar(&cfg.StatePath, "state", state.SystemStatePath, "path to state.json")
	fs.StringVar(&cfg.ConfigTxt, "config", "", "path to config.txt (default: auto-detect). "+
		"Dev/test only: the sudoers rule does not admit --config, so DAC changes "+
		"through sudo will be refused")
	fs.StringVar(&cfg.UpgradesPath, "upgrades", "", "path to upgrades.json "+
		"(default: /var/cache/odio/upgrades.json, or next to a custom --state)")
	if code, done := parse(fs, args); done {
		return code
	}
	return web.RunServe(stdout, stderr, cfg)
}
