package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// setListenEnv installs LISTEN_* for one test; systemdListeners consumes them.
func setListenEnv(t *testing.T, pid, fds string) {
	t.Helper()
	t.Setenv("LISTEN_PID", pid)
	t.Setenv("LISTEN_FDS", fds)
}

// passFds hands the listeners over the way systemd would: their fds,
// duplicated onto consecutive numbers from the returned first one.
func passFds(t *testing.T, lns ...net.Listener) uintptr {
	t.Helper()
	const first = 900 // well clear of what the test binary has open
	for i, ln := range lns {
		f, err := ln.(interface{ File() (*os.File, error) }).File()
		if err != nil {
			t.Fatal(err)
		}
		fd := first + i
		if err := syscall.Dup3(int(f.Fd()), fd, syscall.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		f.Close()
		t.Cleanup(func() { syscall.Close(fd) })
	}
	setListenEnv(t, strconv.Itoa(os.Getpid()), strconv.Itoa(len(lns)))
	return first
}

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func unixListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "web.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// unixClient speaks HTTP over the socket at path, whatever the URL's host.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
}

func TestNoHandoverMeansNoActivation(t *testing.T) {
	t.Setenv("LISTEN_PID", "x") // restore on cleanup…
	os.Unsetenv("LISTEN_PID")   // …but unset for the call
	t.Setenv("LISTEN_FDS", "x")
	os.Unsetenv("LISTEN_FDS")
	lns, err := SystemdListeners()
	if lns != nil || err != nil {
		t.Errorf("got %v, %v", lns, err)
	}
}

func TestHandoverAddressedToAParentIsIgnored(t *testing.T) {
	setListenEnv(t, strconv.Itoa(os.Getpid()+1), "1")
	lns, err := SystemdListeners()
	if lns != nil || err != nil {
		t.Errorf("got %v, %v", lns, err)
	}
	// …and never forwarded to `sudo odioctl dac …`.
	if _, set := os.LookupEnv("LISTEN_PID"); set {
		t.Error("LISTEN_PID still in the environment")
	}
	if _, set := os.LookupEnv("LISTEN_FDS"); set {
		t.Error("LISTEN_FDS still in the environment")
	}
}

func TestZeroSocketsMeansNoActivation(t *testing.T) {
	setListenEnv(t, strconv.Itoa(os.Getpid()), "0")
	lns, err := SystemdListeners()
	if lns != nil || err != nil {
		t.Errorf("got %v, %v", lns, err)
	}
}

func TestUnexpectedHandoverIsRefused(t *testing.T) {
	for _, fds := range []string{"-1", "not-a-number"} {
		setListenEnv(t, strconv.Itoa(os.Getpid()), fds)
		_, err := SystemdListeners()
		var ae *ActivationError
		if !errors.As(err, &ae) {
			t.Errorf("LISTEN_FDS=%s: err = %v, want ActivationError", fds, err)
		}
	}
}

func TestTheInheritedSocketsAreTheOnesSystemdOpened(t *testing.T) {
	tcp, unix := tcpListener(t), unixListener(t)
	got, err := systemdListeners(passFds(t, tcp, unix))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	defer closeAll(got)
	for i, want := range []net.Listener{tcp, unix} {
		if got[i].Addr().String() != want.Addr().String() {
			t.Errorf("listener %d: addr = %v, want %v", i, got[i].Addr(), want.Addr())
		}
	}
	if _, set := os.LookupEnv("LISTEN_PID"); set {
		t.Error("LISTEN_PID still in the environment")
	}
}

func TestADatagramSocketIsRefused(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	f, err := pc.(*net.UDPConn).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	setListenEnv(t, strconv.Itoa(os.Getpid()), "1")
	_, err = systemdListeners(f.Fd())
	var ae *ActivationError
	if !errors.As(err, &ae) {
		t.Errorf("err = %v", err)
	}
}

func TestServesOnSocketsItDidNotBind(t *testing.T) {
	f := newFixture(t)
	tcp, unix := tcpListener(t), unixListener(t)
	inherited, err := systemdListeners(passFds(t, tcp, unix))
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(NewHandler(f.app))
	for _, ln := range inherited {
		go srv.Serve(ln)
	}
	t.Cleanup(func() { srv.Close() })
	for _, get := range []func() (*http.Response, error){
		func() (*http.Response, error) { return http.Get("http://" + tcp.Addr().String() + "/") },
		func() (*http.Response, error) { return unixClient(unix.Addr().String()).Get("http://odioctl/") },
	} {
		resp, err := get()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "<h1>Settings</h1>") {
			t.Errorf("code = %d", resp.StatusCode)
		}
	}
}

func TestServeRefusesABrokenHandover(t *testing.T) {
	setListenEnv(t, strconv.Itoa(os.Getpid()), "nope")
	var stderr bytes.Buffer
	rc := RunServe(&bytes.Buffer{}, &stderr, DefaultConfig())
	if rc != 2 || !strings.Contains(stderr.String(), "not a number") {
		t.Errorf("rc = %d, stderr = %q", rc, stderr.String())
	}
}

// Under the units, no socket passed means no door at all, not port 8021.
func TestSystemdOnlyNeverBinds(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	os.Unsetenv("LISTEN_PID")
	cfg := DefaultConfig()
	cfg.SystemdOnly = true
	cfg.Bind, cfg.Port = "127.0.0.1", 0
	cfg.Socket = filepath.Join(t.TempDir(), "web.sock")
	var stdout, stderr bytes.Buffer
	rc := RunServe(&stdout, &stderr, cfg)
	if rc != 2 || !strings.Contains(stderr.String(), "systemd passed no socket") || stdout.Len() != 0 {
		t.Errorf("rc = %d, stdout = %q, stderr = %q", rc, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(cfg.Socket); err == nil {
		t.Error("--socket was bound anyway")
	}
}

func TestBindOpensThePortAndTheSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.sock")
	lns, err := bind(Config{Bind: "127.0.0.1", Port: 0, Socket: path})
	if err != nil || len(lns) != 2 {
		t.Fatalf("got %v, %v", lns, err)
	}
	defer closeAll(lns)
	if got := describe(lns[0]); !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("describe(tcp) = %q", got)
	}
	if got := describe(lns[1]); got != path {
		t.Errorf("describe(unix) = %q", got)
	}
}

// A socket that cannot be opened takes the port back down: no half-served odio.
func TestBindIsAllOrNothing(t *testing.T) {
	blocker := tcpListener(t)
	port := blocker.Addr().(*net.TCPAddr).Port
	blocker.Close()
	_, err := bind(Config{Bind: "127.0.0.1", Port: port, Socket: filepath.Join(t.TempDir(), "no", "such", "dir", "web.sock")})
	if err == nil {
		t.Fatal("bound with a socket in a missing directory")
	}
	ln, err := net.Listen("tcp", blocker.Addr().String())
	if err != nil {
		t.Fatalf("the port was left open: %v", err)
	}
	ln.Close()
}

func TestAPortBoundToAllAddressesIsShownOnOne(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got := describe(ln); strings.Contains(got, "[::]") || strings.Contains(got, "0.0.0.0") {
		t.Errorf("describe = %q", got)
	}
}

func TestListenUnixReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false) // a crashed run leaves it
	stale.Close()
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("stat = %v, %v", fi, err)
	}
}

func TestListenUnixLeavesOtherFilesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.sock")
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(path); err == nil {
		t.Fatal("listened over a regular file")
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Errorf("file = %q", b)
	}
}
