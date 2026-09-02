// Package fsutil holds small filesystem helpers shared by the writers
// (state.json, config.txt).
package fsutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

func whoami() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return strconv.Itoa(os.Geteuid())
}

func defaultMode() fs.FileMode {
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	return fs.FileMode(0o666 &^ umask)
}

const specialBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func warnIfErr(err error) {
	if err != nil {
		slog.Warn(err.Error())
	}
}

// AtomicWriteText writes text to path atomically (temp file + rename in the
// same directory).
//
// Mode (and owner/group, when running as root) are copied from the existing
// file so a rewrite never widens or narrows permissions; a new file gets the
// umask default (CreateTemp's 0600 would be wrong for a config other users
// must read). chmod/chown are best-effort — vfat (the Pi boot partition)
// fakes modes. When the directory itself refuses new files (e.g.
// /var/lib/odio is 2750 and we are not root) we fall back to an in-place
// rewrite of the existing file, which is not atomic but keeps the tool
// usable; a file that is not writable either gets an actionable error.
func AtomicWriteText(path, text string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	prev, statErr := os.Stat(path)

	tmp, err := os.CreateTemp(dir, ".odioctl-")
	if errors.Is(err, fs.ErrPermission) {
		return writeInPlace(path, text)
	}
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Best-effort — vfat fakes modes — but a failure is worth a warning:
	// a state.json left 0600 locks the odio group out.
	if statErr == nil {
		warnIfErr(os.Chmod(tmp.Name(), prev.Mode()&specialBits))
		if st, ok := prev.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
			warnIfErr(os.Chown(tmp.Name(), int(st.Uid), int(st.Gid)))
		}
	} else {
		warnIfErr(os.Chmod(tmp.Name(), defaultMode()))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	renamed = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func writeInPlace(path, text string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf(
			"%s is not writable by %s and its directory refuses new files "+
				"(expected /var/lib/odio 2770 root:odio and state.json 0660): %w",
			path, whoami(), err)
	}
	if err != nil {
		return err
	}
	slog.Warn("directory not writable, rewriting in place",
		"dir", filepath.Dir(path), "path", path)
	defer func() { _ = f.Close() }()
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		return err
	}
	return f.Sync()
}

// AtomicWriteJSON writes data as indent-4 JSON (maps sort their keys), the
// shape ansible's to_nice_json produces.
func AtomicWriteJSON(path string, data any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(data); err != nil {
		return err
	}
	return AtomicWriteText(path, buf.String())
}
