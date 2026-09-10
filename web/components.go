package web

import (
	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

// SetComponent enables or disables a component in state.json, then keeps
// upgrades.json in step so odio-ui's badge and `upgrade apply` see the
// pending install without waiting for the daily timer.
func (a *App) SetComponent(kind components.Kind, name string, enabled bool) (string, error) {
	if err := a.writeComponent(kind, name, enabled); err != nil {
		return "", err
	}
	// Outside the lock: it fetches the manifest.
	report := upgrade.Refresh(upgrade.CheckOptions{
		State:  a.cfg.StatePath,
		Output: a.cfg.ResolvedUpgradesPath(),
	})
	a.changes.Changed("upgrade", "components")
	label := components.LabelOf(kind, name)
	switch {
	case !enabled:
		return label + " disabled — it stays installed but will no longer be updated.", nil
	case report != nil && report.HasPending(string(kind)+":"+name):
		return label + " enabled — it will be installed by the next upgrade (apply it below).", nil
	default:
		return label + " enabled.", nil
	}
}

func (a *App) writeComponent(kind components.Kind, name string, enabled bool) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	st, err := a.ReadState()
	if err != nil {
		return &UserError{Msg: stateErrorMsg(a.cfg.StatePath, err)}
	}
	next, err := components.Set(st, kind, name, enabled)
	if err != nil {
		return &UserError{Msg: err.Error()}
	}
	if err := state.Write(a.cfg.StatePath, next); err != nil {
		return userErrorf("cannot write state.json: %v", err)
	}
	return nil
}
