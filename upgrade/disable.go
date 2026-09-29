package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/procutil"
	"github.com/b0bbywan/odioctl/state"
)

// ReleaseDir is the release install.sh keeps once installed, whose
// disable.yml `apply` runs offline; a var so tests can point it elsewhere.
var ReleaseDir = "/var/lib/odio/release"

// componentName is what may reach disable.yml: odios reads roles/<name>/ as
// root, and the names come from group-writable files.
var componentName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func disablePlaybook() string { return filepath.Join(ReleaseDir, "ansible", "disable.yml") }

// canDisable: the installed release has disable.yml, and was kept at all.
func canDisable() bool {
	_, err := os.Stat(disablePlaybook())
	return err == nil
}

// disableVars is disable.yml's extra-vars: odios derives the rest from state.json.
func disableVars(targetUser string, r components.Removals) ([]byte, error) {
	names := slices.Concat(r.Roles, slices.Collect(maps.Keys(r.Features)), slices.Collect(maps.Values(r.Features)))
	for _, n := range names {
		if !componentName.MatchString(n) {
			return nil, fmt.Errorf("not a component name: %q", n)
		}
	}
	return json.Marshal(struct {
		TargetUser string            `json:"target_user"`
		Roles      []string          `json:"odios_disable_roles"`
		Features   map[string]string `json:"odios_disable_features"`
	}{targetUser, orEmpty(r.Roles, []string{}), orEmpty(r.Features, map[string]string{})})
}

// orEmpty is v, or empty when v is nil: disable.yml takes a list and a dict, never null.
func orEmpty[T ~[]string | ~map[string]string](v, empty T) T {
	if v == nil {
		return empty
	}
	return v
}

// runDisable runs the kept release's disable.yml through its vendored
// ansible, the way install.sh runs playbook.yml; a var so tests replace it.
var runDisable = func(vars []byte, env map[string]string) int {
	path, err := writeVars(vars)
	if err != nil {
		slog.Error("disable.yml vars", "err", err)
		return 1
	}
	defer removeVars(path)
	cmd := exec.Command("python3", filepath.Join(ReleaseDir, "vendor", "bin", "ansible-playbook"),
		"-i", filepath.Join(ReleaseDir, "ansible", "inventory", "localhost.yml"),
		disablePlaybook(), "-e", "@"+path)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ReleaseDir, "vendor"))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	code, err := procutil.ExitCode(cmd.Run())
	if err != nil {
		slog.Error("disable.yml invocation failed", "err", err)
		return 1
	}
	return code
}

// writeVars puts vars in a temp file of its own (0600), for ansible's -e @file.
func writeVars(vars []byte) (string, error) {
	f, err := os.CreateTemp("", "odioctl-disable-*.json")
	if err != nil {
		return "", err
	}
	_, err = f.Write(vars)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeVars(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func removeVars(path string) {
	if err := os.Remove(path); err != nil {
		slog.Warn("disable.yml vars left behind", "path", path, "err", err)
	}
}

// disable runs r through disable.yml, then drops it from state.json and
// refreshes upgrades.json. On failure r stays pending.
func disable(stdout, stderr io.Writer, statePath string, r components.Removals,
	man *manifest.Manifest, opts ApplyOptions) int {
	st, err := state.Read(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", statePath, err)
		return 2
	}
	vars, err := disableVars(st.TargetUser, r)
	if err != nil {
		fmt.Fprintf(stdout, "Refusing to disable: %v.\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "Disabling %s via %s\n", strings.Join(r.Refs(), ", "), disablePlaybook())
	fmt.Fprintf(stdout, "  vars passed to disable.yml: %s\n", vars)
	if opts.DryRun {
		return 0
	}
	env := map[string]string{}
	if opts.Progress {
		env["ANSIBLE_CALLBACK_PLUGINS"] = filepath.Join(ReleaseDir, "ansible", "callback_plugins")
		env["ANSIBLE_CALLBACKS_ENABLED"] = "odio_progress"
		if dir, ok := runtimeDirOf(stdout, st.TargetUser); ok {
			env["XDG_RUNTIME_DIR"] = dir
		}
	}
	if rc := runDisable(vars, env); rc != 0 {
		fmt.Fprintf(stdout, "disable.yml failed (exit %d): %s stay pending.\n", rc, strings.Join(r.Refs(), ", "))
		return rc
	}
	// Read again: an install.sh run in between rewrote it.
	if st, err = state.Read(statePath); err == nil {
		err = state.Write(statePath, components.Drop(st, man, r))
	}
	if err != nil {
		fmt.Fprintf(stderr, "Error recording the removals in %s: %v\n", statePath, err)
		return 1
	}
	Refresh(CheckOptions{State: statePath})
	return 0
}
