package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validJSON = `{
    "odios": "2026.5.0",
    "install_mode": "image",
    "target_user": "odio",
    "roles": {"mpd": "2026.5.0"},
    "roles_excluded": [],
    "features": ["tidal"],
    "features_excluded": [],
    "release_history": ["2026.5.0"]
}`

func wantSchemaError(t *testing.T, err error, substr string) {
	t.Helper()
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *SchemaError", err)
	}
	if !strings.Contains(se.Reason, substr) {
		t.Errorf("error %q does not mention %q", se.Reason, substr)
	}
}

func TestCurrentSchemaRoundTrips(t *testing.T) {
	got, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Odios:            "2026.5.0",
		InstallMode:      "image",
		TargetUser:       "odio",
		Roles:            map[string]string{"mpd": "2026.5.0"},
		RolesExcluded:    []string{},
		Features:         []string{"tidal"},
		FeaturesExcluded: []string{},
		ReleaseHistory:   []string{"2026.5.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestNotAnObject(t *testing.T) {
	_, err := Parse([]byte(`["nope"]`))
	wantSchemaError(t, err, "JSON object")
}

func TestMissingFieldsAreListed(t *testing.T) {
	_, err := Parse([]byte(`{"odios": "2026.5.0", "roles": {}}`))
	wantSchemaError(t, err, "target_user")
	wantSchemaError(t, err, "release_history")
}

func TestLegacyFeaturesDictIsRejected(t *testing.T) {
	// rc3-era shape ({name: bool}) is legacy — not supported any more.
	raw := strings.Replace(validJSON, `["tidal"]`, `{"tidal": true}`, 1)
	_, err := Parse([]byte(raw))
	wantSchemaError(t, err, "features")
}

func TestRolesMustMapStrToStr(t *testing.T) {
	raw := strings.Replace(validJSON, `{"mpd": "2026.5.0"}`, `{"mpd": 1}`, 1)
	_, err := Parse([]byte(raw))
	wantSchemaError(t, err, "roles")
}

func TestEmptyTargetUserIsRejected(t *testing.T) {
	raw := strings.Replace(validJSON, `"target_user": "odio"`, `"target_user": ""`, 1)
	_, err := Parse([]byte(raw))
	wantSchemaError(t, err, "target_user")
}

func TestReadValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(validJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles["mpd"] != "2026.5.0" {
		t.Errorf("roles = %v", got.Roles)
	}
}

func TestReadInvalidJSONFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := Read(path); err == nil {
		t.Error("want error on invalid JSON")
	}
}

func TestWriteMatchesAnsibleToNiceJSON(t *testing.T) {
	st, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	st.Roles = map[string]string{"mpd": "x"}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Write(path, st); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `{
    "features": [
        "tidal"
    ],
    "features_excluded": [],
    "install_mode": "image",
    "odios": "2026.5.0",
    "release_history": [
        "2026.5.0"
    ],
    "roles": {
        "mpd": "x"
    },
    "roles_excluded": [],
    "target_user": "odio"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteNilSlicesStayReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := State{Odios: "x", InstallMode: "image", TargetUser: "odio"}
	if err := Write(path, st); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err != nil {
		t.Errorf("read back: %v", err)
	}
}

func TestWritePreservesMode(t *testing.T) {
	st, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Write(path, st); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o660)
	if err := Write(path, st); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o660 {
		t.Errorf("mode = %o, want 0660", fi.Mode().Perm())
	}
}
