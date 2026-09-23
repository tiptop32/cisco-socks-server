package service

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenBoundToInterface sets IP_BOUND_IF on the listener socket; accepted
// sockets inherit it, so replies egress via iface regardless of the routes
// Cisco installs. tcp4 is required: IP_BOUND_IF is the AF_INET variant.
func listenBoundToInterface(ctx context.Context, iface string) (net.Listener, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}

	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error

			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifi.Index)
			}); err != nil {
				return err
			}

			return serr
		},
	}

	return lc.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", proxyPort))
}
