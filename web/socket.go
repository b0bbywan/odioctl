package web

// odioctl-web.socket is the unit that gets enabled: systemd binds port 8021
// and starts the service on the first connection, passing the listening
// socket as fd 3 (sd_listen_fds(3)). Without LISTEN_FDS the server binds for
// itself, so the dev loop and --bind/--port are unchanged.

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

const sdListenFdsStart = 3

// ActivationError: systemd handed over something other than the one socket
// odioctl-web.socket declares.
type ActivationError struct{ Reason string }

func (e *ActivationError) Error() string { return e.Reason }

// SystemdListener returns the listening socket passed by systemd, nil when
// not socket-activated. The LISTEN_* variables are removed from the
// environment so nothing we exec later (`sudo odioctl dac …`) sees a
// handover meant for us.
func SystemdListener() (net.Listener, error) {
	return systemdListener(sdListenFdsStart)
}

func systemdListener(fd uintptr) (net.Listener, error) {
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
	if err != nil {
		return nil, &ActivationError{"LISTEN_FDS is not a number: " + strconv.Quote(fds)}
	}
	if count == 0 {
		return nil, nil
	}
	if count != 1 {
		return nil, &ActivationError{fmt.Sprintf("expected one socket from systemd, got %d", count)}
	}
	f := os.NewFile(fd, "systemd-socket")
	ln, err := net.FileListener(f)
	_ = f.Close() // FileListener holds its own duplicate
	if err != nil {
		return nil, &ActivationError{"the activation socket is not a stream socket (ListenStream=)"}
	}
	if _, ok := ln.(*net.TCPListener); !ok {
		// The page links to odio-ui by host:port and reads the Host header; a
		// Unix socket would have no address to speak of.
		_ = ln.Close()
		return nil, &ActivationError{"the activation socket is not TCP (use ListenStream=PORT)"}
	}
	return ln, nil
}
