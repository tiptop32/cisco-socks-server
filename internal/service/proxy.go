package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"golang.org/x/sync/errgroup"
)

const (
	proxyPort          = "8080"
	acceptErrorBackoff = 500 * time.Millisecond
	vpnWatchInterval   = 2 * time.Second
)

type proxyLogger struct{}

func (*proxyLogger) Errorf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
}

// connTracker remembers live client connections so they can be dropped at
// once when the tunnel goes away: their upstream legs are dead by then, and
// without an explicit close clients hang until TCP gives up.
type connTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[net.Conn]struct{})}
}

func (ct *connTracker) add(c net.Conn) {
	ct.mu.Lock()
	ct.conns[c] = struct{}{}
	ct.mu.Unlock()
}

func (ct *connTracker) remove(c net.Conn) {
	ct.mu.Lock()
	delete(ct.conns, c)
	ct.mu.Unlock()
}

func (ct *connTracker) closeAll() int {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	n := len(ct.conns)
	for c := range ct.conns {
		_ = c.Close()
	}

	clear(ct.conns)

	return n
}

func (s *Service) startProxy(ctx context.Context) error {
	defer s.setStatus(func(st *State) {
		st.ProxyStarted = false
	})

	select {
	case <-s.ciscoReady:
	case <-ctx.Done():
		return nil
	}

	listeners, err := s.proxyListeners(ctx)
	if err != nil {
		return err
	}

	server := socks5.NewServer(socks5.WithConnectMiddleware(func(_ context.Context, _ io.Writer, request *socks5.Request) error {
		slog.Info("connection to " + request.DestAddr.Address())

		return nil
	}), socks5.WithLogger(&proxyLogger{}))

	tracker := newConnTracker()
	defer tracker.closeAll()

	s.setStatus(func(st *State) {
		st.ProxyStarted = true
	})

	slog.Info("starting SOCKS5 server on " + proxyPort)

	g, gctx := errgroup.WithContext(ctx)

	for _, l := range listeners {
		g.Go(func() error {
			return acceptConns(gctx, l, server, tracker)
		})
	}

	g.Go(func() error {
		s.dropConnsOnVPNLoss(gctx, tracker)

		return nil
	})

	// unblocks Accept; gctx is also cancelled when Wait returns, so this
	// goroutine never outlives startProxy
	go func() {
		<-gctx.Done()

		for _, l := range listeners {
			_ = l.Close()
		}
	}()

	if err := g.Wait(); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}

	slog.Info("proxy server stopped")

	return nil
}

// proxyListeners opens the loopback listener (mandatory, serves localhost)
// and, when a LAN interface is known, a wildcard listener pinned to it via
// IP_BOUND_IF (serves LAN clients). The LAN listener is best-effort.
func (s *Service) proxyListeners(ctx context.Context) ([]net.Listener, error) {
	var lc net.ListenConfig

	loopback, err := lc.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", proxyPort))
	if err != nil {
		return nil, fmt.Errorf("failed to listen on loopback: %w", err)
	}

	listeners := []net.Listener{loopback}

	iface := s.GetState().LANInterface
	if iface == "" {
		slog.Warn("no LAN interface detected, proxy serves localhost only")

		return listeners, nil
	}

	lan, err := listenBoundToInterface(ctx, iface)
	if err != nil {
		slog.Warn("failed to listen on LAN interface", "interface", iface, "error", err)

		return listeners, nil
	}

	slog.Info("proxy bound to LAN interface", "interface", iface)

	return append(listeners, lan), nil
}

func acceptConns(ctx context.Context, l net.Listener, server *socks5.Server, tracker *connTracker) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			if errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("listener %s closed unexpectedly: %w", l.Addr(), err)
			}

			// transient (EMFILE, ECONNABORTED, ...): back off and keep serving
			slog.Warn("accept error", "error", err)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(acceptErrorBackoff):
			}

			continue
		}

		tracker.add(conn)

		go func() {
			defer tracker.remove(conn)

			_ = server.ServeConn(conn)
		}()
	}
}

func (s *Service) dropConnsOnVPNLoss(ctx context.Context, tracker *connTracker) {
	ticker := time.NewTicker(vpnWatchInterval)
	defer ticker.Stop()

	wasConnected := s.GetState().CiscoConnected

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		connected := s.GetState().CiscoConnected
		if wasConnected && !connected {
			if n := tracker.closeAll(); n > 0 {
				slog.Info(fmt.Sprintf("VPN lost, dropped %d active connections", n))
			}
		}

		wasConnected = connected
	}
}
