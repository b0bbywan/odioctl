package web

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

// setListenEnv installs LISTEN_* for one test; systemdListener consumes them.
func setListenEnv(t *testing.T, pid, fds string) {
	t.Helper()
	t.Setenv("LISTEN_PID", pid)
	t.Setenv("LISTEN_FDS", fds)
}

// listenFd makes a bound TCP listener and hands back a duplicate of its fd,
// standing in for the fd 3 systemd would pass.
func listenFd(t *testing.T) (net.Listener, uintptr) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return ln, f.Fd()
}

func TestNoHandoverMeansNoActivation(t *testing.T) {
	t.Setenv("LISTEN_PID", "x") // restore on cleanup…
	os.Unsetenv("LISTEN_PID")   // …but unset for the call
	t.Setenv("LISTEN_FDS", "x")
	os.Unsetenv("LISTEN_FDS")
	ln, err := SystemdListener()
	if ln != nil || err != nil {
		t.Errorf("got %v, %v", ln, err)
	}
}

func TestHandoverAddressedToAParentIsIgnored(t *testing.T) {
	setListenEnv(t, strconv.Itoa(os.Getpid()+1), "1")
	ln, err := SystemdListener()
	if ln != nil || err != nil {
		t.Errorf("got %v, %v", ln, err)
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
	ln, err := SystemdListener()
	if ln != nil || err != nil {
		t.Errorf("got %v, %v", ln, err)
	}
}

func TestUnexpectedHandoverIsRefused(t *testing.T) {
	for _, fds := range []string{"2", "not-a-number"} {
		setListenEnv(t, strconv.Itoa(os.Getpid()), fds)
		_, err := SystemdListener()
		var ae *ActivationError
		if !errors.As(err, &ae) {
			t.Errorf("LISTEN_FDS=%s: err = %v, want ActivationError", fds, err)
		}
	}
}

func TestTheInheritedSocketIsTheOneSystemdOpened(t *testing.T) {
	ln, fd := listenFd(t)
	setListenEnv(t, strconv.Itoa(os.Getpid()), "1")
	got, err := systemdListener(fd)
	if err != nil || got == nil {
		t.Fatalf("got %v, %v", got, err)
	}
	defer got.Close()
	if got.Addr().String() != ln.Addr().String() {
		t.Errorf("addr = %v, want %v", got.Addr(), ln.Addr())
	}
	if _, set := os.LookupEnv("LISTEN_PID"); set {
		t.Error("LISTEN_PID still in the environment")
	}
}

func TestAUnixSocketIsRefused(t *testing.T) {
	ln, err := net.Listen("unix", t.TempDir()+"/web.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f, err := ln.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	setListenEnv(t, strconv.Itoa(os.Getpid()), "1")
	_, err = systemdListener(f.Fd())
	var ae *ActivationError
	if !errors.As(err, &ae) || !strings.Contains(ae.Reason, "not TCP") {
		t.Errorf("err = %v", err)
	}
}

func TestServesOnASocketItDidNotBind(t *testing.T) {
	f := newFixture(t)
	ln, fd := listenFd(t)
	setListenEnv(t, strconv.Itoa(os.Getpid()), "1")
	inherited, err := systemdListener(fd)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: NewHandler(f.svc)}
	go srv.Serve(inherited)
	t.Cleanup(func() { srv.Close() })
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(buf.String(), "<h1>Settings</h1>") {
		t.Errorf("code = %d", resp.StatusCode)
	}
}

func TestServeRefusesABrokenHandover(t *testing.T) {
	setListenEnv(t, strconv.Itoa(os.Getpid()), "2")
	var stderr bytes.Buffer
	rc := RunServe(&bytes.Buffer{}, &stderr, DefaultConfig())
	if rc != 2 || !strings.Contains(stderr.String(), "got 2") {
		t.Errorf("rc = %d, stderr = %q", rc, stderr.String())
	}
}
