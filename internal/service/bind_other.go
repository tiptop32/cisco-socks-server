//go:build !darwin

package service

import (
	"context"
	"errors"
	"net"
)

// listenBoundToInterface is darwin-only (IP_BOUND_IF); elsewhere the proxy
// falls back to the loopback listener.
func listenBoundToInterface(context.Context, string) (net.Listener, error) {
	return nil, errors.New("binding to an interface is only supported on macOS")
}
