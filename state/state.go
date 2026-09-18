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
	Odios            string            `json:"odios"`
	InstallMode      string            `json:"install_mode"`
	TargetUser       string            `json:"target_user"`
	Audioserver      string            `json:"audioserver" state:"optional"`
	Roles            map[string]string `json:"roles"`
	RolesExcluded    []string          `json:"roles_excluded"`
	Features         []string          `json:"features"`
	FeaturesExcluded []string          `json:"features_excluded"`
	ReleaseHistory   []string          `json:"release_history"`
}

// defaults is what an optional field meant before odios wrote it: PulseAudio
// was the only server.
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

// SchemaError reports a state.json missing required fields or with the wrong
// shape.
type SchemaError struct{ Reason string }

func (e *SchemaError) Error() string { return e.Reason }

func schemaErrorf(format string, args ...any) error {
	return &SchemaError{Reason: fmt.Sprintf(format, args...)}
}

// decode reads b over st (holding the defaults) once every required key is
// there and not null.
func decode(b []byte, st *State) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return schemaErrorf("state.json must be a JSON object")
		}
		return err
	}
	var missing []string
	for _, name := range required {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return schemaErrorf("state.json missing required fields: %s", strings.Join(missing, ", "))
	}
	if err := json.Unmarshal(b, st); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return schemaErrorf("state.json field %q has the wrong shape (want %s)", ute.Field, ute.Type)
		}
		return err
	}
	return validate(*st)
}

func validate(st State) error {
	for name, v := range map[string]string{
		"odios": st.Odios, "install_mode": st.InstallMode, "target_user": st.TargetUser,
	} {
		if v == "" {
			return schemaErrorf("state.json field %q must be a non-empty string", name)
		}
	}
	if st.Audioserver != PulseAudio && st.Audioserver != PipeWire {
		return schemaErrorf("state.json field \"audioserver\" must be %q or %q, got %q",
			PulseAudio, PipeWire, st.Audioserver)
	}
	return nil
}

// Parse decodes and validates state.json content.
func Parse(b []byte) (State, error) {
	st := defaults()
	if err := decode(b, &st); err != nil {
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
