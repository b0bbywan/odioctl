package state

// `odioctl state record`: odios' write_state.yml hands over what its run
// installed, odioctl owns the rest — the schema, the history, the file's mode.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
)

// FileMode is state.json's side of the contract with odios: /var/lib/odio is
// 2770 root:odio, the file group-writable for the web UI's toggles.
const FileMode fs.FileMode = 0o660

// runKeys: a run is a State without the history, odioctl keeps that.
var runKeys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "release_history" })

// ParseRun decodes what one odios run reports, strictly: every field, nothing
// else — a field odioctl does not know is an odios newer than it, refused.
func ParseRun(b []byte) (State, error) {
	var run State
	if err := decode(b, &run, "the run", runKeys, runKeys); err != nil {
		return State{}, err
	}
	return run, nil
}

// Record is the state after run, prev being what state.json held (nil: none).
func Record(prev *State, run State) State {
	st := run
	st.ReleaseHistory = nil
	if prev != nil {
		st.ReleaseHistory = slices.Clone(prev.ReleaseHistory)
	}
	if n := len(st.ReleaseHistory); n == 0 || st.ReleaseHistory[n-1] != run.Odios {
		st.ReleaseHistory = append(st.ReleaseHistory, run.Odios)
	}
	// The server not picked was not declined: only the picked one excluded
	// means no audio server at all.
	other := PipeWire
	if run.Audioserver == PipeWire {
		other = PulseAudio
	}
	st.RolesExcluded = slices.DeleteFunc(slices.Clone(run.RolesExcluded), func(r string) bool { return r == other })
	return st
}

// RunRecord is `odioctl state record`: the run on in, state.json written. 2
// for a run refused, 1 when state.json cannot be written.
func RunRecord(in io.Reader, stdout, stderr io.Writer, path string) int {
	b, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintf(stderr, "odioctl state record: %v\n", err)
		return 1
	}
	run, err := ParseRun(bytes.TrimSpace(b))
	if err != nil {
		fmt.Fprintf(stderr, "odioctl state record: %v\n", err)
		return 2
	}
	var prev *State
	switch st, err := Read(path); {
	case err == nil:
		prev = &st
	case errors.Is(err, fs.ErrNotExist):
	case errors.As(err, new(*fs.PathError)):
		fmt.Fprintf(stderr, "odioctl state record: %v\n", err)
		return 1
	default:
		// What is there cannot be kept anyway: this run's state replaces it.
		fmt.Fprintf(stderr, "odioctl state record: replacing %s (%v); release history restarts\n", path, err)
	}
	if err := Write(path, Record(prev, run)); err != nil {
		fmt.Fprintf(stderr, "odioctl state record: %v\n", err)
		return 1
	}
	if err := os.Chmod(path, FileMode); err != nil {
		fmt.Fprintf(stderr, "odioctl state record: warning: %v\n", err)
	}
	fmt.Fprintf(stdout, "Recorded odios %s in %s\n", run.Odios, path)
	return 0
}
