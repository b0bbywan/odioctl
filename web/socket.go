package web

// The sockets are the units that get enabled — odioctl-web.socket (port
// 8021), odioctl-web-proxy.socket (the Unix socket odio-api proxies to), one
// or both — and systemd passes those running from fd 3 on (sd_listen_fds(3)).
// Without LISTEN_FDS the server binds for itself unless --systemd-only.

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
)

const sdListenFdsStart = 3

// ActivationError: systemd handed over something odioctl-web's socket units
// do not declare.
type ActivationError struct{ Reason string }

func (e *ActivationError) Error() string { return e.Reason }

// SystemdListeners returns the listening sockets passed by systemd, none when
// not socket-activated. LISTEN_* is unset so nothing we exec later sees a
// handover meant for us.
func SystemdListeners() ([]net.Listener, error) {
	return systemdListeners(sdListenFdsStart)
}

func systemdListeners(first uintptr) ([]net.Listener, error) {
	pid, pidSet := os.LookupEnv("LISTEN_PID")
	fds, fdsSet := os.LookupEnv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_PID")
	_ = os.Unsetenv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_FDNAMES")
	if !pidSet || !fdsSet {
		return nil, nil
	}
	if pid != strconv.Itoa(os.Getpid()) {
		return nil, nil // inherited from a parent; the handover was not addressed to us
	}
	count, err := strconv.Atoi(fds)
	if err != nil || count < 0 {
		return nil, &ActivationError{"LISTEN_FDS is not a number: " + strconv.Quote(fds)}
	}
	var lns []net.Listener
	for fd := first; fd < first+uintptr(count); fd++ {
		ln, err := streamListener(fd)
		if err != nil {
			closeAll(lns)
			return nil, err
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

// streamListener adopts one passed fd: a TCP port (the LAN door) or a Unix
// socket (the reverse proxy's), nothing else.
func streamListener(fd uintptr) (net.Listener, error) {
	f := os.NewFile(fd, "systemd-socket")
	ln, err := net.FileListener(f)
	_ = f.Close() // FileListener holds its own duplicate
	if err != nil {
		return nil, &ActivationError{fmt.Sprintf("fd %d is not a stream socket (ListenStream=)", fd)}
	}
	switch ln.(type) {
	case *net.TCPListener, *net.UnixListener:
		return ln, nil
	}
	_ = ln.Close()
	return nil, &ActivationError{fmt.Sprintf("fd %d is neither TCP nor a Unix socket", fd)}
}

// listenUnix binds path, replacing a socket a crashed run left behind; any
// other file there is not ours to remove.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil { // the proxy runs as us
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}
