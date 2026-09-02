package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRoundTripAndNoTempLeftBehind(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "f.txt")
	if err := AtomicWriteText(path, "héllo\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "héllo\n" {
		t.Errorf("read back %q", got)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 || entries[0].Name() != "f.txt" {
		t.Errorf("leftover files in %s: %v", d, entries)
	}
}

func TestPreservesModeOfExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o640)
	if err := AtomicWriteText(path, "new"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 0640", st.Mode().Perm())
	}
}

func TestFallsBackToInPlaceWhenDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	d := t.TempDir()
	path := filepath.Join(d, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(d, 0o500)
	defer os.Chmod(d, 0o700)
	if err := AtomicWriteText(path, "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("read back %q, want \"new\"", got)
	}
}

func TestActionableErrorWhenNothingWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	d := t.TempDir()
	path := filepath.Join(d, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o440)
	os.Chmod(d, 0o500)
	defer os.Chmod(d, 0o700)
	err := AtomicWriteText(path, "new")
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Errorf("err = %v, want actionable message", err)
	}
}

func TestAtomicWriteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := AtomicWriteJSON(path, map[string]any{"b": 1, "a": []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "{\n    \"a\": [\n        1,\n        2\n    ],\n    \"b\": 1\n}\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewFileGetsUmaskDefaultNot0600(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "new.txt")
	if err := AtomicWriteText(path, "x"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0644", st.Mode().Perm())
	}
}
