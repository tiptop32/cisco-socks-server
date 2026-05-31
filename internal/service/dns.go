package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/miekg/dns"
)

func (s *Service) startDNS(ctx context.Context) error {
	defer s.setStatus(func(st *State) {
		st.DNSStarted = false
	})

	select {
	case <-s.ciscoReady:
	case <-ctx.Done():
		return nil
	}

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		if len(r.Question) > 0 {
			q := r.Question[0]
			slog.Info("dns query", "name", q.Name, "type", dns.TypeToString[q.Qtype])
		}

		resp, err := s.forwardDNS(r)
		if err != nil {
			slog.Error("dns forward failed", "error", err)

			msg := new(dns.Msg)
			msg.SetRcode(r, dns.RcodeServerFailure)
			_ = w.WriteMsg(msg)

			return
		}

		_ = w.WriteMsg(resp)
	})

	server := &dns.Server{
		Addr:    "127.0.0.1:53",
		Net:     "tcp",
		Handler: handler,
	}

	go func() {
		<-ctx.Done()
		_ = server.Shutdown()
	}()

s.setStatus(func(st *State) {
	st.DNSStarted = true
})

slog.Info("starting DNS server on 127.0.0.1:53")
if err := server.ListenAndServe(); err != nil {
	if ctx.Err() != nil && errors.Is(err, net.ErrClosed) {
		slog.Info("DNS server stopped due to context cancellation")
		return nil
	}

	slog.Error("DNS server failed", "error", err)
	return fmt.Errorf("dns server error: %w", err)
}

slog.Info("DNS server stopped")
return nil
}

func (s *Service) forwardDNS(r *dns.Msg) (*dns.Msg, error) {
	client := &dns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}

	var lastErr error

	for _, server := range s.dnsServers {
		resp, _, err := client.Exchange(r, server+":53")
		if err != nil {
			lastErr = err
			slog.Debug("dns upstream failed", "server", server, "error", err)

			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("all dns servers failed: %w", lastErr)
}
