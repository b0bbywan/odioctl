package web

import "github.com/b0bbywan/odioctl/dac"

// SetDAC escalates through `sudo -n odioctl dac set <id>`.
func (a *App) SetDAC(id string) (string, error) {
	entry, ok := dac.ByID(id)
	if !ok {
		return "", userErrorf("unknown DAC id %q", id)
	}
	// Re-applying the current selection is one click away, so catch the no-op
	// rather than claim a reboot nothing needs. What config.txt would become
	// decides, not the id: a block odioctl wrote in an older layout carries
	// the same id and still has to be rewritten. Unreadable: let sudo judge.
	if changed, err := dac.WouldChange(a.cfg.ConfigTxt, entry); err == nil && !changed {
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

// Reboot is the reboot the DAC change waits for: logind as the target user
// (odios' polkit rule, no sudo), the flag under /run going with the boot.
// The page already has its answer, so a refusal shows on the banners.
func (a *App) Reboot() {
	err := runChecked(a.run.User, []string{"systemctl", "reboot"}, "systemctl reboot")
	if err == nil {
		a.log.Printf("reboot requested")
		return
	}
	a.log.Printf("reboot: %v", err)
	a.rebootMu.Lock()
	a.rebootErr = err.Error()
	a.rebootMu.Unlock()
	a.changes.Changed("banners")
}

// RebootError is why the last reboot was refused, "" when it was not.
func (a *App) RebootError() string {
	a.rebootMu.Lock()
	defer a.rebootMu.Unlock()
	return a.rebootErr
}
