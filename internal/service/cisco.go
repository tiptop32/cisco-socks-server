package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/merzzzl/cisco-socks-server/internal/utils/cisco"
	"github.com/merzzzl/cisco-socks-server/internal/utils/route"
)

func (s *Service) startCisco(ctx context.Context) error {
	maxRetries := 3
	ciscoReadyNotified := false
	connectedByUs := false

	defer func() {
		s.setStatus(func(st *State) {
			st.CiscoConnected = false
		})

		if connectedByUs {
			if err := cisco.Disconnect(context.Background()); err != nil {
				slog.Error("failed to disconnect cisco", "error", err)
			}
		}
	}()

	for ctx.Err() == nil {
		// snapshot LAN before Cisco hijacks the default route — listener will
		// be bound to this interface via IP_BOUND_IF so reply traffic egresses
		// via the physical NIC regardless of routing table.
		if state := s.GetState(); state.LANSubnet == "" {
			if subnet, iface, derr := route.DetectLAN(ctx); derr == nil {
				slog.Info("LAN detected", "subnet", subnet, "interface", iface)
				s.setStatus(func(st *State) {
					st.LANSubnet = subnet
					st.LANInterface = iface
				})
			} else if !errors.Is(derr, route.ErrNoLANInterface) {
				slog.Warn("failed to detect LAN", "error", derr)
			}
		}

		connected, err := cisco.IsConnected(ctx)
		if err != nil {
			slog.Error("failed to get cisco state", "error", err)
		}

		if !connected && err == nil {
			s.setStatus(func(st *State) {
				st.CiscoConnected = false
			})

			if err := cisco.Connect(ctx, s.ciscoProfile, s.ciscoUser, s.ciscoPassword); errors.Is(err, cisco.ErrAcquired) {
				slog.Warn("another Cisco client has connection capability, will retry")
			} else if err != nil {
				if maxRetries == 0 {
					return fmt.Errorf("failed to connect to cisco: %w", err)
				}

				slog.Error("failed to connect to cisco", "error", err)

				maxRetries--
			} else {
				maxRetries = 3
				connectedByUs = true

				s.setStatus(func(st *State) {
					st.CiscoConnected = true
				})
			}
		}

		if connected && err == nil {
			s.setStatus(func(st *State) {
				st.CiscoConnected = true
			})
		}

		state := s.GetState()

		if state.CiscoConnected && !ciscoReadyNotified {
			close(s.ciscoReady)
			ciscoReadyNotified = true
		}

		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}

	return nil
}
