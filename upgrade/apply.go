package upgrade

import (
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/procutil"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/versions"
)

type ApplyOptions struct {
	Version   string // target tag, "" = what `check` recorded
	State     string // "" = state.SystemStatePath
	DryRun    bool
	Force     bool
	Reinstall bool // re-run every role in full, scaffold included (implies Force)
	Progress  bool // ODIOS_PROGRESS=Y for odio-api's upgrade socket
}

// OdioAPIListening reports whether odio-api's upgrade socket exists — a real
// instance, not CI. Under sudo XDG_RUNTIME_DIR is not the target_user's, so
// this stays false and the service path keeps its explicit --progress.
func OdioAPIListening() bool {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(runtime, "odio-api", "upgrade.sock"))
	return err == nil
}

// DeriveInstallEnv emits INSTALL_X=N for the *_excluded lists and Y for
// Roles/Features. A name in neither list is left unset so install.sh's own
// defaults take over — that's how a later-added role self-installs.
func DeriveInstallEnv(st state.State) map[string]string {
	env := map[string]string{}
	for _, role := range st.RolesExcluded {
		env["INSTALL_"+strings.ToUpper(role)] = "N"
	}
	for _, feature := range st.FeaturesExcluded {
		env["INSTALL_"+strings.ToUpper(feature)] = "N"
	}
	for role := range st.Roles {
		env["INSTALL_"+strings.ToUpper(role)] = "Y"
	}
	for _, feature := range st.Features {
		env["INSTALL_"+strings.ToUpper(feature)] = "Y"
	}
	return env
}

// DeriveRunEnv emits RUN_X=N for roles whose target version matches
// installed. Asymmetric: anything else falls through to install.sh's
// RUN_X=${RUN_X:-$INSTALL_X} default — RUN_X stays an internal optimisation
// channel, INSTALL_X the user-facing API.
func DeriveRunEnv(st state.State, man *manifest.Manifest, installEnv map[string]string) map[string]string {
	env := map[string]string{}
	if man == nil {
		return env
	}
	for role, installed := range st.Roles {
		// Excluded roles are already gated by INSTALL_X=N.
		if installEnv["INSTALL_"+strings.ToUpper(role)] == "N" {
			continue
		}
		if versions.RoleUpToDate(installed, man.Roles[role], st.Odios) {
			env["RUN_"+strings.ToUpper(role)] = "N"
		}
	}
	return env
}

// loadState resolves (statePath, state) from opts; ok=false on read/schema error.
func loadState(stdout, stderr io.Writer, opts ApplyOptions) (string, state.State, bool) {
	statePath := opts.State
	if statePath == "" {
		statePath = state.SystemStatePath
	}
	st, err := state.Read(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", statePath, err)
		return "", state.State{}, false
	}
	fmt.Fprintf(stdout, "state.json read from %s:\n", statePath)
	state.PrintSummary(stdout, st)
	return statePath, st, true
}

func buildApplyEnv(w io.Writer, st state.State, version, targetUser, upgradesPath string,
	opts ApplyOptions) map[string]string {
	installEnv := DeriveInstallEnv(st)
	man, err := manifest.Resolve(version, upgradesPath)
	if err != nil {
		// Degrade, never block: no RUN_X skips, install.sh runs every role.
		slog.Warn("could not resolve the target manifest", "err", err)
		man = nil
	}
	// --reinstall bypasses both skip layers: no RUN_X=N (every role runs) and
	// ODIOS_FORCE_SCAFFOLD=Y (read_state.yml blanks odios_prior_* so first-
	// install scaffold re-applies).
	runEnv := map[string]string{}
	if !opts.Reinstall {
		runEnv = DeriveRunEnv(st, man, installEnv)
	}
	env := maps.Clone(installEnv)
	maps.Copy(env, runEnv)
	env["ODIOS_VERSION"] = version
	env["TARGET_USER"] = targetUser
	if opts.Reinstall {
		env["ODIOS_FORCE_SCAFFOLD"] = "Y"
	}
	if opts.Progress {
		env["ODIOS_PROGRESS"] = "Y"
	}

	var skipped []string
	for k := range runEnv {
		skipped = append(skipped, strings.ToLower(strings.TrimPrefix(k, "RUN_")))
	}
	slices.Sort(skipped)
	switch {
	case opts.Reinstall:
		fmt.Fprintln(w, "  reinstall: running all roles with full scaffold")
	case len(skipped) > 0:
		fmt.Fprintf(w, "  smart-upgrade: skipping unchanged roles: %s\n", strings.Join(skipped, ", "))
	case man == nil:
		fmt.Fprintln(w, "  smart-upgrade: manifest unavailable, running all roles")
	default:
		fmt.Fprintln(w, "  smart-upgrade: all roles bumped, running everything")
	}
	return env
}

// runInstall executes `curl install.sh | bash` with env appended to the
// environment; a var so tests can assert it is never reached.
var runInstall = func(url string, env map[string]string) int {
	cmd := exec.Command("bash", "-c", "curl -fsSL "+url+" | bash")
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	code, err := procutil.ExitCode(cmd.Run())
	if err != nil {
		slog.Error("install.sh invocation failed", "err", err)
		return 1
	}
	return code
}

// RunApply re-runs install.sh from the target release with INSTALL_X derived
// from state.json and RUN_X from the per-role manifest diff.
func RunApply(stdout, stderr io.Writer, opts ApplyOptions) int {
	statePath, st, ok := loadState(stdout, stderr, opts)
	if !ok {
		return 2
	}

	// An explicit --state points at a test/dev tree: use its sibling
	// upgrades.json. The system state uses the canonical /var/cache path.
	upgradesPath := state.SystemUpgradesPath
	if opts.State != "" {
		upgradesPath = filepath.Join(filepath.Dir(statePath), "upgrades.json")
	}

	if !opts.Force && !opts.Reinstall && opts.Version == "" &&
		!manifest.UpgradeReported(upgradesPath) {
		fmt.Fprintln(stdout, "No upgrade reported in upgrades.json — use --force to override.")
		return 0
	}

	version := manifest.ResolveVersion(opts.Version, upgradesPath)
	// The tag lands in a github.com path that is curl'd into bash as root,
	// and upgrades.json is group-writable by design: refuse anything that
	// could walk out of the odios release path.
	if !manifest.IsReleaseTag(version) {
		fmt.Fprintf(stdout, "Refusing target %q: not a release tag.\n", version)
		return 2
	}
	if versions.IsDowngrade(version, st.Odios) {
		fmt.Fprintf(stdout, "Refusing to downgrade: target %s < installed %s.\n", version, st.Odios)
		return 2
	}
	url, err := manifest.InstallURL(version)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	env := buildApplyEnv(stdout, st, version, st.TargetUser, upgradesPath, opts)

	fmt.Fprintf(stdout, "Upgrading to %s via %s\n", version, url)
	fmt.Fprintln(stdout, "  env passed to install.sh:")
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(stdout, "    %s=%s\n", k, env[k])
	}

	if opts.DryRun {
		fmt.Fprintln(stdout, "(dry-run, not invoking)")
		return 0
	}
	return runInstall(url, env)
}
