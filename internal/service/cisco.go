package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/merzzzl/cisco-socks-server/internal/utils/cisco"
	"github.com/merzzzl/cisco-socks-server/internal/utils/route"
)

const (
	ciscoPollInterval    = 5 * time.Second
	ciscoMaxConnectDelay = time.Minute
	// how long to trust agent-driven reconnection before forcing a disconnect:
	// the agent can stay in "Reconnecting" indefinitely (observed 6+ min), and
	// only an explicit "vpn -s disconnect" kicks it out of that state.
	ciscoReconnectGrace = 2 * time.Minute
	// shutdown must not hang on a wedged agent
	ciscoDisconnectTimeout = 30 * time.Second
)

func (s *Service) startCisco(ctx context.Context) error {
	ciscoReadyNotified := false
	connectedByUs := false
	connectDelay := ciscoPollInterval

	var (
		reconnectingSince time.Time
		delay             time.Duration // first tick runs immediately
	)

	defer func() {
		s.setStatus(func(st *State) {
			st.CiscoConnected = false
		})

		if connectedByUs {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ciscoDisconnectTimeout)
			defer cancel()

			if err := cisco.Disconnect(dctx); err != nil {
				slog.Error("failed to disconnect cisco", "error", err)
			}
		}
	}()

	for sleepCtx(ctx, delay) {
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

		delay = ciscoPollInterval

		ciscoState, err := cisco.Status(ctx)

		switch {
		case err != nil:
			// transient CLI failure (the vpn binary occasionally aborts);
			// keep the last known status and poll again
			slog.Error("failed to get cisco state", "error", err)
		case ciscoState == cisco.StateConnected:
			connectDelay = ciscoPollInterval
			reconnectingSince = time.Time{}

			s.setStatus(func(st *State) {
				st.CiscoConnected = true
			})
		case ciscoState == cisco.StateReconnecting:
			// the agent is re-establishing the tunnel on its own; issuing
			// "connect" now would only interfere — wait for it to settle.
			// Cisco re-enables pf on reconnect, so pfctl -d must run again.
			s.setStatus(func(st *State) {
				st.CiscoConnected = false
				st.PFDisabled = false
			})

			if reconnectingSince.IsZero() {
				reconnectingSince = time.Now()
			}

			if elapsed := time.Since(reconnectingSince); elapsed > ciscoReconnectGrace {
				slog.Warn("cisco agent stuck in reconnecting, forcing disconnect", "elapsed", elapsed.Round(time.Second))

				if err := cisco.Disconnect(ctx); err != nil {
					slog.Error("failed to force disconnect", "error", err)
				} else {
					reconnectingSince = time.Time{}
				}
			} else {
				slog.Info("cisco agent is reconnecting, waiting")
			}
		default:
			reconnectingSince = time.Time{}

			s.setStatus(func(st *State) {
				st.CiscoConnected = false
				st.PFDisabled = false
			})

			if err := cisco.Connect(ctx, s.ciscoProfile, s.ciscoUser, s.ciscoPassword); errors.Is(err, cisco.ErrAcquired) {
				slog.Warn("another Cisco client has connection capability, will retry")
			} else if err != nil {
				slog.Error("failed to connect to cisco", "error", err, "retry_in", connectDelay)

				delay = connectDelay
				connectDelay = min(connectDelay*2, ciscoMaxConnectDelay)
			} else {
				connectDelay = ciscoPollInterval
				connectedByUs = true

				s.setStatus(func(st *State) {
					st.CiscoConnected = true
				})
			}
		}

		state := s.GetState()

		if state.CiscoConnected && !state.PFDisabled {
			slog.Info("disabling network packet filter")
			if err := cisco.DisablePF(ctx); err != nil {
				slog.Error("failed to disable network pf", "error", err)
			} else {
				slog.Info("network packet filter disabled successfully")
				s.setStatus(func(st *State) {
					st.PFDisabled = true
				})
			}
		}

		if state.CiscoConnected && !ciscoReadyNotified {
			close(s.ciscoReady)
			ciscoReadyNotified = true
		}
	}

	return nil
}
