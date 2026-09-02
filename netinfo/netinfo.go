// Package netinfo answers "how does the LAN reach this box": the source IP
// of the default route, and the PWA deep link built from it.
package netinfo

import "net"

const BaseURL = "https://pwa.odio.love"

// DefaultRouteIP is the source IP of the default route, "" when there is
// none. A UDP dial makes the kernel pick route and source address without
// sending a packet.
func DefaultRouteIP() string {
	conn, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}

// PWAURL is https://pwa.odio.love/#/i/<ip>, or the bare PWA URL when no IP is
// detectable, so callers (motd, post-install summary) always get something
// printable.
func PWAURL() string {
	if ip := DefaultRouteIP(); ip != "" {
		return BaseURL + "/#/i/" + ip
	}
	return BaseURL
}
