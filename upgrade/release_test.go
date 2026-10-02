package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// tarball builds a tar.gz of hdrs, a regular file's content being its name.
func tarball(t *testing.T, hdrs ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(h.Name))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(h.Name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dirEntry(name string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}
}

func fileEntry(name string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}
}

// The layout of odios' archive: no top dir, and a file may come before its dir.
func TestFetchReleaseExtractsTheArchive(t *testing.T) {
	body := tarball(t, dirEntry("ansible/"), fileEntry("ansible/disable.yml"),
		fileEntry("vendor/bin/ansible-playbook"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TMPDIR", t.TempDir())
	dir, err := fetchRelease(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ansible/disable.yml", "vendor/bin/ansible-playbook"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != name {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir = %v, %v", fi, err)
	}
}

func TestFetchReleaseLeavesNothingOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/404" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tarball(t, fileEntry("../escape")))
	}))
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for _, path := range []string{"/404", "/escape"} {
		if dir, err := fetchRelease(srv.URL + path); err == nil {
			t.Errorf("%s: extracted into %s", path, dir)
		}
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// It runs as root: nothing but files and directories, nothing out of dir.
func TestExtractRefusesWhatCouldLeaveTheDir(t *testing.T) {
	for name, h := range map[string]tar.Header{
		"symlink":   {Name: "ansible", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"hardlink":  {Name: "passwd", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"},
		"traversal": fileEntry("ansible/../../escape"),
		"absolute":  fileEntry("/escape"),
	} {
		dir := t.TempDir()
		if err := extract(bytes.NewReader(tarball(t, h)), dir); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
}
