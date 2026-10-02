package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/b0bbywan/odioctl/config"
)

// releaseClient: a tarball, not a manifest, hence a timeout of its own.
var releaseClient = &http.Client{Timeout: 5 * time.Minute}

// fetchRelease downloads the release archive at url and extracts it into a
// temp dir of its own, the caller's to remove; a var so tests hand their own.
var fetchRelease = func(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.AppName+"/"+config.AppVersion)
	resp, err := releaseClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	dir, err := os.MkdirTemp("", "odioctl-release-*")
	if err != nil {
		return "", err
	}
	if err := extract(resp.Body, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// extract unpacks a tar.gz of directories and regular files into dir. It runs
// as root: anything else, or a path out of dir (os.Root's refusal), fails it.
func extract(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o755)
		case tar.TypeReg:
			err = extractFile(root, name, tr, h.FileInfo().Mode().Perm())
		default:
			err = fmt.Errorf("%s: neither a file nor a directory", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func extractFile(root *os.Root, name string, r io.Reader, perm os.FileMode) error {
	if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
