package upgrade

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/b0bbywan/odioctl/manifest"
	"github.com/b0bbywan/odioctl/state"
)

func makeState() state.State {
	return state.State{
		Odios:            "2026.5.0",
		InstallMode:      "image",
		TargetUser:       "odio",
		Roles:            map[string]string{},
		RolesExcluded:    []string{},
		Features:         []string{},
		FeaturesExcluded: []string{},
		ReleaseHistory:   []string{"2026.5.0"},
	}
}

func writeState(t *testing.T, dir string, st state.State) string {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	if err := state.Write(path, st); err != nil {
		t.Fatal(err)
	}
	return path
}

func swapFetch(t *testing.T, f func(string) (*manifest.Manifest, error)) *[]string {
	t.Helper()
	var urls []string
	old := manifest.Fetch
	manifest.Fetch = func(url string) (*manifest.Manifest, error) {
		urls = append(urls, url)
		return f(url)
	}
	t.Cleanup(func() { manifest.Fetch = old })
	return &urls
}

// fetchOf serves m on every fetch; fetchDown fails them all.
func fetchOf(m manifest.Manifest) func(string) (*manifest.Manifest, error) {
	return func(string) (*manifest.Manifest, error) { c := m; return &c, nil }
}

func fetchDown(string) (*manifest.Manifest, error) {
	return nil, errors.New("network down")
}

func noFetch(t *testing.T) {
	t.Helper()
	swapFetch(t, func(url string) (*manifest.Manifest, error) {
		t.Errorf("unexpected fetch of %s", url)
		return nil, errors.New("unexpected fetch")
	})
}

func noInstall(t *testing.T) {
	t.Helper()
	old := runInstall
	runInstall = func(url string, env map[string]string) int {
		t.Errorf("unexpected install invocation of %s", url)
		return 1
	}
	t.Cleanup(func() { runInstall = old })
}

func man(odios string, roles map[string]string) manifest.Manifest {
	return manifest.Manifest{Odios: odios, Roles: roles}
}
