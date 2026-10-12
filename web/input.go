package web

import (
	"fmt"
	"strconv"

	"github.com/b0bbywan/odioctl/input"
)

// SetIR escalates through `sudo -n odioctl input ir set <pin>`.
func (a *App) SetIR(pin string) (string, error) {
	n, ok := input.ParsePin(pin)
	if !ok {
		return "", userErrorf("invalid GPIO %q", pin)
	}
	// As for the DAC: what config.txt would become decides whether a reboot
	// is worth claiming. Unreadable: let sudo judge.
	if changed, err := input.WouldChangeIR(a.cfg.ConfigTxt, n); err == nil && !changed {
		return fmt.Sprintf("IR receiver already on GPIO %d — nothing to apply.", n), nil
	}
	if err := a.runConfigTxt("input", "ir", "set", strconv.Itoa(n)); err != nil {
		return "", err
	}
	a.changes.Changed("banners", "input")
	return fmt.Sprintf("IR receiver set on GPIO %d — reboot required.", n), nil
}

// UnsetIR removes the receiver from config.txt, through sudo.
func (a *App) UnsetIR() (string, error) {
	if err := a.runConfigTxt("input", "ir", "unset"); err != nil {
		return "", err
	}
	a.changes.Changed("banners", "input")
	return "IR receiver removed — reboot required.", nil
}
