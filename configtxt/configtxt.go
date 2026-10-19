// Package configtxt is the Raspberry Pi config.txt as odioctl edits it: the
// file, the marked block each owner keeps in it, and the reboot a change
// waits for. Writing needs root.
package configtxt

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/b0bbywan/odioctl/fsutil"
)

// Path is where Raspberry Pi OS keeps config.txt since bookworm. The
// pre-bookworm /boot/config.txt is not looked at: no legacy support.
const Path = "/boot/firmware/config.txt"

// RebootFlag is a var so tests can point it into a tempdir.
var RebootFlag = "/run/odioctl/reboot-required"

// Resolve is the file a command works on: path, Path when none was given.
func Resolve(path string) string {
	if path == "" {
		return Path
	}
	return path
}

var overlayRE = regexp.MustCompile(`^\s*dtoverlay\s*=\s*([^,\s]+)(?:,(.*))?\s*$`)

// Overlay reads an active `dtoverlay=name[,params]` line.
func Overlay(line string) (name, params string, ok bool) {
	m := overlayRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// Block is the marked lines one owner keeps in config.txt. Disabled prefixes
// the lines of the file the block replaces, commented out until Remove.
type Block struct {
	Begin, End, Disabled string
}

// Split returns (before, block, after); block is nil when the file has none,
// and a Begin without End swallows to EOF.
func (b Block) Split(lines []string) (before, block, after []string) {
	start := slices.Index(lines, b.Begin)
	if start < 0 {
		return lines, nil, nil
	}
	end := slices.Index(lines[start+1:], b.End)
	if end < 0 {
		end = len(lines) - 1
	} else {
		end += start + 1
	}
	return lines[:start], lines[start : end+1], lines[end+1:]
}

// Rest is the file without the block, the lines replaced names commented
// out. The owner puts its block before or after it.
func (b Block) Rest(text string, replaced func(line string) bool) []string {
	before, _, after := b.Split(strings.Split(text, "\n"))
	var body []string
	for _, line := range slices.Concat(before, after) {
		if replaced(line) {
			line = b.Disabled + line
		}
		body = append(body, line)
	}
	return trimBlank(body)
}

// Remove returns the file without the block, the commented-out lines restored.
func (b Block) Remove(text string) string {
	before, _, after := b.Split(strings.Split(text, "\n"))
	var body []string
	for _, line := range slices.Concat(before, after) {
		body = append(body, strings.TrimPrefix(line, b.Disabled))
	}
	body = trimBlank(body)
	if len(body) == 0 {
		return ""
	}
	return strings.Join(body, "\n") + "\n"
}

// trimBlank drops blank lines at both ends: a block is set off by one blank
// line of its own, and removing it must not leave a gap behind.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func Read(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// Write keeps a one-time .odioctl.bak of the pristine file, then writes
// atomically.
func Write(path, text string) error {
	backup := path + ".odioctl.bak"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if orig, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(backup, orig, 0o644); err != nil {
				slog.Warn("could not write backup", "path", backup, "err", err)
			}
		}
	}
	return fsutil.AtomicWriteText(path, text)
}

func MarkRebootRequired() {
	if err := os.MkdirAll(filepath.Dir(RebootFlag), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(RebootFlag, []byte("config.txt\n"), 0o644)
}

func RebootRequired() bool {
	_, err := os.Stat(RebootFlag)
	return err == nil
}

// RunEdit rewrites config.txt through edit and flags the reboot; done is
// what it prints once written. With dryRun the result is printed instead.
func RunEdit(stdout, stderr io.Writer, configPath string, dryRun bool, done string, edit func(text string) string) int {
	path := Resolve(configPath)
	if !isFile(path) {
		fmt.Fprintf(stderr, "Error: %s not found — not a Raspberry Pi?\n", path)
		return 2
	}
	text, err := Read(path)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading %s: %v\n", path, err)
		return 2
	}
	updated := edit(text)
	if dryRun {
		fmt.Fprint(stdout, updated)
		return 0
	}
	if updated == text {
		fmt.Fprintln(stdout, "no change")
		return 0
	}
	if err := Write(path, updated); err != nil {
		fmt.Fprintf(stderr, "Error writing %s: %v\n", path, err)
		return 2
	}
	MarkRebootRequired()
	fmt.Fprintf(stdout, "%s in %s — reboot required\n", done, path)
	return 0
}
