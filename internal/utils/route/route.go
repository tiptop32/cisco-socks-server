package route

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

var ErrNoLANInterface = errors.New("no non-tunnel LAN interface found")

func DetectLAN(ctx context.Context) (string, string, error) {
	out, err := run(ctx, "netstat", "-rn", "-f", "inet")
	if err == nil {
		if name := parseDefaultNonTunnel(out); name != "" {
			if cidr, cerr := connectedCIDR(name); cerr == nil {
				return cidr, name, nil
			}
		}
	}

	return scanRFC1918()
}

func parseDefaultNonTunnel(netstatOutput string) string {
	for _, line := range strings.Split(netstatOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		if fields[0] != "default" {
			continue
		}

		netif := fields[len(fields)-1]
		if strings.HasPrefix(netif, "utun") || strings.HasPrefix(netif, "lo") || netif == "" {
			continue
		}

		return netif
	}

	return ""
}

func connectedCIDR(iface string) (string, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", fmt.Errorf("interface %s: %w", iface, err)
	}

	addrs, err := ifi.Addrs()
	if err != nil {
		return "", fmt.Errorf("addrs %s: %w", iface, err)
	}

	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}

		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}

		if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}

		ones, _ := ipnet.Mask.Size()
		network := ip4.Mask(ipnet.Mask)

		return fmt.Sprintf("%s/%d", network, ones), nil
	}

	return "", fmt.Errorf("no IPv4 address on %s", iface)
}

func scanRFC1918() (string, string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", "", fmt.Errorf("list interfaces: %w", err)
	}

	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}

		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagPointToPoint != 0 {
			continue
		}

		if strings.HasPrefix(ifi.Name, "utun") {
			continue
		}

		addrs, aerr := ifi.Addrs()
		if aerr != nil {
			continue
		}

		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}

			if !isRFC1918(ipnet.IP) {
				continue
			}

			ones, _ := ipnet.Mask.Size()
			network := ipnet.IP.To4().Mask(ipnet.Mask)

			return fmt.Sprintf("%s/%d", network, ones), ifi.Name, nil
		}
	}

	return "", "", ErrNoLANInterface
}

func isRFC1918(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	}

	return false
}

// EnsureClientPinned keeps a LAN client reachable while Cisco owns the
// connected subnet: without a per-host pin the kernel has no on-link route to
// the client, so IP_BOUND_IF frames leave with the default gateway's MAC and
// the client never sees them. Host-level route+ARP pins are the only mac-side
// change Cisco tolerates (scoped routes, /24 re-adds, pf route-to all get
// deleted within seconds).
//
// The check is read-only (`arp -n`): mutating unconditionally is not an
// option because `arp -S` replaces the entry (delete + re-add), which drops
// in-flight frames and causes visible stalls for active connections. Only
// when the entry has drifted it re-asserts `route change <ip> -interface`
// plus `arp -S <ip> <mac> temp`. Note: it must be `arp -S` (capital) —
// lowercase `arp -s` fails with "can only proxy" while the subnet is routed
// into the tunnel. Returns true when a re-pin was performed.
func EnsureClientPinned(ctx context.Context, ip, mac, iface string) (bool, error) {
	if out, err := run(ctx, "arp", "-n", ip); err == nil && arpEntryMatches(out, mac, iface) {
		return false, nil
	}

	// route change fails with "not in table" until the ARP entry exists
	// (arp -S creates the host entry itself) — safe to ignore
	_, _ = run(ctx, "route", "change", ip, "-interface", iface)

	if _, err := run(ctx, "arp", "-S", ip, mac, "temp"); err != nil {
		return false, fmt.Errorf("arp -S %s: %w", ip, err)
	}

	return true, nil
}

// arpEntryMatches reports whether `arp -n` output resolves to the expected
// MAC on the expected interface (format: "? (ip) at <mac> on <iface> ...").
func arpEntryMatches(arpOutput, mac, iface string) bool {
	fields := strings.Fields(arpOutput)

	var gotMAC, gotIface string

	for i := 0; i < len(fields)-1; i++ {
		switch fields[i] {
		case "at":
			gotMAC = fields[i+1]
		case "on":
			gotIface = fields[i+1]
		}
	}

	return gotIface == iface && normalizeMAC(gotMAC) == normalizeMAC(mac)
}

// normalizeMAC strips leading zeros from each octet — macOS arp prints
// "72:ce:39:12:45:f" for "72:ce:39:12:45:0f".
func normalizeMAC(mac string) string {
	parts := strings.Split(strings.ToLower(mac), ":")
	for i, p := range parts {
		parts[i] = strings.TrimLeft(p, "0")
	}

	return strings.Join(parts, ":")
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return string(out), nil
}
