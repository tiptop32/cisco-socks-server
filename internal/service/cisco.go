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
)

func (s *Service) startCisco(ctx context.Context) error {
	ciscoReadyNotified := false
	connectedByUs := false
	connectDelay := ciscoPollInterval

	var reconnectingSince time.Time

	// tracks which LAN clients already had a successful route+ARP pin, so the
	// "pinned" log line fires once per client instead of every 5s tick.
	pinned := make(map[string]bool, len(s.lanClients))

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

		delay := ciscoPollInterval

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
			clear(pinned)
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

			clear(pinned)
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

		if state.CiscoConnected && state.LANInterface != "" {
			s.pinLANClients(ctx, state.LANInterface, pinned)
		}

		if state.CiscoConnected && !ciscoReadyNotified {
			close(s.ciscoReady)
			ciscoReadyNotified = true
		}

		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}

	return nil
}

// pinLANClients re-asserts a per-host route + static ARP entry for every
// configured LAN client. Cisco steals the connected subnet into utunX and
// deletes any broader mac-side fix (scoped routes, /24 re-adds, pf route-to),
// but tolerates host-level route+ARP pins — without them, reply frames from
// the IP_BOUND_IF listener leave with the default gateway's MAC and never
// reach the client. Failures are logged at debug level only: the first pass
// after (re)connect is expected to partially fail until the ARP entry exists.
func (s *Service) pinLANClients(ctx context.Context, iface string, pinned map[string]bool) {
	for _, lc := range s.lanClients {
		routeErr := route.PinHostRoute(ctx, lc.IP, iface)
		if routeErr != nil {
			slog.Debug("route pin skipped", "ip", lc.IP, "error", routeErr)
		}

		arpErr := route.PinARP(ctx, lc.IP, lc.MAC)
		if arpErr != nil {
			slog.Debug("arp pin skipped", "ip", lc.IP, "error", arpErr)
		}

		if routeErr == nil && arpErr == nil && !pinned[lc.IP] {
			pinned[lc.IP] = true

			slog.Info("LAN client pinned to interface", "ip", lc.IP, "mac", lc.MAC, "interface", iface)
		}
	}
}
