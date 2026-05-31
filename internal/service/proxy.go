package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"

	socks5 "github.com/things-go/go-socks5"
	"golang.org/x/sys/unix"
)

type proxyLogger struct{}

func (p *proxyLogger) Errorf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
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

	server := socks5.NewServer(socks5.WithConnectMiddleware(func(_ context.Context, _ io.Writer, request *socks5.Request) error {
		slog.Info("connection to " + request.DestAddr.Address())

		return nil
	}), socks5.WithLogger(&proxyLogger{}))

	state := s.GetState()

	// Listener 1: loopback (no IP_BOUND_IF) — for localhost clients
	loopbackList, err := net.Listen("tcp4", "127.0.0.1:8080")
	if err != nil {
		return fmt.Errorf("failed to listen on 127.0.0.1:8080: %w", err)
	}

	listeners := []net.Listener{loopbackList}

	// Listener 2: LAN IP with IP_BOUND_IF — for LAN clients
	if state.LANInterface != "" {
		ifi, ifErr := net.InterfaceByName(state.LANInterface)
		if ifErr != nil {
			slog.Warn("failed to lookup LAN interface",
				"interface", state.LANInterface, "error", ifErr)
		} else {
			lanIP := interfaceIPv4(ifi)
			if lanIP != "" {
				lc := net.ListenConfig{}
				idx := ifi.Index
				lc.Control = func(_, _ string, c syscall.RawConn) error {
					var serr error
					cerr := c.Control(func(fd uintptr) {
						serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, idx)
					})
					if cerr != nil {
						return cerr
					}
					return serr
				}

				lanList, lanErr := lc.Listen(ctx, "tcp4", lanIP+":8080")
				if lanErr != nil {
					slog.Warn("failed to listen on LAN IP",
						"ip", lanIP, "error", lanErr)
				} else {
					slog.Info("proxy bound to LAN interface",
						"interface", state.LANInterface, "ip", lanIP)
					listeners = append(listeners, lanList)
				}
			} else {
				slog.Warn("no IPv4 address found on LAN interface",
					"interface", state.LANInterface)
			}
		}
	} else {
		slog.Warn("no LAN interface detected, proxy will use loopback only")
	}

	s.setStatus(func(st *State) {
		st.ProxyStarted = true
	})

	slog.Info("starting SOCKS5 server on 8080")

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, l := range listeners {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptConns(ctx, l, server)
		}()
		go func() {
			<-ctx.Done()
			_ = l.Close()
		}()
	}

	wg.Wait()
	slog.Info("proxy server stopped")
	return nil
}

func interfaceIPv4(ifi *net.Interface) string {
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ip := ipnet.IP.To4(); ip != nil {
				return ip.String()
			}
		}
	}
	return ""
}

func acceptConns(ctx context.Context, l net.Listener, server *socks5.Server) {
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("accept error", "error", err)
			return
		}
		go server.ServeConn(conn)
	}
}
