// Package state reads and writes /var/lib/odio/state.json — the record of
// what odios installed here. The current schema, or what an earlier odios
// wrote of it (see `state:"optional"`); anything else is a *SchemaError.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/b0bbywan/odioctl/fsutil"
)

// state.json holds what cannot be regenerated, hence /var/lib; upgrades.json
// is a delta `odioctl upgrade check` rewrites, hence /var/cache.
const (
	SystemStatePath    = "/var/lib/odio/state.json"
	SystemUpgradesPath = "/var/cache/odio/upgrades.json"
)

// UpgradesPathFor is where upgrades.json lives for a given state.json: the
// system cache path, a sibling file for a dev tree. One rule, so `check`,
// `apply` and `web` always meet on the same file.
func UpgradesPathFor(statePath string) string {
	if statePath == "" || statePath == SystemStatePath {
		return SystemUpgradesPath
	}
	return filepath.Join(filepath.Dir(statePath), "upgrades.json")
}

// The audio servers odios installs, one at a time: roles of those names.
const (
	PulseAudio = "pulseaudio"
	PipeWire   = "pipewire"
)

// State is the schema of state.json, the only place it is spelled out.
// `state:"optional"` marks what an earlier odios did not write: Parse keeps
// the value defaults() gives it.
type State struct {
	Odios             string            `json:"odios"`
	InstallMode       string            `json:"install_mode"`
	TargetUser        string            `json:"target_user"`
	Audioserver       string            `json:"audioserver" state:"optional"`
	MPDMusicDirectory string            `json:"mpd_music_directory" state:"optional"` // "" → install.sh's default
	MPDConfPath       string            `json:"mpd_conf_path" state:"optional"`       // an external MPD's; "" → detected
	Roles             map[string]string `json:"roles"`
	RolesExcluded     []string          `json:"roles_excluded"`
	Features          []string          `json:"features"`
	FeaturesExcluded  []string          `json:"features_excluded"`
	ReleaseHistory    []string          `json:"release_history"`
}

// defaults is what an optional field meant before odios wrote it: PulseAudio
// was the only server, the paths were install.sh's to pick.
func defaults() State { return State{Audioserver: PulseAudio} }

// keys are State's json names in field order, required the non-optional ones.
var keys, required = func() (all, required []string) {
	t := reflect.TypeFor[State]()
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		all = append(all, name)
		if f.Tag.Get("state") != "optional" {
			required = append(required, name)
		}
	}
	return all, required
}()

// SchemaError reports a state.json (or a run) missing required fields or with
// the wrong shape.
type SchemaError struct{ Reason string }

func (e *SchemaError) Error() string { return e.Reason }

func schemaErrorf(format string, args ...any) error {
	return &SchemaError{Reason: fmt.Sprintf(format, args...)}
}

// decode reads b over st: the required keys must be there and not null, and
// with a non-nil allowed, nothing else may be. what names the input in errors.
func decode(b []byte, st *State, what string, required, allowed []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return schemaErrorf("%s must be a JSON object", what)
		}
		return err
	}
	var missing, unknown []string
	for _, name := range required {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return schemaErrorf("%s missing required fields: %s", what, strings.Join(missing, ", "))
	}
	for name := range fields {
		if allowed != nil && !slices.Contains(allowed, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return schemaErrorf("%s has fields this odioctl does not know: %s", what, strings.Join(unknown, ", "))
	}
	if err := json.Unmarshal(b, st); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return schemaErrorf("%s field %q has the wrong shape (want %s)", what, ute.Field, ute.Type)
		}
		return err
	}
	return validate(*st, what)
}

func validate(st State, what string) error {
	for name, v := range map[string]string{
		"odios": st.Odios, "install_mode": st.InstallMode, "target_user": st.TargetUser,
	} {
		if v == "" {
			return schemaErrorf("%s field %q must be a non-empty string", what, name)
		}
	}
	if st.Audioserver != PulseAudio && st.Audioserver != PipeWire {
		return schemaErrorf("%s field \"audioserver\" must be %q or %q, got %q",
			what, PulseAudio, PipeWire, st.Audioserver)
	}
	for name, v := range map[string]string{
		"mpd_music_directory": st.MPDMusicDirectory, "mpd_conf_path": st.MPDConfPath,
	} {
		if !validPath(v) {
			return schemaErrorf("%s field %q must be an absolute path without quotes, "+
				"backslashes or control characters, got %q", what, name, v)
		}
	}
	return nil
}

// validPath: "" (not recorded) or an absolute path install.sh can splice into
// its extra-vars JSON — `apply` runs it as root, state.json is group-writable.
func validPath(s string) bool {
	if s == "" {
		return true
	}
	return strings.HasPrefix(s, "/") && !strings.ContainsFunc(s, func(r rune) bool {
		return r == '"' || r == '\\' || r < 0x20 || r == 0x7f
	})
}

// Parse decodes and validates state.json content.
func Parse(b []byte) (State, error) {
	st := defaults()
	if err := decode(b, &st, "state.json", required, nil); err != nil {
		return State{}, err
	}
	return st, nil
}

// Read loads and validates state.json.
func Read(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	return Parse(b)
}

// Write rewrites state.json, indent 4 in field order.
func Write(path string, st State) error {
	return fsutil.AtomicWriteJSON(path, st.complete())
}

// complete fills what Parse would refuse on read-back: a nil list or map
// marshals as null, an empty audioserver is not one.
func (st State) complete() State {
	if st.Audioserver == "" {
		st.Audioserver = defaults().Audioserver
	}
	if st.Roles == nil {
		st.Roles = map[string]string{}
	}
	for _, list := range []*[]string{&st.RolesExcluded, &st.Features, &st.FeaturesExcluded, &st.ReleaseHistory} {
		if *list == nil {
			*list = []string{}
		}
	}
	return st
}

// PrintSummary writes the four component lists, one per line.
func PrintSummary(w io.Writer, st State) {
	roles := make([]string, 0, len(st.Roles))
	for n := range st.Roles {
		roles = append(roles, n)
	}
	sort.Strings(roles)
	features := append([]string{}, st.Features...)
	sort.Strings(features)
	fmt.Fprintf(w, "  roles:             %s\n", orNone(roles))
	fmt.Fprintf(w, "  roles_excluded:    %s\n", orNone(st.RolesExcluded))
	fmt.Fprintf(w, "  features:          %s\n", orNone(features))
	fmt.Fprintf(w, "  features_excluded: %s\n", orNone(st.FeaturesExcluded))
}

func orNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
