package web

import "github.com/b0bbywan/odioctl/dac"

// SetDAC escalates through `sudo -n odioctl dac set <id>`.
func (a *App) SetDAC(id string) (string, error) {
	if _, ok := dac.ByID(id); !ok {
		return "", userErrorf("unknown DAC id %q", id)
	}
	// Plain HTML cannot grey the Apply button out, so re-applying the current
	// selection is one click away: recognise the no-op here rather than
	// escalate through sudo and claim a reboot that nothing needs. Only when
	// odioctl owns the block, though — same id over an unmanaged config.txt
	// does change the file (takes ownership, comments the stray lines out).
	if d := a.DacStatus(); d.Managed && d.Current == id {
		return "DAC already set to " + id + " — nothing to apply.", nil
	}
	if err := a.runDac("dac", "set", id); err != nil {
		return "", err
	}
	a.changes.Changed("banners", "dac")
	return "DAC set to " + id + " — reboot required.", nil
}

// UnsetDAC removes the odioctl block from config.txt, through sudo.
func (a *App) UnsetDAC() (string, error) {
	if err := a.runDac("dac", "unset"); err != nil {
		return "", err
	}
	a.changes.Changed("banners", "dac")
	return "DAC block removed — reboot required.", nil
}

func (a *App) runDac(args ...string) error {
	if a.cfg.ConfigTxt != "" {
		args = append(args, "--config", a.cfg.ConfigTxt)
	}
	return runChecked(a.run.Privileged, args, "odioctl dac")
}

// Reboot is the reboot the DAC change waits for. It asks logind as the
// target user: odios' polkit rule lets that user reboot, no sudo. The flag
// under /run goes with the boot.
func (a *App) Reboot() (string, error) {
	if err := runChecked(a.run.User, []string{"systemctl", "reboot"}, "systemctl reboot"); err != nil {
		return "", err
	}
	a.log.Printf("reboot requested")
	return "Rebooting — odio will be back soon.", nil
}
