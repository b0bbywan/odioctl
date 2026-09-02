package netinfo

import (
	"net"
	"strings"
	"testing"
)

func TestDefaultRouteIPIsEmptyOrValid(t *testing.T) {
	// Environment-dependent: a box with no default route legitimately gets "".
	if ip := DefaultRouteIP(); ip != "" && net.ParseIP(ip) == nil {
		t.Errorf("DefaultRouteIP() = %q, not an IP", ip)
	}
}

func TestPWAURLAlwaysPrintable(t *testing.T) {
	url := PWAURL()
	if !strings.HasPrefix(url, BaseURL) {
		t.Errorf("PWAURL() = %q", url)
	}
	if url != BaseURL && !strings.HasPrefix(url, BaseURL+"/#/i/") {
		t.Errorf("PWAURL() = %q, want the /#/i/<ip> shape", url)
	}
}
