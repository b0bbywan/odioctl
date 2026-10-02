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

// componentName is what may reach disable.yml: odios reads roles/<name>/ as
// root, and the names come from group-writable files.
var componentName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

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

// runDisable runs the disable.yml of the release extracted in dir through its
// vendored ansible, the way install.sh runs playbook.yml; a var for tests.
var runDisable = func(dir string, vars []byte, env map[string]string) int {
	path, err := writeVars(vars)
	if err != nil {
		slog.Error("disable.yml vars", "err", err)
		return 1
	}
	defer removeVars(path)
	cmd := exec.Command("python3", filepath.Join(dir, "vendor", "bin", "ansible-playbook"),
		"-i", filepath.Join(dir, "ansible", "inventory", "localhost.yml"),
		filepath.Join(dir, "ansible", "disable.yml"), "-e", "@"+path)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(dir, "vendor"))
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

// archiveURL is t's release tarball. "latest" names none: its manifest's
// version is the tag, a published release being tagged by its version.
func (t *target) archiveURL() (string, error) {
	tag := t.version
	if tag == "latest" && t.man != nil {
		tag = t.man.Odios
	}
	return manifest.ArchiveURL(tag)
}

// disable runs r through the target release's disable.yml, then drops it from
// state.json, returned, and refreshes upgrades.json. On failure r stays pending.
func disable(stdout, stderr io.Writer, statePath string, st state.State, r components.Removals,
	t *target, opts ApplyOptions) (state.State, int) {
	refs := strings.Join(r.Refs(), ", ")
	vars, err := disableVars(st.TargetUser, r)
	if err != nil {
		fmt.Fprintf(stdout, "Refusing to disable: %v.\n", err)
		return st, 2
	}
	url, err := t.archiveURL()
	if err != nil {
		fmt.Fprintf(stdout, "Cannot disable %s: %v.\n", refs, err)
		return st, 2
	}
	fmt.Fprintf(stdout, "Disabling %s via %s\n", refs, url)
	fmt.Fprintf(stdout, "  vars passed to disable.yml: %s\n", vars)
	if opts.DryRun {
		return st, 0
	}
	dir, err := fetchRelease(url)
	if err != nil {
		fmt.Fprintf(stdout, "Downloading %s failed (%v): %s stay pending.\n", url, err, refs)
		return st, 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	env := map[string]string{}
	if opts.Progress {
		env["ANSIBLE_CALLBACK_PLUGINS"] = filepath.Join(dir, "ansible", "callback_plugins")
		env["ANSIBLE_CALLBACKS_ENABLED"] = "odio_progress"
		if rt, ok := runtimeDirOf(stdout, st.TargetUser); ok {
			env["XDG_RUNTIME_DIR"] = rt
		}
	}
	if rc := runDisable(dir, vars, env); rc != 0 {
		fmt.Fprintf(stdout, "disable.yml failed (exit %d): %s stay pending.\n", rc, refs)
		return st, rc
	}
	// Read again: the web UI may have written it during the run.
	if st, err = state.Read(statePath); err == nil {
		st = components.Drop(st, t.man, r)
		err = state.Write(statePath, st)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Error recording the removals in %s: %v\n", statePath, err)
		return st, 1
	}
	Refresh(CheckOptions{State: statePath})
	return st, 0
}
