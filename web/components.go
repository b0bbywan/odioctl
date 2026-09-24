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
	err := a.writeState(func(st state.State) (state.State, error) {
		return components.Set(st, a.TargetManifest(), kind, name, enabled)
	})
	if err != nil {
		return "", err
	}
	report := a.refresh()
	label := components.LabelOf(a.TargetManifest(), kind, name)
	switch {
	case !enabled:
		return label + " disabled — it stays installed but will no longer be updated.", nil
	case report != nil && report.HasPending(string(kind)+":"+name):
		return label + " enabled — it will be installed by the next upgrade (apply it below).", nil
	default:
		return label + " enabled.", nil
	}
}

// SetAudioserver picks the audio server; the switch waits for apply, which
// the refreshed upgrades.json then offers.
func (a *App) SetAudioserver(name string) (string, error) {
	err := a.writeState(func(st state.State) (state.State, error) {
		return components.SetAudioserver(st, a.TargetManifest(), name)
	})
	if err != nil {
		return "", err
	}
	a.refresh()
	st, err := a.ReadState()
	if err != nil {
		return "", &UserError{Msg: stateErrorMsg(a.cfg.StatePath, err)}
	}
	man := a.TargetManifest()
	msg := "Audio server: " + components.LabelOf(man, components.Role, name)
	if as := components.AudioserverOf(st, man); as.Switching() {
		return msg + " — it replaces " + components.LabelOf(man, components.Role, as.Installed) +
			" on the next upgrade (apply it below).", nil
	}
	return msg + ".", nil
}

// refresh keeps upgrades.json in step with a change of state.json, outside
// the state lock: it may fetch the manifest.
func (a *App) refresh() *upgrade.Report {
	report := upgrade.Refresh(upgrade.CheckOptions{
		State:  a.cfg.StatePath,
		Output: a.cfg.ResolvedUpgradesPath(),
	})
	a.changes.Changed("upgrade", "components")
	return report
}

// writeState applies change to state.json under the state lock.
func (a *App) writeState(change func(state.State) (state.State, error)) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	st, err := a.ReadState()
	if err != nil {
		return &UserError{Msg: stateErrorMsg(a.cfg.StatePath, err)}
	}
	next, err := change(st)
	if err != nil {
		return &UserError{Msg: err.Error()}
	}
	if err := state.Write(a.cfg.StatePath, next); err != nil {
		return userErrorf("cannot write state.json: %v", err)
	}
	return nil
}
